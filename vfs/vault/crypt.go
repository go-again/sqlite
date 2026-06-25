package vault

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"

	"gosqlite.org/crypto/keyring"
	"gosqlite.org/vfs/crypto"
)

// At-rest encryption reuses vfs/crypto's length-preserving page cipher
// ([crypto.PageCipher]), applied per block-aligned slot extent inside the
// storage engine (compress first, then encrypt — ciphertext is incompressible).
// The cipher adds no nonce, tag, or length, so the container format is
// unchanged; the trade is confidentiality at rest only, no integrity (matching
// vfs/crypto). The superblock's enc byte records the cipher so a reopen knows
// to decrypt; the data blocks and (a later increment) the directory are the
// encrypted units, while the superblock header stays plaintext to bootstrap.

// Cipher domain bytes (the file-kind tweak ingredient), distinct per on-disk
// unit so a ciphertext can't be replayed as a different one. Grouped persistent
// (the container's own at-rest extents) then transient (the pass-through journal,
// WAL, and temp files), assigned sequentially.
const (
	// Persistent container extents.
	domainPageData  byte = 1 // a compressed data slot
	domainDirectory byte = 2 // a page-directory segment; cipher tweak = segment index
	domainSegIndex  byte = 3 // the segment index; the root the superblock hashes/signs

	// Transient pass-through files (recreated each session).
	domainJournal     byte = 4 // the rollback journal (-journal)
	domainWAL         byte = 5 // the write-ahead log (-wal)
	domainTempDB      byte = 6 // a temporary database
	domainTempJournal byte = 7 // a temporary database's journal
	domainSubJournal  byte = 8 // a statement sub-journal
	domainTempAux     byte = 9 // anonymous / super-journal / other transient temp files
)

// dirCanary is a known plaintext written at the front of the encrypted segment
// index. After decrypting it, a mismatch means the key is wrong — a crisp signal
// ([ErrWrongKey]) instead of a downstream parse/decompress failure. It is branded
// from (and so versioned with) the container's [superblockMagic], keeping the
// on-disk branding in one place; dirCanaryLen bytes, always present in an
// encrypted container (even an empty one writes a canary-only index).
const dirCanaryLen = 16

// dirCanary is superblockMagic ("VAULTv01") followed by "-canary", NUL-padded to
// dirCanaryLen, e.g. "VAULTv01-canary\0".
var dirCanary = func() (c [dirCanaryLen]byte) {
	copy(c[:], superblockMagic+"-canary")
	return
}()

// ErrWrongKey reports that the supplied key (of the right cipher) failed to
// decrypt the container — the directory canary did not match. Test for it with
// [errors.Is].
var ErrWrongKey = errors.New("vault: wrong encryption key")

// superblock.enc markers. 0 means unencrypted; the cipher kinds are offset by
// one from [crypto.Cipher] so that crypto.Adiantum (value 0) is distinguishable
// from "unencrypted" on disk.
const (
	encNone     uint8 = 0
	encAdiantum uint8 = 1
	encAESXTS   uint8 = 2
)

// ErrEncrypted reports that the container is encrypted but no (matching) key or
// identity was supplied in [Options]. Test for it with [errors.Is].
var ErrEncrypted = errors.New("vault: database is encrypted; a matching key or identity is required")

// ErrNoIdentity reports that none of the supplied [Options.Identities] could
// unwrap the data key of a recipients-encrypted database. Test with [errors.Is].
var ErrNoIdentity = errors.New("vault: no supplied identity matched a keyslot")

// ErrUnauthorized reports that a master-protected keyslot was not signed by any
// of the [Options.Masters] the opener trusts (forged, tampered, or downgraded).
// Test with [errors.Is].
var ErrUnauthorized = errors.New("vault: keyslot is not signed by a trusted master")

// ErrNotMaster reports that a non-master tried to change the recipient set of a
// master-protected database (use a master identity for [Rewrap]/[Rekey]). Test
// with [errors.Is].
var ErrNotMaster = errors.New("vault: only a master can change the recipient set")

// ErrReadOnlyRecipient reports a write attempt on an authenticated database
// opened without a writer identity ([Options.WriteAs]) — a read-only recipient.
// Test with [errors.Is].
var ErrReadOnlyRecipient = errors.New("vault: read-only recipient; writes require a writer identity (Options.WriteAs)")

// errRoleMismatch rejects a second opener whose write role differs from the
// already-open shared container's (a path is opened under one role per process).
var errRoleMismatch = errors.New("vault: database already open under a different write role")

// ErrTampered reports that an authenticated container failed its integrity check
// on open — a slot, the directory, or the symmetric MAC'd root did not verify
// (corruption or tampering). Test with [errors.Is].
var ErrTampered = errors.New("vault: authenticated container failed integrity verification (tampered or corrupt)")

// macKeyInfo domain-separates the data key when deriving the per-database MAC key
// for symmetric authenticated mode. Bump the suffix on any breaking change.
var macKeyInfo = []byte("vault-mac-v1")

// deriveMacKey derives the symmetric root-MAC key from the data key. The cipher
// uses the data key directly; the MAC key is an independent, domain-separated
// derivative (HMAC as a KDF), so the two never share key material.
func deriveMacKey(dek []byte) []byte { return macTag(dek, macKeyInfo) }

// macTagLen is the length of a macTag — the symmetric root proof stored in (and
// compared against) the leading bytes of the superblock's signature field. The
// sign and verify sides both slice by this so they cannot drift if the MAC
// primitive ever changes.
const macTagLen = sha256.Size

// macTag is HMAC-SHA256(key, msg) — both the KDF above and the symmetric root
// proof over a superblock's signed state.
func macTag(key, msg []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(msg)
	return h.Sum(nil)
}

// mapKeyslotErr translates keyring's keyslot errors to the typed vault errors
// (no match → ErrNoIdentity, unsigned/forged under pinning → ErrUnauthorized,
// non-master administering → ErrNotMaster).
func mapKeyslotErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, keyring.ErrNoMatch):
		return ErrNoIdentity
	case errors.Is(err, keyring.ErrUnauthorizedKeyslot):
		return ErrUnauthorized
	case errors.Is(err, keyring.ErrNotMaster):
		return ErrNotMaster
	default:
		return err
	}
}

func encByte(c crypto.Cipher) (uint8, error) {
	switch c {
	case crypto.Adiantum:
		return encAdiantum, nil
	case crypto.AESXTS:
		return encAESXTS, nil
	default:
		return 0, fmt.Errorf("vault: unknown cipher %v", c)
	}
}

func cipherForEnc(enc uint8) (crypto.Cipher, bool) {
	switch enc {
	case encAdiantum:
		return crypto.Adiantum, true
	case encAESXTS:
		return crypto.AESXTS, true
	default:
		return 0, false
	}
}

// keyConfig carries the encryption inputs from [Options] to the container, which
// resolves the data key — directly (a raw key) or by unwrapping a keyslot (for
// recipients) — and builds the page cipher at open.
type keyConfig struct {
	cipher       crypto.Cipher
	rawKey       []byte
	recipients   []keyring.Recipient
	identities   []keyring.Identity
	masters      []keyring.MasterRecipient // create: pinned admins; open: trusted admins
	signWith     keyring.MasterIdentity    // create: who signs the keyslot
	writers      []keyring.WriterRecipient // create: authorized writers (authenticated mode)
	writeAs      keyring.WriterIdentity    // who signs commits on this connection
	authenticate bool                      // symmetric (MAC'd) authenticated mode, without ed25519 writers
	anchor       ReplayAnchor              // external monotonic replay floor (requires authenticated mode)

	// preserve, when set, makes a fresh container reproduce a SOURCE container's
	// encryption verbatim — the same data key and sealed keyslot, not a new key or a
	// re-seal to a new recipient set. It is what lets [Compact] run with only a read
	// identity (no write creds): the densely-repacked copy keeps the existing
	// membership. Set only by the rewrite path; mutually exclusive with the create
	// inputs above.
	preserve *preservedKey
}

// preservedKey is a source container's resolved encryption state, copied into a
// rewrite's destination so it keeps the same data key and membership (see
// [keyConfig.preserve]).
type preservedKey struct {
	dek     []byte
	keyslot []byte
	enc     uint8
	auth    bool                   // source authenticated mode
	writers []ed25519.PublicKey    // writer mode: the authorized writer set (empty ⇒ symmetric/none)
	writeAs keyring.WriterIdentity // writer mode: who re-signs the copy's commits (nil ⇒ cannot rewrite)
}

func (kc keyConfig) encrypting() bool {
	return len(kc.rawKey) > 0 || len(kc.recipients) > 0 || len(kc.identities) > 0 || len(kc.masters) > 0
}

// keyConfigFromOptions validates and extracts the encryption inputs. Key and
// Recipients/Masters are mutually exclusive.
func keyConfigFromOptions(opts Options) (keyConfig, error) {
	if len(opts.Key) > 0 && (len(opts.Recipients) > 0 || len(opts.Masters) > 0) {
		return keyConfig{}, errors.New("vault: set either Options.Key or Options.Recipients/Masters, not both")
	}
	kc := keyConfig{
		cipher:       opts.Cipher,
		rawKey:       opts.Key,
		recipients:   opts.Recipients,
		identities:   opts.Identities,
		masters:      opts.Masters,
		signWith:     opts.SignWith,
		writers:      opts.Writers,
		writeAs:      opts.WriteAs,
		authenticate: opts.Authenticate,
		anchor:       opts.Anchor,
	}
	if opts.Authenticate && !kc.encrypting() {
		return keyConfig{}, errors.New("vault: Options.Authenticate requires encryption (set Options.Key or Options.Recipients)")
	}
	if opts.Anchor != nil && !opts.Authenticate && len(opts.Writers) == 0 {
		return keyConfig{}, errors.New("vault: Options.Anchor requires authenticated mode (set Options.Authenticate or Options.Writers)")
	}
	return kc, nil
}

// initCipherForCreate resolves the cipher for a brand-new container: a raw key
// directly, or a random data key wrapped to the recipients (writing the keyslot
// block). It sets c.cipher/c.enc/c.keyslotOffset; the caller holds the allocator
// ready and commits afterward.
func (c *container) initCipherForCreate(kc keyConfig) error {
	// Reproduce a source container's encryption verbatim (Compact -identity): same
	// data key, same sealed keyslot, no re-seal. Handled before the guards below
	// because it legitimately carries identities but no create creds.
	if kc.preserve != nil {
		return c.initCipherFromPreserved(kc.preserve)
	}
	// Identities alone open an EXISTING encrypted container; they carry no material
	// to create one with. Without this guard such a create would fall through the
	// switch and silently produce a plaintext container (a data-exposure footgun,
	// e.g. via Compact passed only Identities).
	if len(kc.rawKey) == 0 && len(kc.recipients) == 0 && len(kc.masters) == 0 && len(kc.identities) > 0 {
		return errors.New("vault: creating an encrypted container needs Options.Key or Options.Recipients (Options.Identities alone only opens an existing one)")
	}
	switch {
	case len(kc.rawKey) > 0:
		cph, err := crypto.NewCipher(kc.cipher, kc.rawKey)
		if err != nil {
			return err
		}
		enc, err := encByte(kc.cipher)
		if err != nil {
			return err
		}
		c.cipher, c.enc, c.dek = cph, enc, append([]byte(nil), kc.rawKey...)
		if kc.authenticate {
			c.authenticated, c.macKey = true, deriveMacKey(c.dek)
		}
	case len(kc.recipients) > 0 || len(kc.masters) > 0:
		dek := make([]byte, crypto.KeyLen(kc.cipher))
		if _, err := rand.Read(dek); err != nil {
			return err
		}
		cph, err := crypto.NewCipher(kc.cipher, dek)
		if err != nil {
			return err
		}
		enc, err := encByte(kc.cipher)
		if err != nil {
			return err
		}
		if len(kc.writers) > 0 {
			if len(kc.masters) == 0 {
				return errors.New("vault: Options.Writers requires Options.Masters (an admin authorizes the writer list)")
			}
			if !keyring.WriterAuthorized(kc.writers, kc.writeAs) {
				return errors.New("vault: Options.WriteAs must be one of Options.Writers")
			}
		}
		blob, err := keyring.SealKeyslot(dek, keyring.Membership{Masters: kc.masters, Writers: kc.writers, Members: kc.recipients}, kc.signWith)
		if err != nil {
			return err
		}
		off, err := c.writeKeyslot(blob)
		if err != nil {
			return err
		}
		c.cipher, c.enc, c.keyslotOffset, c.dek, c.keyslotBlob = cph, enc, off, dek, blob
		switch {
		case len(kc.writers) > 0:
			c.authenticated, c.writeAs = true, kc.writeAs
		case kc.authenticate:
			c.authenticated, c.macKey = true, deriveMacKey(dek)
		}
	}
	return nil
}

// initCipherFromPreserved builds the cipher for a fresh container from a source's
// resolved key material: the same data key and a verbatim copy of its sealed
// keyslot, so the rewrite keeps the source's membership. The symmetric MAC key is
// re-derived from the data key; in writer mode the source's writers carry over and
// the commits re-sign with p.writeAs (which the caller has verified is present).
func (c *container) initCipherFromPreserved(p *preservedKey) error {
	kind, ok := cipherForEnc(p.enc)
	if !ok {
		return fmt.Errorf("vault: unknown on-disk cipher marker %d", p.enc)
	}
	cph, err := crypto.NewCipher(kind, p.dek)
	if err != nil {
		return err
	}
	off, err := c.writeKeyslot(p.keyslot)
	if err != nil {
		return err
	}
	c.cipher, c.enc, c.keyslotOffset = cph, p.enc, off
	c.dek = append([]byte(nil), p.dek...)
	c.keyslotBlob = p.keyslot
	c.authenticated = p.auth
	c.writers = p.writers
	c.writeAs = p.writeAs
	if p.auth && len(p.writers) == 0 {
		c.macKey = deriveMacKey(c.dek) // symmetric authenticated mode
	}
	return nil
}

// resolveCipherForOpen resolves the cipher for an existing container from its
// superblock: validates the supplied key/identities against the on-disk enc and
// keyslot, builds the cipher, and returns the keyslot's block count (0 if none)
// so the caller can reserve it in the allocator.
func (c *container) resolveCipherForOpen(kc keyConfig, sb *superblock, fileSize int64) (keyslotBlocks uint32, err error) {
	if sb.enc == encNone {
		if kc.encrypting() {
			return 0, errors.New("vault: database is not encrypted, but a key or recipients were supplied")
		}
		return 0, nil
	}
	kind, ok := cipherForEnc(sb.enc)
	if !ok {
		return 0, fmt.Errorf("vault: unknown on-disk cipher marker %d", sb.enc)
	}
	var dek []byte
	if sb.keyslotOffset != 0 { // recipients mode
		if len(kc.identities) == 0 {
			return 0, ErrEncrypted
		}
		blob, nb, rerr := c.readKeyslot(sb.keyslotOffset, fileSize)
		if rerr != nil {
			return 0, rerr
		}
		keyslotBlocks = nb
		c.keyslotBlob = blob
		var ws []ed25519.PublicKey
		dek, ws, err = keyring.OpenKeyslot(blob, kc.masters, kc.identities...)
		if err != nil {
			return 0, mapKeyslotErr(err)
		}
		c.writers = ws
	} else { // raw-key mode
		if len(kc.rawKey) == 0 {
			return 0, ErrEncrypted
		}
		dek = kc.rawKey
	}
	cph, err := crypto.NewCipher(kind, dek)
	if err != nil {
		return 0, err
	}
	// Defensive copy: in raw-key mode dek aliases Options.Key, and c.dek backs the
	// constant-time compare in matchesKeyConfig and the re-wrap in Rewrap/Rekey, so a
	// caller mutating Options.Key after open must not corrupt it (matching the create
	// path, which already copies). The cipher itself already holds its own copy.
	c.cipher, c.enc, c.keyslotOffset, c.dek = cph, sb.enc, sb.keyslotOffset, append([]byte(nil), dek...)
	// In authenticated mode the opener may sign commits (writeAs) or, without one,
	// is a read-only recipient. The writer set verified against is c.writers above.
	c.authenticated = sb.authenticated
	c.writeAs = kc.writeAs
	// An opener that requires authentication must not accept an on-disk container
	// whose authenticated flag is clear — that would be a downgrade (a data-key
	// holder stripping the per-slot hashes and the MAC'd root).
	if kc.authenticate && !c.authenticated {
		return 0, ErrUnauthorized
	}
	// Symmetric authenticated mode (authenticated, no ed25519 writers): derive the
	// MAC key the root proof is verified with.
	if c.authenticated && len(c.writers) == 0 {
		c.macKey = deriveMacKey(dek)
	}
	return keyslotBlocks, nil
}

// matchesKeyConfig reports whether kc unlocks this already-open container with
// the same data key it was opened under. It is the guard on the container
// registry: a second opener of the same path (a different VFS / Open call)
// shares the live in-memory container, so it must prove it holds the right key
// rather than silently inheriting the first opener's cipher. Same-VFS pooled
// connections pass trivially (same kc); a mismatched key/identity, or a key
// supplied for a plaintext database (or vice versa), is rejected.
//
// For recipients mode this re-unwraps the cached keyslot with kc's identities;
// a passphrase recipient therefore re-runs its KDF here, but only once per
// pooled connection's first open, never per query.
func (c *container) matchesKeyConfig(kc keyConfig) error {
	encrypted := c.cipher != nil
	if encrypted != kc.encrypting() {
		if encrypted {
			return ErrEncrypted // live container is encrypted; caller gave no key/identity
		}
		return errors.New("vault: database is not encrypted, but a key or recipients were supplied")
	}
	if !encrypted {
		return nil // both plaintext: nothing to match
	}
	var dek []byte
	if c.keyslotOffset != 0 { // recipients mode
		if len(kc.identities) == 0 {
			return ErrEncrypted
		}
		d, _, err := keyring.OpenKeyslot(c.keyslotBlob, kc.masters, kc.identities...)
		if err != nil {
			return mapKeyslotErr(err)
		}
		dek = d
	} else { // raw-key mode
		if len(kc.rawKey) == 0 {
			return ErrEncrypted
		}
		dek = kc.rawKey
	}
	if subtle.ConstantTimeCompare(dek, c.dek) != 1 {
		return ErrWrongKey
	}
	// The write role is a property of the shared container (set by the first
	// opener). A second opener must match it, so a read-only recipient cannot
	// inherit a writer's in-memory signing identity through the registry (nor a
	// writer be wedged behind a read-only container). Within one process a path is
	// opened under a single write role. This is a writer-mode (ed25519) concept:
	// symmetric authenticated mode (c.macKey != nil) has no per-connection writer
	// role, so any key holder shares the container freely.
	if c.authenticated && c.macKey == nil && (kc.writeAs == nil) != c.readOnlyRecipient {
		return errRoleMismatch
	}
	return nil
}

// maxKeyslotLen bounds the wrapped-data-key blob read from disk (untrusted): an
// age header for many recipients is well under this.
const maxKeyslotLen = 1 << 20

// keyslotBanner brands the on-disk keyslot block as a gosqlite vault container,
// so the file plainly identifies itself right beside the age envelope it wraps
// (which carries age's own "age-encryption.org/v1"). It is vault's own framing
// around the opaque keyring blob; keyring/age supply no such marker.
const keyslotBanner = "gosqlite.org/vault/v1\n"

// keyslotHeaderLen is the banner plus the 4-byte blob length that precede the
// keyring keyslot blob in the on-disk block.
const keyslotHeaderLen = len(keyslotBanner) + 4

// writeKeyslot allocates a block run and writes the banner-and-length-prefixed
// blob, returning its physical byte offset. The block is referenced only by the
// superblock's keyslotOffset, never the directory.
func (c *container) writeKeyslot(blob []byte) (uint64, error) {
	framed := make([]byte, keyslotHeaderLen+len(blob))
	n := copy(framed, keyslotBanner)
	binary.LittleEndian.PutUint32(framed[n:n+4], uint32(len(blob)))
	copy(framed[keyslotHeaderLen:], blob)
	nb := blocksFor(uint64(len(framed)), c.blockSize)
	off := c.alloc.alloc(nb) * c.blockSize
	buf := make([]byte, nb*c.blockSize)
	copy(buf, framed)
	if _, err := c.back.WriteAt(buf, int64(off)); err != nil {
		return 0, err
	}
	return off, nil
}

// readKeyslot reads the keyslot blob at off — the banner, then the blob length,
// then the keyring blob — returning the blob and the blocks it occupies. The
// banner and length are bounded against the file and maxKeyslotLen (untrusted).
func (c *container) readKeyslot(off uint64, fileSize int64) (blob []byte, blocks uint32, err error) {
	head := make([]byte, keyslotHeaderLen)
	if _, err := c.back.ReadAt(head, int64(off)); err != nil {
		return nil, 0, fmt.Errorf("vault: read keyslot header: %w", err)
	}
	if string(head[:len(keyslotBanner)]) != keyslotBanner {
		return nil, 0, errors.New("vault: keyslot banner mismatch (not a vault keyslot, or corruption)")
	}
	n := binary.LittleEndian.Uint32(head[len(keyslotBanner):])
	if n == 0 || n > maxKeyslotLen || off+uint64(keyslotHeaderLen)+uint64(n) > uint64(fileSize) {
		return nil, 0, errors.New("vault: keyslot length out of range (corruption)")
	}
	blob = make([]byte, n)
	if _, err := c.back.ReadAt(blob, int64(off)+int64(keyslotHeaderLen)); err != nil {
		return nil, 0, fmt.Errorf("vault: read keyslot: %w", err)
	}
	return blob, uint32(blocksFor(uint64(keyslotHeaderLen)+uint64(n), c.blockSize)), nil
}

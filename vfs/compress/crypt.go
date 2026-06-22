package compress

import (
	"crypto/ed25519"
	"crypto/rand"
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
// unit so a ciphertext can't be replayed as a different one.
const (
	domainPageData    byte = 1 // a compressed data slot (persistent)
	domainDirectory   byte = 2 // the page directory (persistent)
	domainJournal     byte = 3 // the rollback journal (-journal)
	domainWAL         byte = 4 // the write-ahead log (-wal)
	domainTempDB      byte = 5 // a temporary database
	domainTempJournal byte = 6 // a temporary database's journal
	domainSubJournal  byte = 7 // a statement sub-journal
	domainTempAux     byte = 8 // anonymous / super-journal / other transient temp files
)

// dirTweak is the cipher tweak for the directory. The directory is a single
// unit (not indexed like data pages), so a constant tweak suffices; the domain
// byte separates it from data slots.
const dirTweak uint64 = 0

// dirCanary is a known plaintext written at the front of the encrypted
// directory. After decrypting the directory, a mismatch means the key is wrong
// — a crisp signal ([ErrWrongKey]) instead of a downstream parse/decompress
// failure. dirCanaryLen bytes, always present in an encrypted container (even an
// empty one writes a canary-only directory).
const dirCanaryLen = 16

var dirCanary = [dirCanaryLen]byte{'g', 'o', 's', 'q', 'l', 'i', 't', 'e', 'z', '-', 'e', 'n', 'c', 'v', '1', 0}

// ErrWrongKey reports that the supplied key (of the right cipher) failed to
// decrypt the container — the directory canary did not match. Test for it with
// [errors.Is].
var ErrWrongKey = errors.New("compress: wrong encryption key")

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
var ErrEncrypted = errors.New("compress: database is encrypted; a matching key or identity is required")

// ErrNoIdentity reports that none of the supplied [Options.Identities] could
// unwrap the data key of a recipients-encrypted database. Test with [errors.Is].
var ErrNoIdentity = errors.New("compress: no supplied identity matched a keyslot")

// ErrUnauthorized reports that a master-protected keyslot was not signed by any
// of the [Options.Masters] the opener trusts (forged, tampered, or downgraded).
// Test with [errors.Is].
var ErrUnauthorized = errors.New("compress: keyslot is not signed by a trusted master")

// ErrNotMaster reports that a non-master tried to change the recipient set of a
// master-protected database (use a master identity for [Rewrap]/[Rekey]). Test
// with [errors.Is].
var ErrNotMaster = errors.New("compress: only a master can change the recipient set")

// ErrReadOnlyRecipient reports a write attempt on an authenticated database
// opened without a writer identity ([Options.WriteAs]) — a read-only recipient.
// Test with [errors.Is].
var ErrReadOnlyRecipient = errors.New("compress: read-only recipient; writes require a writer identity (Options.WriteAs)")

// errRoleMismatch rejects a second opener whose write role differs from the
// already-open shared container's (a path is opened under one role per process).
var errRoleMismatch = errors.New("compress: database already open under a different write role")

// mapKeyslotErr translates keyring's keyslot errors to the typed compress errors
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
		return 0, fmt.Errorf("compress: unknown cipher %v", c)
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
	cipher     crypto.Cipher
	rawKey     []byte
	recipients []keyring.Recipient
	identities []keyring.Identity
	masters    []keyring.MasterRecipient // create: pinned admins; open: trusted admins
	signWith   keyring.MasterIdentity    // create: who signs the keyslot
	writers    []keyring.WriterRecipient // create: authorized writers (authenticated mode)
	writeAs    keyring.WriterIdentity    // who signs commits on this connection
}

func (kc keyConfig) encrypting() bool {
	return len(kc.rawKey) > 0 || len(kc.recipients) > 0 || len(kc.identities) > 0 || len(kc.masters) > 0
}

// keyConfigFromOptions validates and extracts the encryption inputs. Key and
// Recipients/Masters are mutually exclusive.
func keyConfigFromOptions(opts Options) (keyConfig, error) {
	if len(opts.Key) > 0 && (len(opts.Recipients) > 0 || len(opts.Masters) > 0) {
		return keyConfig{}, errors.New("compress: set either Options.Key or Options.Recipients/Masters, not both")
	}
	return keyConfig{
		cipher:     opts.Cipher,
		rawKey:     opts.Key,
		recipients: opts.Recipients,
		identities: opts.Identities,
		masters:    opts.Masters,
		signWith:   opts.SignWith,
		writers:    opts.Writers,
		writeAs:    opts.WriteAs,
	}, nil
}

// initCipherForCreate resolves the cipher for a brand-new container: a raw key
// directly, or a random data key wrapped to the recipients (writing the keyslot
// block). It sets c.cipher/c.enc/c.keyslotOffset; the caller holds the allocator
// ready and commits afterward.
func (c *container) initCipherForCreate(kc keyConfig) error {
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
				return errors.New("compress: Options.Writers requires Options.Masters (an admin authorizes the writer list)")
			}
			if !keyring.WriterAuthorized(kc.writers, kc.writeAs) {
				return errors.New("compress: Options.WriteAs must be one of Options.Writers")
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
		if len(kc.writers) > 0 {
			c.authenticated, c.writeAs = true, kc.writeAs
		}
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
			return 0, errors.New("compress: database is not encrypted, but a key or recipients were supplied")
		}
		return 0, nil
	}
	kind, ok := cipherForEnc(sb.enc)
	if !ok {
		return 0, fmt.Errorf("compress: unknown on-disk cipher marker %d", sb.enc)
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
	c.cipher, c.enc, c.keyslotOffset, c.dek = cph, sb.enc, sb.keyslotOffset, dek
	// In authenticated mode the opener may sign commits (writeAs) or, without one,
	// is a read-only recipient. The writer set verified against is c.writers above.
	c.authenticated = sb.authenticated
	c.writeAs = kc.writeAs
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
		return errors.New("compress: database is not encrypted, but a key or recipients were supplied")
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
	// opened under a single write role.
	if c.authenticated && (kc.writeAs == nil) != c.readOnlyRecipient {
		return errRoleMismatch
	}
	return nil
}

// maxKeyslotLen bounds the wrapped-data-key blob read from disk (untrusted): an
// age header for many recipients is well under this.
const maxKeyslotLen = 1 << 20

// writeKeyslot allocates a block run and writes the length-prefixed blob,
// returning its physical byte offset. The block is referenced only by the
// superblock's keyslotOffset, never the directory.
func (c *container) writeKeyslot(blob []byte) (uint64, error) {
	framed := make([]byte, 4+len(blob))
	binary.LittleEndian.PutUint32(framed[:4], uint32(len(blob)))
	copy(framed[4:], blob)
	nb := blocksFor(uint64(len(framed)), c.blockSize)
	off := c.alloc.alloc(nb) * c.blockSize
	buf := make([]byte, nb*c.blockSize)
	copy(buf, framed)
	if _, err := c.back.WriteAt(buf, int64(off)); err != nil {
		return 0, err
	}
	return off, nil
}

// readKeyslot reads the length-prefixed keyslot blob at off, returning it and
// the number of blocks it occupies. The length is bounded against the file and
// maxKeyslotLen (untrusted input).
func (c *container) readKeyslot(off uint64, fileSize int64) (blob []byte, blocks uint32, err error) {
	var lenbuf [4]byte
	if _, err := c.back.ReadAt(lenbuf[:], int64(off)); err != nil {
		return nil, 0, fmt.Errorf("compress: read keyslot length: %w", err)
	}
	n := binary.LittleEndian.Uint32(lenbuf[:])
	if n == 0 || n > maxKeyslotLen || off+4+uint64(n) > uint64(fileSize) {
		return nil, 0, errors.New("compress: keyslot length out of range (corruption)")
	}
	blob = make([]byte, n)
	if _, err := c.back.ReadAt(blob, int64(off)+4); err != nil {
		return nil, 0, fmt.Errorf("compress: read keyslot: %w", err)
	}
	return blob, uint32(blocksFor(4+uint64(n), c.blockSize)), nil
}

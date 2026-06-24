package keyring

// master.go adds master and writer keys on top of the flat recipients model.
//
//   - A master is a recipient whose public key is pinned as an authorized SIGNER
//     of the keyslot's membership. On open a reader verifies that signature
//     against the masters it trusts and refuses any membership a master did not
//     sign — so only a master can add or remove recipients, writers, and masters.
//   - A writer is a recipient whose public key is pinned (inside the
//     master-signed keyslot) as an authorized signer of committed container
//     states (see SignState / VerifyState). It is the basis for read-only
//     recipients: a recipient with no writer key can read but cannot produce a
//     write that a reader will accept.
//
// A master/writer is an ed25519 key — both an age recipient and a signer; build
// one with the SSH or Generate constructors here. X25519 / passphrase recipients
// can be members (read-only), not masters or writers.
//
// The signature prevents FORGING a new membership or a new committed state; it
// does not by itself prevent rolling on-disk bytes back to a prior signed state
// (the container carries no separate integrity tag in the non-authenticated
// modes). Rekey defeats rollback for revocation: a fresh data key makes a removed
// party's retained key — and any rolled-back keyslot — decrypt nothing.

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"

	"filippo.io/age/agessh"
	"golang.org/x/crypto/ssh"
)

// ErrUnauthorizedKeyslot reports that a keyslot pinning one or more masters
// carried no valid signature from any of them — a membership not authorized by a
// master (tampered, forged, or truncated). Test with [errors.Is].
var ErrUnauthorizedKeyslot = errors.New("keyring: keyslot membership not signed by a pinned master")

// ErrNotMaster reports that an identity tried to change the membership of a
// master-protected keyslot but is not one of its current masters. Test with
// [errors.Is].
var ErrNotMaster = errors.New("keyring: identity is not a current master of this keyslot")

// MasterRecipient is a recipient who is also pinned as an authorized administrator
// of the keyslot: only a holder of a corresponding [MasterIdentity] can add or
// remove recipients, writers, and masters. Build one with [SSHMasterRecipient] or
// [GenerateMaster]. The interface is sealed.
type MasterRecipient interface {
	Recipient
	masterPub() ed25519.PublicKey
}

// MasterIdentity recovers access and can authorize (sign) keyslot changes; it is
// also the type used to sign committed states as a writer (see [SignState]).
// Build one with [SSHMasterIdentity] or [GenerateMaster]. The interface is sealed.
type MasterIdentity interface {
	Identity
	masterPub() ed25519.PublicKey
	masterSigner() ed25519.PrivateKey
}

// WriterRecipient and WriterIdentity name the writer role. A writer is the same
// ed25519 signer as a master (an age recipient plus a signer), pinned to
// authenticate committed container states rather than the keyslot.
type (
	WriterRecipient = MasterRecipient
	WriterIdentity  = MasterIdentity
)

// Membership is the full party set of a keyslot: masters administer the keyslot
// and the writer list; writers sign committed states (authenticated mode);
// members are read-only. All three receive the data key.
type Membership struct {
	Masters []MasterRecipient
	Writers []WriterRecipient
	Members []Recipient
}

func (m Membership) recipients() []Recipient {
	out := make([]Recipient, 0, len(m.Masters)+len(m.Writers)+len(m.Members))
	for _, r := range m.Masters {
		out = append(out, r)
	}
	for _, r := range m.Writers {
		out = append(out, r)
	}
	return append(out, m.Members...)
}

type masterRecipient struct {
	recipient
	pub ed25519.PublicKey
}

func (m masterRecipient) masterPub() ed25519.PublicKey { return m.pub }

type masterIdentity struct {
	identity
	priv ed25519.PrivateKey
}

func (m masterIdentity) masterPub() ed25519.PublicKey     { return m.priv.Public().(ed25519.PublicKey) }
func (m masterIdentity) masterSigner() ed25519.PrivateKey { return m.priv }

// SSHMasterRecipient builds a master (or writer) recipient from an ssh-ed25519
// authorized_keys line. Only ed25519 keys can sign; an RSA or other key is
// rejected.
func SSHMasterRecipient(authorizedKeyLine []byte) (MasterRecipient, error) {
	line := bytes.TrimSpace(authorizedKeyLine)
	r, err := agessh.ParseRecipient(string(line))
	if err != nil {
		return nil, fmt.Errorf("keyring: master recipient: %w", err)
	}
	sshPub, comment, _, _, err := ssh.ParseAuthorizedKey(line)
	if err != nil {
		return nil, fmt.Errorf("keyring: master recipient: %w", err)
	}
	cpk, ok := sshPub.(ssh.CryptoPublicKey)
	if !ok {
		return nil, errors.New("keyring: master recipient: unsupported SSH key")
	}
	edpub, ok := cpk.CryptoPublicKey().(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("keyring: a master or writer must be an ssh-ed25519 key")
	}
	pub := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
	return masterRecipient{recipient{r: r, pub: pub, label: comment}, edpub}, nil
}

// SSHMasterIdentity builds a master (or writer) identity from an ed25519 SSH
// private key in PEM form (with its passphrase if encrypted; nil otherwise).
func SSHMasterIdentity(pemPrivateKey, passphrase []byte) (MasterIdentity, error) {
	var raw any
	var err error
	if len(passphrase) == 0 {
		raw, err = ssh.ParseRawPrivateKey(pemPrivateKey)
	} else {
		raw, err = ssh.ParseRawPrivateKeyWithPassphrase(pemPrivateKey, passphrase)
	}
	if err != nil {
		return nil, fmt.Errorf("keyring: master identity: %w", err)
	}
	var priv ed25519.PrivateKey
	switch k := raw.(type) {
	case *ed25519.PrivateKey:
		priv = *k
	case ed25519.PrivateKey:
		priv = k
	default:
		return nil, fmt.Errorf("keyring: a master or writer must be an ed25519 key, got %T", raw)
	}
	ageID, err := agessh.NewEd25519Identity(priv)
	if err != nil {
		return nil, fmt.Errorf("keyring: master identity: %w", err)
	}
	return masterIdentity{identity{ageID}, priv}, nil
}

// GenerateMaster returns a fresh ed25519 keypair usable as a master or a writer:
// the recipient to pin and the identity to authorize changes / sign states.
func GenerateMaster() (MasterRecipient, MasterIdentity, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("keyring: generate master: %w", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, nil, fmt.Errorf("keyring: generate master: %w", err)
	}
	mr, err := SSHMasterRecipient(ssh.MarshalAuthorizedKey(sshPub))
	if err != nil {
		return nil, nil, err
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return nil, nil, fmt.Errorf("keyring: generate master: %w", err)
	}
	mi, err := SSHMasterIdentity(pem.EncodeToMemory(block), nil)
	if err != nil {
		return nil, nil, err
	}
	return mr, mi, nil
}

// SignState signs a committed-state message (the caller's canonical superblock
// bytes) with a writer identity. Verify with [VerifyState].
func SignState(by WriterIdentity, msg []byte) []byte {
	return ed25519.Sign(by.masterSigner(), msg)
}

// WriterAuthorized reports whether id's public key is one of writers — used to
// check that the identity signing commits is actually in the authorized set.
func WriterAuthorized(writers []WriterRecipient, id WriterIdentity) bool {
	return id != nil && masterPinned(id.masterPub(), writers)
}

// VerifyState reports whether sig over msg verifies against any of the authorized
// writer public keys (those returned by [OpenKeyslot]).
func VerifyState(writers []ed25519.PublicKey, msg, sig []byte) bool {
	for _, w := range writers {
		if len(w) == ed25519.PublicKeySize && ed25519.Verify(w, msg, sig) {
			return true
		}
	}
	return false
}

// keyslotVersion tags the sealed-keyslot wire format (see SealKeyslot); it also
// rejects a malformed blob whose first byte is not the current format.
const keyslotVersion byte = 2

// SealKeyslot wraps dek to every party in m (masters, writers, members) and, when
// masters are pinned, signs the membership — the master set, the writer set, the
// wrapped data key, and the sealed member list — with signWith, which must be one
// of the masters. With no masters it is the flat model: the data key wrapped to
// the members, no signature, no member list. Open it with [OpenKeyslot]; an admin
// lists its membership with [Members].
func SealKeyslot(dek []byte, m Membership, signWith MasterIdentity) ([]byte, error) {
	if len(m.Masters) > 255 || len(m.Writers) > 255 {
		return nil, errors.New("keyring: too many masters or writers (max 255 each)")
	}
	wrapped, err := Wrap(dek, m.recipients()...) // errors if the set is empty
	if err != nil {
		return nil, err
	}

	// The membership record: the parties' public forms, sealed to the masters only
	// (a fresh envelope independent of the data key — wrapped to the masters as
	// recipients), so an admin can later enumerate the set ([Members]) while writers
	// and members cannot. Built only when masters are pinned; a flat keyslot has no
	// admin tier and carries none.
	var memberBlob []byte
	if len(m.Masters) > 0 {
		masterRecips := make([]Recipient, len(m.Masters))
		for i, r := range m.Masters {
			masterRecips[i] = r
		}
		list, merr := marshalMembers(m)
		if merr != nil {
			return nil, merr
		}
		if memberBlob, err = Wrap(list, masterRecips...); err != nil {
			return nil, err
		}
	}

	buf := make([]byte, 0, 3+(len(m.Masters)+len(m.Writers))*ed25519.PublicKeySize+8+len(wrapped)+len(memberBlob)+ed25519.SignatureSize)
	buf = append(buf, keyslotVersion, byte(len(m.Masters)))
	for _, r := range m.Masters {
		buf = append(buf, r.masterPub()...)
	}
	buf = append(buf, byte(len(m.Writers)))
	for _, r := range m.Writers {
		buf = append(buf, r.masterPub()...)
	}
	buf = appendLenPrefixed(buf, wrapped)
	buf = appendLenPrefixed(buf, memberBlob)

	if len(m.Masters) > 0 {
		if signWith == nil {
			return nil, errors.New("keyring: a keyslot with masters must be signed by a master identity")
		}
		if !masterPinned(signWith.masterPub(), m.Masters) {
			return nil, errors.New("keyring: the signing identity is not one of the pinned masters")
		}
		buf = append(buf, ed25519.Sign(signWith.masterSigner(), buf)...)
	}
	return buf, nil
}

// appendLenPrefixed appends b to buf with a little-endian uint32 length prefix.
func appendLenPrefixed(buf, b []byte) []byte {
	var lenb [4]byte
	binary.LittleEndian.PutUint32(lenb[:], uint32(len(b)))
	buf = append(buf, lenb[:]...)
	return append(buf, b...)
}

// OpenKeyslot recovers the data key from a sealed keyslot and returns the
// authorized writer public keys (for verifying committed states). When trusted
// masters are supplied the membership MUST carry a signature that verifies against
// one of them — the reader pins the masters it trusts, like SSH known_hosts, so a
// member who holds the data key cannot rewrite the membership under its own master
// key and have a pinned reader accept it; a flat or unsigned keyslot is then
// rejected as a downgrade ([ErrUnauthorizedKeyslot]). With no trusted masters the
// signature is not checked. The data key is unwrapped with the first matching
// identity ([ErrNoMatch] if none).
func OpenKeyslot(blob []byte, trusted []MasterRecipient, with ...Identity) (dek []byte, writers []ed25519.PublicKey, err error) {
	_, writerPubs, wrapped, _, signed, sig, perr := parseKeyslot(blob)
	if perr != nil {
		return nil, nil, perr
	}
	if len(trusted) > 0 {
		pubs := make([]ed25519.PublicKey, len(trusted))
		for i, m := range trusted {
			pubs[i] = m.masterPub()
		}
		if sig == nil || !VerifyState(pubs, signed, sig) { // unsigned, downgraded to flat, or wrong signer
			return nil, nil, ErrUnauthorizedKeyslot
		}
	}
	dek, err = Unwrap(wrapped, with...)
	if err != nil {
		return nil, nil, err
	}
	return dek, writerPubs, nil
}

// parseKeyslot decodes the sealed-keyslot wire format, bounding every length
// against the blob. wrapped is the data-key envelope; memberBlob is the
// master-sealed member list (empty for a flat keyslot); signed is the byte range
// the signature covers; sig is nil when no masters are pinned.
func parseKeyslot(blob []byte) (masters, writers []ed25519.PublicKey, wrapped, memberBlob, signed, sig []byte, err error) {
	if len(blob) < 2 || blob[0] != keyslotVersion {
		return nil, nil, nil, nil, nil, nil, errors.New("keyring: unrecognized keyslot format")
	}
	off := 1
	readPubs := func() ([]ed25519.PublicKey, error) {
		if off >= len(blob) {
			return nil, errors.New("keyring: keyslot truncated")
		}
		n := int(blob[off])
		off++
		if uint64(off)+uint64(n)*ed25519.PublicKeySize > uint64(len(blob)) {
			return nil, errors.New("keyring: keyslot truncated (keys)")
		}
		out := make([]ed25519.PublicKey, 0, n)
		for range n {
			out = append(out, append(ed25519.PublicKey(nil), blob[off:off+ed25519.PublicKeySize]...))
			off += ed25519.PublicKeySize
		}
		return out, nil
	}
	readSection := func() ([]byte, error) {
		if uint64(off)+4 > uint64(len(blob)) {
			return nil, errors.New("keyring: keyslot truncated (length)")
		}
		n := binary.LittleEndian.Uint32(blob[off : off+4])
		off += 4
		if uint64(off)+uint64(n) > uint64(len(blob)) {
			return nil, errors.New("keyring: keyslot section out of range")
		}
		b := blob[off : off+int(n)]
		off += int(n)
		return b, nil
	}
	if masters, err = readPubs(); err != nil {
		return nil, nil, nil, nil, nil, nil, err
	}
	if writers, err = readPubs(); err != nil {
		return nil, nil, nil, nil, nil, nil, err
	}
	if wrapped, err = readSection(); err != nil {
		return nil, nil, nil, nil, nil, nil, err
	}
	if memberBlob, err = readSection(); err != nil {
		return nil, nil, nil, nil, nil, nil, err
	}
	sigStart := off
	if len(masters) > 0 {
		if sigStart+ed25519.SignatureSize > len(blob) {
			return nil, nil, nil, nil, nil, nil, errors.New("keyring: keyslot missing signature")
		}
		signed = blob[:sigStart]
		sig = blob[sigStart : sigStart+ed25519.SignatureSize]
	}
	return masters, writers, wrapped, memberBlob, signed, sig, nil
}

// ResealKeyslot rewrites a keyslot's membership for the data key dek (already
// recovered from old by the caller), enforcing master authority:
//
//   - If old pins masters, by MUST be one of those current masters ([ErrNotMaster]
//     otherwise) and the new membership must also pin masters (no silent downgrade
//     to flat), signed by by.
//   - If old is flat, anyone who could unwrap may rewrite it; pass masters to
//     upgrade to master protection, in which case by must be one of the new masters.
//
// This is the single choke point that makes "only a master can add or remove
// recipients, writers, and masters" hold.
func ResealKeyslot(old []byte, by Identity, dek []byte, m Membership) ([]byte, error) {
	current, _, _, _, _, _, err := parseKeyslot(old)
	if err != nil {
		return nil, err
	}
	var signWith MasterIdentity
	switch {
	case len(current) > 0: // master-protected: only a current master may change it
		mi, ok := by.(MasterIdentity)
		if !ok || !pubIn(mi.masterPub(), current) {
			return nil, ErrNotMaster
		}
		if len(m.Masters) == 0 {
			return nil, errors.New("keyring: cannot drop all masters from a master-protected keyslot")
		}
		signWith = mi
	case len(m.Masters) > 0: // flat → master upgrade: the actor must be one of the new masters
		mi, ok := by.(MasterIdentity)
		if !ok {
			return nil, ErrNotMaster
		}
		signWith = mi
	}
	return SealKeyslot(dek, m, signWith)
}

func masterPinned(pub ed25519.PublicKey, masters []MasterRecipient) bool {
	for _, m := range masters {
		if bytes.Equal(m.masterPub(), pub) {
			return true
		}
	}
	return false
}

func pubIn(pub ed25519.PublicKey, set []ed25519.PublicKey) bool {
	for _, p := range set {
		if bytes.Equal(p, pub) {
			return true
		}
	}
	return false
}

// Member is one entry of a container's membership, for enumeration or display by
// an admin (see [Members]): the Role, the public Key form (an authorized_keys
// line or an age1… recipient), and an optional human Label (e.g. the SSH key
// comment).
type Member struct {
	Role  string // "master" | "writer" | "member"
	Key   string
	Label string
}

const (
	roleMaster byte = 0
	roleWriter byte = 1
	roleMember byte = 2
)

func roleName(b byte) string {
	switch b {
	case roleMaster:
		return "master"
	case roleWriter:
		return "writer"
	default:
		return "member"
	}
}

// Members lists the membership recorded in a master-protected keyslot for an
// admin. by MUST be one of the keyslot's current masters — the membership record
// is sealed to the masters only — else [ErrNotMaster]. A flat (master-less)
// keyslot has no admin tier and no membership record, so Members returns
// ErrNotMaster for it too. The set returned is the masters, writers, and members
// last written by create or [ResealKeyslot]; passphrase recipients, which have no
// enumerable public key, are not listed.
func Members(blob []byte, by MasterIdentity) ([]Member, error) {
	masters, _, _, memberBlob, signed, sig, err := parseKeyslot(blob)
	if err != nil {
		return nil, err
	}
	if len(masters) == 0 {
		return nil, ErrNotMaster // flat keyslot: no admin tier, no membership record
	}
	if by == nil || !pubIn(by.masterPub(), masters) {
		return nil, ErrNotMaster
	}
	// Defense in depth: the record is signed by a master and sealed to the masters,
	// so reject a keyslot whose master signature does not verify before trusting the
	// parsed master set. The seal (Unwrap below) is the real gate — only a genuine
	// master can decrypt it — but a bad signature means a corrupt or forged keyslot.
	if sig == nil || !VerifyState(masters, signed, sig) {
		return nil, ErrUnauthorizedKeyslot
	}
	if len(memberBlob) == 0 {
		return nil, errors.New("keyring: keyslot carries no membership record")
	}
	plain, err := Unwrap(memberBlob, by)
	if err != nil {
		return nil, err
	}
	return unmarshalMembers(plain)
}

// marshalMembers serialises the public forms of m's parties — the plaintext
// [SealKeyslot] seals to the masters. Recipients with no enumerable public form
// (passphrase) are skipped. Layout: uint16 count, then per entry a role byte and
// two uint16-length-prefixed strings (key, label). It errors rather than silently
// truncate when the count or any field would overflow its uint16 length — so the
// wire format can never misframe itself (an over-long SSH key comment, say).
func marshalMembers(m Membership) ([]byte, error) {
	type ent struct {
		role       byte
		key, label string
	}
	var ents []ent
	add := func(role byte, r Recipient) {
		if k, l := r.publicForm(); k != "" {
			ents = append(ents, ent{role, k, l})
		}
	}
	for _, r := range m.Masters {
		add(roleMaster, r)
	}
	for _, r := range m.Writers {
		add(roleWriter, r)
	}
	for _, r := range m.Members {
		add(roleMember, r)
	}
	if len(ents) > 65535 {
		return nil, errors.New("keyring: too many members to record (max 65535)")
	}
	buf := make([]byte, 2)
	binary.LittleEndian.PutUint16(buf, uint16(len(ents)))
	for _, e := range ents {
		if len(e.key) > 65535 || len(e.label) > 65535 {
			return nil, errors.New("keyring: member key or label exceeds 65535 bytes")
		}
		buf = append(buf, e.role)
		buf = appendString16(buf, e.key)
		buf = appendString16(buf, e.label)
	}
	return buf, nil
}

func appendString16(buf []byte, s string) []byte {
	var l [2]byte
	binary.LittleEndian.PutUint16(l[:], uint16(len(s)))
	buf = append(buf, l[:]...)
	return append(buf, s...)
}

// unmarshalMembers decodes the plaintext produced by [marshalMembers], bounding
// every length against the buffer.
func unmarshalMembers(b []byte) ([]Member, error) {
	if len(b) < 2 {
		return nil, errors.New("keyring: member list truncated")
	}
	n := int(binary.LittleEndian.Uint16(b[:2]))
	off := 2
	readStr := func() (string, error) {
		if off+2 > len(b) {
			return "", errors.New("keyring: member list truncated")
		}
		l := int(binary.LittleEndian.Uint16(b[off : off+2]))
		off += 2
		if off+l > len(b) {
			return "", errors.New("keyring: member list truncated")
		}
		s := string(b[off : off+l])
		off += l
		return s, nil
	}
	out := make([]Member, 0, n)
	for range n {
		if off >= len(b) {
			return nil, errors.New("keyring: member list truncated")
		}
		role := b[off]
		off++
		key, err := readStr()
		if err != nil {
			return nil, err
		}
		label, err := readStr()
		if err != nil {
			return nil, err
		}
		out = append(out, Member{Role: roleName(role), Key: key, Label: label})
	}
	return out, nil
}

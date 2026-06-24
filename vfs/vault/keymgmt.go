package vault

// keymgmt.go provides at-rest recipient management for a recipients-encrypted
// database (one created with [Options.Recipients]). Both operations work on a
// closed database file directly — never through a live connection — because
// rewriting the keyslot or re-encrypting pages underneath an open database would
// race its transactions. They mirror the model of age / cryptsetup tools, which
// re-key a container while it is not mounted.
//
// Two levels of revocation:
//
//   - [Rewrap] changes who is on the access list without re-encrypting. It is
//     O(1): the existing data key is recovered with one current identity and
//     re-wrapped to the new recipient set. A removed recipient who already knew
//     the data key can still decrypt data they had — this manages access, not
//     cryptography.
//   - [Rekey] re-encrypts every page under a fresh data key and wraps it to the
//     new set. It is O(database) and is true cryptographic revocation: a removed
//     recipient can read nothing afterward, even with the old key.

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gosqlite.org/crypto/keyring"
	"gosqlite.org/vfs/crypto"
)

// Rewrap changes who can open an existing recipients-encrypted database without
// re-encrypting its contents: the data key is recovered with by and re-wrapped
// to the new membership (the masters and members), so the cost is O(1)
// regardless of database size.
//
// For a master-protected database (created with [Options.Masters]) by must be
// one of the current masters and the new master set must be non-empty, else
// [ErrNotMaster]; this is what makes "only a master can add or remove recipients
// and masters" hold. For a flat database (no masters) any recipient identity may
// rewrap; pass masters to upgrade it to master protection.
//
// Rewrap manages the access list, not the cryptography: because the data key is
// unchanged, a party removed from the membership who already learned that key
// can still decrypt data they previously had. To make removal cryptographically
// effective, use [Rekey].
//
// The database must be closed (not open in this process) and encrypted to
// recipients; a raw-key database ([Options.Key]) has no keyslot and is rejected.
func Rewrap(path string, by keyring.Identity, writeAs keyring.WriterIdentity, m keyring.Membership) error {
	c, err := openAtRest(path, by)
	if err != nil {
		return err
	}
	defer closeContainer(c, &err)
	if c.keyslotOffset == 0 {
		return errors.New("vault: not a recipients-encrypted database (nothing to rewrap)")
	}
	if err = c.applyMembership(m, writeAs); err != nil {
		return err
	}
	blob, err := keyring.ResealKeyslot(c.keyslotBlob, by, c.dek, m)
	if err != nil {
		return mapKeyslotErr(err)
	}
	if err = c.installKeyslot(blob); err != nil {
		return err
	}
	c.dirty = true
	return c.commit()
}

// applyMembership prepares the container to re-commit under a new membership: it
// fixes the writer that re-signs the superblock (authenticated databases only).
// The authenticated status itself cannot change here — toggling it on would
// require re-hashing every slot — so the writer set may change (staying
// non-empty) but a non-authenticated database cannot gain writers via key
// management (recreate it with [Options.Writers]).
func (c *container) applyMembership(m keyring.Membership, writeAs keyring.WriterIdentity) error {
	switch {
	case c.authenticated:
		if len(m.Writers) == 0 {
			return errors.New("vault: an authenticated database must keep at least one writer")
		}
		if !keyring.WriterAuthorized(m.Writers, writeAs) {
			return errors.New("vault: WriteAs must be one of the new Writers to re-sign the database")
		}
		c.writeAs = writeAs
	case len(m.Writers) > 0:
		return errors.New("vault: cannot add writers to a non-authenticated database (recreate it with Options.Writers)")
	}
	return nil
}

// Rekey re-encrypts an existing recipients-encrypted database under a fresh
// random data key and wraps that key to the new membership. Unlike [Rewrap] it
// rewrites every stored page, so a party dropped from the membership can no
// longer read any data even with the old key or an old keyslot — true
// cryptographic revocation, at O(database size), and the only way to truly
// remove a master. The cipher (Adiantum / AES-XTS) is preserved; only the key
// changes. The master gate is the same as [Rewrap]: for a master-protected
// database by must be a current master ([ErrNotMaster] otherwise).
//
// The database must be closed and encrypted to recipients.
func Rekey(path string, by keyring.Identity, writeAs keyring.WriterIdentity, m keyring.Membership) error {
	c, err := openAtRest(path, by)
	if err != nil {
		return err
	}
	defer closeContainer(c, &err)
	if c.keyslotOffset == 0 {
		return errors.New("vault: not a recipients-encrypted database")
	}
	if err = c.applyMembership(m, writeAs); err != nil {
		return err
	}

	kind, ok := cipherForEnc(c.enc)
	if !ok {
		return fmt.Errorf("vault: unknown on-disk cipher marker %d", c.enc)
	}
	newDEK := make([]byte, crypto.KeyLen(kind))
	if _, err = rand.Read(newDEK); err != nil {
		return err
	}
	newCipher, err := crypto.NewCipher(kind, newDEK)
	if err != nil {
		return err
	}
	// Seal the new keyslot first (this runs the master gate), so an unauthorized
	// caller fails before the database is rewritten.
	blob, err := keyring.ResealKeyslot(c.keyslotBlob, by, newDEK, m)
	if err != nil {
		return mapKeyslotErr(err)
	}

	// Re-encrypt every materialised page: decrypt with the old key, then re-store
	// under the new key into a fresh copy-on-write slot (the old slot is released
	// at commit). Sparse pages carry no ciphertext and stay sparse. loadPage uses
	// c.cipher, so the old cipher must be installed when reading each page and the
	// new one when storing it; storePage only touches the page it writes, so the
	// next read still sees the untouched old slots.
	oldCipher := c.cipher
	for pageNo := uint64(0); pageNo < uint64(len(c.dir)); pageNo++ {
		if c.dir[pageNo].physOffset == 0 {
			continue
		}
		c.cipher = oldCipher
		page, lerr := c.loadPage(pageNo)
		if lerr != nil {
			return lerr
		}
		c.cipher = newCipher
		if serr := c.storePage(pageNo, page); serr != nil {
			return serr
		}
	}

	c.cipher = newCipher
	c.dek = newDEK
	if err = c.installKeyslot(blob); err != nil {
		return err
	}
	c.dirty = true
	return c.commit()
}

// Member is one entry of a container's membership, for enumeration or display by
// an admin (see [Members]). It re-exports [keyring.Member].
type Member = keyring.Member

// Members lists the full membership — masters, writers, and read-only members,
// each with its public key and optional label — of a recipients-encrypted
// database. by MUST be one of the database's current masters: the membership
// record is sealed to the masters, so writers and read-only members cannot
// enumerate it and get [keyring.ErrNotMaster], as does a flat database with no
// admin tier. It answers "who has access?", which the underlying age envelope
// cannot (a read-only recipient is otherwise unrecoverable from the keyslot), so
// an admin can recompute the set before [Rewrap] / [Rekey].
//
// The database must be closed (not open in this process), like [Rewrap] and
// [Rekey], and encrypted to recipients with masters pinned; a raw-key or flat
// database has no membership record. Passphrase recipients, which have no
// enumerable public key, are not listed.
func Members(path string, by keyring.Identity) (members []Member, err error) {
	master, ok := by.(keyring.MasterIdentity)
	if !ok {
		return nil, keyring.ErrNotMaster // only a master may enumerate the membership
	}
	c, err := openAtRest(path, by)
	if err != nil {
		return nil, err
	}
	defer closeContainer(c, &err)
	if c.keyslotOffset == 0 {
		return nil, errors.New("vault: not a recipients-encrypted database (no membership)")
	}
	members, err = keyring.Members(c.keyslotBlob, master)
	return members, err
}

// installKeyslot writes a freshly sealed keyslot to a new block and repoints the
// container at it. The previous keyslot block is left unreferenced and reclaimed
// by the next open's allocator rebuild (the same copy-on-write discipline the
// directory and data slots use), so a crash before the superblock flip leaves
// the old keyslot authoritative.
func (c *container) installKeyslot(blob []byte) error {
	off, err := c.writeKeyslot(blob)
	if err != nil {
		return err
	}
	c.keyslotOffset = off
	c.keyslotBlob = blob
	return nil
}

// openAtRest opens an existing encrypted container directly over its file for a
// key-management operation, recovering the data key with identity. It refuses a
// database currently open in this process (whose in-memory container this would
// bypass) and an empty/absent file (which the create path would otherwise turn
// into a new plaintext container).
func openAtRest(path string, identity keyring.Identity) (*container, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	// Reserve the path for the whole operation so a concurrent Open cannot register
	// a live container over it mid-rewrite; closeContainer releases it.
	if !reservePath(abs) {
		return nil, fmt.Errorf("vault: %q is open or busy; close it before key management", path)
	}

	file, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		releasePath(abs)
		return nil, err
	}
	fi, err := file.Stat()
	if err != nil {
		_ = file.Close()
		releasePath(abs)
		return nil, err
	}
	if fi.Size() == 0 {
		_ = file.Close()
		releasePath(abs)
		return nil, fmt.Errorf("vault: %q is empty, not an encrypted database", path)
	}

	kc := keyConfig{identities: []keyring.Identity{identity}}
	c, err := newContainerOver(fileBacking{file}, false, defaultBlockSize, defaultPageSize, CompressionDefault, kc)
	if err != nil {
		releasePath(abs)
		return nil, err // newContainerOver closes the file on error
	}
	c.reserved = abs
	return c, nil
}

// closeContainer closes the backing and releases any reserved path, preserving an
// earlier error over a close error (so a successful operation still surfaces a
// close failure).
func closeContainer(c *container, err *error) {
	if c.reserved != "" {
		releasePath(c.reserved)
		c.reserved = ""
	}
	if cerr := c.back.Close(); cerr != nil && *err == nil {
		*err = cerr
	}
}

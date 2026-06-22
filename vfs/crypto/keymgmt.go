package crypto

// keymgmt.go manages the membership of a recipients database by rewriting its
// keyslot sidecar — without re-encrypting the database. Because the data key is
// unchanged, this is O(1) and crash-safe (the sidecar write is atomic), and a
// live connection keeps working under the unchanged key.
//
// There is deliberately no Rekey here. True cryptographic revocation needs a
// fresh data key and a re-encryption of every page, but the data key lives in a
// DETACHED sidecar: re-encrypting the database file and rewriting the sidecar are
// two files with no atomic cross-file update, so a crash mid-operation would
// leave them describing different keys — unrecoverable. (gosqlite.org/vfs/compress
// keeps the keyslot inside its container, so it can Rekey crash-safely; use it
// when you need to cryptographically revoke a recipient, or re-create the file.)

import (
	"errors"
	"fmt"
	"os"

	"gosqlite.org/crypto/keyring"
)

// Rewrap changes who can open the recipients database at path, re-sealing its
// keyslot sidecar to a new membership WITHOUT re-encrypting the database. The
// data key is recovered with by and re-wrapped to masters + members, so it is
// O(1) regardless of database size.
//
// For a master-protected database (created with [Options.Masters]) by must be one
// of the current masters and the new master set must be non-empty, else
// [ErrNotMaster]; for a flat database any recipient identity may rewrap, and
// passing masters upgrades it to master protection.
//
// Rewrap manages the access list, not the cryptography: because the data key is
// unchanged, a party removed from the membership who already learned that key can
// still decrypt data they previously had. The sidecar write is atomic, so the
// database stays openable across a crash (under the old or new keyslot), and a
// live connection is unaffected (it holds the unchanged key in memory).
func Rewrap(path string, by keyring.Identity, masters []keyring.MasterRecipient, members []keyring.Recipient) error {
	sidecar := path + keyslotSuffix
	blob, err := readSidecar(sidecar)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("crypto: %q has no keyslot sidecar (not a recipients database)", path)
	}
	if err != nil {
		return err
	}
	// Recover the data key with by (the master gate is enforced by ResealKeyslot
	// against the keyslot's current masters, so no trusted-master pin is needed here).
	dek, _, err := keyring.OpenKeyslot(blob, nil, by)
	if err != nil {
		return mapKeyslotErr(err)
	}
	newBlob, err := keyring.ResealKeyslot(blob, by, dek, keyring.Membership{Masters: masters, Members: members})
	if err != nil {
		return mapKeyslotErr(err)
	}
	return writeSidecar(sidecar, newBlob)
}

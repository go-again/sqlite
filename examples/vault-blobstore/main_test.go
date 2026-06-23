package main

import "testing"

// TestVaultBlobstore pins the composition: a blobstore over a multi-recipient,
// compressed, authenticated vfs/vault database round-trips an object, leaves no
// plaintext on disk, opens under either recipient's identity, and refuses a
// non-recipient. It guards against either side regressing the contract that the
// store is VFS-agnostic.
func TestVaultBlobstore(t *testing.T) {
	if _, err := roundTrip(t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

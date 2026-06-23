package main

import "testing"

// TestEncryptedBlobstore pins the composition: a blobstore over a vfs/crypto
// database round-trips an object and leaves no plaintext on disk. It guards
// against either side regressing the contract that the store is VFS-agnostic.
func TestEncryptedBlobstore(t *testing.T) {
	if _, err := roundTrip(t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

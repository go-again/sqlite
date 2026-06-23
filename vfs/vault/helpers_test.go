package vault

import (
	"crypto/rand"
	"testing"
)

// randKey returns a fresh 32-byte cipher key (the Adiantum/default size) for tests.
func randKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

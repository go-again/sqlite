// Helper shared by the vendored mattn upstream tests. Originally defined
// inside mattn's sqlite3_test.go alongside dozens of tests we couldn't
// vendor (mattn-specific Error / SQLiteConn internals). Extracted so the
// tests that DO compile can find the helper.
//
// See dev/upstream/mattn.md for the divergence list.

//go:build mattn_upstream

package sqlite

import (
	"os"
	"testing"
)

func TempFilename(t testing.TB) string {
	t.Helper()
	f, err := os.CreateTemp("", "go-sqlite3-test-")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	return f.Name()
}

package cksm_test

import (
	"strings"
	"testing"

	"gosqlite.org/vfs/cksm"
)

// TestNew_WrapVFSNotRegistered pins the negative-path error for the
// chaining recipe. Without this test, a regression that silently
// accepted an unknown VFS name and quietly proceeded with the system
// default would slip through. Round 7 added this after the audit
// pointed out the error path was untested.
func TestNew_WrapVFSNotRegistered(t *testing.T) {
	_, fs, err := cksm.New(cksm.Options{WrapVFS: "does-not-exist-12345"})
	if err == nil {
		t.Cleanup(func() { _ = fs.Close() })
		t.Fatal("New with unknown WrapVFS: want error, got nil")
	}
	if !strings.Contains(err.Error(), "does-not-exist-12345") {
		t.Errorf("error %q should name the missing VFS so the caller can diagnose", err)
	}
	if !strings.Contains(err.Error(), "not registered") {
		t.Errorf("error %q should mention 'not registered' so the cause is clear", err)
	}
}

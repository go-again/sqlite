//go:build race

package crypto_test

import (
	"fmt"
	"os"
	"testing"
)

// TestMain short-circuits the whole package when -race is in effect.
//
// vfs/crypto's io-method trampolines invoke modernc's transpiled
// _unixRead / _unixWrite through libc.Xpread / Xpwrite, which perform
// uintptr-based pointer arithmetic that Go's checkptr analyzer
// (enabled by -race) rejects with "checkptr: pointer arithmetic
// result points to invalid allocation". The arithmetic is correct in
// the transpiled C ABI sense; checkptr just can't see that. Same
// upstream cause as the root package's LoadExtension skip, but here
// we skip the whole package because every test path exercises the
// trampolines.
//
// Drop this file when checkptr learns the pattern.
func TestMain(m *testing.M) {
	fmt.Fprintln(os.Stderr, "vfs/crypto: skipping all tests under -race "+
		"(checkptr can't reason about transpiled C pointer math through libc.Xpread)")
	os.Exit(0)
}

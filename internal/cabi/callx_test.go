package cabi_test

import (
	"testing"

	"gosqlite.org/internal/cabi"
	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"
)

// CallX* wrappers exist to call a uintptr-encoded function pointer
// recovered from a C-side struct. The unit tests below stash a Go
// closure via FuncPointer and dispatch through each CallX* helper to
// confirm the signature alignment. Using libc.NewTLS() / a fake pFile
// keeps the test self-contained — none of the SQLite I/O methods is
// actually invoked.

func TestCallXClose_Dispatch(t *testing.T) {
	tls := libc.NewTLS()
	defer tls.Close()
	const sentinel = int32(17)
	fp := cabi.FuncPointer(func(*libc.TLS, uintptr) int32 { return sentinel })
	if got := cabi.CallXClose(tls, fp, 0xdead); got != sentinel {
		t.Errorf("CallXClose dispatched return = %d, want %d", got, sentinel)
	}
}

func TestCallXRead_Dispatch(t *testing.T) {
	tls := libc.NewTLS()
	defer tls.Close()
	const sentinel = int32(SQLITE_OK)
	var gotPFile, gotBuf uintptr
	var gotAmt int32
	var gotOff sqlite3.Tsqlite3_int64
	fn := func(_ *libc.TLS, pFile, buf uintptr, amt int32, off sqlite3.Tsqlite3_int64) int32 {
		gotPFile, gotBuf, gotAmt, gotOff = pFile, buf, amt, off
		return sentinel
	}
	fp := cabi.FuncPointer(fn)
	if rc := cabi.CallXRead(tls, fp, 0x1, 0x2, 4096, 8192); rc != sentinel {
		t.Errorf("CallXRead rc = %d, want %d", rc, sentinel)
	}
	if gotPFile != 0x1 || gotBuf != 0x2 || gotAmt != 4096 || gotOff != 8192 {
		t.Errorf("CallXRead args = (%#x,%#x,%d,%d), want (1,2,4096,8192)",
			gotPFile, gotBuf, gotAmt, gotOff)
	}
}

const SQLITE_OK = 0

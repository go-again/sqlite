package sqlite // import "github.com/go-again/sqlite"

import (
	"sync"
	"unsafe"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"
)

// This file holds the process-global SQLite C-API helpers that are not tied to a
// connection: keyword and compile-option introspection, and the GLOB/LIKE/
// complete string utilities.
//
// libc.TLS models per-thread C runtime state, so rather than share one across
// goroutines these helpers funnel through a single mutex-guarded instance. The
// calls are infrequent and cheap (a static-table lookup, a pattern match), so
// serializing them is not a contention concern.
//
// The memory-accounting and heap-limit interfaces (sqlite3_memory_used,
// soft/hard_heap_limit64, status64) are intentionally NOT wrapped: modernc's
// build disables SQLite's memstat subsystem (sqlite3_memory_used returns 0), so
// those calls are no-ops here and a typed wrapper would only mislead. Use Go's
// own runtime/metrics for process memory instead.

var (
	pkgTLSMu  sync.Mutex
	pkgTLSVal *libc.TLS
)

func lockPkgTLS() *libc.TLS {
	pkgTLSMu.Lock()
	if pkgTLSVal == nil {
		pkgTLSVal = libc.NewTLS()
	}
	return pkgTLSVal
}

func unlockPkgTLS() { pkgTLSMu.Unlock() }

// KeywordCount returns the number of distinct SQL keywords this build of SQLite
// recognizes.
//
// https://sqlite.org/c3ref/keyword_check.html
func KeywordCount() int {
	tls := lockPkgTLS()
	defer unlockPkgTLS()
	return int(sqlite3.Xsqlite3_keyword_count(tls))
}

// KeywordName returns the i-th SQL keyword (0-based), or ("", false) if i is out
// of range. Iterate 0..KeywordCount()-1 to enumerate the reserved-word set —
// the authoritative source for identifier quoting, instead of guessing.
//
// https://sqlite.org/c3ref/keyword_check.html
func KeywordName(i int) (string, bool) {
	tls := lockPkgTLS()
	defer unlockPkgTLS()
	pz := tls.Alloc(int(ptrSize))
	defer tls.Free(int(ptrSize))
	pn := tls.Alloc(4)
	defer tls.Free(4)
	if sqlite3.Xsqlite3_keyword_name(tls, int32(i), pz, pn) != sqlite3.SQLITE_OK {
		return "", false
	}
	z := *(*uintptr)(unsafe.Pointer(pz))
	n := *(*int32)(unsafe.Pointer(pn))
	return string(libc.GoBytes(z, int(n))), true
}

// IsKeyword reports whether s is a reserved SQL keyword in this build (case-
// insensitive). An identifier that is a keyword must be quoted.
//
// https://sqlite.org/c3ref/keyword_check.html
func IsKeyword(s string) bool {
	tls := lockPkgTLS()
	defer unlockPkgTLS()
	z, err := libc.CString(s)
	if err != nil {
		return false
	}
	defer libc.Xfree(tls, z)
	return sqlite3.Xsqlite3_keyword_check(tls, z, int32(len(s))) != 0
}

// CompileOptionUsed reports whether SQLite was compiled with the given option
// (the SQLITE_ prefix is optional), e.g. "ENABLE_FTS5".
//
// https://sqlite.org/c3ref/compileoption_get.html
func CompileOptionUsed(name string) bool {
	tls := lockPkgTLS()
	defer unlockPkgTLS()
	z, err := libc.CString(name)
	if err != nil {
		return false
	}
	defer libc.Xfree(tls, z)
	return sqlite3.Xsqlite3_compileoption_used(tls, z) != 0
}

// CompileOptionGet returns the i-th compile-time option (0-based), or
// ("", false) past the end. Iterate to enumerate the build's feature set.
//
// https://sqlite.org/c3ref/compileoption_get.html
func CompileOptionGet(i int) (string, bool) {
	tls := lockPkgTLS()
	defer unlockPkgTLS()
	p := sqlite3.Xsqlite3_compileoption_get(tls, int32(i))
	if p == 0 {
		return "", false
	}
	return libc.GoString(p), true
}

// StrGlob reports whether s matches the GLOB pattern, using SQLite's exact GLOB
// semantics (case-sensitive, * and ? wildcards) — the same matching the GLOB
// operator performs, without running a query.
//
// https://sqlite.org/c3ref/strglob.html
func StrGlob(pattern, s string) bool {
	tls := lockPkgTLS()
	defer unlockPkgTLS()
	zp, err := libc.CString(pattern)
	if err != nil {
		return false
	}
	defer libc.Xfree(tls, zp)
	zs, err := libc.CString(s)
	if err != nil {
		return false
	}
	defer libc.Xfree(tls, zs)
	return sqlite3.Xsqlite3_strglob(tls, zp, zs) == 0 // 0 == match
}

// StrLike reports whether s matches the LIKE pattern (case-insensitive for ASCII,
// % and _ wildcards). escape is the LIKE ESCAPE character, or 0 for none.
//
// https://sqlite.org/c3ref/strlike.html
func StrLike(pattern, s string, escape byte) bool {
	tls := lockPkgTLS()
	defer unlockPkgTLS()
	zp, err := libc.CString(pattern)
	if err != nil {
		return false
	}
	defer libc.Xfree(tls, zp)
	zs, err := libc.CString(s)
	if err != nil {
		return false
	}
	defer libc.Xfree(tls, zs)
	return sqlite3.Xsqlite3_strlike(tls, zp, zs, uint32(escape)) == 0 // 0 == match
}

// Complete reports whether sql is one or more complete statements — i.e. it ends
// at a statement boundary (a ; not inside a string/comment/trigger body). Use it
// to decide whether a REPL has a full statement to execute or needs more input.
//
// https://sqlite.org/c3ref/complete.html
func Complete(sql string) bool {
	tls := lockPkgTLS()
	defer unlockPkgTLS()
	z, err := libc.CString(sql)
	if err != nil {
		return false
	}
	defer libc.Xfree(tls, z)
	return sqlite3.Xsqlite3_complete(tls, z) != 0
}

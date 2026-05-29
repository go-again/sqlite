package crypto

import "unsafe"

// cFuncPointer is the established modernc-trampoline trick: take a
// Go function value and return its entry-point address as a uintptr
// the transpiled SQLite C code can store and later invoke. Mirrors
// the implementation in the root package's sqlite.go (private there;
// duplicated here so vfs/crypto stays self-contained — both files
// must change together if the Go function-value ABI changes).
//
// Why duplicated rather than imported: making it public in the root
// package would expose an unsafe internal as API surface for a single
// out-of-package consumer. The helper is small, and "Go function
// value ABI" is a stable assumption in our existing modernc-derived
// code (vtab.go, hooks.go also rely on it).
func cFuncPointer[T any](f T) uintptr {
	return *(*uintptr)(unsafe.Pointer(&struct{ f T }{f}))
}

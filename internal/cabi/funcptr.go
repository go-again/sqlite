// Package cabi holds tiny helpers for bridging Go function values to
// the modernc-transpiled C ABI used by modernc.org/sqlite/lib. The
// shape is unstable in Go terms (relies on the Go runtime's function-
// value memory layout described at https://golang.org/s/go11func) but
// stable in practice — the root package and vfs/crypto both rely on
// it for VFS / hook / UDF callback registration.
//
// Internal package by design: consumers should not depend on this
// shape, since it leaks Go-runtime ABI choices.
package cabi

import "unsafe"

// FuncPointer converts a Go function value to a uintptr the transpiled
// SQLite C code can store in a function-pointer slot and later invoke.
// The inverse of the consumer-side dance the transpiled C uses to
// call a uintptr-encoded function pointer from Go (see
// _sqlite3OsRead in modernc.org/sqlite/lib for an example).
//
// Implementation notes:
//  1. The argument is wrapped in a struct so its address goes into
//     read-only data and won't move.
//  2. unsafe.Pointer rule #1 converts the struct address to a
//     *uintptr without violating pointer arithmetic constraints.
//  3. The result is the function value's entry-point address.
//
// Don't try to "fix" the cast — the pattern is the contract for
// talking to modernc.org/sqlite/lib's transpiled function pointers.
func FuncPointer[T any](f T) uintptr {
	return *(*uintptr)(unsafe.Pointer(&struct{ f T }{f}))
}

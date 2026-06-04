// Package cabi consolidates the small helpers our packages need for
// talking to the modernc-transpiled SQLite C ABI. Everything here is
// internal to the module — the helpers reach into Go-runtime ABI
// details that downstream consumers must never depend on.
//
// What lives here:
//
//   - [FuncPointer] / [AsFunc] — the unsafe.Pointer dance that turns a
//     Go function value into a uintptr the transpiled C can store in a
//     function-pointer slot, and the inverse for calling one back.
//     Relies on the Go runtime's function-value memory layout
//     (https://golang.org/s/go11func) — stable in practice, unstable
//     in theory.
//   - [Registry][T] — a generic token→*T map with an atomic counter,
//     used by every VFS sub-package to thread FS instances through a
//     uintptr that SQLite stores in `FpAppData` or a per-file tail
//     allocation.
//   - [UniqueName] — process-global counter for handing out unique
//     VFS / module names at registration time.
//   - The CallX* family in callx.go — dispatchers for each
//     [sqlite3.Tsqlite3_io_methods] slot, used by VFS sub-packages
//     that wrap-and-forward to an existing VFS.
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

// AsFunc is the consumer-side inverse of [FuncPointer]: turn a stored
// uintptr (typically read out of an [sqlite3.Tsqlite3_io_methods] slot)
// back into a callable Go function value of the requested signature.
//
// The pattern matches the one modernc's transpiled code uses
// internally (see _sqlite3OsRead in modernc.org/sqlite/lib for an
// example): wrap the uintptr in a struct, take its address, cast
// through unsafe.Pointer to a *F, and deref to get the function value.
// Each VFS-layer call site specializes this on a distinct signature
// via a thin callX* helper; consolidating asFunc here keeps the
// fragile cast in a single place across the module.
func AsFunc[F any](fp uintptr) F {
	return *(*F)(unsafe.Pointer(&struct{ uintptr }{fp}))
}

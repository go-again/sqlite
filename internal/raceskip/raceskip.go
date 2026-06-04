// Package raceskip exposes a single Enabled bool constant whose value
// reflects whether the build was produced with -race. Tests use it to
// skip cases that touch modernc-transpiled code paths Go's checkptr
// analyzer rejects (xtra-tight pointer arithmetic on transpiled C).
// LoadExtension is the canonical example; the BLOB-API and VFS-handle
// paths trip the same analyzer in different ways.
//
// Drop the helper (or just stop referencing it) when modernc-transpiled
// pointer arithmetic learns to satisfy checkptr.
package raceskip

// Enabled is true in -race builds, false otherwise. See the two
// raceenabled_*.go files for the build-tag wiring.

package sqlite

import "runtime"

// isDarwin / isWindows let tests skip behavior whose underlying libc shim
// isn't implemented in modernc.org/libc on that platform:
//
//   - darwin: Xdlopen ("Xdlopen: TODOTODO ...") — used by LoadExtension.
//   - windows: XLoadLibraryW ("XLoadLibraryW: TODOTODO ...") — same path.
//
// Both shims abort the test binary rather than return an error, so tests
// that exercise the LoadExtension path must skip on these platforms even
// when checking the "disabled" negative case.
var (
	isDarwin  = runtime.GOOS == "darwin"
	isWindows = runtime.GOOS == "windows"
)

// loadExtensionUnsupported is true on platforms where modernc.org/libc's
// dynamic-loader shim aborts rather than returning an error. Use this to
// gate LoadExtension-touching tests.
//
// Exported (lowercase-but-package-level) for use by sibling tests only.
// Not stable API — if you import this package as a library, do not
// depend on this variable; it may move or change semantics when the
// upstream libc shim grows real dlopen support.
var loadExtensionUnsupported = isDarwin || isWindows

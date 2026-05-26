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
var loadExtensionUnsupported = isDarwin || isWindows

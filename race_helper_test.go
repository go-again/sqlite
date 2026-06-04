package sqlite

import "github.com/go-again/sqlite/internal/raceskip"

// raceEnabled is true in -race builds. See [raceskip.Enabled]; this is a
// thin local alias to keep call sites in this test package terse.
//
// Tests use this to skip cases that touch modernc-transpiled code paths
// known to trip Go's checkptr analyzer (which -race enables). LoadExtension
// is the canonical example — modernc's _sqlite3LoadExtension does pointer
// arithmetic that fails checkptr, even though the underlying C code is
// correct.
const raceEnabled = raceskip.Enabled

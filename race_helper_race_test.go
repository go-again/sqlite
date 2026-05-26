//go:build race

package sqlite

// raceEnabled = true in -race builds. See race_helper_test.go for the
// non-race counterpart and the rationale for the flag.
const raceEnabled = true

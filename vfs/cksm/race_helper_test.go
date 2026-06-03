//go:build !race

package cksm_test

// raceEnabled mirrors the helper used elsewhere in the repo to skip
// VFS open paths under -race on darwin: modernc's
// _sqlite3OsDeviceCharacteristics does pointer arithmetic against
// the io-methods table that Go's checkptr analyzer flags as
// "invalid allocation" even though the table is a stable allocation
// the VFS itself owns. Same upstream pattern that gates
// LoadExtension and (*Conn).OpenBlob in their own race-skip
// helpers. Should be revisitable when modernc relaxes the
// arithmetic.
const raceEnabled = false

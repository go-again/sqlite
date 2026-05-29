//go:build !race

package sqlite_test

// raceEnabledExt mirrors the internal raceEnabled constant for the
// external test package. Encryption-touching tests in config_test.go
// skip themselves under -race because the vfs/crypto trampolines hit
// the same checkptr / libc.Xpread issue that vfs/crypto's own
// package-wide TestMain skips. Fix in checkptr would let us drop it.
const raceEnabledExt = false

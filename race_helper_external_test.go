package sqlite_test

import "github.com/go-again/sqlite/internal/raceskip"

// raceEnabledExt mirrors the internal raceEnabled constant for the
// external test package. Encryption-touching tests in config_test.go
// skip themselves under -race because the vfs/crypto trampolines hit
// the same checkptr / libc.Xpread issue that vfs/crypto's own
// package-wide TestMain skips.
const raceEnabledExt = raceskip.Enabled

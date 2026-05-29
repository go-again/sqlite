//go:build !race

package sqlite_test

// raceEnabledExt is the gorm-package companion of the root
// raceEnabledExt constant. OpenConfig tests that exercise the
// vfs/crypto trampolines (encryption path) skip themselves under
// -race; the underlying issue is the same checkptr / libc.Xpread
// limitation that vfs/crypto's own TestMain skips.
const raceEnabledExt = false

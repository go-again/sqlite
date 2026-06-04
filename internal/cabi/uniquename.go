package cabi

import (
	"fmt"
	"sync/atomic"
)

// uniqueNameSuffix is the process-global counter UniqueName draws
// from. Each VFS sub-package wants a distinct readable name of the
// form `<prefix><hex>`; sharing a single counter is fine because
// only the (prefix, suffix) pair has to be unique.
var uniqueNameSuffix atomic.Uint64

// UniqueName returns "<prefix><hex>" where the suffix is a
// monotonically increasing process-global counter. Matches the
// existing pattern used by every VFS sub-package's New() at
// registration time. Always non-empty.
func UniqueName(prefix string) string {
	return fmt.Sprintf("%s%x", prefix, uniqueNameSuffix.Add(1))
}

package cabi

import (
	"fmt"
	"sync"
	"sync/atomic"
)

// Registry is a generic token → pointer map with a built-in atomic
// token counter. Each VFS sub-package (vfs/crypto, vfs/cksm,
// vfs/mvcc, vfs/memdb) needs the same shape: a process-global
// map[uintptr]*FS guarded by an RWMutex, plus a counter that hands out
// fresh tokens. Lifting it here removes ~25 lines of boilerplate per
// caller.
//
// The token type is uintptr because the trampoline-side reads it back
// from a C-allocated struct field (FpAppData on Tsqlite3_vfs, or the
// tail-allocated perFileState block on Tsqlite3_file). The map keys
// are minted by Register and never zero — zero is reserved to mean
// "unset / not registered".
type Registry[T any] struct {
	next atomic.Uintptr
	mu   sync.RWMutex
	m    map[uintptr]*T
}

// NewRegistry returns an empty Registry. The zero value is also
// usable — Register lazily allocates the map on first use — but a
// constructor makes the call site more readable.
func NewRegistry[T any]() *Registry[T] {
	return &Registry[T]{m: map[uintptr]*T{}}
}

// Register stores v and returns its token. Tokens are unique per
// process for the lifetime of this Registry and never zero.
func (r *Registry[T]) Register(v *T) uintptr {
	tok := r.next.Add(1)
	r.mu.Lock()
	if r.m == nil {
		r.m = map[uintptr]*T{}
	}
	r.m[tok] = v
	r.mu.Unlock()
	return tok
}

// Lookup returns the pointer stored under tok, or nil when tok was
// never minted, has been [Registry.Unregister]ed, or is zero.
func (r *Registry[T]) Lookup(tok uintptr) *T {
	if tok == 0 {
		return nil
	}
	r.mu.RLock()
	v := r.m[tok]
	r.mu.RUnlock()
	return v
}

// Unregister drops tok from the map. Calling Unregister on an
// already-unregistered tok is a no-op.
func (r *Registry[T]) Unregister(tok uintptr) {
	r.mu.Lock()
	delete(r.m, tok)
	r.mu.Unlock()
}

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

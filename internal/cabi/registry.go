package cabi

import (
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

// Range calls fn for every (token, pointer) pair currently registered,
// stopping early if fn returns false. Iteration order is unspecified.
//
// Range holds only a read lock, so fn MUST NOT mutate the Registry
// (Register / Unregister / DeleteWhere would self-deadlock). To delete
// entries while iterating, use [Registry.DeleteWhere] instead. Iteration
// runs over a snapshot of the keys captured under the lock, so entries
// concurrently added or removed by other goroutines after the snapshot
// may or may not be visited.
func (r *Registry[T]) Range(fn func(tok uintptr, v *T) bool) {
	r.mu.RLock()
	toks := make([]uintptr, 0, len(r.m))
	vals := make([]*T, 0, len(r.m))
	for tok, v := range r.m {
		toks = append(toks, tok)
		vals = append(vals, v)
	}
	r.mu.RUnlock()
	for i, tok := range toks {
		if !fn(tok, vals[i]) {
			return
		}
	}
}

// DeleteWhere removes every entry for which pred returns true, taking the
// write lock once for the whole sweep. pred MUST NOT call back into the
// Registry. It is the FS-scoped drain primitive the in-memory VFS Close
// paths need: delete all file handles owned by the closing FS in a single
// locked pass.
func (r *Registry[T]) DeleteWhere(pred func(tok uintptr, v *T) bool) {
	r.mu.Lock()
	for tok, v := range r.m {
		if pred(tok, v) {
			delete(r.m, tok)
		}
	}
	r.mu.Unlock()
}

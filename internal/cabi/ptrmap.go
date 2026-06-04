package cabi

import "sync"

// PtrMap is a thread-safe `uintptr → *T` lookup table whose keys are
// supplied by the caller (not minted internally). Use it when SQLite
// hands you back a pointer it allocated — typically a `pFile` address
// from `sqlite3_file` or a `pVfs` from `sqlite3_vfs` — and you need
// to recover the Go-side owner. Contrast with [Registry][T], which
// mints fresh keys via an internal counter.
//
// Each VFS-wrapping sub-package (vfs/cksm, vfs/crypto) used to define
// its own `fileMap` for this exact purpose. PtrMap consolidates the
// shape so adding a new wrap-forward VFS layer doesn't ship another
// copy.
type PtrMap[T any] struct {
	mu sync.RWMutex
	m  map[uintptr]*T
}

// NewPtrMap returns an empty PtrMap.
func NewPtrMap[T any]() *PtrMap[T] {
	return &PtrMap[T]{m: map[uintptr]*T{}}
}

// Set stores v under tok. Overwrites any existing entry.
func (p *PtrMap[T]) Set(tok uintptr, v *T) {
	p.mu.Lock()
	if p.m == nil {
		p.m = map[uintptr]*T{}
	}
	p.m[tok] = v
	p.mu.Unlock()
}

// Get returns the value stored under tok, or nil if absent or tok == 0.
func (p *PtrMap[T]) Get(tok uintptr) *T {
	if tok == 0 {
		return nil
	}
	p.mu.RLock()
	v := p.m[tok]
	p.mu.RUnlock()
	return v
}

// Delete drops tok from the map. Calling on a missing tok is a no-op.
func (p *PtrMap[T]) Delete(tok uintptr) {
	p.mu.Lock()
	delete(p.m, tok)
	p.mu.Unlock()
}

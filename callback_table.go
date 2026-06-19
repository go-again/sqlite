package sqlite // import "gosqlite.org"

import "sync"

// callbackTable is a process-global registry mapping a minted id to a Go
// callback value. It threads Go closures through the uintptr context slot that
// transpiled-C trampolines hand back: register a closure to get an id, pass
// the id as the C-side context, and lookup it in the trampoline. ids come from
// an idGen and are reclaimed on drop, so the table doesn't grow unboundedly
// across register/drop cycles.
//
// Every registry backing a C callback whose context is an id (changeset apply,
// rtree geometry/query) uses one of these instead of re-declaring the
// {mu, m, ids} shape and hand-rolling its lock + reclaim dance. (The older
// UDF / collation / aggregate registries in sqlite.go predate this and are
// left as-is.)
type callbackTable[T any] struct {
	mu  sync.RWMutex
	m   map[uintptr]T
	ids idGen
}

func newCallbackTable[T any]() *callbackTable[T] {
	return &callbackTable[T]{m: make(map[uintptr]T)}
}

// register stores v under a fresh id and returns it.
func (t *callbackTable[T]) register(v T) uintptr {
	t.mu.Lock()
	defer t.mu.Unlock()
	id := t.ids.next()
	t.m[id] = v
	return id
}

// lookup returns the value stored under id, if present.
func (t *callbackTable[T]) lookup(id uintptr) (T, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	v, ok := t.m[id]
	return v, ok
}

// drop removes the value under id and reclaims the id for reuse.
func (t *callbackTable[T]) drop(id uintptr) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.m, id)
	t.ids.reclaim(id)
}

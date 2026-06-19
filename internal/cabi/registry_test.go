package cabi_test

import (
	"sync"
	"testing"

	"gosqlite.org/internal/cabi"
)

type entry struct{ id int }

func TestRegistry_Roundtrip(t *testing.T) {
	r := cabi.NewRegistry[entry]()
	e := &entry{id: 42}
	tok := r.Register(e)
	if tok == 0 {
		t.Fatal("token must be non-zero")
	}
	if got := r.Lookup(tok); got != e {
		t.Errorf("Lookup(%d)=%p, want %p", tok, got, e)
	}
	r.Unregister(tok)
	if got := r.Lookup(tok); got != nil {
		t.Errorf("Lookup after Unregister: got %p, want nil", got)
	}
}

func TestRegistry_LookupUnknown(t *testing.T) {
	r := cabi.NewRegistry[entry]()
	if got := r.Lookup(0); got != nil {
		t.Errorf("Lookup(0)=%p, want nil", got)
	}
	if got := r.Lookup(99); got != nil {
		t.Errorf("Lookup(99) on empty registry: got %p, want nil", got)
	}
}

func TestRegistry_UnregisterIdempotent(t *testing.T) {
	r := cabi.NewRegistry[entry]()
	tok := r.Register(&entry{id: 1})
	r.Unregister(tok)
	r.Unregister(tok)
}

// Zero-value Registry: per the docstring, the zero value is usable —
// Register lazily allocates the map on first use.
func TestRegistry_ZeroValueUsable(t *testing.T) {
	var r cabi.Registry[entry]
	e := &entry{id: 7}
	tok := r.Register(e)
	if got := r.Lookup(tok); got != e {
		t.Errorf("Lookup on zero-value Registry: got %p, want %p", got, e)
	}
}

func TestRegistry_Range(t *testing.T) {
	r := cabi.NewRegistry[entry]()
	want := map[uintptr]int{}
	for i := range 5 {
		e := &entry{id: i}
		want[r.Register(e)] = i
	}
	got := map[uintptr]int{}
	r.Range(func(tok uintptr, v *entry) bool {
		got[tok] = v.id
		return true
	})
	if len(got) != len(want) {
		t.Fatalf("Range visited %d entries, want %d", len(got), len(want))
	}
	for tok, id := range want {
		if got[tok] != id {
			t.Errorf("Range tok %d: got id %d, want %d", tok, got[tok], id)
		}
	}
}

func TestRegistry_RangeEarlyStop(t *testing.T) {
	r := cabi.NewRegistry[entry]()
	for i := range 10 {
		r.Register(&entry{id: i})
	}
	visited := 0
	r.Range(func(uintptr, *entry) bool {
		visited++
		return visited < 3 // stop after the third entry
	})
	if visited != 3 {
		t.Errorf("Range visited %d entries before stopping, want 3", visited)
	}
}

func TestRegistry_DeleteWhere(t *testing.T) {
	r := cabi.NewRegistry[entry]()
	var evenToks, oddToks []uintptr
	for i := range 10 {
		tok := r.Register(&entry{id: i})
		if i%2 == 0 {
			evenToks = append(evenToks, tok)
		} else {
			oddToks = append(oddToks, tok)
		}
	}
	// Drop the odd-id entries.
	r.DeleteWhere(func(_ uintptr, v *entry) bool {
		return v.id%2 != 0
	})
	for _, tok := range oddToks {
		if r.Lookup(tok) != nil {
			t.Errorf("odd tok %d survived DeleteWhere", tok)
		}
	}
	for _, tok := range evenToks {
		if r.Lookup(tok) == nil {
			t.Errorf("even tok %d wrongly deleted", tok)
		}
	}
}

func TestRegistry_ConcurrentRegister(t *testing.T) {
	r := cabi.NewRegistry[entry]()
	const goroutines = 32
	const perG = 128

	var wg sync.WaitGroup
	tokens := make(chan uintptr, goroutines*perG)
	for g := range goroutines {
		wg.Go(func() {
			for i := range perG {
				tokens <- r.Register(&entry{id: g*perG + i})
			}
		})
	}
	wg.Wait()
	close(tokens)

	seen := map[uintptr]bool{}
	for tok := range tokens {
		if tok == 0 {
			t.Error("concurrent Register handed out token 0")
		}
		if seen[tok] {
			t.Errorf("duplicate token %d", tok)
		}
		seen[tok] = true
	}
	if len(seen) != goroutines*perG {
		t.Errorf("got %d unique tokens, want %d", len(seen), goroutines*perG)
	}
}

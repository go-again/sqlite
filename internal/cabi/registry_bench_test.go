package cabi_test

import (
	"testing"

	"github.com/go-again/sqlite/internal/cabi"
)

type benchEntry struct{ id int }

func BenchmarkRegistry_Register(b *testing.B) {
	r := cabi.NewRegistry[benchEntry]()
	e := &benchEntry{id: 1}
	for b.Loop() {
		r.Register(e)
	}
}

func BenchmarkRegistry_Lookup_Hit(b *testing.B) {
	r := cabi.NewRegistry[benchEntry]()
	tok := r.Register(&benchEntry{id: 1})
	b.ResetTimer()
	for b.Loop() {
		_ = r.Lookup(tok)
	}
}

func BenchmarkRegistry_Lookup_Miss(b *testing.B) {
	r := cabi.NewRegistry[benchEntry]()
	b.ResetTimer()
	for b.Loop() {
		_ = r.Lookup(99999)
	}
}

func BenchmarkPtrMap_Set(b *testing.B) {
	p := cabi.NewPtrMap[benchEntry]()
	e := &benchEntry{id: 1}
	for b.Loop() {
		p.Set(uintptr(1), e)
	}
}

func BenchmarkPtrMap_Get_Hit(b *testing.B) {
	p := cabi.NewPtrMap[benchEntry]()
	p.Set(uintptr(1), &benchEntry{id: 1})
	b.ResetTimer()
	for b.Loop() {
		_ = p.Get(uintptr(1))
	}
}

func BenchmarkPtrMap_Get_Miss(b *testing.B) {
	p := cabi.NewPtrMap[benchEntry]()
	b.ResetTimer()
	for b.Loop() {
		_ = p.Get(uintptr(99))
	}
}

func BenchmarkUniqueName(b *testing.B) {
	for b.Loop() {
		_ = cabi.UniqueName("bench-")
	}
}

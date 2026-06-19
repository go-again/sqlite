package cabi_test

import (
	"testing"

	"gosqlite.org/internal/cabi"
)

// TestFuncPointer_AsFunc_Roundtrip is the minimal direct test for the
// cabi.FuncPointer / cabi.AsFunc pair. The real-world contract is
// "values you stash via FuncPointer can be retrieved via AsFunc with
// the same signature and called identically". This test pins that
// invariant without involving the modernc C bridge, so it works on
// every platform.
func TestFuncPointer_AsFunc_Roundtrip(t *testing.T) {
	add := func(a, b int) int { return a + b }
	tok := cabi.FuncPointer(add)
	got := cabi.AsFunc[func(int, int) int](tok)
	if r := got(2, 3); r != 5 {
		t.Errorf("recovered fn(2,3) = %d, want 5", r)
	}
}

func TestFuncPointer_TwoDistinctFns(t *testing.T) {
	addOne := func(n int) int { return n + 1 }
	mulTwo := func(n int) int { return n * 2 }
	a := cabi.FuncPointer(addOne)
	b := cabi.FuncPointer(mulTwo)
	if a == b {
		t.Fatal("distinct functions produced identical pointers")
	}
	if got := cabi.AsFunc[func(int) int](a)(10); got != 11 {
		t.Errorf("addOne(10) = %d, want 11", got)
	}
	if got := cabi.AsFunc[func(int) int](b)(10); got != 20 {
		t.Errorf("mulTwo(10) = %d, want 20", got)
	}
}

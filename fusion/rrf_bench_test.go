package fusion_test

import (
	"strconv"
	"testing"

	"github.com/go-again/sqlite/fusion"
)

// buildKeys returns N distinct string keys, suitable as RRF rank input.
func buildKeys(n int) []string {
	out := make([]string, n)
	for i := range n {
		out[i] = "k" + strconv.Itoa(i)
	}
	return out
}

func BenchmarkRRF2_Small(b *testing.B) {
	a := buildKeys(10)
	c := buildKeys(10)
	b.ResetTimer()
	for b.Loop() {
		_ = fusion.RRF2(a, c)
	}
}

func BenchmarkRRF2_Medium(b *testing.B) {
	a := buildKeys(100)
	c := buildKeys(100)
	b.ResetTimer()
	for b.Loop() {
		_ = fusion.RRF2(a, c)
	}
}

func BenchmarkRRF2_Large(b *testing.B) {
	a := buildKeys(1000)
	c := buildKeys(1000)
	b.ResetTimer()
	for b.Loop() {
		_ = fusion.RRF2(a, c)
	}
}

func BenchmarkRRF_5lists(b *testing.B) {
	lists := [][]string{
		buildKeys(200),
		buildKeys(200),
		buildKeys(200),
		buildKeys(200),
		buildKeys(200),
	}
	b.ResetTimer()
	for b.Loop() {
		_ = fusion.RRF(lists)
	}
}

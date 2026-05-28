package fusion_test

import (
	"math"
	"testing"

	"github.com/go-again/sqlite/fusion"
)

// approxEq returns true when two floats are within a small epsilon.
// RRF scores are rational fractions but cross-arch float arithmetic
// can drift a hair.
func approxEq(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

// TestRRF_TwoSlices_OverlappingKeys verifies keys present in both
// inputs accumulate scores. Key 1 appears at rank 1 in both lists so
// it should win even though key 2 holds a higher rank average.
func TestRRF_TwoSlices_OverlappingKeys(t *testing.T) {
	a := []int64{1, 2, 3}
	b := []int64{1, 4, 2}
	got := fusion.RRF([][]int64{a, b})
	if len(got) != 4 {
		t.Fatalf("len=%d, want 4 (union of {1,2,3,4})", len(got))
	}
	// Key 1: 1/(60+1) + 1/(60+1) = 2/61 ≈ 0.0327...
	// Key 2: 1/(60+2) + 1/(60+3) = 1/62 + 1/63
	// Key 3: 1/(60+3) = 1/63
	// Key 4: 1/(60+2) = 1/62
	want1 := 2.0 / 61
	if !approxEq(got[0].Score, want1) || got[0].Key != 1 {
		t.Errorf("top key=%d score=%v, want key=1 score≈%v", got[0].Key, got[0].Score, want1)
	}
}

// TestRRF_DisjointKeys confirms keys appearing in only one slice
// still rank by their position within that slice.
func TestRRF_DisjointKeys(t *testing.T) {
	a := []int64{10, 20}
	b := []int64{30, 40}
	got := fusion.RRF([][]int64{a, b})
	if len(got) != 4 {
		t.Fatalf("len=%d, want 4", len(got))
	}
	// Tied scores by rank: 10 ties 30 at 1/61, 20 ties 40 at 1/62.
	if got[0].Score != got[1].Score {
		t.Errorf("rank-1 keys should tie: %v vs %v", got[0].Score, got[1].Score)
	}
	if got[2].Score != got[3].Score {
		t.Errorf("rank-2 keys should tie: %v vs %v", got[2].Score, got[3].Score)
	}
}

// TestRRF_WithWeights confirms weights scale per-slice contributions.
// A 10× weight on slice A makes its rank-1 dominate slice B's rank-1.
func TestRRF_WithWeights(t *testing.T) {
	a := []int64{1}
	b := []int64{2}
	got := fusion.RRF([][]int64{a, b}, fusion.WithWeights(10.0, 1.0))
	if got[0].Key != 1 {
		t.Errorf("top key=%d, want key=1 (10× weight)", got[0].Key)
	}
}

// TestRRF_WithLimit truncates to the top-N.
func TestRRF_WithLimit(t *testing.T) {
	a := []int64{1, 2, 3, 4, 5}
	got := fusion.RRF([][]int64{a}, fusion.WithLimit(2))
	if len(got) != 2 {
		t.Errorf("len=%d, want 2", len(got))
	}
	if got[0].Key != 1 || got[1].Key != 2 {
		t.Errorf("keys=%v, want [1, 2]", []int64{got[0].Key, got[1].Key})
	}
}

// TestRRF_EmptyInputs returns an empty result without panicking.
func TestRRF_EmptyInputs(t *testing.T) {
	got := fusion.RRF([][]int64{nil, nil})
	if len(got) != 0 {
		t.Errorf("len=%d, want 0", len(got))
	}
}

// TestRRF_KConstant_TightSpread confirms a smaller k gives the top of
// the list more influence — rank-1 vs rank-2 scores are further
// apart at k=10 than at k=60.
func TestRRF_KConstant_TightSpread(t *testing.T) {
	a := []int64{1, 2}
	tight := fusion.RRF([][]int64{a}, fusion.WithK(10))
	loose := fusion.RRF([][]int64{a}, fusion.WithK(60))
	tightRatio := tight[0].Score / tight[1].Score
	looseRatio := loose[0].Score / loose[1].Score
	if tightRatio <= looseRatio {
		t.Errorf("k=10 ratio=%v should exceed k=60 ratio=%v", tightRatio, looseRatio)
	}
}

// TestRRF_WeightCountMismatch_Panics is a programming-error path: if
// the caller hands WithWeights a length mismatch, RRF panics. We
// document that explicitly in the WithWeights docstring.
func TestRRF_WeightCountMismatch_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on weight count mismatch")
		}
	}()
	fusion.RRF([][]int64{{1}, {2}}, fusion.WithWeights(1.0))
}

// TestRRF_StringKeys exercises the generic over K with string-typed
// keys to confirm the generic constraint is just `comparable`.
func TestRRF_StringKeys(t *testing.T) {
	a := []string{"alpha", "beta"}
	b := []string{"beta", "alpha"}
	got := fusion.RRF([][]string{a, b})
	if len(got) != 2 {
		t.Fatalf("len=%d, want 2", len(got))
	}
	// Both keys appear in both lists at ranks 1 and 2 respectively,
	// so their scores should be equal: 1/61 + 1/62.
	if !approxEq(got[0].Score, got[1].Score) {
		t.Errorf("symmetric inputs should produce tied scores: %v vs %v",
			got[0].Score, got[1].Score)
	}
}

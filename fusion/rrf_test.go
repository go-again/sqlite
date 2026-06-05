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

// mustRRF fails the test if the call returns an error. Test helper to
// keep happy-path call sites readable when error returns aren't the
// thing under test.
func mustRRF[K comparable](t *testing.T, slices [][]K, opts ...fusion.Option) []fusion.Result[K] {
	t.Helper()
	got, err := fusion.RRF(slices, opts...)
	if err != nil {
		t.Fatalf("RRF: %v", err)
	}
	return got
}

func mustRRF2[K comparable](t *testing.T, a, b []K, opts ...fusion.Option) []fusion.Result[K] {
	t.Helper()
	got, err := fusion.RRF2(a, b, opts...)
	if err != nil {
		t.Fatalf("RRF2: %v", err)
	}
	return got
}

// TestRRF_TwoSlices_OverlappingKeys verifies keys present in both
// inputs accumulate scores. Key 1 appears at rank 1 in both lists so
// it should win even though key 2 holds a higher rank average.
func TestRRF_TwoSlices_OverlappingKeys(t *testing.T) {
	a := []int64{1, 2, 3}
	b := []int64{1, 4, 2}
	got := mustRRF(t, [][]int64{a, b})
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
	got := mustRRF(t, [][]int64{a, b})
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
	got := mustRRF(t, [][]int64{a, b}, fusion.WithWeights(10.0, 1.0))
	if got[0].Key != 1 {
		t.Errorf("top key=%d, want key=1 (10× weight)", got[0].Key)
	}
}

// TestRRF_WithLimit truncates to the top-N.
func TestRRF_WithLimit(t *testing.T) {
	a := []int64{1, 2, 3, 4, 5}
	got := mustRRF(t, [][]int64{a}, fusion.WithLimit(2))
	if len(got) != 2 {
		t.Errorf("len=%d, want 2", len(got))
	}
	if got[0].Key != 1 || got[1].Key != 2 {
		t.Errorf("keys=%v, want [1, 2]", []int64{got[0].Key, got[1].Key})
	}
}

// TestRRF_EmptyInputs returns an empty result without panicking.
func TestRRF_EmptyInputs(t *testing.T) {
	got := mustRRF(t, [][]int64{nil, nil})
	if len(got) != 0 {
		t.Errorf("len=%d, want 0", len(got))
	}
}

// TestRRF_KConstant_TightSpread confirms a smaller k gives the top of
// the list more influence — rank-1 vs rank-2 scores are further
// apart at k=10 than at k=60.
func TestRRF_KConstant_TightSpread(t *testing.T) {
	a := []int64{1, 2}
	tight := mustRRF(t, [][]int64{a}, fusion.WithK(10))
	loose := mustRRF(t, [][]int64{a}, fusion.WithK(60))
	tightRatio := tight[0].Score / tight[1].Score
	looseRatio := loose[0].Score / loose[1].Score
	if tightRatio <= looseRatio {
		t.Errorf("k=10 ratio=%v should exceed k=60 ratio=%v", tightRatio, looseRatio)
	}
}

// TestRRF_WeightCountMismatch_ReturnsError pins the error-return
// contract: WithWeights length mismatch produces a clean error
// instead of panicking the caller. The error message names both
// counts so the caller can fix the call site.
func TestRRF_WeightCountMismatch_ReturnsError(t *testing.T) {
	got, err := fusion.RRF([][]int64{{1}, {2}}, fusion.WithWeights(1.0))
	if err == nil {
		t.Fatal("want error on weight count mismatch, got nil")
	}
	if got != nil {
		t.Errorf("want nil result on error, got %v", got)
	}
}

// TestRRF_DeterministicTiebreak pins the tiebreak rule: when scores
// collide, results sort by fmt.Sprint(Key) ascending. Without an
// explicit tiebreak, Go's map iteration order would leak into the
// final ordering, making a function callers depend on flake. Run the
// same input multiple times and assert ordering is stable.
func TestRRF_DeterministicTiebreak(t *testing.T) {
	// Two slices producing many tied scores. Disjoint keys at the
	// same rank tie by construction; we want lexicographic key order
	// when that happens.
	a := []int64{10, 20, 30}
	b := []int64{40, 50, 60}
	first := mustRRF(t, [][]int64{a, b})
	for i := range 16 {
		got := mustRRF(t, [][]int64{a, b})
		if len(got) != len(first) {
			t.Fatalf("run %d: len=%d, want %d", i, len(got), len(first))
		}
		for j := range got {
			if got[j].Key != first[j].Key {
				t.Errorf("run %d index %d: key=%d, want %d (ordering not stable)",
					i, j, got[j].Key, first[j].Key)
			}
		}
	}
	// Within each rank tier the order should be ascending by key:
	// rank-1 tier {10, 40} → 10 before 40; rank-2 tier {20, 50} → 20
	// before 50; rank-3 tier {30, 60} → 30 before 60.
	wantOrder := []int64{10, 40, 20, 50, 30, 60}
	for i, k := range wantOrder {
		if first[i].Key != k {
			t.Errorf("position %d: key=%d, want %d", i, first[i].Key, k)
		}
	}
}

// TestRRF2_MatchesRRFTwoSlice confirms the two-slice convenience
// produces output identical to the variadic form. Same inputs, same
// options, same output ordering and scores.
func TestRRF2_MatchesRRFTwoSlice(t *testing.T) {
	a := []int64{1, 2, 3}
	b := []int64{2, 3, 4}
	via2 := mustRRF2(t, a, b, fusion.WithLimit(3))
	viaN := mustRRF(t, [][]int64{a, b}, fusion.WithLimit(3))
	if len(via2) != len(viaN) {
		t.Fatalf("len mismatch: RRF2=%d, RRF=%d", len(via2), len(viaN))
	}
	for i := range via2 {
		if via2[i].Key != viaN[i].Key || !approxEq(via2[i].Score, viaN[i].Score) {
			t.Errorf("idx %d: RRF2=%+v, RRF=%+v", i, via2[i], viaN[i])
		}
	}
}

// TestRRF_StringKeys exercises the generic over K with string-typed
// keys to confirm the generic constraint is just `comparable`.
func TestRRF_StringKeys(t *testing.T) {
	a := []string{"alpha", "beta"}
	b := []string{"beta", "alpha"}
	got := mustRRF(t, [][]string{a, b})
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

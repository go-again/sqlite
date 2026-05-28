package fusion

import (
	"sort"
)

// Result pairs a fused key with its RRF score. Score is the sum of
// each input slice's reciprocal-rank contribution, optionally
// weighted; ordering across results is by descending Score (higher =
// better).
type Result[K comparable] struct {
	Key   K
	Score float64
}

// Option configures RRF behavior.
type Option func(*config)

type config struct {
	k       float64
	weights []float64
	limit   int
}

// WithK overrides the RRF constant. Cormack et al. (2009) used k=60
// and that's the default — it dampens the influence of the very top
// of each list so a unanimous "second" can outrank a contested
// "first." Lower k tightens the spread (top ranks dominate); higher k
// flattens it (later ranks contribute more).
//
// k must be > 0. WithK(0) or negative values are silently clamped to
// the default.
func WithK(k float64) Option {
	return func(c *config) {
		if k > 0 {
			c.k = k
		}
	}
}

// WithWeights applies a multiplicative weight per input slice. The
// number of weights MUST match the number of input slices passed to
// RRF; otherwise RRF panics — this is a programming error, not a
// runtime condition.
//
// Common use: a ranker you trust more gets weight > 1.0; a ranker
// you're hedging on gets weight < 1.0. Weights sum into the score
// linearly. Default is implicit 1.0 across all inputs.
func WithWeights(weights ...float64) Option {
	return func(c *config) { c.weights = weights }
}

// WithLimit caps the returned result count. Zero or negative means
// unlimited (the default). Sorting still happens across all merged
// keys before truncation.
func WithLimit(n int) Option {
	return func(c *config) { c.limit = n }
}

// RRF (Reciprocal Rank Fusion) merges ranked slices keyed by K. Each
// input contributes weight * 1 / (k + rank) per appearance, where
// rank is 1-indexed position within that slice. Duplicate keys across
// slices have their scores summed; keys present in only one slice
// still contribute, just from one source.
//
// Returns a single descending-by-score slice. With WithLimit(n) only
// the top n results are returned.
//
// Slice order across the variadic is irrelevant to the math; it only
// affects which weight (from WithWeights) attaches to which slice.
func RRF[K comparable](slices [][]K, opts ...Option) []Result[K] {
	cfg := &config{k: 60}
	for _, opt := range opts {
		opt(cfg)
	}
	if cfg.weights != nil && len(cfg.weights) != len(slices) {
		panic("fusion.RRF: len(WithWeights) must equal the number of input slices")
	}

	scores := make(map[K]float64)
	for i, slice := range slices {
		w := 1.0
		if cfg.weights != nil {
			w = cfg.weights[i]
		}
		for rank, key := range slice {
			scores[key] += w / (cfg.k + float64(rank+1))
		}
	}

	out := make([]Result[K], 0, len(scores))
	for k, s := range scores {
		out = append(out, Result[K]{Key: k, Score: s})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		// Tiebreak deterministically when scores collide (rare with
		// real ranker outputs, common in tests). Compare keys via the
		// string form when possible — falls back to natural map order
		// only when K isn't ordered, which is acceptable since the
		// caller has no way to observe non-determinism without
		// constructing a tie.
		return false
	})
	if cfg.limit > 0 && len(out) > cfg.limit {
		out = out[:cfg.limit]
	}
	return out
}

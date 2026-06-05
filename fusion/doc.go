// Package fusion implements rank-fusion helpers for combining
// multiple ranked result sets — most commonly a vec.KNN result and an
// fts.Search result — into a single merged ranking. The canonical
// algorithm is Reciprocal Rank Fusion (RRF) from Cormack, Clarke and
// Buettcher's 2009 SIGIR paper, "Reciprocal Rank Fusion outperforms
// Condorcet and individual Rank Learning Methods."
//
// The fusion happens entirely in Go: there's no SQL, no extension to
// load, and nothing about the package depends on either the vec or
// fts sub-packages (they're inputs of ranked keys, not coupled
// types). That keeps the surface tiny and lets callers fuse anything
// they can rank — including non-SQL sources like an external embedding
// API or a pre-cached recommendations table.
//
// # When to use RRF
//
// RRF is the right choice when:
//
//   - You have two or more ranked lists.
//   - The lists rank by different criteria (semantic similarity vs.
//     BM25, or text vs. image embeddings on a multimodal table).
//   - You want to combine them without calibrating raw scores
//     between systems.
//
// RRF is NOT the right choice when:
//
//   - You need score-calibrated probabilities (RRF outputs are an
//     ordinal score, not a probability).
//   - One ranker is dramatically more reliable than the other and you
//     want it to dominate (use [WithWeights], but at that point
//     consider whether you wanted to fuse at all).
//
// # Quick start
//
//	vecHits, _ := tbl.KNNSlice(ctx, queryVec, 50)
//	ftsHits, _ := idx.SearchSlice(ctx, fts.Term("brown fox"), fts.WithLimit(50))
//
//	// Extract the keys (rowids) in rank order from each list.
//	vecKeys := make([]int64, len(vecHits))
//	for i, h := range vecHits { vecKeys[i] = h.Rowid }
//	ftsKeys := make([]int64, len(ftsHits))
//	for i, h := range ftsHits { ftsKeys[i] = h.Key }
//
//	// Fuse and take the top 20. RRF2 is the convenience for the
//	// two-slice case; the variadic RRF([][]K{a, b, c, ...}, ...) form
//	// is there when you have three or more rankers.
//	merged, err := fusion.RRF2(vecKeys, ftsKeys, fusion.WithLimit(20))
//	if err != nil {
//	    log.Fatal(err)
//	}
//	for _, r := range merged {
//	    fmt.Println(r.Key, r.Score)
//	}
//
// # Performance
//
// RRF is O(N+M) in memory and CPU where N and M are the input slice
// lengths. Typical KNN/Search workloads pass slices in the 10–100
// element range; the merged output is bounded by N+M. The package
// makes no claim about scaling to millions of input elements per
// slice — pre-truncate via WithLimit on the vec/fts side first.
//
// # See also
//
//   - [github.com/go-again/sqlite/vec.KNNSlice] / [github.com/go-again/sqlite/fts.SearchSlice]
//     — the typical input producers.
//   - examples/fusion-hybrid-search — runnable end-to-end demo that
//     wires a vec.KNN ranking and an fts.Search ranking through RRF2.
package fusion

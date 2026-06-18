// Package pcache installs an application-controlled SQLite page cache —
// the heap SQLite uses to hold database pages between reads and writes.
// In a write-heavy program that heap is where most of modernc's memory
// growth lives; replacing it with a bounded cache makes the footprint
// predictable and observable.
//
// [InstallBoundedLRU] is the batteries-included entry point: a
// fixed-capacity LRU over C-allocated page buffers, with hit / miss /
// eviction / live-page counters for expvar or Prometheus. Call it once
// at startup, before the first sql.Open:
//
//	stats, err := pcache.InstallBoundedLRU(2000) // ≤2000 pages per cache
//	if err != nil { log.Fatal(err) }
//	// ... open databases as usual ...
//	s := stats.Snapshot()
//	log.Printf("page cache: %d hits, %d misses, %d evictions, %d live",
//		s.Hits, s.Misses, s.Evictions, s.Pages)
//
// # One-time, process-global
//
// SQLite's page-cache hook is a process-global switch that must be set
// before SQLite initializes (which happens on the first connection
// open). Install therefore affects every connection in the program,
// including those opened by libraries you don't control, and cannot be
// changed once a database is open — a second call, or a call after the
// first sql.Open, returns an error rather than silently doing nothing.
//
// # Precedence over PRAGMA cache_size
//
// maxPages is a hard per-cache ceiling. SQLite still issues the
// "PRAGMA cache_size" hint, but it is advisory here: the cache never
// holds more than maxPages pages regardless of a larger cache_size. A
// smaller cache_size is honoured implicitly (fewer pages get fetched).
// A cache may briefly exceed maxPages only when SQLite has more than
// that many pages pinned at once (pinned pages cannot be evicted) — the
// bound governs the resident, evictable working set.
//
// # Memory safety
//
// Page buffers are C allocations (libc.Xcalloc), never Go heap, because
// SQLite holds the returned pointer across many later calls and a moving
// collector would invalidate it. The Go bookkeeping keys on the C
// address; the buffers are freed on eviction, truncate, and cache
// destroy. This is the same off-heap discipline the vfs sub-packages use.
package pcache

---
title: Bounded page cache
description: Bound and observe SQLite's page-cache heap with pcache.InstallBoundedLRU — fixed-capacity LRU + hit/miss/eviction stats.
sidebar:
  order: 12
---

# Bounded page cache

In a write-heavy program, SQLite's page cache is where most of the heap lives. `pcache.InstallBoundedLRU` swaps it for a fixed-capacity LRU over C-allocated buffers with hit / miss / eviction / live-page counters — making the footprint predictable and observable.

```go
import "github.com/go-again/sqlite/pcache"

stats, err := pcache.InstallBoundedLRU(2000) // ≤2000 pages per cache
if err != nil { log.Fatal(err) }
// ... open databases as usual ...
s := stats.Snapshot()
log.Printf("page cache: %d hits, %d misses, %d evictions, %d live",
	s.Hits, s.Misses, s.Evictions, s.Pages)
```

Runnable: [`examples/features/advanced/pcache/`](../../examples/features/advanced/pcache/main.go).

## One-time, process-global

SQLite's page-cache hook is a process-global switch that must be set **before** the first `sql.Open` (SQLite initializes lazily on first open). `InstallBoundedLRU` therefore affects every connection in the program; a second call, or a call after the first open, returns an error rather than silently doing nothing.

## Precedence over `PRAGMA cache_size`

`maxPages` is a hard per-cache ceiling. SQLite still issues the `cache_size` hint, but it's advisory here: the cache never holds more than `maxPages` resident pages (a cache may briefly exceed it only while SQLite has more than that many pages pinned at once — pinned pages can't be evicted). A smaller `cache_size` is honoured implicitly.

In-memory databases don't evict (their pages aren't purgeable); the LRU bound applies to on-disk (purgeable) pages.

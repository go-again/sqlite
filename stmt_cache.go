package sqlite

import (
	"container/list"
	"strings"
)

// stmtCacheEntry holds a prepared statement that's been donated back to the
// cache for reuse. psql owns the C-allocated SQL text (the C string remains
// referenced by SQLite via pstmt and must be freed only after pstmt is
// finalized — see drainAll).
type stmtCacheEntry struct {
	key   string
	psql  uintptr
	pstmt uintptr
	elem  *list.Element // pointer back into ord so we can move-to-front in O(1)
}

// stmtCache is a tiny LRU over prepared-statement handles, keyed on the
// trimmed SQL text. It is **not** safe for concurrent use; the only caller is
// (*conn), and database/sql pins exactly one goroutine per *conn at a time,
// so we don't pay a mutex.
//
// The cache is per-connection because SQLite prepared statements are
// connection-bound — a pstmt from conn A cannot be executed on conn B.
type stmtCache struct {
	cap int                        // 0 means disabled
	m   map[string]*stmtCacheEntry // SQL → entry
	ord *list.List                 // *stmtCacheEntry, front = most-recently used
}

// newStmtCache returns a cache with the given capacity. cap <= 0 yields a
// disabled cache; calls to its methods are short-circuited cheaply.
func newStmtCache(capacity int) *stmtCache {
	if capacity <= 0 {
		return &stmtCache{}
	}
	return &stmtCache{
		cap: capacity,
		m:   make(map[string]*stmtCacheEntry, capacity),
		ord: list.New(),
	}
}

// enabled reports whether the cache will retain anything.
func (s *stmtCache) enabled() bool { return s.cap > 0 }

// normalize is the canonical key transform: trim leading/trailing whitespace.
// We deliberately do NOT lowercase or collapse internal whitespace, since
// FTS5 MATCH expressions and JSON path strings can be case- and whitespace-
// sensitive inside the SQL text.
func normalize(sql string) string { return strings.TrimSpace(sql) }

// take removes and returns the entry for sql if one is cached. Caller takes
// ownership of psql + pstmt — they must reset the pstmt (sqlite3_reset +
// sqlite3_clear_bindings) before binding new parameters.
func (s *stmtCache) take(sql string) *stmtCacheEntry {
	if !s.enabled() {
		return nil
	}
	key := normalize(sql)
	entry, ok := s.m[key]
	if !ok {
		return nil
	}
	delete(s.m, key)
	s.ord.Remove(entry.elem)
	entry.elem = nil
	return entry
}

// put donates a prepared statement back. If the cache is at capacity, the
// LRU entry is evicted and returned to the caller, who must finalize its
// pstmt and free its psql. If sql is already cached (rare — only happens if
// two stmts with the same SQL coexist and both Close in close succession),
// the existing entry is evicted to make room for the new one.
func (s *stmtCache) put(sql string, psql, pstmt uintptr) (evicted *stmtCacheEntry) {
	if !s.enabled() {
		// Caller is responsible for finalizing the pstmt and freeing psql
		// when the cache is disabled — they shouldn't have called put.
		return &stmtCacheEntry{key: sql, psql: psql, pstmt: pstmt}
	}
	key := normalize(sql)
	if existing, ok := s.m[key]; ok {
		// Replace: evict the old, insert the new. Caller finalizes old.
		delete(s.m, key)
		s.ord.Remove(existing.elem)
		entry := &stmtCacheEntry{key: key, psql: psql, pstmt: pstmt}
		entry.elem = s.ord.PushFront(entry)
		s.m[key] = entry
		return existing
	}
	if s.ord.Len() >= s.cap {
		// Evict LRU.
		back := s.ord.Back()
		oldest := back.Value.(*stmtCacheEntry)
		s.ord.Remove(back)
		delete(s.m, oldest.key)
		evicted = oldest
	}
	entry := &stmtCacheEntry{key: key, psql: psql, pstmt: pstmt}
	entry.elem = s.ord.PushFront(entry)
	s.m[key] = entry
	return evicted
}

// drainAll empties the cache and returns every retained entry. Used by
// conn.Close to finalize cached pstmts and free their backing SQL strings
// before the underlying TLS goes away.
func (s *stmtCache) drainAll() []*stmtCacheEntry {
	if !s.enabled() {
		return nil
	}
	out := make([]*stmtCacheEntry, 0, s.ord.Len())
	for e := s.ord.Front(); e != nil; e = e.Next() {
		out = append(out, e.Value.(*stmtCacheEntry))
	}
	s.ord.Init()
	s.m = make(map[string]*stmtCacheEntry, s.cap)
	return out
}

// len reports the number of currently-cached entries. Exposed for tests.
func (s *stmtCache) len() int {
	if !s.enabled() {
		return 0
	}
	return s.ord.Len()
}

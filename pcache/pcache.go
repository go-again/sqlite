package pcache

import (
	"container/list"
	"fmt"
	"sync"
	"sync/atomic"
	"unsafe"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"

	"gosqlite.org/internal/cabi"
)

// Stats holds live page-cache counters shared across every cache
// instance the engine creates. All fields are safe to read concurrently
// via [Stats.Snapshot].
type Stats struct {
	hits      atomic.Int64
	misses    atomic.Int64
	evictions atomic.Int64
	pages     atomic.Int64 // live gauge: current pages across all caches
}

// StatsSnapshot is a consistent-enough point-in-time read of [Stats]
// (each field is read atomically; the four are not a single atomic
// transaction, which is fine for metrics).
type StatsSnapshot struct {
	Hits      int64 // page found in cache
	Misses    int64 // page absent, a fetch had to allocate or decline
	Evictions int64 // unpinned page dropped to stay within the bound
	Pages     int64 // pages currently held (pinned + unpinned)
}

// Snapshot reads the current counters.
func (s *Stats) Snapshot() StatsSnapshot {
	return StatsSnapshot{
		Hits:      s.hits.Load(),
		Misses:    s.misses.Load(),
		Evictions: s.evictions.Load(),
		Pages:     s.pages.Load(),
	}
}

var (
	installed atomic.Bool
	gMaxPages int
	gStats    *Stats
	caches    = cabi.NewRegistry[lruCache]()
)

// InstallBoundedLRU replaces SQLite's page cache with a bounded LRU that
// holds at most maxPages pages per cache instance and reports activity
// through the returned [Stats]. It must be called once, before the first
// sql.Open; a later call returns an error (see the package doc on the
// one-time, process-global nature of the hook).
func InstallBoundedLRU(maxPages int) (*Stats, error) {
	if maxPages < 1 {
		return nil, fmt.Errorf("pcache: maxPages must be >= 1, got %d", maxPages)
	}
	if installed.Swap(true) {
		return nil, fmt.Errorf("pcache: a page cache is already installed")
	}

	tls := libc.NewTLS()
	defer tls.Close()

	m := libc.Xmalloc(tls, libc.Tsize_t(unsafe.Sizeof(sqlite3.Tsqlite3_pcache_methods2{})))
	if m == 0 {
		installed.Store(false)
		return nil, fmt.Errorf("pcache: out of memory allocating methods")
	}
	*(*sqlite3.Tsqlite3_pcache_methods2)(unsafe.Pointer(m)) = sqlite3.Tsqlite3_pcache_methods2{
		FiVersion:   1,
		FxInit:      cabi.FuncPointer(xInit),
		FxShutdown:  cabi.FuncPointer(xShutdown),
		FxCreate:    cabi.FuncPointer(xCreate),
		FxCachesize: cabi.FuncPointer(xCachesize),
		FxPagecount: cabi.FuncPointer(xPagecount),
		FxFetch:     cabi.FuncPointer(xFetch),
		FxUnpin:     cabi.FuncPointer(xUnpin),
		FxRekey:     cabi.FuncPointer(xRekey),
		FxTruncate:  cabi.FuncPointer(xTruncate),
		FxDestroy:   cabi.FuncPointer(xDestroy),
		FxShrink:    cabi.FuncPointer(xShrink),
	}

	// SQLite copies the methods struct into its own buffer during
	// config, so m can be freed once the call returns. The trampolines
	// are Go function values and stay valid for the process lifetime.
	va := tls.Alloc(8)
	libc.VaList(va, m)
	rc := sqlite3.Xsqlite3_config(tls, sqlite3.SQLITE_CONFIG_PCACHE2, va)
	tls.Free(8)
	libc.Xfree(tls, m)

	if rc != sqlite3.SQLITE_OK {
		installed.Store(false)
		return nil, fmt.Errorf("pcache: sqlite3_config(PCACHE2) rc=%d — too late? (must precede the first sql.Open)", rc)
	}

	gMaxPages = maxPages
	gStats = &Stats{}
	return gStats, nil
}

// lruCache is one cache instance (typically one per open pager). The C
// page blocks are allocated through tls under mu, so all C memory access
// is serialized.
type lruCache struct {
	mu       sync.Mutex
	tls      *libc.TLS
	szPage   int32
	szExtra  int32
	maxPages int
	byKey    map[uint32]*pcPage
	byPtr    map[uintptr]*pcPage // C block address → page
	lru      *list.List          // unpinned pages, front = least-recently-used
}

// pcPage is one cached page. block is a single C allocation laid out as
// [Tsqlite3_pcache_page header | szPage buffer | szExtra buffer]; the
// header's FpBuf / FpExtra point into the tail, and block itself is the
// pointer handed back to SQLite.
type pcPage struct {
	key    uint32
	pinned bool
	block  uintptr
	elem   *list.Element // position in lru when unpinned, else nil
}

func cacheOf(p uintptr) *lruCache { return caches.Lookup(p) }

func (c *lruCache) allocPage(key uint32) *pcPage {
	sz := unsafe.Sizeof(sqlite3.Tsqlite3_pcache_page{}) + uintptr(c.szPage) + uintptr(c.szExtra)
	block := libc.Xcalloc(c.tls, 1, libc.Tsize_t(sz))
	if block == 0 {
		return nil
	}
	hdr := (*sqlite3.Tsqlite3_pcache_page)(unsafe.Pointer(block))
	hdr.FpBuf = block + unsafe.Sizeof(sqlite3.Tsqlite3_pcache_page{})
	hdr.FpExtra = hdr.FpBuf + uintptr(c.szPage)

	p := &pcPage{key: key, pinned: true, block: block}
	c.byKey[key] = p
	c.byPtr[block] = p
	gStats.pages.Add(1)
	return p
}

// freePage removes p from both maps and the LRU and frees its C block.
func (c *lruCache) freePage(p *pcPage) {
	if p.elem != nil {
		c.lru.Remove(p.elem)
		p.elem = nil
	}
	delete(c.byKey, p.key)
	delete(c.byPtr, p.block)
	libc.Xfree(c.tls, p.block)
	gStats.pages.Add(-1)
}

// evictLRU drops the least-recently-used unpinned page. Returns false
// when every page is pinned (nothing evictable).
func (c *lruCache) evictLRU() bool {
	front := c.lru.Front()
	if front == nil {
		return false
	}
	c.freePage(front.Value.(*pcPage))
	gStats.evictions.Add(1)
	return true
}

// --- PCACHE2 trampolines (fire from transpiled C) ---

func xInit(_ *libc.TLS, _ uintptr) int32 { return sqlite3.SQLITE_OK }

func xShutdown(_ *libc.TLS, _ uintptr) {}

func xCreate(_ *libc.TLS, szPage, szExtra, _ int32) uintptr {
	c := &lruCache{
		tls:      libc.NewTLS(),
		szPage:   szPage,
		szExtra:  szExtra,
		maxPages: gMaxPages,
		byKey:    map[uint32]*pcPage{},
		byPtr:    map[uintptr]*pcPage{},
		lru:      list.New(),
	}
	return caches.Register(c)
}

// xCachesize is advisory — maxPages is the hard ceiling, so the PRAGMA
// cache_size hint is intentionally ignored here (documented precedence).
func xCachesize(_ *libc.TLS, _ uintptr, _ int32) {}

func xPagecount(_ *libc.TLS, pCache uintptr) int32 {
	c := cacheOf(pCache)
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return int32(len(c.byKey))
}

func xFetch(_ *libc.TLS, pCache uintptr, key uint32, createFlag int32) uintptr {
	c := cacheOf(pCache)
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if p := c.byKey[key]; p != nil {
		if !p.pinned { // resurrect from the unpinned LRU
			if p.elem != nil {
				c.lru.Remove(p.elem)
				p.elem = nil
			}
			p.pinned = true
		}
		gStats.hits.Add(1)
		return p.block
	}
	gStats.misses.Add(1)
	if createFlag == 0 {
		return 0 // pure lookup; do not allocate
	}

	// createFlag 1: allocate only if it's easy (evict an unpinned page
	// when at the bound; decline if everything is pinned so SQLite can
	// spill and retry with createFlag 2). createFlag 2: allocate
	// unconditionally, evicting if possible.
	if len(c.byKey) >= c.maxPages {
		if !c.evictLRU() && createFlag == 1 {
			return 0
		}
	}
	p := c.allocPage(key)
	if p == nil {
		return 0
	}
	return p.block
}

func xUnpin(_ *libc.TLS, pCache, pPage uintptr, discard int32) {
	c := cacheOf(pCache)
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.byPtr[pPage]
	if p == nil {
		return
	}
	p.pinned = false
	if discard != 0 {
		c.freePage(p)
		return
	}
	p.elem = c.lru.PushBack(p) // most-recently-used at the back
	for len(c.byKey) > c.maxPages {
		if !c.evictLRU() {
			break
		}
	}
}

func xRekey(_ *libc.TLS, pCache, pPage uintptr, oldKey, newKey uint32) {
	c := cacheOf(pCache)
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.byPtr[pPage]
	if p == nil {
		return
	}
	// Any existing page under newKey is guaranteed unpinned; discard it.
	if existing := c.byKey[newKey]; existing != nil && existing != p {
		c.freePage(existing)
	}
	delete(c.byKey, oldKey)
	p.key = newKey
	c.byKey[newKey] = p
}

func xTruncate(_ *libc.TLS, pCache uintptr, iLimit uint32) {
	c := cacheOf(pCache)
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, p := range c.byKey {
		if k >= iLimit {
			c.freePage(p)
		}
	}
}

func xDestroy(_ *libc.TLS, pCache uintptr) {
	c := cacheOf(pCache)
	if c == nil {
		return
	}
	c.mu.Lock()
	for _, p := range c.byKey {
		libc.Xfree(c.tls, p.block)
		gStats.pages.Add(-1)
	}
	c.byKey = nil
	c.byPtr = nil
	c.lru.Init()
	tls := c.tls
	c.mu.Unlock()
	tls.Close()
	caches.Unregister(pCache)
}

func xShrink(_ *libc.TLS, pCache uintptr) {
	c := cacheOf(pCache)
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for c.evictLRU() {
	}
}

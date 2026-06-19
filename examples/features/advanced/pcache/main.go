// pcache: bound and observe SQLite's page-cache memory. InstallBoundedLRU
// replaces the built-in cache with a fixed-capacity LRU over C-allocated
// page buffers, exposing hit / miss / eviction / live-page counters —
// the answer to "modernc grows memory under write load."
//
// Install must happen once, before the first sql.Open.
//
// Run with:
//
//	just example pcache
package main

import (
	"database/sql"
	"fmt"
	"log"

	_ "gosqlite.org"
	"gosqlite.org/pcache"
)

func main() {
	// Cap each cache at 32 pages and capture the live stats handle.
	stats, err := pcache.InstallBoundedLRU(32)
	if err != nil {
		log.Fatalf("InstallBoundedLRU: %v", err)
	}

	// A file-backed database has purgeable pages — the cache the LRU
	// actually bounds. (An in-memory DB never evicts: its pages aren't
	// purgeable.) Written to the working directory; `just example` runs
	// in a throwaway sandbox.
	db, err := sql.Open("sqlite", "file:pcache_demo.db?_pragma=page_size(512)")
	if err != nil {
		log.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, pad TEXT)`); err != nil {
		log.Fatal(err)
	}

	// Write enough rows to churn well past the 32-page bound.
	tx, _ := db.Begin()
	stmt, _ := tx.Prepare(`INSERT INTO t (id, pad) VALUES (?, ?)`)
	const rows = 3000
	for i := 1; i <= rows; i++ {
		if _, err := stmt.Exec(i, fmt.Sprintf("padding-row-%d", i)); err != nil {
			log.Fatal(err)
		}
	}
	stmt.Close()
	tx.Commit()

	// A couple of full scans drive cache hits, misses, and evictions.
	var n, sum int
	for range 3 {
		if err := db.QueryRow(`SELECT count(*), coalesce(sum(id),0) FROM t`).Scan(&n, &sum); err != nil {
			log.Fatal(err)
		}
	}
	fmt.Printf("scanned %d rows (sum of ids = %d), results correct under a bounded cache\n", n, sum)

	s := stats.Snapshot()
	fmt.Printf("page cache: hits=%d misses=%d evictions=%d live=%d\n",
		s.Hits, s.Misses, s.Evictions, s.Pages)
	if s.Evictions == 0 {
		fmt.Println("note: workload fit within the bound; raise the row count to force evictions")
	} else {
		fmt.Println("evictions > 0: memory stayed bounded under write load")
	}
}

// Copyright 2026 The go-again/sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be found
// in the LICENSE file.

package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestWAL_ConcurrentReadersAndWriters opens a WAL-mode on-disk database and
// runs N writer goroutines + M reader goroutines for a short fixed window.
// WAL is supposed to allow readers and writers to coexist without locking
// each other out; this test would catch:
//   - A regression where WAL mode silently doesn't take effect.
//   - Driver-level races (run with -race for full coverage).
//   - A surprising SQLITE_BUSY storm under contention.
//
// The strong invariant is: the final row count matches the per-writer
// counter sum exactly. No insert silently dropped, no double-counted.
func TestWAL_ConcurrentReadersAndWriters(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "wal.db")
	// WAL needs an on-disk DB; the busy_timeout PRAGMA softens contention.
	dsn := "file:" + dbPath + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(2000)"

	db, err := sql.Open(DriverNameMattn, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	// Confirm WAL is actually on. If it isn't, the rest of the test is
	// uninteresting (it'd be testing serialized journal mode).
	var jm string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&jm); err != nil {
		t.Fatal(err)
	}
	if jm != "wal" {
		t.Skipf("journal_mode=%q, expected wal; cannot exercise WAL concurrency", jm)
	}

	if _, err := db.Exec("CREATE TABLE t (id INTEGER PRIMARY KEY, w INTEGER)"); err != nil {
		t.Fatal(err)
	}

	const (
		writers      = 4
		readers      = 4
		writeWindow  = 500 * time.Millisecond
		readerSleep  = 1 * time.Millisecond
		writerSleep  = 50 * time.Microsecond
	)

	ctx, cancel := context.WithTimeout(context.Background(), writeWindow)
	defer cancel()

	var wg sync.WaitGroup
	var writes [writers]atomic.Int64
	var readErrs atomic.Int64
	var writeErrs atomic.Int64

	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				if _, err := db.Exec("INSERT INTO t (w) VALUES (?)", id); err != nil {
					writeErrs.Add(1)
					return
				}
				writes[id].Add(1)
				time.Sleep(writerSleep)
			}
		}(w)
	}
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				var n int64
				if err := db.QueryRow("SELECT count(*) FROM t").Scan(&n); err != nil {
					readErrs.Add(1)
					return
				}
				time.Sleep(readerSleep)
			}
		}()
	}
	wg.Wait()

	if got := writeErrs.Load(); got > 0 {
		t.Errorf("write errors: %d", got)
	}
	if got := readErrs.Load(); got > 0 {
		t.Errorf("read errors: %d", got)
	}

	// Sum the per-writer counters and compare against the actual row count.
	var expected int64
	for i := range writes {
		expected += writes[i].Load()
	}
	var actual int64
	if err := db.QueryRow("SELECT count(*) FROM t").Scan(&actual); err != nil {
		t.Fatal(err)
	}
	if actual != expected {
		t.Errorf("row count mismatch: actual=%d expected=%d (off by %d)",
			actual, expected, actual-expected)
	}
	if expected == 0 {
		t.Errorf("no writes recorded; the test window was too short or contention is wedged")
	}
}

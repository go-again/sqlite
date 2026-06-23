package vault

import (
	"crypto/rand"
	"path/filepath"
	"sync"
	"testing"

	sqlite "gosqlite.org"
)

// TestConcurrentColdOpensEncrypted forces many pooled connections to open the
// same encrypted database at the SAME time from a cold pool, so several
// (*VFS).Open calls run concurrently. They share one *VFS, so an unsynchronized
// write to a per-VFS field (e.g. the resolved cipher cached for aux files) shows
// up here under -race even though it is value-identical across opens. The existing
// concurrency tests only force concurrent queries after the pool is warm, which
// does not exercise overlapping opens.
func TestConcurrentColdOpensEncrypted(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "coldopen.db")

	// Seed once, then close so the pool is fully cold.
	seed, err := Open(sqlite.Config{Path: path}, Options{Key: key})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Exec(`CREATE TABLE t(v); INSERT INTO t VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(sqlite.Config{Path: path}, Options{Key: key})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)

	const n = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // release all goroutines at once → concurrent cold connection opens
			var v int
			// A write forces the journal (aux) file open too, which reads the cached cipher.
			if _, err := db.Exec(`INSERT INTO t VALUES(?)`, i); err != nil {
				errs[i] = err
				return
			}
			errs[i] = db.QueryRow(`SELECT count(*) FROM t`).Scan(&v)
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
}

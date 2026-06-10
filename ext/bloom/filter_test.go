package bloom_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/bloom"
	"github.com/go-again/sqlite/internal/testhelp"
)

// openFilterDB returns a *sql.DB with the bloom module on every connection
// (via ConnectHook) pinned to one conn so the in-memory vtab persists. The
// typed Filter API runs over the *sql.DB directly, so the module must be
// pool-wide rather than on a single held conn.
func openFilterDB(t *testing.T) *sql.DB {
	t.Helper()
	if raceEnabled {
		t.Skip("skipping under -race: bloom persists via (*Conn).OpenBlob; modernc Xsqlite3_blob_open trips checkptr")
	}
	testhelp.WithConnectHook(t, bloom.Register)
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestFilter_AddContains(t *testing.T) {
	ctx := context.Background()
	db := openFilterDB(t)

	f, err := bloom.Create(ctx, db, "seen", bloom.WithSize(1000), bloom.WithFalsePositiveRate(0.01))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.AddMany(ctx, []string{"alice", "bob", "carol"}); err != nil {
		t.Fatalf("AddMany: %v", err)
	}
	if err := f.Add(ctx, "dave"); err != nil {
		t.Fatalf("Add: %v", err)
	}

	for _, k := range []string{"alice", "bob", "carol", "dave"} {
		ok, err := f.Contains(ctx, k)
		if err != nil {
			t.Fatalf("Contains(%q): %v", k, err)
		}
		if !ok {
			t.Errorf("Contains(%q) = false, want true (no false negatives)", k)
		}
	}
	// A clearly-absent key returns false (the filter is far from saturated).
	if ok, err := f.Contains(ctx, "nobody-zzz-9999"); err != nil {
		t.Fatalf("Contains(absent): %v", err)
	} else if ok {
		t.Errorf("Contains(absent) = true; filter should discriminate when unsaturated")
	}
}

func TestFilter_NoFalseNegatives_LowFalsePositives(t *testing.T) {
	ctx := context.Background()
	db := openFilterDB(t)
	f, err := bloom.Create(ctx, db, "f", bloom.WithSize(5000), bloom.WithFalsePositiveRate(0.01))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	const n = 500
	added := make([]string, n)
	for i := range added {
		added[i] = fmt.Sprintf("key-%d", i)
	}
	if err := f.AddMany(ctx, added); err != nil {
		t.Fatalf("AddMany: %v", err)
	}

	// Core Bloom guarantee: never a false negative.
	for _, k := range added {
		if ok, err := f.Contains(ctx, k); err != nil {
			t.Fatalf("Contains(%q): %v", k, err)
		} else if !ok {
			t.Fatalf("false negative for added key %q", k)
		}
	}

	// Disjoint absent set: the observed false-positive rate must stay low
	// (deterministic for fixed keys). Generous bound vs the configured 0.01.
	const probes = 5000
	fp := 0
	for i := range probes {
		if ok, _ := f.Contains(ctx, fmt.Sprintf("absent-%d", i)); ok {
			fp++
		}
	}
	if rate := float64(fp) / probes; rate > 0.05 {
		t.Errorf("false-positive rate %.4f over %d probes, want < 0.05 (configured p=0.01)", rate, probes)
	}
}

func TestFilter_Create_WithIfNotExists_Idempotent(t *testing.T) {
	ctx := context.Background()
	db := openFilterDB(t)
	if _, err := bloom.Create(ctx, db, "f", bloom.WithIfNotExists()); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := bloom.Create(ctx, db, "f", bloom.WithIfNotExists()); err != nil {
		t.Fatalf("second Create with WithIfNotExists must be a no-op, got: %v", err)
	}
}

func TestFilter_Create_ErrAlreadyExists(t *testing.T) {
	ctx := context.Background()
	db := openFilterDB(t)
	if _, err := bloom.Create(ctx, db, "f"); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	_, err := bloom.Create(ctx, db, "f")
	if !errors.Is(err, bloom.ErrAlreadyExists) {
		t.Errorf("second Create error = %v, want errors.Is(err, ErrAlreadyExists)", err)
	}
}

func TestFilter_AddMany_OneTransaction(t *testing.T) {
	if raceEnabled {
		t.Skip("skipping under -race: bloom persists via (*Conn).OpenBlob")
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)

	ctx := context.Background()
	sc, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("db.Conn: %v", err)
	}
	var commits atomic.Int32
	if err := sc.Raw(func(dc any) error {
		c, ok := dc.(*sqlite.Conn)
		if !ok {
			return errors.New("driver conn is not *sqlite.Conn")
		}
		if err := bloom.Register(c); err != nil {
			return err
		}
		c.RegisterCommitHook(func() int32 { commits.Add(1); return 0 })
		return nil
	}); err != nil {
		_ = sc.Close()
		t.Fatalf("install module + commit hook: %v", err)
	}
	if err := sc.Close(); err != nil {
		t.Fatalf("sc.Close: %v", err)
	}

	f, err := bloom.Create(ctx, db, "f")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	baseline := commits.Load()
	if err := f.AddMany(ctx, []string{"a", "b", "c", "d", "e"}); err != nil {
		t.Fatalf("AddMany: %v", err)
	}
	if got := commits.Load() - baseline; got != 1 {
		t.Errorf("AddMany fired %d commits, want 1", got)
	}
}

func TestFilter_Drop(t *testing.T) {
	ctx := context.Background()
	db := openFilterDB(t)
	f, err := bloom.Create(ctx, db, "f")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.Add(ctx, "x"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := f.Drop(ctx); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	// Dropped — a fresh Create with the same name succeeds.
	if _, err := bloom.Create(ctx, db, "f"); err != nil {
		t.Errorf("Create after Drop: %v", err)
	}
}

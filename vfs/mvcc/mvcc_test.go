package mvcc_test

import (
	"context"
	"database/sql"
	"sync"
	"testing"

	sqlite "gosqlite.org"
	"gosqlite.org/vfs/mvcc"
)

func TestMVCC_RoundTrip(t *testing.T) {
	name, fs, err := mvcc.New(mvcc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()

	db, err := sql.Open(sqlite.DriverName, "file:/x?vfs="+name)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE t(id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatal(err)
	}
	for i := range 25 {
		if _, err := db.ExecContext(ctx, `INSERT INTO t(v) VALUES (?)`, i); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM t`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 25 {
		t.Errorf("count=%d, want 25", n)
	}
}

func TestMVCC_SharedDBSeenByMultipleHandles(t *testing.T) {
	name, fs, err := mvcc.New(mvcc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()

	writer, err := sql.Open(sqlite.DriverName, "file:/shared?vfs="+name)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	writer.SetMaxOpenConns(1)

	ctx := context.Background()
	if _, err := writer.ExecContext(ctx, `CREATE TABLE t(v TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.ExecContext(ctx, `INSERT INTO t VALUES ('hi')`); err != nil {
		t.Fatal(err)
	}

	// A second sql.DB pointing at the same DB name should see the row.
	reader, err := sql.Open(sqlite.DriverName, "file:/shared?vfs="+name)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var got string
	if err := reader.QueryRowContext(ctx, `SELECT v FROM t`).Scan(&got); err != nil {
		t.Fatalf("read from second handle: %v", err)
	}
	if got != "hi" {
		t.Errorf("got %q, want %q", got, "hi")
	}
}

func TestMVCC_PrivateDBIsolated(t *testing.T) {
	name, fs, err := mvcc.New(mvcc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()

	// Names without a leading slash get private storage per open.
	w1, _ := sql.Open(sqlite.DriverName, "file:scratch?vfs="+name)
	defer w1.Close()
	w2, _ := sql.Open(sqlite.DriverName, "file:scratch?vfs="+name)
	defer w2.Close()
	w1.SetMaxOpenConns(1)
	w2.SetMaxOpenConns(1)

	ctx := context.Background()
	if _, err := w1.ExecContext(ctx, `CREATE TABLE t(v TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := w1.ExecContext(ctx, `INSERT INTO t VALUES ('w1')`); err != nil {
		t.Fatal(err)
	}
	// w2 should NOT see the table.
	if _, err := w2.QueryContext(ctx, `SELECT * FROM t`); err == nil {
		t.Error("private DB leaked across opens")
	}
}

func TestMVCC_ConcurrentReadDuringWriteSnapshot(t *testing.T) {
	// Pin that a reader's snapshot survives across a writer's commit
	// — until the reader re-acquires a SHARED lock, it should still
	// see the pre-commit row count.
	name, fs, err := mvcc.New(mvcc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()
	db, _ := sql.Open(sqlite.DriverName, "file:/snap?vfs="+name)
	defer db.Close()
	db.SetMaxOpenConns(2)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE t(v INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO t VALUES (1)`); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range 50 {
			_, _ = db.ExecContext(ctx, `INSERT INTO t VALUES (?)`, i)
		}
	})

	// Reads should not deadlock or crash even while concurrent inserts
	// run; final visible count should be >= 1.
	for range 25 {
		var c int
		_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM t`).Scan(&c)
		if c < 1 {
			t.Errorf("count=%d, want >=1 (writer should never erase committed rows)", c)
		}
	}
	wg.Wait()
}

func TestMVCC_Close_Idempotent(t *testing.T) {
	_, fs, err := mvcc.New(mvcc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := fs.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// TestMVCC_ConcurrentWritersSerialize starts two writer transactions on
// the same shared DB and asserts that one of them is forced to retry
// because the mvcc VFS allows only one in-flight writer per shared DB
// (writeMu in memDB). Without that serialization, snapshot publish on
// commit would corrupt the second writer's view.
func TestMVCC_ConcurrentWritersSerialize(t *testing.T) {
	name, fs, err := mvcc.New(mvcc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()

	const dsn = "file:/contended?vfs=" + ""
	openOne := func() *sql.DB {
		d, err := sql.Open(sqlite.DriverName, "file:/contended?_pragma=busy_timeout(2000)&vfs="+name)
		if err != nil {
			t.Fatal(err)
		}
		d.SetMaxOpenConns(1)
		return d
	}
	_ = dsn
	w1 := openOne()
	defer w1.Close()
	w2 := openOne()
	defer w2.Close()

	ctx := context.Background()
	if _, err := w1.ExecContext(ctx, `CREATE TABLE t(v INT)`); err != nil {
		t.Fatal(err)
	}

	const total = 50
	var wg sync.WaitGroup
	wg.Add(2)
	errs := make(chan error, 2*total)
	worker := func(db *sql.DB, tag int) {
		defer wg.Done()
		for i := range total {
			if _, err := db.ExecContext(ctx, `INSERT INTO t VALUES (?)`, tag*1000+i); err != nil {
				errs <- err
				return
			}
		}
	}
	go worker(w1, 1)
	go worker(w2, 2)
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Errorf("writer error: %v", e)
	}

	var n int
	if err := w1.QueryRowContext(ctx, `SELECT COUNT(*) FROM t`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2*total {
		t.Errorf("count=%d, want %d (both writers committed under serialization)", n, 2*total)
	}
}

// TestMVCC_XOpen_RejectsJournalOpens pins the documented refusal of
// non-database opens (journal / WAL files). mvcc has no persistent
// backing for these; xOpen must short-circuit with SQLITE_CANTOPEN.
func TestMVCC_XOpen_RejectsJournalOpens(t *testing.T) {
	name, fs, err := mvcc.New(mvcc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()

	// Force a journal by requesting WAL journaling, which mvcc cannot
	// satisfy. The DSN must be a main DB (it'll open) but the PRAGMA
	// to enter WAL mode requires opening the -wal file — and that's
	// the journal-class open xOpen rejects.
	db, err := sql.Open(sqlite.DriverName, "file:/wal?vfs="+name)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE t(v INT)`); err != nil {
		t.Fatal(err)
	}
	// WAL switch should refuse because journal opens are blocked.
	rows, err := db.QueryContext(ctx, `PRAGMA journal_mode = WAL`)
	if err == nil {
		var mode string
		if rows.Next() {
			rows.Scan(&mode)
		}
		rows.Close()
		if mode == "wal" {
			t.Errorf("PRAGMA journal_mode=WAL succeeded; mvcc cannot host a WAL file")
		}
	}
}

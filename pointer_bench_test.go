package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// benchSetup opens an in-memory DB pinned to 1 conn, registers a noop
// UDF, and returns a pinned *sql.Conn ready for prepared-statement
// benchmarks.
func benchSetup(b *testing.B) (*sql.DB, *sql.Conn) {
	b.Helper()
	db, err := sql.Open(DriverNameSQLite3, ":memory:")
	if err != nil {
		b.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	b.Cleanup(func() { _ = db.Close() })
	sc, err := db.Conn(context.Background())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = sc.Close() })
	if err := sc.Raw(func(driverConn any) error {
		c, ok := driverConn.(*Conn)
		if !ok {
			return errors.New("not *Conn")
		}
		return c.RegisterFunc("noop", func(any) int64 { return 0 }, false)
	}); err != nil {
		b.Fatal(err)
	}
	return db, sc
}

func BenchmarkPointer_BindRelease(b *testing.B) {
	_, sc := benchSetup(b)
	ctx := context.Background()
	stmt, err := sc.PrepareContext(ctx, `SELECT noop(?)`)
	if err != nil {
		b.Fatal(err)
	}
	defer stmt.Close()
	slice := []int{1, 2, 3}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := stmt.ExecContext(ctx, Pointer(slice)); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPointer_Registry_StoreLoad(b *testing.B) {
	// Direct registry exercise (no SQLite round-trip) — measures the
	// mutex-guarded path in isolation.
	v := []int{10, 20, 30}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		tok := storePointer(v)
		_, _ = loadPointer(tok)
		releasePointer(tok)
	}
}

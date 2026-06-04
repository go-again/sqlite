package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// BenchmarkAuthorizer_NoOp measures the per-statement overhead of installing
// a no-op authorizer compared to running the same statements with no
// authorizer attached. The authorizer is invoked for every parse/compile
// event (table reads, columns, pragmas, etc.) so the constant factor here
// scales with statement complexity, not row count.
//
// To compare:
//
//	go test -run=^$ -bench=BenchmarkAuthorizer -benchmem -count=5
//
// The two sub-benchmarks share fixture setup so the only delta is the
// authorizer install.
func BenchmarkAuthorizer_NoOp(b *testing.B) {
	db, err := sql.Open(DriverNameSQLite3, ":memory:")
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
CREATE TABLE t (id INTEGER PRIMARY KEY, a INTEGER, b INTEGER, c INTEGER);
INSERT INTO t (a, b, c) VALUES (1, 2, 3), (4, 5, 6), (7, 8, 9);
`); err != nil {
		b.Fatal(err)
	}

	// Pin the conn we install the authorizer on.
	sc, err := db.Conn(ctx)
	if err != nil {
		b.Fatal(err)
	}
	defer sc.Close()
	var c *Conn
	if err := sc.Raw(func(dc any) error {
		gc, ok := dc.(*Conn)
		if !ok {
			return errors.New("driver conn is not *sqlite.Conn")
		}
		c = gc
		return nil
	}); err != nil {
		b.Fatal(err)
	}

	const query = `SELECT a, b, c FROM t WHERE a > ? AND b < ?`

	// Reusable scan target so the benchmark doesn't measure allocator churn.
	var av, bv, cv int

	b.Run("WithoutAuthorizer", func(b *testing.B) {
		c.RegisterAuthorizer(nil) // ensure clean baseline
		b.ResetTimer()
		for range b.N {
			rows, err := sc.QueryContext(ctx, query, 0, 100)
			if err != nil {
				b.Fatal(err)
			}
			for rows.Next() {
				if err := rows.Scan(&av, &bv, &cv); err != nil {
					b.Fatal(err)
				}
			}
			rows.Close()
		}
	})

	b.Run("WithAuthorizer", func(b *testing.B) {
		// No-op authorizer that allows everything. The point is to measure
		// the trampoline + map-lookup cost on every authorize call, not the
		// logic inside the callback.
		c.RegisterAuthorizer(func(op int, a, b, dbName, trigger string) int { return SQLITE_OK })
		defer c.RegisterAuthorizer(nil)
		b.ResetTimer()
		for range b.N {
			rows, err := sc.QueryContext(ctx, query, 0, 100)
			if err != nil {
				b.Fatal(err)
			}
			for rows.Next() {
				if err := rows.Scan(&av, &bv, &cv); err != nil {
					b.Fatal(err)
				}
			}
			rows.Close()
		}
	})
}

package stats_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/stats"
)

func benchSetup(b *testing.B, n int) (*sql.DB, *sql.Conn) {
	b.Helper()
	db, err := sql.Open(sqlite.DriverName, ":memory:")
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
		c, ok := driverConn.(*sqlite.Conn)
		if !ok {
			return errors.New("not *sqlite.Conn")
		}
		return stats.Register(c)
	}); err != nil {
		b.Fatal(err)
	}
	if _, err := sc.ExecContext(context.Background(),
		`CREATE TABLE t(x REAL, y REAL)`); err != nil {
		b.Fatal(err)
	}
	// Bulk insert. Use a single multi-row INSERT for speed.
	var sb strings.Builder
	sb.WriteString(`INSERT INTO t(x, y) VALUES `)
	for i := range n {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, "(%d, %d)", i, i*2)
	}
	if _, err := sc.ExecContext(context.Background(), sb.String()); err != nil {
		b.Fatal(err)
	}
	return db, sc
}

func BenchmarkStats_VarPop_100K(b *testing.B) {
	_, sc := benchSetup(b, 100_000)
	b.ResetTimer()
	for range b.N {
		var v float64
		if err := sc.QueryRowContext(context.Background(),
			`SELECT var_pop(x) FROM t`).Scan(&v); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStats_RegrSlope_100K(b *testing.B) {
	_, sc := benchSetup(b, 100_000)
	b.ResetTimer()
	for range b.N {
		var v float64
		if err := sc.QueryRowContext(context.Background(),
			`SELECT regr_slope(y, x) FROM t`).Scan(&v); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStats_PercentileCont_10K(b *testing.B) {
	_, sc := benchSetup(b, 10_000)
	b.ResetTimer()
	for range b.N {
		var v float64
		if err := sc.QueryRowContext(context.Background(),
			`SELECT percentile_cont(x, 0.5) FROM t`).Scan(&v); err != nil {
			b.Fatal(err)
		}
	}
}

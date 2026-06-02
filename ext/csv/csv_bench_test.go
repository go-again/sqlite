package csv_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/csv"
)

func benchSetup(b *testing.B, rows int) (*sql.DB, *sql.Conn) {
	b.Helper()
	// Build a deterministic CSV with N rows of `k,v` data + a 1-line
	// header.
	var sb strings.Builder
	sb.WriteString("k,v\n")
	for i := range rows {
		fmt.Fprintf(&sb, "%d,line-%d\n", i, i)
	}
	fsys := fstest.MapFS{"data.csv": {Data: []byte(sb.String())}}

	db, err := sql.Open("sqlite", ":memory:")
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
		return csv.RegisterFS(c, fsys)
	}); err != nil {
		b.Fatal(err)
	}
	if _, err := sc.ExecContext(context.Background(),
		`CREATE VIRTUAL TABLE temp.t USING csv(filename='data.csv', header=on,
		    schema='CREATE TABLE x(k INTEGER, v TEXT)')`); err != nil {
		b.Fatal(err)
	}
	return db, sc
}

func BenchmarkCSV_FullScan_10K(b *testing.B) {
	_, sc := benchSetup(b, 10_000)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		var n int
		if err := sc.QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM temp.t`).Scan(&n); err != nil {
			b.Fatal(err)
		}
		if n != 10_000 {
			b.Fatalf("count = %d, want 10000", n)
		}
	}
}

func BenchmarkCSV_Filtered_10K(b *testing.B) {
	_, sc := benchSetup(b, 10_000)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		var v string
		if err := sc.QueryRowContext(context.Background(),
			`SELECT v FROM temp.t WHERE k = 5000`).Scan(&v); err != nil {
			b.Fatal(err)
		}
	}
}

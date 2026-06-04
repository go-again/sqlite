package bloom_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/bloom"
)

func benchSetup(b *testing.B) (*sql.DB, *sql.Conn) {
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
		return bloom.Register(c)
	}); err != nil {
		b.Fatal(err)
	}
	return db, sc
}

func BenchmarkBloom_Insert_10K(b *testing.B) {
	for range b.N {
		_, sc := benchSetup(b)
		ctx := context.Background()
		if _, err := sc.ExecContext(ctx,
			`CREATE VIRTUAL TABLE temp.f USING bloom(size=10000, p=0.01)`); err != nil {
			b.Fatal(err)
		}
		stmt, err := sc.PrepareContext(ctx, `INSERT INTO temp.f(word) VALUES (?)`)
		if err != nil {
			b.Fatal(err)
		}
		for i := range 10_000 {
			if _, err := stmt.ExecContext(ctx, fmt.Sprintf("k%d", i)); err != nil {
				b.Fatal(err)
			}
		}
		stmt.Close()
	}
}

func BenchmarkBloom_Membership_10K(b *testing.B) {
	_, sc := benchSetup(b)
	ctx := context.Background()
	if _, err := sc.ExecContext(ctx,
		`CREATE VIRTUAL TABLE temp.f USING bloom(size=10000, p=0.01)`); err != nil {
		b.Fatal(err)
	}
	for i := range 10_000 {
		if _, err := sc.ExecContext(ctx,
			`INSERT INTO temp.f(word) VALUES (?)`, fmt.Sprintf("k%d", i)); err != nil {
			b.Fatal(err)
		}
	}
	stmt, err := sc.PrepareContext(ctx, `SELECT present FROM temp.f WHERE word = ?`)
	if err != nil {
		b.Fatal(err)
	}
	defer stmt.Close()
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		key := fmt.Sprintf("k%d", i%20_000) // half present, half absent
		var present bool
		_ = stmt.QueryRowContext(ctx, key).Scan(&present)
	}
}

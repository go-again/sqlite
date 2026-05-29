package crypto_test

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	_ "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/vfs/crypto"
)

// benchInsertN runs a deterministic insert-heavy workload against a
// freshly-opened DB and is used by all three cipher benchmarks.
func benchInsertN(b *testing.B, dsn string) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		b.Fatal(err)
	}
	stmt, err := db.Prepare(`INSERT INTO t (id, v) VALUES (?, ?)`)
	if err != nil {
		b.Fatal(err)
	}
	defer stmt.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := stmt.Exec(i, "benchmark payload value goes here"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkInsert_Plaintext is the no-encryption baseline. Use the
// default VFS so there's no wrapping overhead at all.
func BenchmarkInsert_Plaintext(b *testing.B) {
	dbPath := filepath.Join(b.TempDir(), "plain.db")
	benchInsertN(b, "file:"+dbPath)
}

// BenchmarkInsert_Adiantum measures the cost of Adiantum on a write-
// heavy workload. The cipher is pure Go; expect ~50–100% overhead
// relative to plaintext on most arches.
func BenchmarkInsert_Adiantum(b *testing.B) {
	name, fs, err := crypto.New(crypto.Options{Key: make([]byte, 32)})
	if err != nil {
		b.Fatal(err)
	}
	defer fs.Close()
	dbPath := filepath.Join(b.TempDir(), "adiantum.db")
	benchInsertN(b, fmt.Sprintf("file:%s?vfs=%s", dbPath, name))
}

// BenchmarkInsert_AESXTS measures the cost of AES-XTS-256. AES-NI
// hardware support (most amd64/arm64 since 2010) makes this faster
// than Adiantum on those targets; on arches without AES-NI Adiantum
// usually wins.
func BenchmarkInsert_AESXTS(b *testing.B) {
	name, fs, err := crypto.New(crypto.Options{
		Key:    make([]byte, 64),
		Cipher: crypto.AESXTS,
	})
	if err != nil {
		b.Fatal(err)
	}
	defer fs.Close()
	dbPath := filepath.Join(b.TempDir(), "xts.db")
	benchInsertN(b, fmt.Sprintf("file:%s?vfs=%s", dbPath, name))
}

package crypto_test

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	sqlite "gosqlite.org"
	"gosqlite.org/vfs/crypto"
)

// BenchmarkWALWrite measures a large sequential blob workload through the full
// SQLite path in WAL mode (synchronous=OFF isolates the CPU/encryption path from
// fsync), comparing plaintext vs Adiantum vs AES-XTS. It is the large-file scenario
// the auxiliary-file cipher unit (auxCryptUnit) keeps fast: WAL frames are written
// at a stride that never aligns to the page size, so a page-size cipher unit would
// turn every frame into a full-page read-modify-write.
func BenchmarkWALWrite(b *testing.B) {
	const totalMB, blobKB = 24, 64
	nRows := totalMB * 1024 / blobKB
	blob := make([]byte, blobKB*1024)
	for i := range blob {
		blob[i] = byte(i*2654435761 + 7) // incompressible-ish
	}

	run := func(b *testing.B, dsn string) {
		b.SetBytes(int64(nRows) * int64(len(blob)))
		b.ResetTimer()
		for n := 0; n < b.N; n++ {
			b.StopTimer()
			db, err := sql.Open(sqlite.DriverName, dsn)
			if err != nil {
				b.Fatal(err)
			}
			db.SetMaxOpenConns(1)
			for _, p := range []string{`PRAGMA page_size=8192`, `PRAGMA journal_mode=WAL`, `PRAGMA synchronous=OFF`} {
				if _, err := db.Exec(p); err != nil {
					b.Fatal(err)
				}
			}
			if _, err := db.Exec(`CREATE TABLE t(id INTEGER PRIMARY KEY, v BLOB)`); err != nil {
				b.Fatal(err)
			}
			b.StartTimer()
			tx, _ := db.Begin()
			for r := 0; r < nRows; r++ {
				if _, err := tx.Exec(`INSERT INTO t(id, v) VALUES(?, ?)`, r, blob); err != nil {
					b.Fatal(err)
				}
			}
			if err := tx.Commit(); err != nil {
				b.Fatal(err)
			}
			b.StopTimer()
			_ = db.Close()
			b.StartTimer()
		}
	}

	b.Run("Plaintext", func(b *testing.B) { run(b, "file:"+filepath.Join(b.TempDir(), "p.db")) })
	for _, c := range []struct {
		name   string
		cipher crypto.Cipher
		keyLen int
	}{{"Adiantum", crypto.Adiantum, 32}, {"AESXTS", crypto.AESXTS, 64}} {
		name, fs, err := crypto.New(crypto.Options{Key: make([]byte, c.keyLen), Cipher: c.cipher, PageSize: 8192})
		if err != nil {
			b.Fatal(err)
		}
		b.Run(c.name, func(b *testing.B) {
			run(b, fmt.Sprintf("file:%s?vfs=%s", filepath.Join(b.TempDir(), "e.db"), name))
		})
		_ = fs.Close()
	}
}

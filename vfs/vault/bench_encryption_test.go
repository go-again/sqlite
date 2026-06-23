package vault

import (
	"path/filepath"
	"testing"

	sqlite "gosqlite.org"
	"gosqlite.org/vfs/crypto"
)

// BenchmarkMatrix estimates the cost of vault's option matrix on one workload —
// compression and encryption are independent options, and authenticated mode is a
// third axis (it requires encryption):
//
//   - raw-sqlite                    raw SQLite, no VFS (baseline)
//   - plain                         vault container, no transforms
//   - compress                      compression only
//   - encrypt                       encryption only (Adiantum)
//   - compress+encrypt              both
//   - encrypt+auth                  encryption + symmetric tamper-evidence
//   - compress+encrypt+auth         all three
//   - vfs-crypto-adiantum/-aesxts   vfs/crypto only, for comparison
//
// The vault rows run at the engine's native 64 KiB page; the vfs/crypto rows run at
// SQLite's default page size, which is how that layer is used, so account for the
// page-size difference when comparing them against the vault rows. Each variant runs
// /write (per-row insert) and /read (full-scan). Run with `just bench-vault`.
func BenchmarkMatrix(b *testing.B) {
	key := make([]byte, crypto.KeyLen(crypto.Adiantum))
	variants := []struct {
		name string
		open func(b *testing.B, path string) *sqlite.DB
	}{
		{"raw-sqlite", func(b *testing.B, path string) *sqlite.DB { return openRaw(b, path) }},
		{"plain", func(b *testing.B, path string) *sqlite.DB { return openCompressBench(b, path, Options{}) }},
		{"compress", func(b *testing.B, path string) *sqlite.DB {
			return openCompressBench(b, path, Options{Level: CompressionDefault})
		}},
		{"encrypt", func(b *testing.B, path string) *sqlite.DB {
			return openCompressBench(b, path, Options{Key: key})
		}},
		{"compress+encrypt", func(b *testing.B, path string) *sqlite.DB {
			return openCompressBench(b, path, Options{Level: CompressionDefault, Key: key})
		}},
		{"encrypt+auth", func(b *testing.B, path string) *sqlite.DB {
			return openCompressBench(b, path, Options{Key: key, Authenticate: true})
		}},
		{"compress+encrypt+auth", func(b *testing.B, path string) *sqlite.DB {
			return openCompressBench(b, path, Options{Level: CompressionDefault, Key: key, Authenticate: true})
		}},
		{"vfs-crypto-adiantum", func(b *testing.B, path string) *sqlite.DB {
			return openCryptoBench(b, path, crypto.Adiantum, crypto.KeyLen(crypto.Adiantum))
		}},
		{"vfs-crypto-aesxts", func(b *testing.B, path string) *sqlite.DB {
			return openCryptoBench(b, path, crypto.AESXTS, crypto.KeyLen(crypto.AESXTS))
		}},
	}
	for _, v := range variants {
		b.Run(v.name+"/write", func(b *testing.B) {
			db := v.open(b, filepath.Join(b.TempDir(), "bench.db"))
			defer db.Close()
			benchInsert(b, db)
		})
		b.Run(v.name+"/read", func(b *testing.B) {
			db := v.open(b, filepath.Join(b.TempDir(), "bench.db"))
			defer db.Close()
			benchScan(b, db)
		})
	}
}

// openCompressBench opens a compressed (optionally encrypted) database at the
// live VFS's page size for benchmarking.
func openCompressBench(b *testing.B, path string, opts Options) *sqlite.DB {
	b.Helper()
	db, err := Open(sqlite.Config{Path: path}, opts)
	if err != nil {
		b.Fatalf("open compress: %v", err)
	}
	return db
}

// openCryptoBench opens an encrypted-only database (vfs/crypto, no compression)
// at the VFS's default page size — the realistic configuration for that layer.
func openCryptoBench(b *testing.B, path string, cipher crypto.Cipher, keyLen int) *sqlite.DB {
	b.Helper()
	name, fs, err := crypto.New(crypto.Options{Key: make([]byte, keyLen), Cipher: cipher})
	if err != nil {
		b.Fatalf("crypto.New: %v", err)
	}
	db, err := sqlite.Open(sqlite.Config{
		Path:         path,
		VFS:          name,
		VFSCloser:    fs,
		MaxOpenConns: 1,
		Pragmas:      sqlite.Pragmas{JournalMode: sqlite.JournalDelete},
	})
	if err != nil {
		b.Fatalf("open crypto: %v", err)
	}
	return db
}

// benchScan pre-populates a table and times a full scan of it per iteration.
func benchScan(b *testing.B, db *sqlite.DB) {
	const rows = 5000
	insertRows(b, db, rows)
	b.ResetTimer()
	for b.Loop() {
		r, err := db.Query(`SELECT v FROM t`)
		if err != nil {
			b.Fatal(err)
		}
		var v string
		n := 0
		for r.Next() {
			if err := r.Scan(&v); err != nil {
				b.Fatal(err)
			}
			n++
		}
		if err := r.Err(); err != nil {
			b.Fatal(err)
		}
		_ = r.Close()
		if n != rows {
			b.Fatalf("scanned %d rows, want %d", n, rows)
		}
	}
}

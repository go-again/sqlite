package vault

import (
	"crypto/rand"
	"path/filepath"
	"testing"

	sqlite "gosqlite.org"
	"gosqlite.org/vfs/crypto"
)

// BenchmarkEncryptedWAL writes a large sequential blob workload through the full
// SQLite path in WAL mode at the 8 KiB page size, comparing plaintext vs Adiantum
// vs AES-XTS. It is the large-file-into-an-encrypted-image scenario: the cost is
// dominated by the encrypted auxiliary -wal file (passFile), whose cipher unit is
// auxCryptUnit (small) precisely so the misaligned WAL frame writes do not each
// trigger a full-page read-modify-write. synchronous=OFF removes fsync so the
// measurement reflects the CPU/encryption path rather than the disk.
func BenchmarkEncryptedWAL(b *testing.B) {
	const (
		totalMB = 32
		blobKB  = 64
	)
	nRows := totalMB * 1024 / blobKB
	blob := make([]byte, blobKB*1024)
	_, _ = rand.Read(blob) // incompressible

	cases := []struct {
		name   string
		cipher crypto.Cipher
		keyLen int
	}{
		{"Plaintext", 0, 0},
		{"Adiantum", crypto.Adiantum, 32},
		{"AESXTS", crypto.AESXTS, 64},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			opts := Options{PageSize: 8192}
			if tc.keyLen > 0 {
				opts.Cipher, opts.Key = tc.cipher, make([]byte, tc.keyLen)
			}
			b.SetBytes(int64(nRows) * int64(len(blob)))
			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				b.StopTimer()
				path := filepath.Join(b.TempDir(), "v.db")
				db, err := Open(sqlite.Config{Path: path, Pragmas: sqlite.Pragmas{
					JournalMode: sqlite.JournalWAL, Extra: map[string]string{"synchronous": "OFF"},
				}}, opts)
				if err != nil {
					b.Fatal(err)
				}
				if _, err := db.Exec(`CREATE TABLE t(id INTEGER PRIMARY KEY, v BLOB)`); err != nil {
					b.Fatal(err)
				}
				b.StartTimer()

				tx, err := db.Begin()
				if err != nil {
					b.Fatal(err)
				}
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
		})
	}
}

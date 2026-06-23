package vault

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestMatrix exercises the four independent combinations of compression and
// encryption. Each round-trips and verifies; the on-disk superblock reflects the
// configuration (codec byte raw vs az, enc byte off vs on); and an encrypted
// container never leaks the plaintext to disk. This is the capability that makes
// vault one module instead of two: compress? and encrypt? are orthogonal options.
func TestMatrix(t *testing.T) {
	key := randKey(t)
	secret := bytes.Repeat([]byte("matrix-secret "), 400) // compressible and distinctive

	cases := []struct {
		name              string
		opts              Options
		compress, encrypt bool
	}{
		{"plain", Options{}, false, false},
		{"compress", Options{Level: CompressionBest}, true, false},
		{"encrypt", Options{Key: key}, false, true},
		{"both", Options{Level: CompressionBest, Key: key}, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "m.db")

			db := openLive(t, path, tc.opts)
			if _, err := db.Exec(`CREATE TABLE t (v BLOB)`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO t VALUES (?)`, secret); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			// The superblock (plaintext container metadata) records the config:
			// byte 46 = codec (0 raw / 1 az), byte 47 = enc (0 = unencrypted).
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if gotC := raw[46] != codecRaw; gotC != tc.compress {
				t.Errorf("on-disk codec compressed=%v, want %v", gotC, tc.compress)
			}
			if gotE := raw[47] != 0; gotE != tc.encrypt {
				t.Errorf("on-disk enc encrypted=%v, want %v", gotE, tc.encrypt)
			}
			if tc.encrypt && bytes.Contains(raw, secret) {
				t.Error("plaintext secret found in an encrypted container")
			}

			// Reopen with the same options and read it back.
			db2 := openLive(t, path, tc.opts)
			defer db2.Close()
			var got []byte
			if err := db2.QueryRow(`SELECT v FROM t`).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, secret) {
				t.Errorf("round-trip mismatch: %d bytes back", len(got))
			}
			var ic string
			if err := db2.QueryRow(`PRAGMA integrity_check`).Scan(&ic); err != nil || ic != "ok" {
				t.Fatalf("integrity_check = (%q, %v)", ic, err)
			}
		})
	}
}

package vault

import (
	"bytes"
	"crypto/rand"
	"path/filepath"
	"testing"

	sqlite "gosqlite.org"
)

// TestCompactLogicalOnlineReclaims is the headline online reclaim: delete a large
// table, checkpoint, and CompactLogicalOnline shrinks the OPEN encrypted image to
// ~live size by reading the freelist and dropping the dead slots — NO
// incremental_vacuum, NO secure_delete, no re-encryption. Data stays intact and the
// database stays writable (re-allocating a dropped page re-materialises its block). Run
// with auto_vacuum off (default) and incremental (pointer-map pages present) to prove
// the parser is independent of the inner-db setting.
func TestCompactLogicalOnlineReclaims(t *testing.T) {
	for _, tc := range []struct {
		name string
		av   sqlite.AutoVacuumMode
	}{
		{"default", ""},
		{"incremental", sqlite.AutoVacuumIncremental},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "c.db")
			key := bytes.Repeat([]byte{6}, 32)
			pragmas := sqlite.Pragmas{JournalMode: sqlite.JournalWAL}
			if tc.av != "" {
				pragmas.AutoVacuum = tc.av
			}
			db, err := Open(sqlite.Config{Path: path, Pragmas: pragmas}, Options{Key: key, PageSize: 8192})
			if err != nil {
				t.Fatal(err)
			}
			mustExec(t, db, `CREATE TABLE big(id INTEGER PRIMARY KEY, v BLOB)`)
			mustExec(t, db, `CREATE TABLE small(id INTEGER PRIMARY KEY, v BLOB)`)
			blob := make([]byte, 64*1024)
			_, _ = rand.Read(blob)
			for i := range 256 { // ~16 MiB to free
				mustExec(t, db, `INSERT INTO big(id, v) VALUES(?, ?)`, i, blob)
			}
			want := map[int][]byte{}
			for i := range 40 {
				v := append([]byte(nil), blob[:300+i]...)
				mustExec(t, db, `INSERT INTO small(id, v) VALUES(?, ?)`, i, v)
				want[i] = v
			}
			mustExec(t, db, `DELETE FROM big`) // NO incremental_vacuum
			ckpt(t, db)                        // fold the freelist into the container
			before := fileSize(t, path)

			rec, err := CompactLogicalOnline(path)
			if err != nil {
				t.Fatalf("CompactLogicalOnline: %v", err)
			}
			after := fileSize(t, path)
			t.Logf("%s: %d KiB -> %d KiB (reclaimed %d KiB), no pre-vacuum", tc.name, before/1024, after/1024, rec/1024)
			if after >= before/8 {
				t.Fatalf("did not reclaim O-live: before %d, after %d", before, after)
			}

			// Data byte-identical (this is also the parser-correctness check: a dropped
			// LIVE page would corrupt a row), big empty, and still writable + durable.
			for id, v := range want {
				var got []byte
				if err := db.QueryRow(`SELECT v FROM small WHERE id=?`, id).Scan(&got); err != nil || !bytes.Equal(got, v) {
					t.Fatalf("small row %d corrupted after reclaim (err %v)", id, err)
				}
			}
			var n int
			if err := db.QueryRow(`SELECT count(*) FROM big`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("big rows after reclaim = (%d,%v), want 0", n, err)
			}
			mustExec(t, db, `INSERT INTO small(id, v) VALUES(88888, ?)`, []byte("post-reclaim")) // re-allocates from the freelist
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db2, err := Open(sqlite.Config{Path: path, Pragmas: pragmas}, Options{Key: key, PageSize: 8192})
			if err != nil {
				t.Fatalf("reopen after reclaim: %v", err)
			}
			defer db2.Close()
			if err := db2.QueryRow(`SELECT count(*) FROM small`).Scan(&n); err != nil || n != 41 {
				t.Fatalf("small rows after reopen = (%d,%v), want 41", n, err)
			}
		})
	}
}

// TestCompactLogicalOnlineReadOnlyRefused: a read-only / read-only-recipient container
// refuses the reclaim before touching anything.
func TestCompactLogicalOnlineReadOnlyRefused(t *testing.T) {
	c := &container{readOnlyRecipient: true, blockSize: defaultBlockSize}
	if _, err := c.compactLogicalOnline(); err != ErrReadOnlyRecipient {
		t.Fatalf("compactLogicalOnline on a read-only recipient = %v, want ErrReadOnlyRecipient", err)
	}
	c = &container{readOnly: true, blockSize: defaultBlockSize}
	if _, err := c.compactLogicalOnline(); err != errReadOnly {
		t.Fatalf("compactLogicalOnline on a read-only container = %v, want errReadOnly", err)
	}
}

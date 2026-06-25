package vault

import (
	"bytes"
	"path/filepath"
	"testing"

	sqlite "gosqlite.org"
	"gosqlite.org/vfs/crypto"
)

func tmpPath(t *testing.T, name string) string { return filepath.Join(t.TempDir(), name) }

// openVaultWAL opens a WAL, incremental-auto-vacuum encrypted/plain image for the
// reclaim tests (incremental_vacuum needs auto_vacuum set at create).
func openVaultWAL(t *testing.T, path string, opts Options) (*sqlite.DB, error) {
	t.Helper()
	return Open(sqlite.Config{Path: path, Pragmas: sqlite.Pragmas{
		JournalMode: sqlite.JournalWAL,
		AutoVacuum:  sqlite.AutoVacuumIncremental,
	}}, opts)
}

func ckpt(t *testing.T, db *sqlite.DB) {
	t.Helper()
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
}

// holeScenario builds a committed container with free holes scattered BELOW live
// slots (write 20 pages, then overwrite the first 10 — copy-on-write frees their low
// slots and re-allocates new ones higher up), so online compaction has middle holes
// to relocate into. It returns the handle, its backing, and the durable image.
func holeScenario(t *testing.T, kc keyConfig) (*mainFile, *crashBacking, []byte) {
	t.Helper()
	const ps = crashPageSize
	cb := newCrashBacking(nil)
	f, err := newContainerOverFile(t, cb, ps, kc)
	if err != nil {
		t.Fatal(err)
	}
	for p := range 20 {
		if _, err := f.WriteAt(constPage(byte(p+1), ps), int64(p)*ps); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Sync(0); err != nil {
		t.Fatal(err)
	}
	for p := range 10 { // overwrite the low half → frees low slots, allocates new ones high
		if _, err := f.WriteAt(constPage(byte(0xA0+p), ps), int64(p)*ps); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Sync(0); err != nil {
		t.Fatal(err)
	}
	return f, cb, append([]byte(nil), cb.synced...)
}

// newContainerOverFile opens an (unshared) handle over back with the given key
// config — the encrypted analogue of openMainOver, for the compaction tests.
func newContainerOverFile(t *testing.T, back backing, ps uint64, kc keyConfig) (*mainFile, error) {
	t.Helper()
	ct, err := newContainerOver(back, false, defaultBlockSize, ps, 4, CompressionNone, kc)
	if err != nil {
		return nil, err
	}
	ct.refs = 1
	return &mainFile{c: ct}, nil
}

// TestCompactOnlineReclaimsAndPreserves proves online compaction shrinks the file
// (relocating middle holes), preserves the exact logical content, and leaves the
// container writable — for plaintext, encrypted, and authenticated containers.
func TestCompactOnlineReclaimsAndPreserves(t *testing.T) {
	for _, tc := range []struct {
		name string
		kc   keyConfig
	}{
		{"plaintext", keyConfig{}},
		{"encrypted", keyConfig{cipher: crypto.Adiantum, rawKey: bytes.Repeat([]byte{7}, 32)}},
		{"authenticated", keyConfig{cipher: crypto.Adiantum, rawKey: bytes.Repeat([]byte{9}, 32), authenticate: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, cb, durable := holeScenario(t, tc.kc)
			before := readAllLogical(t, f)
			beforeSize := len(cb.data)

			reclaimed, err := f.c.compactOnline(0)
			if err != nil {
				t.Fatalf("compactOnline: %v", err)
			}
			if reclaimed <= 0 {
				t.Fatalf("compactOnline reclaimed %d bytes; expected the middle holes back", reclaimed)
			}
			if len(cb.data) >= beforeSize {
				t.Fatalf("file did not shrink: %d -> %d", beforeSize, len(cb.data))
			}
			if got := readAllLogical(t, f); !bytes.Equal(got, before) {
				t.Fatal("logical content changed across online compaction")
			}
			// Still writable, and a fresh reopen over the durable post-compaction image
			// reads the same data.
			if _, err := f.WriteAt(constPage(0xEE, crashPageSize), 0); err != nil {
				t.Fatalf("write after compaction: %v", err)
			}
			_ = f.Sync(0)
			_ = f.Close()

			fr, err := newContainerOverFile(t, newCrashBacking(cb.synced), crashPageSize, tc.kc)
			if err != nil {
				t.Fatalf("reopen after compaction: %v", err)
			}
			defer fr.Close()
			got := readAllLogical(t, fr)
			want := append([]byte(nil), before...)
			copy(want, constPage(0xEE, crashPageSize)) // the post-compaction write to page 0
			if !bytes.Equal(got, want) {
				t.Fatal("reopened content wrong after compaction + write")
			}
			_ = durable
		})
	}
}

// TestCompactOnlineCrashSafe injects a crash at every backing op of an online
// compaction and proves each reopen recovers the exact pre-compaction logical
// content — never a torn or corrupt image. Compaction only relocates (logical
// content is invariant), so a consistent recovery is byte-identical data regardless
// of how far the relocation got. Run for plaintext and authenticated containers.
func TestCompactOnlineCrashSafe(t *testing.T) {
	for _, tc := range []struct {
		name string
		kc   keyConfig
	}{
		{"plaintext", keyConfig{}},
		{"authenticated", keyConfig{cipher: crypto.Adiantum, rawKey: bytes.Repeat([]byte{5}, 32), authenticate: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, durable := holeScenario(t, tc.kc)

			// Reference: the committed content before compaction, and a clean compaction
			// op count.
			fref, err := newContainerOverFile(t, newCrashBacking(durable), crashPageSize, tc.kc)
			if err != nil {
				t.Fatal(err)
			}
			before := readAllLogical(t, fref)
			_ = fref.Close()

			clean := newCrashBacking(durable)
			fc, err := newContainerOverFile(t, clean, crashPageSize, tc.kc)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fc.c.compactOnline(0); err != nil {
				t.Fatalf("clean compaction: %v", err)
			}
			if !bytes.Equal(readAllLogical(t, fc), before) {
				t.Fatal("clean compaction changed logical content")
			}
			totalOps := clean.ops
			_ = fc.Close()
			if totalOps == 0 {
				t.Fatal("compaction issued no backing ops; scenario is vacuous")
			}

			for k := 1; k <= totalOps; k++ {
				cb := newCrashBacking(durable)
				fk, err := newContainerOverFile(t, cb, crashPageSize, tc.kc)
				if err != nil {
					t.Fatalf("crash %d: open: %v", k, err)
				}
				cb.failAt = k
				_, _ = fk.c.compactOnline(0) // a crash here is expected
				_ = fk.Close()

				fr, err := newContainerOverFile(t, newCrashBacking(cb.synced), crashPageSize, tc.kc)
				if err != nil {
					t.Fatalf("crash at op %d: reopen failed: %v", k, err)
				}
				got := readAllLogical(t, fr)
				_ = fr.Close()
				if !bytes.Equal(got, before) {
					t.Fatalf("crash at op %d: recovered content differs from the committed image (TORN)", k)
				}
			}
		})
	}
}

// TestCompactOnlineAfterDelete is the end-to-end reclaim scenario through the full
// SQLite path: write a large object plus small ones into an encrypted WAL image,
// delete the large one and free its pages (incremental_vacuum + checkpoint), then
// reclaim. Tail-only Trim recovers little (the freed container blocks are scattered
// in the middle); CompactOnline recovers them down to near the offline-Compact size,
// while the database stays open and writable.
func TestCompactOnlineAfterDelete(t *testing.T) {
	path := tmpPath(t, "reclaim.db")
	key := bytes.Repeat([]byte{3}, 32)
	db, err := openVaultWAL(t, path, Options{Key: key, PageSize: 8192})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `CREATE TABLE big(id INTEGER PRIMARY KEY, v BLOB)`)
	mustExec(t, db, `CREATE TABLE small(id INTEGER PRIMARY KEY, v BLOB)`)
	blob := bytes.Repeat([]byte("xY9"), 22000) // ~64 KiB, incompressible-ish
	for i := range 256 {                       // ~16 MiB "big file"
		mustExec(t, db, `INSERT INTO big(id, v) VALUES(?, ?)`, i, blob)
	}
	for i := range 100 {
		mustExec(t, db, `INSERT INTO small(id, v) VALUES(?, ?)`, i, blob[:400])
	}
	ckpt(t, db)
	afterWrite := fileSize(t, path)

	mustExec(t, db, `DELETE FROM big`)
	if _, err := db.Exec(`PRAGMA incremental_vacuum`); err != nil {
		t.Fatal(err)
	}
	ckpt(t, db)

	tail, err := Trim(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	afterTrim := fileSize(t, path)

	rec, err := CompactOnline(path, 0)
	if err != nil {
		t.Fatalf("CompactOnline: %v", err)
	}
	afterOnline := fileSize(t, path)
	t.Logf("write=%dKiB  trim→%dKiB(+%dKiB)  online→%dKiB(+%dKiB)",
		afterWrite/1024, afterTrim/1024, tail/1024, afterOnline/1024, rec/1024)

	// Online compaction must beat tail-only Trim by a wide margin here.
	if afterOnline >= afterTrim/2 {
		t.Fatalf("CompactOnline reclaimed too little: trim left %d, online left %d", afterTrim, afterOnline)
	}
	// Data intact and still writable.
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM small`).Scan(&n); err != nil || n != 100 {
		t.Fatalf("small rows after reclaim = (%d,%v), want 100", n, err)
	}
	mustExec(t, db, `INSERT INTO small(id, v) VALUES(88888, ?)`, []byte("post-reclaim"))
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// Durable: reopens intact.
	db2, err := openVaultWAL(t, path, Options{Key: key, PageSize: 8192})
	if err != nil {
		t.Fatalf("reopen after reclaim: %v", err)
	}
	defer db2.Close()
	if err := db2.QueryRow(`SELECT count(*) FROM small`).Scan(&n); err != nil || n != 101 {
		t.Fatalf("small rows after reopen = (%d,%v), want 101", n, err)
	}
}

// TestCompactOnlineReadOnlyRefused: a read-only-recipient (authenticated, no writer)
// container must refuse online compaction, like Trim.
func TestCompactOnlineReadOnlyRefused(t *testing.T) {
	c := &container{readOnlyRecipient: true, blockSize: defaultBlockSize}
	if _, err := c.compactOnline(0); err != ErrReadOnlyRecipient {
		t.Fatalf("compactOnline on a read-only recipient = %v, want ErrReadOnlyRecipient", err)
	}
	c = &container{readOnly: true, blockSize: defaultBlockSize}
	if _, err := c.compactOnline(0); err != errReadOnly {
		t.Fatalf("compactOnline on a read-only container = %v, want errReadOnly", err)
	}
}

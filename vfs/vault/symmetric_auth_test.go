package vault

import (
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	sqlite "gosqlite.org"
)

// TestSymmetricAuthRoundTrip: Options{Key, Authenticate} authenticates with a
// symmetric MAC'd root (no ed25519 writers). It round-trips, and a reopen with
// only the Key still verifies while the on-disk authenticated flag is intact (the
// honest case). To REQUIRE verification against a keyless attacker who strips the
// flag, reopen with Authenticate — see [TestSymmetricAuthFlagStripDetected].
func TestSymmetricAuthRoundTrip(t *testing.T) {
	key := randKey(t)
	path := filepath.Join(t.TempDir(), "sa.db")

	db, err := Open(sqlite.Config{Path: path}, Options{Key: key, Authenticate: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE t(v TEXT)`); err != nil {
		t.Fatal(err)
	}
	for i := range 40 {
		if _, err := db.Exec(`INSERT INTO t VALUES(?)`, "row"+strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen with only the key (no Authenticate): while the on-disk flag is intact
	// it is still verified, and it round-trips. (A stripped flag is NOT caught on
	// this bare-key path — reopen with Authenticate to require it, see
	// [TestSymmetricAuthFlagStripDetected].)
	db2, err := Open(sqlite.Config{Path: path}, Options{Key: key})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	var n int
	if err := db2.QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil || n != 40 {
		t.Fatalf("count = (%d, %v), want 40", n, err)
	}
	var ic string
	if err := db2.QueryRow(`PRAGMA integrity_check`).Scan(&ic); err != nil || ic != "ok" {
		t.Fatalf("integrity_check = (%q, %v)", ic, err)
	}
}

// TestSymmetricAuthRequiresKey: Authenticate without a key/recipients has no
// secret to key the MAC, so it is rejected at open.
func TestSymmetricAuthRequiresKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "noauth.db")
	if _, err := Open(sqlite.Config{Path: path}, Options{Authenticate: true}); err == nil {
		t.Fatal("Authenticate without a key: want an error")
	}
}

// TestSymmetricAuthDowngradeRejected: a container created WITHOUT authentication
// must not be openable as authenticated — that would let a data-key holder strip
// the integrity (clear the flag, drop the hashes) and have it accepted.
func TestSymmetricAuthDowngradeRejected(t *testing.T) {
	key := randKey(t)
	path := filepath.Join(t.TempDir(), "down.db")

	db, err := Open(sqlite.Config{Path: path}, Options{Key: key}) // encrypted, NOT authenticated
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE t(v)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	// The VFS rejects it (ErrUnauthorized, flattened to a generic open failure
	// through SQLite's open path).
	if db2, err := Open(sqlite.Config{Path: path}, Options{Key: key, Authenticate: true}); err == nil {
		_ = db2.Close()
		t.Fatal("opening a non-authenticated container with Authenticate succeeded; want rejection")
	}
}

// TestSymmetricAuthFlagStripDetected: a keyless attacker can clear the on-disk
// authenticated flag (the superblock CRC is unkeyed) so a bare key-only reopen would
// skip verification — the raw-key symmetric flag is not self-protecting. The defense
// is to REQUIRE authentication on reopen: Options{Key, Authenticate} rejects the
// stripped container. (Recipients-with-Masters / Writers mode is self-protecting and
// covered by TestAuthDowngradeRejected.)
func TestSymmetricAuthFlagStripDetected(t *testing.T) {
	key := randKey(t)
	path := filepath.Join(t.TempDir(), "strip.db")

	db, err := Open(sqlite.Config{Path: path}, Options{Key: key, Authenticate: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE t(v TEXT)`); err != nil {
		t.Fatal(err)
	}
	for i := range 30 {
		if _, err := db.Exec(`INSERT INTO t VALUES(?)`, "row"+strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()

	// Strip the authenticated flag from the authoritative superblock and re-seal its
	// (unkeyed) CRC — exactly what a keyless on-disk attacker can do.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	a := parseOrNil(raw, 0)
	if a == nil {
		t.Fatal("no superblock A")
	}
	bs := int64(a.blockSize)
	_, slot, perr := pickSuperblockSlot(raw[:superblockSize], raw[bs:bs+superblockSize])
	if perr != nil {
		t.Fatalf("pick superblock: %v", perr)
	}
	off := int64(slot) * bs
	raw[off+sbAuthOff] = 0
	binary.LittleEndian.PutUint32(raw[off+sbCRCOff:off+sbCRCOff+4], crc32.Checksum(raw[off:off+sbCRCOff], crc32C))
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	// Reopen REQUIRING authentication: the cleared flag is rejected (ErrUnauthorized,
	// flattened through SQLite's open path). Belt and suspenders: if the open somehow
	// succeeds, a read must still fail.
	if rdb, err := Open(sqlite.Config{Path: path}, Options{Key: key, Authenticate: true}); err == nil {
		var n int
		if qerr := rdb.QueryRow(`SELECT count(*) FROM t`).Scan(&n); qerr == nil {
			t.Errorf("flag-stripped container opened and read %d rows with Authenticate; want rejection", n)
		}
		_ = rdb.Close()
	}
}

// TestSymmetricAuthDirectoryTamper: corrupting the on-disk directory of a
// symmetric-authenticated container makes reopen fail with ErrTampered (the
// directory no longer matches the MAC'd dirHash).
func TestSymmetricAuthDirectoryTamper(t *testing.T) {
	key := randKey(t)
	path := filepath.Join(t.TempDir(), "tamper.db")

	db, err := Open(sqlite.Config{Path: path}, Options{Key: key, Authenticate: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE t(v TEXT)`); err != nil {
		t.Fatal(err)
	}
	for i := range 30 {
		if _, err := db.Exec(`INSERT INTO t VALUES(?)`, "row"+strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	a := parseOrNil(raw, 0)
	if a == nil {
		t.Fatal("no superblock A")
	}
	bs := int64(a.blockSize)
	sb, _, perr := pickSuperblockSlot(raw[:superblockSize], raw[bs:bs+superblockSize])
	if perr != nil {
		t.Fatalf("pick superblock: %v", perr)
	}
	if !sb.authenticated || sb.dirOffset == 0 {
		t.Fatalf("expected an authenticated container with a directory (auth=%v dir=%d)", sb.authenticated, sb.dirOffset)
	}
	raw[sb.dirOffset] ^= 0xff
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	// The directory no longer matches the MAC'd dirHash, so the open is rejected
	// (ErrTampered, flattened through SQLite's open path). Belt and suspenders: if
	// the open somehow succeeds, a read must still fail.
	rdb, err := Open(sqlite.Config{Path: path}, Options{Key: key, Authenticate: true})
	if err == nil {
		var n int
		if qerr := rdb.QueryRow(`SELECT count(*) FROM t`).Scan(&n); qerr == nil {
			t.Errorf("tampered directory opened and read %d rows; want rejection", n)
		}
		_ = rdb.Close()
	}
}

package vault

import (
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	sqlite "gosqlite.org"
	"gosqlite.org/crypto/keyring"
)

// TestAuthenticatedReadOnly drives the full authenticated-writers / read-only
// model: a writer creates and signs the database, a read-only member reads and
// verifies it but cannot write, the writer appends more (signed), and a reader
// pinning the wrong master is rejected.
func TestAuthenticatedReadOnly(t *testing.T) {
	master, masterID, _ := keyring.GenerateMaster() // master is also the writer here
	reader, readerID, _ := keyring.GenerateX25519() // read-only member
	wrong, _, _ := keyring.GenerateMaster()
	path := filepath.Join(t.TempDir(), "auth.dbz")

	create := Options{
		Masters:    []keyring.MasterRecipient{master},
		SignWith:   masterID,
		Writers:    []keyring.WriterRecipient{master},
		WriteAs:    masterID,
		Recipients: []keyring.Recipient{reader},
	}
	db, err := Open(sqlite.Config{Path: path}, create)
	if err != nil {
		t.Fatalf("create authenticated: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE t(v TEXT)`); err != nil {
		t.Fatal(err)
	}
	for i := range 30 {
		if _, err := db.Exec(`INSERT INTO t VALUES(?)`, "row"+strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	trust := []keyring.MasterRecipient{master}

	// The read-only member reads (the writer signature verifies) but cannot write.
	rdb, err := Open(sqlite.Config{Path: path}, Options{Identities: []keyring.Identity{readerID}, Masters: trust})
	if err != nil {
		t.Fatalf("open read-only: %v", err)
	}
	var n int
	if err := rdb.QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil || n != 30 {
		t.Fatalf("read-only count = %d, err = %v", n, err)
	}
	if _, err := rdb.Exec(`INSERT INTO t VALUES('nope')`); err == nil {
		t.Error("read-only recipient write: want error")
	}
	_ = rdb.Close()

	// The writer reopens and appends (signed); the count grows.
	wdb, err := Open(sqlite.Config{Path: path}, Options{Identities: []keyring.Identity{masterID}, Masters: trust, WriteAs: masterID})
	if err != nil {
		t.Fatalf("reopen as writer: %v", err)
	}
	if _, err := wdb.Exec(`INSERT INTO t VALUES('more')`); err != nil {
		t.Fatalf("writer append: %v", err)
	}
	if err := wdb.QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil || n != 31 {
		t.Fatalf("after writer append: count = %d, err = %v", n, err)
	}
	_ = wdb.Close()

	// A reader pinning the WRONG master is rejected (the keyslot is not signed by it).
	if bdb, err := Open(sqlite.Config{Path: path}, Options{Identities: []keyring.Identity{readerID}, Masters: []keyring.MasterRecipient{wrong}}); err == nil {
		var m int
		if qerr := bdb.QueryRow(`SELECT count(*) FROM t`).Scan(&m); qerr == nil {
			t.Error("pinning the wrong master: want rejection")
		}
		_ = bdb.Close()
	}
}

// TestAuthenticatedTamperRejected corrupts the on-disk directory of an
// authenticated database and confirms reopen is rejected (the writer-signed
// directory hash no longer matches).
func TestAuthenticatedTamperRejected(t *testing.T) {
	master, masterID, _ := keyring.GenerateMaster()
	reader, readerID, _ := keyring.GenerateX25519()
	path := filepath.Join(t.TempDir(), "tamper.dbz")

	db, err := Open(sqlite.Config{Path: path}, Options{
		Masters:    []keyring.MasterRecipient{master},
		SignWith:   masterID,
		Writers:    []keyring.WriterRecipient{master},
		WriteAs:    masterID,
		Recipients: []keyring.Recipient{reader},
	})
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

	// Find the authoritative directory and flip a byte in it.
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

	// Reopening must fail: the directory no longer matches the signed hash.
	rdb, err := Open(sqlite.Config{Path: path}, Options{Identities: []keyring.Identity{readerID}, Masters: []keyring.MasterRecipient{master}})
	if err == nil {
		var n int
		if qerr := rdb.QueryRow(`SELECT count(*) FROM t`).Scan(&n); qerr == nil {
			t.Error("tampered directory opened and read: want rejection")
		}
		_ = rdb.Close()
	}
}

// TestAuthDowngradeRejected forges an authentication downgrade: it clears the
// superblock's authenticated flag (re-sealing the CRC) while keeping the genuine
// master-signed keyslot, and confirms a reader pinning the master rejects it —
// a keyslot that authorizes writers must be in authenticated mode.
func TestAuthDowngradeRejected(t *testing.T) {
	master, masterID, _ := keyring.GenerateMaster()
	reader, readerID, _ := keyring.GenerateX25519()
	path := filepath.Join(t.TempDir(), "downgrade.dbz")

	db, err := Open(sqlite.Config{Path: path}, Options{
		Masters:    []keyring.MasterRecipient{master},
		SignWith:   masterID,
		Writers:    []keyring.WriterRecipient{master},
		WriteAs:    masterID,
		Recipients: []keyring.Recipient{reader},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE t(v TEXT)`); err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
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
	_, slot, perr := pickSuperblockSlot(raw[:superblockSize], raw[bs:bs+superblockSize])
	if perr != nil {
		t.Fatalf("pick: %v", perr)
	}
	off := int64(slot) * bs                                                                                        // the authoritative superblock
	raw[off+sbAuthOff] = 0                                                                                         // strip the authenticated flag ...
	binary.LittleEndian.PutUint32(raw[off+sbCRCOff:off+sbCRCOff+4], crc32.Checksum(raw[off:off+sbCRCOff], crc32C)) // ... and re-seal its CRC
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	rdb, err := Open(sqlite.Config{Path: path}, Options{Identities: []keyring.Identity{readerID}, Masters: []keyring.MasterRecipient{master}})
	if err == nil {
		var n int
		if qerr := rdb.QueryRow(`SELECT count(*) FROM t`).Scan(&n); qerr == nil {
			t.Error("auth-stripped container opened and read: want rejection")
		}
		_ = rdb.Close()
	}
}

// TestRoleMismatchConcurrent confirms a read-only recipient opening a path a
// writer already has open is refused, rather than inheriting the writer's role
// through the shared container registry.
func TestRoleMismatchConcurrent(t *testing.T) {
	master, masterID, _ := keyring.GenerateMaster()
	reader, readerID, _ := keyring.GenerateX25519()
	path := filepath.Join(t.TempDir(), "role.dbz")

	wdb, err := Open(sqlite.Config{Path: path}, Options{
		Masters:    []keyring.MasterRecipient{master},
		SignWith:   masterID,
		Writers:    []keyring.WriterRecipient{master},
		WriteAs:    masterID,
		Recipients: []keyring.Recipient{reader},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wdb.Exec(`CREATE TABLE t(v)`); err != nil { // force the container open + registered
		t.Fatal(err)
	}
	defer wdb.Close()

	// A read-only opener (no WriteAs) must not share the writer's open container.
	rdb, err := Open(sqlite.Config{Path: path}, Options{Identities: []keyring.Identity{readerID}, Masters: []keyring.MasterRecipient{master}})
	if err == nil {
		var n int
		if qerr := rdb.QueryRow(`SELECT count(*) FROM t`).Scan(&n); qerr == nil {
			t.Error("read-only open shared the writer's container: want role-mismatch rejection")
		}
		_ = rdb.Close()
	}
}

func parseOrNil(raw []byte, off int) *superblock {
	if len(raw) < off+superblockSize {
		return nil
	}
	sb, err := parseSuperblock(raw[off : off+superblockSize])
	if err != nil {
		return nil
	}
	return sb
}

package crypto_test

import (
	"bytes"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	sqlite "gosqlite.org"
	"gosqlite.org/vfs/crypto"
)

// These exercise crypto.Open — the Config-layer convenience that registers an
// encrypting VFS, routes the Config through it, and bundles teardown into
// db.Close(). The whole package is skipped under -race by TestMain.

func TestOpen_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	db, err := crypto.Open(
		sqlite.Config{Path: filepath.Join(dir, "secret.db"), Pragmas: sqlite.RecommendedPragmas()},
		crypto.Options{Key: make([]byte, 32), Cipher: crypto.Adiantum},
	)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`CREATE TABLE t (v TEXT); INSERT INTO t VALUES ('encrypted')`); err != nil {
		t.Fatalf("exec: %v", err)
	}
	var v string
	if err := db.QueryRow(`SELECT v FROM t`).Scan(&v); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if v != "encrypted" {
		t.Errorf("v=%q, want \"encrypted\"", v)
	}
}

// TestOpen_KeyDefensiveCopy: mutating the caller's key after Open must not
// corrupt the in-flight cipher.
func TestOpen_KeyDefensiveCopy(t *testing.T) {
	dir := t.TempDir()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	db, err := crypto.Open(
		sqlite.Config{Path: filepath.Join(dir, "keymut.db")},
		crypto.Options{Key: key},
	)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	for i := range key { // scribble over the caller's slice
		key[i] = 0xAA
	}
	if _, err := db.Exec(`CREATE TABLE t (v TEXT); INSERT INTO t VALUES ('still works')`); err != nil {
		t.Fatalf("post-mutation exec: %v", err)
	}
	var v string
	if err := db.QueryRow(`SELECT v FROM t`).Scan(&v); err != nil {
		t.Fatalf("post-mutation scan: %v", err)
	}
	if v != "still works" {
		t.Errorf("v=%q, want \"still works\"", v)
	}
}

func TestOpen_BadKeyLength(t *testing.T) {
	dir := t.TempDir()
	_, err := crypto.Open(
		sqlite.Config{Path: filepath.Join(dir, "badkey.db")},
		crypto.Options{Key: make([]byte, 16), Cipher: crypto.Adiantum}, // Adiantum needs 32
	)
	if err == nil {
		t.Fatal("expected error for wrong key length, got nil")
	}
	if !strings.Contains(err.Error(), "32-byte key") {
		t.Errorf("error %q does not mention required key length", err)
	}
}

func TestOpen_InvalidCipher(t *testing.T) {
	dir := t.TempDir()
	_, err := crypto.Open(
		sqlite.Config{Path: filepath.Join(dir, "badcipher.db")},
		crypto.Options{Key: make([]byte, 32), Cipher: crypto.Cipher(99)},
	)
	if err == nil {
		t.Fatal("expected error for unknown cipher, got nil")
	}
}

func TestOpen_RejectsInMemory(t *testing.T) {
	for _, cfg := range []sqlite.Config{
		{Path: sqlite.InMemory},
		{Path: "x.db", Mode: sqlite.ModeMemory},
	} {
		if _, err := crypto.Open(cfg, crypto.Options{Key: make([]byte, 32)}); err == nil {
			t.Errorf("Open(%+v): want error for in-memory, got nil", cfg)
		} else if !strings.Contains(err.Error(), "on-disk") {
			t.Errorf("Open(%+v): error %q should mention on-disk requirement", cfg, err)
		}
	}
}

func TestOpen_RejectsVFSSet(t *testing.T) {
	if _, err := crypto.Open(
		sqlite.Config{Path: "x.db", VFS: "other"},
		crypto.Options{Key: make([]byte, 32)},
	); err == nil {
		t.Error("Open with Config.VFS set: want error, got nil")
	}
}

// TestOpen_PageSizeRoundTrip: a non-default PageSize survives close + reopen.
func TestOpen_PageSizeRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "page8k.db")
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 100)
	}
	mk := func() (*sqlite.DB, error) {
		return crypto.Open(
			sqlite.Config{Path: path, Pragmas: sqlite.RecommendedPragmas()},
			crypto.Options{Key: key, PageSize: 8192},
		)
	}
	db, err := mk()
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, err := db.Exec(`PRAGMA page_size = 8192; CREATE TABLE t (v TEXT); INSERT INTO t VALUES ('page8k');`); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	db2, err := mk()
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	t.Cleanup(func() { _ = db2.Close() })
	var v string
	if err := db2.QueryRow(`SELECT v FROM t`).Scan(&v); err != nil {
		t.Fatalf("read after reopen: %v", err)
	}
	if v != "page8k" {
		t.Errorf("v=%q, want \"page8k\"", v)
	}
}

func TestOpen_RecorderFires(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	db, err := crypto.Open(
		sqlite.Config{Path: filepath.Join(dir, "obs.db"), Pragmas: sqlite.RecommendedPragmas()},
		crypto.Options{Key: make([]byte, 32), Recorder: crypto.NewSlogRecorder(logger)},
	)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE t (v TEXT); INSERT INTO t VALUES ('obs')`); err != nil {
		t.Fatalf("exec: %v", err)
	}
	if buf.Len() == 0 {
		t.Error("recorder captured zero events for an encrypted write path")
	}
}

func TestOpen_CloseIdempotent(t *testing.T) {
	dir := t.TempDir()
	db, err := crypto.Open(
		sqlite.Config{Path: filepath.Join(dir, "idem.db")},
		crypto.Options{Key: make([]byte, 32)},
	)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Errorf("second Close: %v (should be idempotent)", err)
	}
}

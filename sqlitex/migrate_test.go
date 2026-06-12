package sqlitex_test

import (
	"context"
	"testing"
	"testing/fstest"

	"github.com/go-again/sqlite/sqlitex"
)

func migrationsFS() fstest.MapFS {
	return fstest.MapFS{
		"0001_init.sql":      {Data: []byte(`CREATE TABLE users(id INTEGER PRIMARY KEY, name TEXT);`)},
		"0002_add_email.sql": {Data: []byte(`ALTER TABLE users ADD COLUMN email TEXT;`)},
		"0003_seed.sql": {Data: []byte(`INSERT INTO users(name, email) VALUES ('alice', 'a@x');
INSERT INTO users(name, email) VALUES ('bob', 'b@x');`)},
	}
}

func TestMigrate(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()

	n, err := sqlitex.Migrate(ctx, db, migrationsFS())
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if n != 3 {
		t.Errorf("applied = %d, want 3", n)
	}
	if uv, _ := sqlitex.ResultInt(ctx, db, `PRAGMA user_version`); uv != 3 {
		t.Errorf("user_version = %d, want 3", uv)
	}
	if seeded, _ := sqlitex.ResultInt(ctx, db, `SELECT count(*) FROM users WHERE email IS NOT NULL`); seeded != 2 {
		t.Errorf("seeded users = %d, want 2", seeded)
	}

	// Idempotent re-run applies nothing.
	n2, err := sqlitex.Migrate(ctx, db, migrationsFS())
	if err != nil {
		t.Fatalf("re-run Migrate: %v", err)
	}
	if n2 != 0 {
		t.Errorf("re-run applied = %d, want 0", n2)
	}
}

func TestMigrate_FailureRollsBack(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	fsys := fstest.MapFS{
		"0001_ok.sql":  {Data: []byte(`CREATE TABLE t(x);`)},
		"0002_bad.sql": {Data: []byte(`CREATE TABLE t2(y); INSERT INTO does_not_exist VALUES (1);`)},
	}

	n, err := sqlitex.Migrate(ctx, db, fsys)
	if err == nil {
		t.Fatal("expected an error from the bad migration")
	}
	if n != 1 {
		t.Errorf("applied = %d, want 1 (0001 ok, 0002 failed)", n)
	}
	// user_version stays at 1 — the failed migration's bump rolled back.
	if uv, _ := sqlitex.ResultInt(ctx, db, `PRAGMA user_version`); uv != 1 {
		t.Errorf("user_version = %d, want 1", uv)
	}
	// The table created earlier in the failed migration must be rolled back.
	if exists, _ := sqlitex.ResultInt(ctx, db, `SELECT count(*) FROM sqlite_master WHERE name = 't2'`); exists != 0 {
		t.Errorf("failed migration's table t2 survived (count=%d); should have rolled back", exists)
	}
}

func TestLoadMigrations_Errors(t *testing.T) {
	if _, err := sqlitex.LoadMigrations(fstest.MapFS{
		"0001_a.sql": {Data: []byte(`SELECT 1;`)},
		"0001_b.sql": {Data: []byte(`SELECT 2;`)},
	}); err == nil {
		t.Error("duplicate version should error")
	}
	if _, err := sqlitex.LoadMigrations(fstest.MapFS{
		"init.sql": {Data: []byte(`SELECT 1;`)},
	}); err == nil {
		t.Error("non-numeric prefix should error")
	}
	// A leading sign must be rejected (strconv.Atoi would otherwise accept it,
	// yielding a negative or aliased version).
	for _, name := range []string{"-1_x.sql", "+5_x.sql"} {
		if _, err := sqlitex.LoadMigrations(fstest.MapFS{name: {Data: []byte(`SELECT 1;`)}}); err == nil {
			t.Errorf("signed version prefix %q should error", name)
		}
	}
	// A prefix exceeding user_version's 32-bit range must be rejected (it would
	// truncate on round-trip and break idempotency).
	if _, err := sqlitex.LoadMigrations(fstest.MapFS{
		"9999999999_x.sql": {Data: []byte(`SELECT 1;`)},
	}); err == nil {
		t.Error("over-int32 version prefix should error")
	}
}

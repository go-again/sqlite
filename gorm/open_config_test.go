package sqlite_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gorm.io/gorm"

	sqlite "gosqlite.org"
	sqlitegorm "gosqlite.org/gorm"
)

type row struct {
	ID   uint   `gorm:"primaryKey"`
	Body string `gorm:"not null"`
}

func TestOpenConfig_Plain(t *testing.T) {
	dir := t.TempDir()
	db, err := sqlitegorm.OpenConfig(sqlite.Config{
		Path: filepath.Join(dir, "plain.db"),
	})
	if err != nil {
		t.Fatalf("OpenConfig: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := db.AutoMigrate(&row{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	if err := db.Create(&row{Body: "hi"}).Error; err != nil {
		t.Fatalf("Create: %v", err)
	}
	var got row
	if err := db.First(&got).Error; err != nil {
		t.Fatalf("First: %v", err)
	}
	if got.Body != "hi" {
		t.Errorf("Body=%q, want \"hi\"", got.Body)
	}
}

func TestOpenConfig_RecommendedPragmas(t *testing.T) {
	dir := t.TempDir()
	db, err := sqlitegorm.OpenConfig(sqlite.Config{
		Path:    filepath.Join(dir, "wal.db"),
		Pragmas: sqlite.RecommendedPragmas(),
	})
	if err != nil {
		t.Fatalf("OpenConfig: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var mode string
	if err := db.Raw(`PRAGMA journal_mode`).Scan(&mode).Error; err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Errorf("journal_mode=%q, want \"wal\"", mode)
	}
}

// TestOpenConfig_PragmasPropagateAcrossPool — gorm version of the
// per-connection PRAGMA propagation guarantee. With pragmas riding
// via DSN `_pragma=` URL flags, every conn the gorm pool opens
// should report the configured values.
func TestOpenConfig_PragmasPropagateAcrossPool(t *testing.T) {
	dir := t.TempDir()
	db, err := sqlitegorm.OpenConfig(sqlite.Config{
		Path:         filepath.Join(dir, "pool-pragmas.db"),
		Pragmas:      sqlite.RecommendedPragmas(),
		MaxOpenConns: 2,
	})
	if err != nil {
		t.Fatalf("OpenConfig: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	sqlDB, err := db.DB.DB()
	if err != nil {
		t.Fatalf("get *sql.DB: %v", err)
	}
	ctx := context.Background()
	conns := make([]*sql.Conn, 2)
	for i := range conns {
		c, err := sqlDB.Conn(ctx)
		if err != nil {
			t.Fatalf("Conn[%d]: %v", i, err)
		}
		conns[i] = c
	}
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()
	for i, c := range conns {
		var fk int
		if err := c.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil {
			t.Fatalf("conn[%d] foreign_keys: %v", i, err)
		}
		if fk != 1 {
			t.Errorf("conn[%d] foreign_keys=%d, want 1 (pragmas should propagate)", i, fk)
		}
	}
}

func TestOpenConfig_Encrypted(t *testing.T) {
	if raceEnabledExt {
		t.Skip("touches vfs/crypto; same -race checkptr skip as the vfs/crypto package")
	}
	dir := t.TempDir()
	db, err := sqlitegorm.OpenConfig(sqlite.Config{
		Path:    filepath.Join(dir, "secret.db"),
		Pragmas: sqlite.RecommendedPragmas(),
		Encryption: &sqlite.Encryption{
			Key:    make([]byte, 32),
			Cipher: sqlite.Adiantum,
		},
	})
	if err != nil {
		t.Fatalf("OpenConfig: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := db.AutoMigrate(&row{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	if err := db.Create(&row{Body: "encrypted via gorm"}).Error; err != nil {
		t.Fatalf("Create: %v", err)
	}
	var got row
	if err := db.First(&got).Error; err != nil {
		t.Fatalf("First: %v", err)
	}
	if got.Body != "encrypted via gorm" {
		t.Errorf("Body=%q", got.Body)
	}
	if name := db.VFSName(); !strings.HasPrefix(name, "crypto") {
		t.Errorf("VFSName=%q, want prefix \"crypto\"", name)
	}
}

func TestOpenConfig_Errors(t *testing.T) {
	cases := []struct {
		name string
		cfg  sqlite.Config
		want string
	}{
		{"missing path", sqlite.Config{}, "Path is required"},
		{"VFS and Encryption both set", sqlite.Config{
			Path:       "/tmp/x",
			VFS:        "some_vfs",
			Encryption: &sqlite.Encryption{Key: make([]byte, 32)},
		}, "mutually exclusive"},
		{":memory: with Encryption", sqlite.Config{
			Path:       ":memory:",
			Encryption: &sqlite.Encryption{Key: make([]byte, 32)},
		}, "on-disk path"},
		{"ModeMemory with Encryption", sqlite.Config{
			Path:       "any.db",
			Mode:       sqlite.ModeMemory,
			Encryption: &sqlite.Encryption{Key: make([]byte, 32)},
		}, "on-disk path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := sqlitegorm.OpenConfig(tc.cfg)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
}

func TestOpenConfig_BackwardCompat(t *testing.T) {
	// The legacy Open(dsn) entry must continue to round-trip rows —
	// gorm users on the old API shouldn't have to migrate to keep
	// CRUD working.
	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "compat.db")
	d := sqlitegorm.Open(dsn)
	if d == nil {
		t.Fatal("legacy sqlitegorm.Open(dsn) returned nil dialector")
	}
	gormDB, err := gorm.Open(d, &gorm.Config{})
	if err != nil {
		t.Fatalf("gorm.Open(legacy dialector): %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := gormDB.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	if err := gormDB.AutoMigrate(&row{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	if err := gormDB.Create(&row{Body: "legacy"}).Error; err != nil {
		t.Fatalf("Create via legacy: %v", err)
	}
	var got row
	if err := gormDB.First(&got).Error; err != nil {
		t.Fatalf("First via legacy: %v", err)
	}
	if got.Body != "legacy" {
		t.Errorf("Body=%q, want \"legacy\"", got.Body)
	}
}

// TestOpenConfig_VariadicGormConfig pins all three call shapes:
// no gormCfg, explicit nil, explicit &gorm.Config{}. Each should
// produce a working *DB.
func TestOpenConfig_VariadicGormConfig(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		open func(path string) (*sqlitegorm.DB, error)
	}{
		{"no arg", func(p string) (*sqlitegorm.DB, error) {
			return sqlitegorm.OpenConfig(sqlite.Config{Path: p})
		}},
		{"explicit nil", func(p string) (*sqlitegorm.DB, error) {
			return sqlitegorm.OpenConfig(sqlite.Config{Path: p}, nil)
		}},
		{"empty struct", func(p string) (*sqlitegorm.DB, error) {
			return sqlitegorm.OpenConfig(sqlite.Config{Path: p}, &gorm.Config{})
		}},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, fmt.Sprintf("variadic%d.db", i))
			db, err := tc.open(path)
			if err != nil {
				t.Fatalf("OpenConfig: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			if err := db.AutoMigrate(&row{}); err != nil {
				t.Fatalf("AutoMigrate: %v", err)
			}
		})
	}
}

// TestOpenConfig_ConcurrentEncryptedOpens — gorm version of the
// concurrent-Open isolation test. Four parallel encrypted opens get
// distinct VFS names; closing half doesn't affect the survivors.
func TestOpenConfig_ConcurrentEncryptedOpens(t *testing.T) {
	if raceEnabledExt {
		t.Skip("touches vfs/crypto; same -race checkptr skip")
	}
	dir := t.TempDir()
	const n = 4
	dbs := make([]*sqlitegorm.DB, n)
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			db, err := sqlitegorm.OpenConfig(sqlite.Config{
				Path: filepath.Join(dir, fmt.Sprintf("conc%d.db", i)),
				Encryption: &sqlite.Encryption{
					Key:    make([]byte, 32),
					Cipher: sqlite.Adiantum,
				},
			})
			if err != nil {
				errs[i] = err
				return
			}
			dbs[i] = db
		}(i)
	}
	wg.Wait()
	for i, e := range errs {
		if e != nil {
			t.Fatalf("OpenConfig[%d]: %v", i, e)
		}
	}

	seen := map[string]bool{}
	for i, db := range dbs {
		name := db.VFSName()
		if seen[name] {
			t.Errorf("OpenConfig[%d] reused VFS name %q", i, name)
		}
		seen[name] = true
	}

	for i := range n / 2 {
		if err := dbs[i].Close(); err != nil {
			t.Fatalf("Close[%d]: %v", i, err)
		}
	}
	for i := n / 2; i < n; i++ {
		if err := dbs[i].AutoMigrate(&row{}); err != nil {
			t.Errorf("AutoMigrate on survivor[%d]: %v", i, err)
		}
		if err := dbs[i].Close(); err != nil {
			t.Errorf("Close survivor[%d]: %v", i, err)
		}
	}
}

// TestOpenConfig_NilSafe pins the zero-value safety guard on the gorm
// DB wrapper so accidental `var db *sqlitegorm.DB; db.Close()` doesn't
// panic.
func TestOpenConfig_NilSafe(t *testing.T) {
	var db *sqlitegorm.DB
	if err := db.Close(); err != nil {
		t.Errorf("nil receiver Close: %v", err)
	}
	if name := db.VFSName(); name != "" {
		t.Errorf("nil receiver VFSName=%q", name)
	}
}

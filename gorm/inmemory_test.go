package sqlite

import (
	"path/filepath"
	"testing"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type shortcutItem struct {
	ID    uint `gorm:"primaryKey"`
	Value string
}

func openShortcutDB(t *testing.T, dial gorm.Dialector) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(dial, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, _ := db.DB(); sqlDB != nil {
			sqlDB.Close()
		}
	})
	return db
}

func TestGorm_OpenInMemory(t *testing.T) {
	db := openShortcutDB(t, OpenInMemory())
	if err := db.AutoMigrate(&shortcutItem{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&shortcutItem{Value: "x"}).Error; err != nil {
		t.Fatal(err)
	}
	var n int64
	db.Model(&shortcutItem{}).Count(&n)
	if n != 1 {
		t.Errorf("count=%d, want 1", n)
	}
}

// TestGorm_OpenWAL: production preset applies. WAL mode is a DB-file
// attribute so we can verify it via a raw PRAGMA on any conn.
func TestGorm_OpenWAL(t *testing.T) {
	dir := t.TempDir()
	db := openShortcutDB(t, OpenWAL(filepath.Join(dir, "wal.db")))
	var mode string
	if err := db.Raw(`PRAGMA journal_mode`).Scan(&mode).Error; err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode=%q, want \"wal\"", mode)
	}
}

// TestGorm_OpenReadOnly: writes through gorm must fail once the dialect
// is opened in mode=ro.
func TestGorm_OpenReadOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "seed.db")

	// Seed via OpenWAL, close.
	seed := openShortcutDB(t, OpenWAL(path))
	if err := seed.AutoMigrate(&shortcutItem{}); err != nil {
		t.Fatal(err)
	}
	if err := seed.Create(&shortcutItem{Value: "seed"}).Error; err != nil {
		t.Fatal(err)
	}
	if sqlDB, _ := seed.DB(); sqlDB != nil {
		sqlDB.Close()
	}

	ro := openShortcutDB(t, OpenReadOnly(path))
	var rows []shortcutItem
	if err := ro.Find(&rows).Error; err != nil {
		t.Fatalf("read from RO open: %v", err)
	}
	if len(rows) != 1 || rows[0].Value != "seed" {
		t.Errorf("rows=%+v, want one {Value:\"seed\"}", rows)
	}
	if err := ro.Create(&shortcutItem{Value: "denied"}).Error; err == nil {
		t.Error("write through gorm to RO DB: want error, got nil")
	}
}

// TestGorm_OpenShared: two dialectors at the same name see the same
// rows — the shared-cache contract OpenShared exists for.
func TestGorm_OpenShared(t *testing.T) {
	const name = "gorm-shortcuts-shared-test"

	a := openShortcutDB(t, OpenShared(name))
	if err := a.AutoMigrate(&shortcutItem{}); err != nil {
		t.Fatal(err)
	}
	if err := a.Create(&[]shortcutItem{{Value: "one"}, {Value: "two"}}).Error; err != nil {
		t.Fatal(err)
	}

	b := openShortcutDB(t, OpenShared(name))
	var n int64
	if err := b.Model(&shortcutItem{}).Count(&n).Error; err != nil {
		t.Fatalf("b count: %v", err)
	}
	if n != 2 {
		t.Errorf("b count=%d, want 2 (shared cache should expose a's writes)", n)
	}
}

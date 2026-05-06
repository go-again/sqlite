// Copyright 2026 The go-again/sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be found
// in the LICENSE file.

package sqlite

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// User is a typical gorm model with the conventional fields a consumer would
// declare. We exercise AutoMigrate / Create / First / Find / Update / Delete /
// Where queries against it.
type User struct {
	ID        uint `gorm:"primaryKey"`
	Name      string
	Email     string `gorm:"uniqueIndex"`
	Age       int
	CreatedAt time.Time
	UpdatedAt time.Time
}

func openInMemory(t *testing.T) *gorm.DB {
	t.Helper()
	const dsn = "file:gorm-int?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := gorm.Open(Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			sqlDB.Close()
		}
	})
	return db
}

// TestGorm_Mattn3DriverName verifies the dialector also accepts the mattn-style
// "sqlite3" driver name, since github.com/go-again/sqlite registers under both
// names. Existing go-gorm/sqlite users typically pass DriverName="sqlite3".
func TestGorm_Mattn3DriverName(t *testing.T) {
	db, err := gorm.Open(New(Config{
		DriverName: "sqlite3",
		DSN:        "file:gorm-mattn3?mode=memory&cache=shared",
	}), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	if err := db.AutoMigrate(&User{}); err != nil {
		t.Fatal(err)
	}
}

// TestGorm_NewWithConfig verifies the sqlite.New(Config{...}) form used by
// the official go-gorm/sqlite driver works as a drop-in.
func TestGorm_NewWithConfig(t *testing.T) {
	db, err := gorm.Open(New(Config{
		DriverName: DriverName,
		DSN:        "file:gorm-new?mode=memory&cache=shared",
	}), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	if err := db.AutoMigrate(&User{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&User{Email: "n@example.com"}).Error; err != nil {
		t.Fatal(err)
	}
}

func TestGorm_AutoMigrate_CreatesTable(t *testing.T) {
	db := openInMemory(t)
	if err := db.AutoMigrate(&User{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	if !db.Migrator().HasTable(&User{}) {
		t.Fatal("HasTable(User) returned false after AutoMigrate")
	}
	if !db.Migrator().HasIndex(&User{}, "idx_users_email") {
		t.Errorf("HasIndex(idx_users_email) returned false")
	}
}

func TestGorm_CreateAndFirst(t *testing.T) {
	db := openInMemory(t)
	if err := db.AutoMigrate(&User{}); err != nil {
		t.Fatal(err)
	}
	u := User{Name: "alice", Email: "alice@example.com", Age: 30}
	if err := db.Create(&u).Error; err != nil {
		t.Fatalf("Create: %v", err)
	}
	if u.ID == 0 {
		t.Fatal("ID not populated after Create")
	}
	var got User
	if err := db.First(&got, u.ID).Error; err != nil {
		t.Fatalf("First: %v", err)
	}
	if got.Email != "alice@example.com" || got.Age != 30 {
		t.Errorf("got %+v, want alice@example.com / age 30", got)
	}
}

func TestGorm_UpdateAndDelete(t *testing.T) {
	db := openInMemory(t)
	if err := db.AutoMigrate(&User{}); err != nil {
		t.Fatal(err)
	}
	u := User{Name: "bob", Email: "bob@example.com", Age: 25}
	db.Create(&u)

	if err := db.Model(&u).Update("Age", 26).Error; err != nil {
		t.Fatalf("Update: %v", err)
	}
	var got User
	db.First(&got, u.ID)
	if got.Age != 26 {
		t.Errorf("after update Age=%d, want 26", got.Age)
	}

	if err := db.Delete(&u).Error; err != nil {
		t.Fatalf("Delete: %v", err)
	}
	err := db.First(&got, u.ID).Error
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("after delete First err=%v, want gorm.ErrRecordNotFound", err)
	}
}

func TestGorm_UniqueViolation_TranslatedToErrDuplicatedKey(t *testing.T) {
	const dsn = "file:gorm-unique?mode=memory&cache=shared"
	db, err := gorm.Open(Open(dsn), &gorm.Config{
		Logger:         logger.Default.LogMode(logger.Silent),
		TranslateError: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB, _ := db.DB(); sqlDB.Close() })

	if err := db.AutoMigrate(&User{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&User{Email: "dup@example.com"}).Error; err != nil {
		t.Fatal(err)
	}
	err = db.Create(&User{Email: "dup@example.com"}).Error
	if !errors.Is(err, gorm.ErrDuplicatedKey) {
		t.Errorf("got %v, want gorm.ErrDuplicatedKey", err)
	}
}

func TestGorm_FileBackedRoundTrip(t *testing.T) {
	// Verify the dialector also works with on-disk databases — common case for
	// CLI tools and tests that need persistence between runs of the same
	// process.
	path := filepath.Join(t.TempDir(), "gorm-file.db")
	dsn := "file:" + path + "?_pragma=foreign_keys(1)"

	open := func() *gorm.DB {
		db, err := gorm.Open(Open(dsn), &gorm.Config{
			Logger: logger.Default.LogMode(logger.Silent),
		})
		if err != nil {
			t.Fatal(err)
		}
		return db
	}

	db := open()
	if err := db.AutoMigrate(&User{}); err != nil {
		t.Fatal(err)
	}
	u := User{Name: "carol", Email: "carol@example.com"}
	if err := db.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.Close()

	db2 := open()
	defer func() { sqlDB, _ := db2.DB(); sqlDB.Close() }()
	var got User
	if err := db2.First(&got, "email = ?", "carol@example.com").Error; err != nil {
		t.Fatalf("after reopen First: %v", err)
	}
	if got.Name != "carol" {
		t.Errorf("got Name=%q, want carol", got.Name)
	}
}

func TestGorm_RawSQL_AndScan(t *testing.T) {
	db := openInMemory(t)
	if err := db.AutoMigrate(&User{}); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"x", "y", "z"} {
		db.Create(&User{Name: n, Email: n + "@e.com"})
	}
	var count int64
	if err := db.Raw("SELECT count(*) FROM users").Scan(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Errorf("count=%d, want 3", count)
	}
}

func TestGorm_Transaction_CommitAndRollback(t *testing.T) {
	db := openInMemory(t)
	db.AutoMigrate(&User{})

	// Commit
	if err := db.Transaction(func(tx *gorm.DB) error {
		return tx.Create(&User{Name: "tx1", Email: "tx1@example.com"}).Error
	}); err != nil {
		t.Fatal(err)
	}

	// Rollback
	want := errors.New("rollback me")
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&User{Name: "tx2", Email: "tx2@example.com"}).Error; err != nil {
			return err
		}
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("Transaction err=%v, want %v", err, want)
	}

	var n int64
	db.Model(&User{}).Count(&n)
	if n != 1 {
		t.Errorf("after rollback count=%d, want 1 (only the committed row)", n)
	}
}

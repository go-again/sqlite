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

// TestGorm_Migrator_RecreateRollbackOnCheckConstraint exercises the
// recreate-table path in (Migrator).recreateTable: drop a column from a
// table whose existing rows violate a CHECK constraint on the new
// schema. The Tx (CREATE __temp / INSERT / DROP / RENAME) must roll
// back as a unit; the original table must survive intact.
func TestGorm_Migrator_RecreateRollbackOnCheckConstraint(t *testing.T) {
	db := openInMemory(t)

	if err := db.Exec(`CREATE TABLE widgets (
		id INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		size INTEGER NOT NULL CHECK(size >= 0)
	)`).Error; err != nil {
		t.Fatalf("create widgets: %v", err)
	}
	if err := db.Exec(`INSERT INTO widgets(name, size) VALUES ('alpha', 5), ('beta', 10)`).Error; err != nil {
		t.Fatalf("seed widgets: %v", err)
	}

	// AlterColumn on size with a tighter CHECK that one of the existing
	// rows violates (size > 5) forces the INSERT into the recreated
	// table to fail mid-Tx. recreateTable's transaction must roll back.
	type Widget struct {
		ID   uint   `gorm:"primaryKey"`
		Name string `gorm:"not null"`
		Size int    `gorm:"check:size > 5;not null"`
	}
	err := db.AutoMigrate(&Widget{})
	if err == nil {
		t.Fatalf("AutoMigrate: want CHECK-constraint failure, got nil")
	}

	// Original table must still hold both rows; the recreate Tx must
	// have rolled back rather than leaving a half-applied schema.
	var rows []struct {
		ID   int
		Name string
		Size int
	}
	if err := db.Raw(`SELECT id, name, size FROM widgets ORDER BY id`).Scan(&rows).Error; err != nil {
		t.Fatalf("post-rollback select: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("after rollback rows=%d, want 2 (original schema intact)", len(rows))
	}

	// Lingering __temp table from a failed recreate would be the
	// canonical signal of a botched rollback.
	var leftover int
	db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name LIKE '%__temp%'`).Scan(&leftover)
	if leftover != 0 {
		t.Errorf("found %d leftover __temp table(s); recreate rollback was incomplete", leftover)
	}
}

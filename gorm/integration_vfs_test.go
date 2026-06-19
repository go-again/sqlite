package sqlite

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	sqlite "gosqlite.org"
	"gosqlite.org/vfs"
)

// TestGormVFS_ReadFromMapFS demonstrates that the vfs sub-package composes
// with the gorm dialector via the DSN: register an fs.FS, pass ?vfs=<name>,
// and the gorm pool reads through the VFS like any other open.
//
// The point of the test is to prove integration is "just DSN" — no
// gorm-side code changes are needed. If this ever breaks, vfs's
// auto-registration or the dialector's DSN parsing has regressed.
func TestGormVFS_ReadFromMapFS(t *testing.T) {
	// Build a real SQLite database on disk to get a valid file image.
	tmp := filepath.Join(t.TempDir(), "seed.db")
	src, err := sql.Open(sqlite.DriverNameSQLite3, tmp)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := src.Exec(`CREATE TABLE notes (id INTEGER PRIMARY KEY, text TEXT);
INSERT INTO notes (text) VALUES ('alpha'), ('beta'), ('gamma');`); err != nil {
		t.Fatal(err)
	}
	src.Close()

	seedBytes, err := os.ReadFile(tmp)
	if err != nil {
		t.Fatal(err)
	}

	// Serve those bytes through a Go fs.FS and register as a SQLite VFS.
	embedded := fstest.MapFS{"seed.db": &fstest.MapFile{Data: seedBytes}}
	name, _, err := vfs.New(embedded)
	if err != nil {
		t.Fatalf("vfs.New: %v", err)
	}

	// Open gorm against the registered VFS in read-only mode.
	dsn := "file:seed.db?vfs=" + name + "&mode=ro"
	db, err := gorm.Open(Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("gorm.Open via vfs: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			sqlDB.Close()
		}
	})

	type Note struct {
		ID   uint
		Text string
	}
	var got []Note
	if err := db.Order("id").Find(&got).Error; err != nil {
		t.Fatalf("Find: %v", err)
	}
	want := []string{"alpha", "beta", "gamma"}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Text != w {
			t.Errorf("[%d] Text=%q, want %q", i, got[i].Text, w)
		}
	}
}

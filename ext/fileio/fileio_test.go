package fileio_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	sqlite "gosqlite.org"
	"gosqlite.org/ext/fileio"
	"gosqlite.org/internal/testhelp"
)

func openOS(t *testing.T) (*sql.DB, *sql.Conn) {
	t.Helper()
	db, sc := testhelp.OpenPinned(t, "sqlite", ":memory:")
	testhelp.RegisterOn(t, sc, fileio.Register)
	return db, sc
}

func openFS(t *testing.T, fsys fstest.MapFS) (*sql.DB, *sql.Conn) {
	t.Helper()
	db, sc := testhelp.OpenPinned(t, "sqlite", ":memory:")
	testhelp.RegisterOn(t, sc, func(c *sqlite.Conn) error { return fileio.RegisterFS(c, fsys) })
	return db, sc
}

func TestLsmode(t *testing.T) {
	_, sc := openOS(t)
	for _, tc := range []struct {
		mode int64
		want string
	}{
		{int64(0o644), "-rw-r--r--"},
		{int64(0o755) | int64(os.ModeDir), "drwxr-xr-x"},
	} {
		var got string
		if err := sc.QueryRowContext(context.Background(),
			`SELECT lsmode(?)`, tc.mode).Scan(&got); err != nil {
			t.Errorf("lsmode(%o): %v", tc.mode, err)
			continue
		}
		if got != tc.want {
			t.Errorf("lsmode(%o)=%q, want %q", tc.mode, got, tc.want)
		}
	}
}

func TestReadfile_OS(t *testing.T) {
	_, sc := openOS(t)
	tmp := t.TempDir()
	path := filepath.Join(tmp, "hello.txt")
	want := []byte("hello, fileio")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	var got []byte
	if err := sc.QueryRowContext(context.Background(),
		`SELECT readfile(?)`, path).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("got %q, want %q", got, want)
	}

	// Missing file → NULL (which Scan into []byte yields as nil).
	got = nil
	if err := sc.QueryRowContext(context.Background(),
		`SELECT readfile(?)`, filepath.Join(tmp, "nope.txt")).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("missing file readfile got %v, want nil", got)
	}
}

func TestReadfile_FS_Sandbox(t *testing.T) {
	fsys := fstest.MapFS{
		"hello.txt": &fstest.MapFile{Data: []byte("from-fsys")},
	}
	_, sc := openFS(t, fsys)
	var got []byte
	if err := sc.QueryRowContext(context.Background(),
		`SELECT readfile('hello.txt')`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "from-fsys" {
		t.Errorf("got %q, want %q", got, "from-fsys")
	}
}

func TestWritefile_NotRegisteredInFSMode(t *testing.T) {
	_, sc := openFS(t, fstest.MapFS{})
	_, err := sc.ExecContext(context.Background(),
		`SELECT writefile('/tmp/x', 'data')`)
	if err == nil {
		t.Error("writefile in FS mode: want error, got nil")
	}
}

func TestWritefile_OSRoundTrip(t *testing.T) {
	_, sc := openOS(t)
	tmp := t.TempDir()
	path := filepath.Join(tmp, "out", "f.txt")
	var written int64
	if err := sc.QueryRowContext(context.Background(),
		`SELECT writefile(?, ?)`, path, []byte("payload")).Scan(&written); err != nil {
		t.Fatal(err)
	}
	if written != 7 {
		t.Errorf("written=%d, want 7", written)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "payload" {
		t.Errorf("file=%q, want \"payload\"", got)
	}
}

func TestFsdir_OS(t *testing.T) {
	_, sc := openOS(t)
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmp, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "sub", "b.txt"), []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}

	rows, err := sc.QueryContext(context.Background(),
		`SELECT name FROM fsdir(?) WHERE name != '' ORDER BY name`, tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	wantNames := []string{"a.txt", "sub", filepath.Join("sub", "b.txt")}
	sort.Strings(names)
	sort.Strings(wantNames)
	if strings.Join(names, ",") != strings.Join(wantNames, ",") {
		t.Errorf("names=%v, want %v", names, wantNames)
	}
}

func TestFsdir_FSDepthCap(t *testing.T) {
	fsys := fstest.MapFS{
		"root.txt":         &fstest.MapFile{Data: []byte("r")},
		"a/level1.txt":     &fstest.MapFile{Data: []byte("a1")},
		"a/b/level2.txt":   &fstest.MapFile{Data: []byte("a2")},
		"a/b/c/level3.txt": &fstest.MapFile{Data: []byte("a3")},
	}
	_, sc := openFS(t, fsys)
	rows, err := sc.QueryContext(context.Background(),
		`SELECT name FROM fsdir(?, ?) WHERE name != '' AND name != '.'`,
		".", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	// At max depth=2 we should see root.txt, a, a/b, and a/level1.txt
	// (depths 1, 1, 2, 2) but not level2 or level3.
	for _, n := range names {
		if strings.Contains(n, "level2") || strings.Contains(n, "level3") {
			t.Errorf("depth cap leaked: %s", n)
		}
	}
	if len(names) == 0 {
		t.Errorf("got no entries, want some")
	}
}

func TestFsdir_RequiresPathConstraint(t *testing.T) {
	_, sc := openOS(t)
	_, err := sc.QueryContext(context.Background(),
		`SELECT * FROM fsdir`)
	if err == nil {
		t.Error("fsdir without path: want error, got nil")
	}
}

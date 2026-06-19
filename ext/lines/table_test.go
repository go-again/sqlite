package lines_test

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"slices"
	"testing"
	"testing/fstest"

	sqlite "gosqlite.org"
	"gosqlite.org/ext/lines"
	"gosqlite.org/internal/testhelp"
)

// openLinesDB returns a *sql.DB with the lines module (rooted at fsys) on
// every connection, pinned to one conn. The typed Table API runs over the
// *sql.DB directly, so the module must be pool-wide.
func openLinesDB(t *testing.T, fsys fs.FS) *sql.DB {
	t.Helper()
	testhelp.WithConnectHook(t, func(c *sqlite.Conn) error { return lines.RegisterFS(c, fsys) })
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestTable_Create_Columns_Rows(t *testing.T) {
	ctx := context.Background()
	db := openLinesDB(t, fstest.MapFS{
		"app.log": {Data: []byte("alpha\nbravo\ncharlie\n")},
	})
	tbl, err := lines.Create(ctx, db, "log", lines.WithFilename("app.log"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	cols, err := tbl.Columns(ctx)
	if err != nil {
		t.Fatalf("Columns: %v", err)
	}
	if want := []string{"lineno", "line"}; !slices.Equal(cols, want) {
		t.Errorf("Columns = %v, want %v", cols, want)
	}
	rows, err := tbl.Rows(ctx)
	if err != nil {
		t.Fatalf("Rows: %v", err)
	}
	defer rows.Close()
	var got []string
	var wantLineno int64 = 1
	for rows.Next() {
		var n int64
		var line string
		if err := rows.Scan(&n, &line); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if n != wantLineno {
			t.Errorf("lineno = %d, want %d", n, wantLineno)
		}
		wantLineno++
		got = append(got, line)
	}
	if want := []string{"alpha", "bravo", "charlie"}; !slices.Equal(got, want) {
		t.Errorf("lines = %v, want %v", got, want)
	}
}

func TestTable_WithData_Inline(t *testing.T) {
	ctx := context.Background()
	db := openLinesDB(t, fstest.MapFS{})
	tbl, err := lines.Create(ctx, db, "t", lines.WithData("one\ntwo\nthree\n"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	var n int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+tbl.Name()).Scan(&n); err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 3 {
		t.Errorf("COUNT(*) = %d, want 3", n)
	}
}

func TestTable_Create_QuotesValues(t *testing.T) {
	ctx := context.Background()
	db := openLinesDB(t, fstest.MapFS{})
	// Inline data containing a single quote: Create must escape it so the
	// USING lines(data='…') argument string stays valid — the footgun the
	// typed API removes.
	tbl, err := lines.Create(ctx, db, "t", lines.WithData("it's a line\nplain\n"))
	if err != nil {
		t.Fatalf("Create with apostrophe in data: %v", err)
	}
	var line string
	if err := db.QueryRowContext(ctx, "SELECT line FROM "+tbl.Name()+" WHERE lineno = 1").Scan(&line); err != nil {
		t.Fatalf("query: %v", err)
	}
	if line != "it's a line" {
		t.Errorf("line = %q, want %q", line, "it's a line")
	}
}

func TestTable_Create_RequiresExactlyOneSource(t *testing.T) {
	ctx := context.Background()
	db := openLinesDB(t, fstest.MapFS{})
	if _, err := lines.Create(ctx, db, "t"); err == nil {
		t.Error("Create with neither WithFilename nor WithData should error")
	}
	if _, err := lines.Create(ctx, db, "t", lines.WithFilename("x.log"), lines.WithData("a\n")); err == nil {
		t.Error("Create with both WithFilename and WithData should error")
	}
}

func TestTable_Create_WithIfNotExists_Idempotent(t *testing.T) {
	ctx := context.Background()
	db := openLinesDB(t, fstest.MapFS{})
	if _, err := lines.Create(ctx, db, "t", lines.WithData("a\n"), lines.WithIfNotExists()); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := lines.Create(ctx, db, "t", lines.WithData("a\n"), lines.WithIfNotExists()); err != nil {
		t.Fatalf("second Create with WithIfNotExists: %v", err)
	}
}

func TestTable_Create_ErrAlreadyExists(t *testing.T) {
	ctx := context.Background()
	db := openLinesDB(t, fstest.MapFS{})
	if _, err := lines.Create(ctx, db, "t", lines.WithData("a\n")); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := lines.Create(ctx, db, "t", lines.WithData("a\n")); !errors.Is(err, lines.ErrAlreadyExists) {
		t.Errorf("second Create error = %v, want ErrAlreadyExists", err)
	}
}

func TestTable_Drop(t *testing.T) {
	ctx := context.Background()
	db := openLinesDB(t, fstest.MapFS{})
	tbl, err := lines.Create(ctx, db, "t", lines.WithData("a\n"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := tbl.Drop(ctx); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	if _, err := lines.Create(ctx, db, "t", lines.WithData("a\n")); err != nil {
		t.Errorf("Create after Drop: %v", err)
	}
}

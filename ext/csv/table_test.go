package csv_test

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"slices"
	"testing"
	"testing/fstest"

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/csv"
	"github.com/go-again/sqlite/internal/testhelp"
)

// openCSVDB returns a *sql.DB with the csv module (rooted at fsys) on every
// connection, pinned to one conn. The typed Table API runs over the
// *sql.DB directly, so the module must be pool-wide.
func openCSVDB(t *testing.T, fsys fs.FS) *sql.DB {
	t.Helper()
	testhelp.WithConnectHook(t, func(c *sqlite.Conn) error { return csv.RegisterFS(c, fsys) })
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
	db := openCSVDB(t, fstest.MapFS{
		"data.csv": {Data: []byte("name,age\nalice,30\nbob,25\n")},
	})
	tbl, err := csv.Create(ctx, db, "people", csv.WithFilename("data.csv"), csv.WithHeader())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	cols, err := tbl.Columns(ctx)
	if err != nil {
		t.Fatalf("Columns: %v", err)
	}
	if want := []string{"name", "age"}; !slices.Equal(cols, want) {
		t.Errorf("Columns = %v, want %v", cols, want)
	}
	rows, err := tbl.Rows(ctx)
	if err != nil {
		t.Fatalf("Rows: %v", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name, age string
		if err := rows.Scan(&name, &age); err != nil {
			t.Fatalf("scan: %v", err)
		}
		names = append(names, name)
	}
	if want := []string{"alice", "bob"}; !slices.Equal(names, want) {
		t.Errorf("rows = %v, want %v", names, want)
	}
}

func TestTable_WithData_Inline(t *testing.T) {
	ctx := context.Background()
	db := openCSVDB(t, fstest.MapFS{})
	tbl, err := csv.Create(ctx, db, "t", csv.WithData("k,v\nx,1\ny,2\n"), csv.WithHeader())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	var sum int
	if err := db.QueryRowContext(ctx, "SELECT SUM(v) FROM "+tbl.Name()).Scan(&sum); err != nil {
		t.Fatalf("query: %v", err)
	}
	if sum != 3 {
		t.Errorf("SUM(v) = %d, want 3", sum)
	}
}

func TestTable_WithComma(t *testing.T) {
	ctx := context.Background()
	db := openCSVDB(t, fstest.MapFS{"d.csv": {Data: []byte("a;b\n1;2\n")}})
	tbl, err := csv.Create(ctx, db, "t", csv.WithFilename("d.csv"), csv.WithHeader(), csv.WithComma(';'))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	cols, _ := tbl.Columns(ctx)
	if want := []string{"a", "b"}; !slices.Equal(cols, want) {
		t.Errorf("Columns = %v, want %v (semicolon-delimited)", cols, want)
	}
}

func TestTable_WithComment(t *testing.T) {
	ctx := context.Background()
	db := openCSVDB(t, fstest.MapFS{
		"c.csv": {Data: []byte("# a comment line\nname,age\n# another comment\nalice,30\nbob,25\n")},
	})
	tbl, err := csv.Create(ctx, db, "t", csv.WithFilename("c.csv"), csv.WithHeader(), csv.WithComment('#'))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	var n int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+tbl.Name()).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("WithComment row count = %d, want 2 (comment lines skipped)", n)
	}
}

func TestTable_WithColumns(t *testing.T) {
	ctx := context.Background()
	db := openCSVDB(t, fstest.MapFS{"d.csv": {Data: []byte("1,2,3\n4,5,6\n")}})
	tbl, err := csv.Create(ctx, db, "t", csv.WithFilename("d.csv"), csv.WithColumns(3))
	if err != nil {
		t.Fatalf("Create with WithColumns: %v", err)
	}
	cols, err := tbl.Columns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 3 {
		t.Errorf("WithColumns(3) produced %d columns (%v), want 3", len(cols), cols)
	}
}

func TestTable_Create_QuotesValues(t *testing.T) {
	ctx := context.Background()
	db := openCSVDB(t, fstest.MapFS{})
	// Inline data containing a single quote: Create must escape it so the
	// USING csv(data='…') argument string stays valid — the footgun the
	// typed API removes.
	tbl, err := csv.Create(ctx, db, "t", csv.WithData("name\nO'Brien\n"), csv.WithHeader())
	if err != nil {
		t.Fatalf("Create with apostrophe in data: %v", err)
	}
	var name string
	if err := db.QueryRowContext(ctx, "SELECT name FROM "+tbl.Name()).Scan(&name); err != nil {
		t.Fatalf("query: %v", err)
	}
	if name != "O'Brien" {
		t.Errorf("name = %q, want O'Brien", name)
	}
}

func TestTable_Create_RequiresExactlyOneSource(t *testing.T) {
	ctx := context.Background()
	db := openCSVDB(t, fstest.MapFS{})
	if _, err := csv.Create(ctx, db, "t", csv.WithHeader()); err == nil {
		t.Error("Create with neither WithFilename nor WithData should error")
	}
	if _, err := csv.Create(ctx, db, "t", csv.WithFilename("x.csv"), csv.WithData("a\n1\n")); err == nil {
		t.Error("Create with both WithFilename and WithData should error")
	}
}

func TestTable_Create_WithIfNotExists_Idempotent(t *testing.T) {
	ctx := context.Background()
	db := openCSVDB(t, fstest.MapFS{})
	if _, err := csv.Create(ctx, db, "t", csv.WithData("a\n1\n"), csv.WithHeader(), csv.WithIfNotExists()); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := csv.Create(ctx, db, "t", csv.WithData("a\n1\n"), csv.WithHeader(), csv.WithIfNotExists()); err != nil {
		t.Fatalf("second Create with WithIfNotExists: %v", err)
	}
}

func TestTable_Create_ErrAlreadyExists(t *testing.T) {
	ctx := context.Background()
	db := openCSVDB(t, fstest.MapFS{})
	if _, err := csv.Create(ctx, db, "t", csv.WithData("a\n1\n"), csv.WithHeader()); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := csv.Create(ctx, db, "t", csv.WithData("a\n1\n"), csv.WithHeader()); !errors.Is(err, csv.ErrAlreadyExists) {
		t.Errorf("second Create error = %v, want ErrAlreadyExists", err)
	}
}

func TestTable_Drop(t *testing.T) {
	ctx := context.Background()
	db := openCSVDB(t, fstest.MapFS{})
	tbl, err := csv.Create(ctx, db, "t", csv.WithData("a\n1\n"), csv.WithHeader())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := tbl.Drop(ctx); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	if _, err := csv.Create(ctx, db, "t", csv.WithData("a\n1\n"), csv.WithHeader()); err != nil {
		t.Errorf("Create after Drop: %v", err)
	}
}

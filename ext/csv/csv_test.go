package csv_test

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/csv"
	"github.com/go-again/sqlite/ext/internal/filevtab"
)

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

func withCSV(t *testing.T, fsys fs.FS) (*sql.DB, *sql.Conn) {
	t.Helper()
	db, err := sql.Open(sqlite.DriverName, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	sc, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	t.Cleanup(func() { _ = sc.Close() })
	if err := sc.Raw(func(driverConn any) error {
		c, ok := driverConn.(*sqlite.Conn)
		if !ok {
			return errors.New("not *sqlite.Conn")
		}
		if fsys == nil {
			return csv.Register(c)
		}
		return csv.RegisterFS(c, fsys)
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return db, sc
}

func TestCSV_InlineDataNoHeader(t *testing.T) {
	_, sc := withCSV(t, nil)
	ctx := context.Background()
	if _, err := sc.ExecContext(ctx,
		`CREATE VIRTUAL TABLE temp.t USING csv(data='1,2,3
4,5,6
7,8,9')`); err != nil {
		t.Fatalf("CREATE VIRTUAL TABLE: %v", err)
	}
	rows, err := sc.QueryContext(ctx, `SELECT c1, c2, c3 FROM temp.t`)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	defer rows.Close()
	var got [][]string
	for rows.Next() {
		var a, b, c string
		if err := rows.Scan(&a, &b, &c); err != nil {
			t.Fatal(err)
		}
		got = append(got, []string{a, b, c})
	}
	want := [][]string{{"1", "2", "3"}, {"4", "5", "6"}, {"7", "8", "9"}}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d: %v", len(got), len(want), got)
	}
	for i, row := range got {
		if strings.Join(row, ",") != strings.Join(want[i], ",") {
			t.Errorf("row[%d] = %v, want %v", i, row, want[i])
		}
	}
}

func TestCSV_HeaderRow(t *testing.T) {
	// header=on uses row 1 as column names; row 2+ are data.
	fsys := fstest.MapFS{
		"data.csv": {Data: []byte("name,age\nAlice,30\nBob,42\nCarol,28\n")},
	}
	_, sc := withCSV(t, fsys)
	ctx := context.Background()
	if _, err := sc.ExecContext(ctx,
		`CREATE VIRTUAL TABLE temp.people USING csv(filename='data.csv', header=on)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	rows, err := sc.QueryContext(ctx, `SELECT name, age FROM temp.people ORDER BY name`)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	defer rows.Close()
	type row struct{ Name, Age string }
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.Name, &r.Age); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if len(got) != 3 || got[0].Name != "Alice" || got[2].Name != "Carol" {
		t.Errorf("got %v, want 3 rows alphabetical", got)
	}
}

func TestCSV_ExplicitSchemaWithAffinity(t *testing.T) {
	// schema=... with INTEGER and REAL affinity → CSV strings become
	// SQL INTEGER and REAL values, so SUM works as expected.
	fsys := fstest.MapFS{
		"sales.csv": {Data: []byte("region,amount\nNorth,1200\nSouth,800.50\nEast,1500\nWest,950.25\n")},
	}
	_, sc := withCSV(t, fsys)
	ctx := context.Background()
	if _, err := sc.ExecContext(ctx,
		`CREATE VIRTUAL TABLE temp.sales USING csv(
		    filename='sales.csv', header=on,
		    schema='CREATE TABLE x(region TEXT, amount REAL)')`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	var total float64
	if err := sc.QueryRowContext(ctx, `SELECT SUM(amount) FROM temp.sales`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total < 4450.0 || total > 4451.0 {
		t.Errorf("SUM=%v, want ≈4450.75", total)
	}
}

func TestCSV_AlternateSeparator(t *testing.T) {
	_, sc := withCSV(t, nil)
	ctx := context.Background()
	// Pipe-separated values via comma='|'.
	if _, err := sc.ExecContext(ctx,
		`CREATE VIRTUAL TABLE temp.t USING csv(data='1|2|3', comma='|')`); err != nil {
		t.Fatal(err)
	}
	var a, b, c string
	if err := sc.QueryRowContext(ctx, `SELECT c1, c2, c3 FROM temp.t`).Scan(&a, &b, &c); err != nil {
		t.Fatal(err)
	}
	if a != "1" || b != "2" || c != "3" {
		t.Errorf("got %q,%q,%q", a, b, c)
	}
}

func TestCSV_CommentLines(t *testing.T) {
	// `#` introduces a comment line that the reader skips.
	_, sc := withCSV(t, nil)
	if _, err := sc.ExecContext(context.Background(),
		`CREATE VIRTUAL TABLE temp.t USING csv(data='# comment
1,2
# another
3,4', comment='#')`); err != nil {
		t.Fatal(err)
	}
	rows, err := sc.QueryContext(context.Background(), `SELECT c1, c2 FROM temp.t`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got [][]string
	for rows.Next() {
		var a, b string
		_ = rows.Scan(&a, &b)
		got = append(got, []string{a, b})
	}
	if len(got) != 2 {
		t.Errorf("got %d rows, want 2: %v", len(got), got)
	}
}

func TestCSV_ColumnsTruncate(t *testing.T) {
	// columns=2 caps the declared schema to 2 columns even if rows have
	// more. Extra fields are dropped.
	_, sc := withCSV(t, nil)
	if _, err := sc.ExecContext(context.Background(),
		`CREATE VIRTUAL TABLE temp.t USING csv(data='1,2,3,4
5,6,7,8', columns=2)`); err != nil {
		t.Fatal(err)
	}
	rows, err := sc.QueryContext(context.Background(), `SELECT * FROM temp.t`)
	if err != nil {
		t.Fatal(err)
	}
	cols, _ := rows.Columns()
	rows.Close()
	if len(cols) != 2 {
		t.Errorf("got %d columns, want 2: %v", len(cols), cols)
	}
}

func TestCSV_FilenameMissingErrors(t *testing.T) {
	_, sc := withCSV(t, fstest.MapFS{})
	_, err := sc.ExecContext(context.Background(),
		`CREATE VIRTUAL TABLE temp.t USING csv(filename='nope.csv')`)
	if err == nil {
		t.Fatal("expected open error, got nil")
	}
}

func TestCSV_DuplicateArgRejected(t *testing.T) {
	_, sc := withCSV(t, nil)
	_, err := sc.ExecContext(context.Background(),
		`CREATE VIRTUAL TABLE temp.t USING csv(data='1', data='2')`)
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("got %v, want duplicate-arg error", err)
	}
}

func TestCSV_RequiresFilenameOrData(t *testing.T) {
	_, sc := withCSV(t, nil)
	_, err := sc.ExecContext(context.Background(),
		`CREATE VIRTUAL TABLE temp.t USING csv(header=on)`)
	if err == nil || !strings.Contains(err.Error(), `filename`) {
		t.Errorf("got %v, want filename/data requirement error", err)
	}
}

func TestCSV_UnknownArgRejected(t *testing.T) {
	_, sc := withCSV(t, nil)
	_, err := sc.ExecContext(context.Background(),
		`CREATE VIRTUAL TABLE temp.t USING csv(data='x', bogus=1)`)
	if err == nil || !strings.Contains(err.Error(), "unknown parameter") {
		t.Errorf("got %v, want unknown-param error", err)
	}
}

func TestCSV_JoinAgainstRegularTable(t *testing.T) {
	// Compose: JOIN csv rows against an in-memory table. Pins that the
	// vtab integrates with regular SQL.
	fsys := fstest.MapFS{
		"lookups.csv": {Data: []byte("code,label\n1,first\n2,second\n3,third\n")},
	}
	_, sc := withCSV(t, fsys)
	ctx := context.Background()
	if _, err := sc.ExecContext(ctx,
		`CREATE VIRTUAL TABLE temp.lookups USING csv(filename='lookups.csv', header=on,
		    schema='CREATE TABLE x(code INTEGER, label TEXT)')`); err != nil {
		t.Fatal(err)
	}
	if _, err := sc.ExecContext(ctx, `
		CREATE TEMP TABLE events(id INTEGER);
		INSERT INTO events VALUES (1), (3), (1);`); err != nil {
		t.Fatal(err)
	}
	rows, err := sc.QueryContext(ctx, `
		SELECT events.id, lookups.label
		FROM events JOIN temp.lookups ON events.id = lookups.code
		ORDER BY events.rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []struct {
		ID    int64
		Label string
	}
	for rows.Next() {
		var r struct {
			ID    int64
			Label string
		}
		if err := rows.Scan(&r.ID, &r.Label); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if len(got) != 3 || got[0].Label != "first" || got[1].Label != "third" || got[2].Label != "first" {
		t.Errorf("got %v, want [{1 first} {3 third} {1 first}]", got)
	}
}

func TestCSV_ModuleName(t *testing.T) {
	if csv.ModuleName != "csv" {
		t.Errorf("ModuleName=%q, want %q", csv.ModuleName, "csv")
	}
}

func TestCSV_BOMStripped(t *testing.T) {
	// A UTF-8 BOM at the start of a CSV file must NOT bleed into the
	// first column name.
	fsys := fstest.MapFS{
		"bom.csv": {Data: []byte(filevtab.UTF8BOM + "key,value\nfoo,bar\n")},
	}
	_, sc := withCSV(t, fsys)
	if _, err := sc.ExecContext(context.Background(),
		`CREATE VIRTUAL TABLE temp.t USING csv(filename='bom.csv', header=on)`); err != nil {
		t.Fatal(err)
	}
	cols, err := sc.QueryContext(context.Background(), `SELECT * FROM temp.t`)
	if err != nil {
		t.Fatal(err)
	}
	defer cols.Close()
	colNames, _ := cols.Columns()
	if len(colNames) == 0 || colNames[0] != "key" {
		t.Errorf("first column name = %q, want %q (BOM not stripped)", colNames, "key")
	}
}

func TestCSV_EmptySource(t *testing.T) {
	// header=on + empty file → clear error (B2 fix).
	fsys := fstest.MapFS{"empty.csv": {Data: []byte{}}}
	_, sc := withCSV(t, fsys)
	_, err := sc.ExecContext(context.Background(),
		`CREATE VIRTUAL TABLE temp.t USING csv(filename='empty.csv', header=on)`)
	if err == nil {
		t.Fatal("expected error for header=on + empty file")
	}
	if !strings.Contains(err.Error(), "header=on but source is empty") {
		t.Errorf("error %q does not surface the empty-source case", err.Error())
	}

	// header=off + empty file + columns=3 → declares 3 columns; SELECT returns no rows.
	_, sc2 := withCSV(t, fsys)
	if _, err := sc2.ExecContext(context.Background(),
		`CREATE VIRTUAL TABLE temp.t USING csv(filename='empty.csv', columns=3)`); err != nil {
		t.Fatalf("empty file + columns=3: %v", err)
	}
	rows, err := sc2.QueryContext(context.Background(), `SELECT * FROM temp.t`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	if len(cols) != 3 {
		t.Errorf("got %d columns, want 3", len(cols))
	}
	if rows.Next() {
		t.Error("expected no rows from empty file")
	}
}

func TestCSV_EmbeddedQuotes(t *testing.T) {
	// `"a""b"` is RFC 4180 doubled-quote escape → literal `a"b`.
	_, sc := withCSV(t, nil)
	if _, err := sc.ExecContext(context.Background(),
		`CREATE VIRTUAL TABLE temp.t USING csv(data='"a""b",c')`); err != nil {
		t.Fatal(err)
	}
	var a, b string
	if err := sc.QueryRowContext(context.Background(),
		`SELECT c1, c2 FROM temp.t`).Scan(&a, &b); err != nil {
		t.Fatal(err)
	}
	if a != `a"b` || b != "c" {
		t.Errorf("got %q, %q; want %q, %q", a, b, `a"b`, "c")
	}
}

func TestCSV_EmbeddedNewlines(t *testing.T) {
	// Multi-line value via "..." continues to the next physical line.
	fsys := fstest.MapFS{
		"multi.csv": {Data: []byte("name,bio\nAlice,\"first line\nsecond line\"\nBob,short\n")},
	}
	_, sc := withCSV(t, fsys)
	if _, err := sc.ExecContext(context.Background(),
		`CREATE VIRTUAL TABLE temp.t USING csv(filename='multi.csv', header=on)`); err != nil {
		t.Fatal(err)
	}
	var name, bio string
	if err := sc.QueryRowContext(context.Background(),
		`SELECT name, bio FROM temp.t WHERE name = 'Alice'`).Scan(&name, &bio); err != nil {
		t.Fatal(err)
	}
	if bio != "first line\nsecond line" {
		t.Errorf("bio=%q, want %q", bio, "first line\nsecond line")
	}
}

func TestCSV_EmptyCellInTypedColumn(t *testing.T) {
	// An empty cell `,,` in an INTEGER-affinity column should produce NULL.
	fsys := fstest.MapFS{
		"sparse.csv": {Data: []byte("a,b\n1,\n,2\n")},
	}
	_, sc := withCSV(t, fsys)
	if _, err := sc.ExecContext(context.Background(),
		`CREATE VIRTUAL TABLE temp.t USING csv(filename='sparse.csv', header=on,
		    schema='CREATE TABLE x(a INTEGER, b INTEGER)')`); err != nil {
		t.Fatal(err)
	}
	rows, err := sc.QueryContext(context.Background(), `SELECT a, b FROM temp.t`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []struct{ A, B sql.NullInt64 }
	for rows.Next() {
		var r struct{ A, B sql.NullInt64 }
		_ = rows.Scan(&r.A, &r.B)
		got = append(got, r)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2", len(got))
	}
	if !got[0].A.Valid || got[0].A.Int64 != 1 || got[0].B.Valid {
		t.Errorf("row 0: got %+v, want a=1 b=NULL", got[0])
	}
	if got[1].A.Valid || !got[1].B.Valid || got[1].B.Int64 != 2 {
		t.Errorf("row 1: got %+v, want a=NULL b=2", got[1])
	}
}

// roundtrip checks the example carved out in the doc.
func TestCSV_OSBackedDefaultRegister(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.csv")
	if err := writeFile(path, "k,v\n1,one\n2,two\n"); err != nil {
		t.Fatal(err)
	}
	_, sc := withCSV(t, nil) // nil → Register (os-backed)
	if _, err := sc.ExecContext(context.Background(),
		`CREATE VIRTUAL TABLE temp.t USING csv(filename='`+path+`', header=on)`); err != nil {
		t.Fatal(err)
	}
	var k, v string
	if err := sc.QueryRowContext(context.Background(),
		`SELECT k, v FROM temp.t WHERE k = '2'`).Scan(&k, &v); err != nil {
		t.Fatal(err)
	}
	if k != "2" || v != "two" {
		t.Errorf("got k=%q v=%q, want k=2 v=two", k, v)
	}
}

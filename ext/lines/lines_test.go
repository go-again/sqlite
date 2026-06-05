package lines_test

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/internal/filevtab"
	"github.com/go-again/sqlite/ext/lines"
)

func withLines(t *testing.T, fsys fs.FS) (*sql.DB, *sql.Conn) {
	t.Helper()
	db, err := sql.Open(sqlite.DriverName, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	sc, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sc.Close() })
	if err := sc.Raw(func(driverConn any) error {
		c, ok := driverConn.(*sqlite.Conn)
		if !ok {
			return errors.New("not *sqlite.Conn")
		}
		if fsys == nil {
			return lines.Register(c)
		}
		return lines.RegisterFS(c, fsys)
	}); err != nil {
		t.Fatal(err)
	}
	return db, sc
}

func TestLines_InlineData(t *testing.T) {
	_, sc := withLines(t, nil)
	ctx := context.Background()
	if _, err := sc.ExecContext(ctx,
		`CREATE VIRTUAL TABLE temp.l USING lines(data='hello
world
goodbye')`); err != nil {
		t.Fatal(err)
	}
	rows, err := sc.QueryContext(ctx, `SELECT lineno, line FROM temp.l ORDER BY lineno`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type r struct {
		LineNo int64
		Line   string
	}
	var got []r
	for rows.Next() {
		var v r
		_ = rows.Scan(&v.LineNo, &v.Line)
		got = append(got, v)
	}
	want := []r{{1, "hello"}, {2, "world"}, {3, "goodbye"}}
	if len(got) != 3 {
		t.Fatalf("got %d rows, want 3", len(got))
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("row[%d]=%v, want %v", i, got[i], w)
		}
	}
}

func TestLines_FromFS(t *testing.T) {
	fsys := fstest.MapFS{
		"app.log": {Data: []byte("INFO startup\nERROR boom\nINFO shutdown\n")},
	}
	_, sc := withLines(t, fsys)
	if _, err := sc.ExecContext(context.Background(),
		`CREATE VIRTUAL TABLE temp.l USING lines(filename='app.log')`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := sc.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM temp.l WHERE line LIKE 'ERROR%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("ERROR-line count = %d, want 1", n)
	}
}

func TestLines_EmptyFile(t *testing.T) {
	fsys := fstest.MapFS{"empty.log": {Data: []byte{}}}
	_, sc := withLines(t, fsys)
	if _, err := sc.ExecContext(context.Background(),
		`CREATE VIRTUAL TABLE temp.l USING lines(filename='empty.log')`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := sc.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM temp.l`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("empty file row count = %d, want 0", n)
	}
}

func TestLines_LongLines(t *testing.T) {
	// 1 MB single line. Default bufio.Scanner buffer is 64 KB; lines.go
	// bumps it to 16 MB. Make sure the long line round-trips.
	long := strings.Repeat("x", 1024*1024)
	fsys := fstest.MapFS{"big.log": {Data: []byte(long + "\n")}}
	_, sc := withLines(t, fsys)
	if _, err := sc.ExecContext(context.Background(),
		`CREATE VIRTUAL TABLE temp.l USING lines(filename='big.log')`); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := sc.QueryRowContext(context.Background(),
		`SELECT line FROM temp.l`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(long) {
		t.Errorf("got %d bytes, want %d", len(got), len(long))
	}
}

func TestLines_BOMStripped(t *testing.T) {
	fsys := fstest.MapFS{
		"bom.log": {Data: []byte(filevtab.UTF8BOM + "first line\nsecond line\n")},
	}
	_, sc := withLines(t, fsys)
	if _, err := sc.ExecContext(context.Background(),
		`CREATE VIRTUAL TABLE temp.l USING lines(filename='bom.log')`); err != nil {
		t.Fatal(err)
	}
	var first string
	if err := sc.QueryRowContext(context.Background(),
		`SELECT line FROM temp.l WHERE lineno = 1`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if first != "first line" {
		t.Errorf("first line = %q, want %q (BOM not stripped)", first, "first line")
	}
}

func TestLines_BadArgs(t *testing.T) {
	_, sc := withLines(t, nil)
	cases := []string{
		`CREATE VIRTUAL TABLE temp.t USING lines()`,
		`CREATE VIRTUAL TABLE temp.t USING lines(data='x', filename='y')`,
		`CREATE VIRTUAL TABLE temp.t USING lines(data='x', data='y')`,
		`CREATE VIRTUAL TABLE temp.t USING lines(unknown=1)`,
	}
	for _, q := range cases {
		if _, err := sc.ExecContext(context.Background(), q); err == nil {
			t.Errorf("%q: expected error, got nil", q)
		}
	}
}

func TestLines_ModuleName(t *testing.T) {
	if lines.ModuleName != "lines" {
		t.Errorf("ModuleName=%q, want %q", lines.ModuleName, "lines")
	}
}

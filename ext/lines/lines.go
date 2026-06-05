// Package lines exposes a text file as a SQLite virtual table — one
// row per line. Companion to [ext/csv] for unstructured logs and
// newline-delimited content where the data isn't comma-separated.
//
//	CREATE VIRTUAL TABLE temp.log USING lines(filename='app.log');
//	SELECT lineno, line FROM temp.log WHERE line LIKE 'ERROR%';
//
// # Module parameters
//
//   - filename='path' — read from the configured filesystem ([os.Open]
//     by default; [fs.FS] when [RegisterFS] is used).
//   - data='inline content' — inline source. Mutually exclusive with
//     filename.
//   - schema='CREATE TABLE x(lineno INTEGER, line TEXT)' — override the
//     declared schema. Default declares `lineno INTEGER, line TEXT`.
//
// # Usage
//
//	import (
//	    sqlite "github.com/go-again/sqlite"
//	    "github.com/go-again/sqlite/ext/lines"
//	)
//
//	if err := lines.Register(conn); err != nil { ... }
//
// For sandboxed file access (embed.FS, fstest.MapFS, os.DirFS):
//
//	lines.RegisterFS(conn, fsys)
//
// Blank-import auto-registration uses os-backed file access:
//
//	import _ "github.com/go-again/sqlite/ext/lines/auto"
//
// Ported from [ncruces/ext/lines].
//
// [ncruces/ext/lines]: https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/lines
package lines

import (
	"bufio"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"io/fs"

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/internal/filevtab"
	"github.com/go-again/sqlite/internal/sqlid"
)

// ModuleName is the SQL module name: `lines`.
const ModuleName = "lines"

// Register installs the lines module on c using os-backed file access.
func Register(c *sqlite.Conn) error {
	return RegisterFS(c, filevtab.OSFS{})
}

// RegisterFS installs the lines module on c, routing file opens through fsys.
func RegisterFS(c *sqlite.Conn, fsys fs.FS) error {
	return c.CreateModule(ModuleName,
		func(conn *sqlite.Conn, _, _, _ string, args []string) (sqlite.VTab, error) {
			t, err := buildTable(fsys, args)
			if err != nil {
				return nil, err
			}
			if err := conn.DeclareVTab(t.schema); err != nil {
				return nil, err
			}
			return t, nil
		})
}

type table struct {
	fsys   fs.FS
	name   string
	data   string
	schema string
}

func (*table) Disconnect() error { return nil }
func (*table) Destroy() error    { return nil }

func (t *table) BestIndex(info *sqlite.IndexInfo) error {
	// Full-scan only; no usable index.
	filevtab.FullScanBestIndex(info)
	return nil
}

func (t *table) Open() (sqlite.VTabCursor, error) {
	return &cursor{table: t}, nil
}

func buildTable(fsys fs.FS, args []string) (*table, error) {
	t := &table{
		fsys:   fsys,
		schema: `CREATE TABLE x(lineno INTEGER, line TEXT)`,
	}
	seen := make(map[string]bool, len(args))
	for _, a := range args {
		// sqlid.NamedArg trims both halves; key=="" means a bare token
		// (no '=') or an empty key ('=value'), neither of which is a
		// usable parameter.
		key, val := sqlid.NamedArg(a)
		if key == "" {
			return nil, fmt.Errorf("lines: argument %q is not a key=value pair", a)
		}
		if seen[key] {
			return nil, fmt.Errorf("lines: duplicate %q parameter", key)
		}
		seen[key] = true
		switch key {
		case "filename":
			t.name = sqlid.Unquote(val)
		case "data":
			t.data = sqlid.Unquote(val)
		case "schema":
			t.schema = sqlid.Unquote(val)
		default:
			return nil, fmt.Errorf("lines: unknown parameter %q", key)
		}
	}
	if (t.name == "") == (t.data == "") {
		return nil, errors.New(`lines: must specify exactly one of "filename" or "data"`)
	}
	return t, nil
}

func (t *table) newReader() (*bufio.Scanner, io.Closer, error) {
	r, cl, err := filevtab.OpenSource(t.fsys, t.name, t.data, "lines")
	if err != nil {
		return nil, nil, err
	}
	scanner := bufio.NewScanner(r)
	// Allow long lines — default 64KB buffer is too small for many logs.
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	return scanner, cl, nil
}

type cursor struct {
	table   *table
	scanner *bufio.Scanner
	closer  io.Closer
	lineno  int64
	line    string
	eof     bool
}

func (c *cursor) Close() error {
	c.scanner = nil
	c.eof = true
	if c.closer != nil {
		err := c.closer.Close()
		c.closer = nil
		return err
	}
	return nil
}

func (c *cursor) Filter(int, string, []driver.Value) error {
	if err := c.Close(); err != nil {
		return err
	}
	s, cl, err := c.table.newReader()
	if err != nil {
		return err
	}
	c.scanner = s
	c.closer = cl
	c.lineno = 0
	c.eof = false
	return c.advance()
}

func (c *cursor) advance() error {
	if !c.scanner.Scan() {
		if err := c.scanner.Err(); err != nil {
			return fmt.Errorf("lines: scan: %w", err)
		}
		c.eof = true
		return nil
	}
	c.lineno++
	c.line = c.scanner.Text()
	return nil
}

func (c *cursor) Next() error           { return c.advance() }
func (c *cursor) Eof() bool             { return c.eof }
func (c *cursor) Rowid() (int64, error) { return c.lineno, nil }

func (c *cursor) Column(col int) (driver.Value, error) {
	switch col {
	case 0:
		return c.lineno, nil
	case 1:
		return c.line, nil
	}
	return nil, nil
}

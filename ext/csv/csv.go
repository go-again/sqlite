package csv

import (
	"database/sql/driver"
	encodingcsv "encoding/csv"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strconv"
	"strings"

	sqlite "gosqlite.org"
	"gosqlite.org/ext/internal/filevtab"
	"gosqlite.org/internal/sqlid"
)

// ModuleName is the name the vtab registers under: `csv`.
const ModuleName = "csv"

// Register installs the csv module on c using os-backed file access.
// CSV files specified via the `filename` named-arg are opened with
// [os.Open]. For sandboxed access, use [RegisterFS] instead.
func Register(c *sqlite.Conn) error {
	return RegisterFS(c, filevtab.OSFS{})
}

// RegisterFS installs the csv module on c, routing file opens through
// fsys. Use this to scope file access to an [embed.FS], an
// [io/fs/fstest.MapFS], or an [os.DirFS]-rooted sandbox.
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
	fsys    fs.FS
	name    string // filename (mutually exclusive with data)
	data    string // inline content (mutually exclusive with name)
	schema  string // CREATE TABLE declaration handed to DeclareVTab
	types   []affinity
	comma   rune
	comment rune
	header  bool
	skip    int // leading rows to discard before the header / first record
}

func (*table) Disconnect() error { return nil }
func (*table) Destroy() error    { return nil }

func (t *table) BestIndex(info *sqlite.IndexInfo) error {
	// No usable index — full scan every time.
	filevtab.FullScanBestIndex(info)
	return nil
}

func (t *table) Open() (sqlite.VTabCursor, error) {
	return &cursor{table: t}, nil
}

func buildTable(fsys fs.FS, args []string) (*table, error) {
	t := &table{
		fsys:  fsys,
		comma: ',',
	}
	var (
		schemaSrc string
		columns   = -1
		seen      = make(map[string]bool, len(args))
		err       error
	)
	for _, a := range args {
		// sqlid.NamedArg returns key="" for a bare token (no '=') or an
		// empty key ('=value'); both are "not a key=value pair" here,
		// matching the previous splitNamedArg ok==false contract.
		key, val := sqlid.NamedArg(a)
		if key == "" {
			return nil, fmt.Errorf("csv: argument %q is not a key=value pair", a)
		}
		if seen[key] {
			return nil, fmt.Errorf("csv: duplicate %q parameter", key)
		}
		seen[key] = true
		switch key {
		case "filename":
			t.name = sqlid.Unquote(val)
		case "data":
			t.data = sqlid.Unquote(val)
		case "schema":
			schemaSrc = sqlid.Unquote(val)
		case "header":
			t.header, err = boolArg(key, val)
		case "columns":
			columns, err = uintArg(key, val)
		case "comma":
			t.comma, err = runeArg(key, val)
		case "comment":
			t.comment, err = runeArg(key, val)
		case "skip":
			t.skip, err = uintArg(key, val)
		default:
			return nil, fmt.Errorf("csv: unknown parameter %q", key)
		}
		if err != nil {
			return nil, err
		}
	}
	if (t.name == "") == (t.data == "") {
		return nil, errors.New(`csv: must specify exactly one of "filename" or "data"`)
	}
	if schemaSrc == "" {
		var headerRow []string
		if t.header || columns < 0 {
			r, c, err := t.newReader()
			if c != nil {
				defer c.Close()
			}
			if err != nil {
				return nil, err
			}
			row, err := r.Read()
			if err != nil {
				if errors.Is(err, io.EOF) {
					if t.header {
						return nil, errors.New(`csv: header=on but source is empty`)
					}
					// No rows AND no explicit columns count — declare a
					// single TEXT column so the CREATE VIRTUAL TABLE
					// succeeds; SELECT just returns zero rows.
					headerRow = nil
				} else {
					return nil, fmt.Errorf("csv: read header / probe row: %w", err)
				}
			} else {
				headerRow = row
			}
		}
		t.schema = buildSchema(t.header, columns, headerRow)
	} else {
		t.schema = schemaSrc
		t.types = parseAffinities(schemaSrc)
	}
	return t, nil
}

func (t *table) newReader() (*encodingcsv.Reader, io.Closer, error) {
	r, cl, err := filevtab.OpenSource(t.fsys, t.name, t.data, "csv")
	if err != nil {
		return nil, nil, err
	}
	cr := encodingcsv.NewReader(r)
	cr.ReuseRecord = true
	cr.Comma = t.comma
	cr.Comment = t.comment
	cr.FieldsPerRecord = -1 // tolerate ragged rows; column padding handled in Column.
	// Discard skip leading rows (e.g. provenance banners above the
	// header) so every reader — the schema probe and each cursor scan —
	// starts at the same logical row 0.
	for range t.skip {
		if _, err := cr.Read(); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			if cl != nil {
				cl.Close()
			}
			return nil, nil, fmt.Errorf("csv: skip rows: %w", err)
		}
	}
	return cr, cl, nil
}

type cursor struct {
	table  *table
	csv    *encodingcsv.Reader
	closer io.Closer
	row    []string
	rowID  int64
	eof    bool
}

func (c *cursor) Close() error {
	c.row = nil
	c.csv = nil
	c.eof = true
	if c.closer != nil {
		err := c.closer.Close()
		c.closer = nil
		return err
	}
	return nil
}

func (c *cursor) Filter(_ int, _ string, _ []driver.Value) error {
	if err := c.Close(); err != nil {
		return err
	}
	r, closer, err := c.table.newReader()
	if err != nil {
		return err
	}
	c.csv = r
	c.closer = closer
	c.rowID = 0
	c.eof = false
	if c.table.header {
		// Skip the header row.
		if _, err := c.csv.Read(); err != nil {
			c.eof = true
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
	return c.advance()
}

func (c *cursor) advance() error {
	row, err := c.csv.Read()
	if err != nil {
		if errors.Is(err, io.EOF) {
			c.eof = true
			c.row = nil
			return nil
		}
		return err
	}
	c.row = row
	c.rowID++
	return nil
}

func (c *cursor) Next() error           { return c.advance() }
func (c *cursor) Eof() bool             { return c.eof }
func (c *cursor) Rowid() (int64, error) { return c.rowID, nil }

func (c *cursor) Column(col int) (driver.Value, error) {
	if col >= len(c.row) {
		return nil, nil
	}
	txt := c.row[col]
	typ := affinityText
	if col < len(c.table.types) {
		typ = c.table.types[col]
	}
	// SQLite's CSV semantics: empty cell in a non-TEXT column → NULL.
	if txt == "" && typ != affinityText {
		return nil, nil
	}
	switch typ {
	case affinityInteger, affinityNumeric:
		if i, err := strconv.ParseInt(txt, 10, 64); err == nil {
			return i, nil
		}
		fallthrough
	case affinityReal:
		if f, err := strconv.ParseFloat(txt, 64); err == nil {
			return f, nil
		}
	}
	return txt, nil
}

// --- named-arg value parsing helpers ---

func boolArg(key, val string) (bool, error) {
	switch strings.ToLower(sqlid.Unquote(val)) {
	case "1", "true", "yes", "on", "":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	}
	return false, fmt.Errorf("csv: invalid bool %q for %q", val, key)
}

func uintArg(key, val string) (int, error) {
	// 31 bits = up to math.MaxInt32, which covers any realistic CSV
	// column / row count. The previous 15-bit cap (max 32767) silently
	// rejected larger values with a misleading "invalid uint" error.
	n, err := strconv.ParseUint(sqlid.Unquote(val), 10, 31)
	if err != nil {
		return 0, fmt.Errorf("csv: invalid uint %q for %q", val, key)
	}
	return int(n), nil
}

func runeArg(key, val string) (rune, error) {
	s := sqlid.Unquote(val)
	if s == "" {
		return 0, nil
	}
	rs := []rune(s)
	if len(rs) != 1 {
		return 0, fmt.Errorf("csv: %q parameter must be a single rune, got %q", key, val)
	}
	return rs[0], nil
}

// Package pivot implements a pivot virtual table — a parametrized
// cross-tab over three caller-supplied SELECT statements:
//
//	CREATE VIRTUAL TABLE p USING pivot(
//	    'SELECT DISTINCT region FROM sales',                       -- row keys
//	    'SELECT DISTINCT product, product FROM sales',             -- column keys (value, name)
//	    'SELECT SUM(units) FROM sales WHERE region=? AND product=?' -- per-cell aggregate
//	);
//
// Each row in the result represents one row-key tuple; each non-key
// column maps to one distinct column-key value, with the cell value
// supplied by the cell query. The cell query's bound parameters are
// the row-key columns followed by the column-key value.
//
// # Argument shape
//
//   - args[0] — row-key SELECT. Its columns define the leading
//     (non-pivoted) columns of the vtab.
//   - args[1] — column-key SELECT. Must return two columns: the bind
//     value the cell query receives (typically the column key) and the
//     display name (used as the vtab's column name).
//   - args[2] — cell SELECT. Must return exactly one column and accept
//     `(len(rowkeys) + 1)` bound parameters.
//
// # Ported from
//
// [ncruces/ext/pivot], itself a Go port of the jakethaw/pivot_vtab
// SQLite extension. We support EQ constraint push-down on row-key
// columns and currently skip the ORDER BY rewrite.
//
// [ncruces/ext/pivot]: https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/pivot
package pivot

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/internal/sqlid"
)

// ModuleName is the name the vtab registers under: `pivot`.
const ModuleName = "pivot"

// Register installs the `pivot` virtual table on c.
//
// Registration is per-connection. For pool-wide install via
// [sqlite.Driver.ConnectHook], blank-import the auto sub-package:
//
//	import _ "github.com/go-again/sqlite/ext/pivot/auto"
func Register(c *sqlite.Conn) error {
	return c.CreateModule(ModuleName, ctor)
}

func ctor(c *sqlite.Conn, _, _, _ string, args []string) (sqlite.VTab, error) {
	if len(args) != 3 {
		return nil, errors.New("pivot: expected 3 arguments (rowKeySQL, colKeySQL, cellSQL)")
	}
	rowKeySQL := unquote(strings.TrimSpace(args[0]))
	colKeySQL := unquote(strings.TrimSpace(args[1]))
	cellSQL := unquote(strings.TrimSpace(args[2]))

	t := &table{
		conn: c,
		scan: rowKeySQL,
		cell: cellSQL,
	}

	// Prepare the row-key query to read its column names and types.
	rowStmt, err := c.Prepare(t.scan)
	if err != nil {
		return nil, fmt.Errorf("pivot: prepare row-key query: %w", err)
	}
	rs := rowStmt.(*sqlite.Stmt)
	if rs.ColumnCount() == 0 {
		_ = rs.Close()
		return nil, errors.New("pivot: row-key query must produce at least one column")
	}
	var schema strings.Builder
	schema.WriteString("CREATE TABLE x(")
	for i := range rs.ColumnCount() {
		if i > 0 {
			schema.WriteString(", ")
		}
		name := rs.ColumnName(i)
		t.keys = append(t.keys, name)
		schema.WriteString(quote(name))
		if dt := rs.ColumnDeclType(i); dt != "" {
			schema.WriteByte(' ')
			schema.WriteString(dt)
		}
	}
	_ = rs.Close()

	// Run the column-key query to enumerate pivot columns.
	colRows, err := c.Prepare(colKeySQL)
	if err != nil {
		return nil, fmt.Errorf("pivot: prepare col-key query: %w", err)
	}
	cs := colRows.(*sqlite.Stmt)
	if cs.ColumnCount() != 2 {
		_ = cs.Close()
		return nil, errors.New("pivot: col-key query must produce exactly 2 columns (bind_value, display_name)")
	}
	colDeclType := cs.ColumnDeclType(0)
	r, err := cs.QueryContext(context.Background(), nil)
	if err != nil {
		_ = cs.Close()
		return nil, fmt.Errorf("pivot: run col-key query: %w", err)
	}
	row := make([]driver.Value, 2)
	for {
		if err := r.Next(row); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			_ = r.Close()
			_ = cs.Close()
			return nil, fmt.Errorf("pivot: iterate col-key query: %w", err)
		}
		bindVal := row[0]
		display := stringify(row[1])
		t.cols = append(t.cols, bindVal)
		schema.WriteString(", ")
		schema.WriteString(quote(display))
		if colDeclType != "" {
			schema.WriteByte(' ')
			schema.WriteString(colDeclType)
		}
	}
	_ = r.Close()
	_ = cs.Close()

	if len(t.cols) == 0 {
		return nil, errors.New("pivot: col-key query produced no rows; vtab would have no pivot columns")
	}

	// Validate the cell query.
	cellStmt, err := c.Prepare(t.cell)
	if err != nil {
		return nil, fmt.Errorf("pivot: prepare cell query: %w", err)
	}
	cs2 := cellStmt.(*sqlite.Stmt)
	if cs2.ColumnCount() != 1 {
		_ = cs2.Close()
		return nil, errors.New("pivot: cell query must produce exactly 1 column")
	}
	if cs2.BindCount() != len(t.keys)+1 {
		_ = cs2.Close()
		return nil, fmt.Errorf("pivot: cell query expects %d bound parameters (keys + col value), got %d",
			len(t.keys)+1, cs2.BindCount())
	}
	_ = cs2.Close()

	schema.WriteByte(')')
	if err := c.DeclareVTab(schema.String()); err != nil {
		return nil, fmt.Errorf("pivot: declare schema: %w", err)
	}
	return t, nil
}

type table struct {
	conn *sqlite.Conn
	scan string         // SELECT * FROM (row-key SQL)
	cell string         // SELECT * FROM (cell SQL)
	keys []string       // row-key column names
	cols []driver.Value // bind values for the per-column cell query
}

func (t *table) Disconnect() error { return nil }
func (t *table) Destroy() error    { return nil }

func (t *table) BestIndex(info *sqlite.IndexInfo) error {
	// Wrap the user's scan SQL as a subquery so we can safely append a
	// WHERE clause without colliding with any existing one inside.
	var b strings.Builder
	b.WriteString("SELECT * FROM (")
	b.WriteString(t.scan)
	b.WriteString(")")
	idx := 0
	sep := " WHERE "
	for i, cst := range info.Constraints {
		if !cst.Usable || cst.Column >= len(t.keys) {
			continue
		}
		op := opString(cst.Op)
		if op == "" {
			continue
		}
		b.WriteString(sep)
		b.WriteString(quote(t.keys[cst.Column]))
		b.WriteByte(' ')
		b.WriteString(op)
		b.WriteString(" ?")
		info.Constraints[i].ArgIndex = idx
		info.Constraints[i].Omit = true
		sep = " AND "
		idx++
	}
	info.EstimatedCost = 1e6
	info.IdxStr = b.String()
	return nil
}

func (t *table) Open() (sqlite.VTabCursor, error) {
	return &cursor{table: t}, nil
}

type cursor struct {
	table    *table
	scan     driver.Rows
	scanStmt *sqlite.Stmt   // prepared scan SQL, lifetime-tied to c.scan
	cellStmt *sqlite.Stmt   // prepared cell SQL, reused for every (row, col) cell
	cellArgs []driver.Value // scratch arg slice for cellStmt.Query
	row      []driver.Value
	rowID    int64
	eof      bool
}

func (c *cursor) Filter(_ int, idxStr string, args []driver.Value) error {
	if c.scan != nil {
		_ = c.scan.Close()
		c.scan = nil
	}
	if c.scanStmt != nil {
		_ = c.scanStmt.Close()
		c.scanStmt = nil
	}
	if idxStr == "" {
		idxStr = c.table.scan
	}
	stmt, err := c.table.conn.Prepare(idxStr)
	if err != nil {
		return fmt.Errorf("pivot: prepare scan: %w", err)
	}
	scanStmt := stmt.(*sqlite.Stmt)
	rs, err := scanStmt.QueryContext(context.Background(), sqlid.ToNamedValues(args))
	if err != nil {
		_ = scanStmt.Close()
		return fmt.Errorf("pivot: run scan: %w", err)
	}
	c.scan = rs
	// Hold the stmt shell so cursor.Close can free its psql CString;
	// dropping it here would leak the CString per Filter call. The
	// stmt's pstmt was donated/cached by QueryContext, but the shell
	// still owns psql until Close runs.
	c.scanStmt = scanStmt

	// Prepare the cell statement once per Filter, reuse across every
	// (row, col) pair the cursor enumerates. The hot-path savings vs
	// re-preparing per cell is R*C - 1 round-trips for a result of R
	// rows × C pivot columns.
	if c.cellStmt == nil {
		cs, err := c.table.conn.Prepare(c.table.cell)
		if err != nil {
			return fmt.Errorf("pivot: prepare cell: %w", err)
		}
		c.cellStmt = cs.(*sqlite.Stmt)
	}
	if c.cellArgs == nil || cap(c.cellArgs) < len(c.table.keys)+1 {
		c.cellArgs = make([]driver.Value, 0, len(c.table.keys)+1)
	}

	c.rowID = 0
	c.eof = false
	if c.row == nil || len(c.row) != len(c.table.keys) {
		c.row = make([]driver.Value, len(c.table.keys))
	}
	return c.Next()
}

func (c *cursor) Next() error {
	if c.scan == nil {
		c.eof = true
		return nil
	}
	// Honor the enclosing statement's cancellation between row-key
	// advances; the pivot can otherwise iterate R*C cells without
	// observing sqlite3_interrupt.
	if c.table.conn.IsInterrupted() {
		return fmt.Errorf("pivot: interrupted")
	}
	if err := c.scan.Next(c.row); err != nil {
		if errors.Is(err, io.EOF) {
			c.eof = true
			_ = c.scan.Close()
			c.scan = nil
			return nil
		}
		return err
	}
	c.rowID++
	return nil
}

func (c *cursor) Eof() bool { return c.eof }

func (c *cursor) Column(col int) (sqlite.Value, error) {
	if col < len(c.table.keys) {
		return c.row[col], nil
	}
	idx := col - len(c.table.keys)
	if idx >= len(c.table.cols) {
		return nil, nil
	}
	// Reuse the prepared cell statement; just rebind row keys + the
	// column value. driver.Stmt.Query reuses the underlying handle
	// (sqlite_stmt) and only does reset+bind+step+reset internally,
	// dramatically cheaper than prepare/finalize per cell.
	c.cellArgs = append(c.cellArgs[:0], c.row...)
	c.cellArgs = append(c.cellArgs, c.table.cols[idx])
	rs, err := c.cellStmt.QueryContext(context.Background(), sqlid.ToNamedValues(c.cellArgs))
	if err != nil {
		return nil, fmt.Errorf("pivot: query cell: %w", err)
	}
	defer func() { _ = rs.Close() }()
	one := make([]driver.Value, 1)
	if err := rs.Next(one); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		return nil, err
	}
	return one[0], nil
}

func (c *cursor) Rowid() (int64, error) { return c.rowID, nil }

func (c *cursor) Close() error {
	var err error
	if c.scan != nil {
		err = c.scan.Close()
		c.scan = nil
	}
	if c.scanStmt != nil {
		if e := c.scanStmt.Close(); e != nil && err == nil {
			err = e
		}
		c.scanStmt = nil
	}
	if c.cellStmt != nil {
		if e := c.cellStmt.Close(); e != nil && err == nil {
			err = e
		}
		c.cellStmt = nil
	}
	return err
}

func opString(op sqlite.ConstraintOp) string {
	switch op {
	case sqlite.OpEQ:
		return "="
	case sqlite.OpLT:
		return "<"
	case sqlite.OpGT:
		return ">"
	case sqlite.OpLE:
		return "<="
	case sqlite.OpGE:
		return ">="
	case sqlite.OpNE:
		return "<>"
	case sqlite.OpLIKE:
		return "LIKE"
	case sqlite.OpGLOB:
		return "GLOB"
	}
	return ""
}

func quote(s string) string { return sqlid.QuoteIdent(s) }

func unquote(s string) string {
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		return strings.ReplaceAll(s[1:len(s)-1], "''", "'")
	}
	return s
}

// stringify is used only for the col-key display name; we accept
// strings, []byte (TEXT-as-bytes), and numeric coercions.
func stringify(v driver.Value) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []byte:
		return string(x)
	case int64:
		return fmt.Sprintf("%d", x)
	case float64:
		return fmt.Sprintf("%g", x)
	}
	return fmt.Sprint(v)
}

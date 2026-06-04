// Package statement implements a virtual table that turns any SQL
// statement into a parametrized view. The statement is supplied as the
// USING argument; its bound parameters become HIDDEN columns on the
// vtab, so WHERE-clause equality constraints get pushed back through
// the prepared statement.
//
//	CREATE VIRTUAL TABLE adults USING statement(
//	    'SELECT id, name FROM users WHERE age >= ?');
//
//	-- min_age is exposed as a HIDDEN column; constrain it to bind ?1.
//	SELECT * FROM adults WHERE "?1" = 18;
//
// Named parameters become correspondingly-named HIDDEN columns:
//
//	CREATE VIRTUAL TABLE q USING statement(
//	    'SELECT id FROM users WHERE name LIKE :pat');
//
//	SELECT * FROM q WHERE pat = 'al%';
//
// # Constraints
//
// Only positional `?` and named `:name`/`@name`/`$name` parameters are
// supported as HIDDEN columns. Output columns inherit their declared
// types from the underlying SELECT via sqlite3_column_decltype.
//
// Ported from [ncruces/ext/statement], the SQLite community's
// statement-vtab extension.
//
// [ncruces/ext/statement]: https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/statement
package statement

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/internal/sqlid"
)

// ModuleName is the name the vtab registers under: `statement`.
const ModuleName = "statement"

// Register installs the `statement` virtual table on c.
//
// Registration is per-connection. For pool-wide install via
// [sqlite.Driver.ConnectHook], blank-import the auto sub-package:
//
//	import _ "github.com/go-again/sqlite/ext/statement/auto"
func Register(c *sqlite.Conn) error {
	return c.CreateModule(ModuleName, ctor)
}

func ctor(c *sqlite.Conn, _, _, _ string, args []string) (sqlite.VTab, error) {
	if len(args) != 1 {
		return nil, errors.New("statement: expected exactly one argument (the SQL string)")
	}
	sql := strings.TrimSpace(args[0])
	// Unquote single-quoted argument (the common case from SQLite's
	// argv tokenizer when the caller writes USING statement('SELECT…')).
	if len(sql) >= 2 && sql[0] == '\'' && sql[len(sql)-1] == '\'' {
		sql = strings.ReplaceAll(sql[1:len(sql)-1], "''", "'")
	}

	prep, err := c.Prepare(sql)
	if err != nil {
		return nil, fmt.Errorf("statement: prepare: %w", err)
	}
	s := prep.(*sqlite.Stmt)

	out := s.ColumnCount()
	in := s.BindCount()

	var schema strings.Builder
	schema.WriteString("CREATE TABLE x(")
	for i := range out {
		if i > 0 {
			schema.WriteString(", ")
		}
		schema.WriteString(quote(s.ColumnName(i)))
		if t := s.ColumnDeclType(i); t != "" {
			schema.WriteByte(' ')
			schema.WriteString(t)
		}
	}
	for i := 1; i <= in; i++ {
		if out > 0 || i > 1 {
			schema.WriteString(", ")
		}
		name := s.BindName(i)
		if name == "" {
			schema.WriteString(`"?` + strconv.Itoa(i) + `"`)
		} else {
			schema.WriteString(quote(name[1:]))
		}
		schema.WriteString(" HIDDEN")
	}
	schema.WriteByte(')')

	if err := c.DeclareVTab(schema.String()); err != nil {
		s.Close()
		return nil, fmt.Errorf("statement: declare: %w", err)
	}

	bindNames := make([]string, in)
	for i := 1; i <= in; i++ {
		if n := s.BindName(i); n != "" {
			// BindName returns the leading prefix (`:`, `@`, `$`) plus
			// the identifier; database/sql expects the bare name.
			bindNames[i-1] = n[1:]
		}
	}
	return &table{
		conn:      c,
		sql:       sql,
		out:       out,
		in:        in,
		bindNames: bindNames,
		template:  s,
	}, nil
}

type table struct {
	conn *sqlite.Conn
	sql  string
	out  int // number of output columns produced by the SELECT
	in   int // number of bind parameters

	// bindNames[i] holds the SQLite parameter name (e.g. "pat" for
	// ":pat") for 1-based index i+1, or "" for an anonymous `?`. We
	// keep this list so the cursor can rebuild the NamedValue slice
	// without re-querying the stmt.
	bindNames []string

	// template is a single shared prepared stmt held for the lifetime
	// of the vtab. Cursors borrow it when no other cursor has it; if a
	// second cursor opens concurrently, that cursor prepares its own
	// short-lived statement.
	template *sqlite.Stmt
	mu       sync.Mutex
	refs     int  // open-cursor count borrowing template
	closed   bool // Disconnect/Destroy was called
}

func (t *table) BestIndex(info *sqlite.IndexInfo) error {
	info.EstimatedCost = 1000
	// Record the column → stmt-bind-position permutation in IdxStr so
	// Filter can rebind correctly when the user constrains HIDDEN
	// columns out of declaration order
	// (e.g. WHERE "?2" = X AND "?1" = Y).
	var ord []byte
	idx := 0
	for i, cst := range info.Constraints {
		if cst.Column < t.out {
			continue
		}
		if !cst.Usable || cst.Op != sqlite.OpEQ {
			return errors.New("statement: only `=` constraints on bound parameters are usable")
		}
		// HIDDEN columns appear after t.out output columns; the k-th HIDDEN
		// column (0-based) corresponds to bind parameter k+1 in the prepared
		// stmt.
		stmtPos := cst.Column - t.out + 1
		info.Constraints[i].ArgIndex = idx
		info.Constraints[i].Omit = true
		ord = append(ord, byte(stmtPos))
		idx++
	}
	info.IdxStr = string(ord)
	return nil
}

func (t *table) Open() (sqlite.VTabCursor, error) {
	t.mu.Lock()
	if !t.closed && t.refs == 0 && t.template != nil {
		t.refs = 1
		t.mu.Unlock()
		return &cursor{table: t, stmt: t.template, owns: false}, nil
	}
	t.mu.Unlock()
	// Concurrent cursor or template already finalized — prepare a
	// fresh statement on the host conn.
	prep, err := t.conn.Prepare(t.sql)
	if err != nil {
		return nil, fmt.Errorf("statement: re-prepare: %w", err)
	}
	return &cursor{table: t, stmt: prep.(*sqlite.Stmt), owns: true}, nil
}

// releaseTemplate is called by cursor.Close when the cursor was using
// the shared template. If Disconnect/Destroy is pending and this was the
// last borrower, the template is finalized here.
func (t *table) releaseTemplate() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.refs--
	if t.closed && t.refs == 0 && t.template != nil {
		stmt := t.template
		t.template = nil
		return stmt.Close()
	}
	return nil
}

func (t *table) Disconnect() error {
	t.mu.Lock()
	t.closed = true
	if t.refs > 0 || t.template == nil {
		t.mu.Unlock()
		// Live cursor still borrows the template; its Close will
		// finalize via releaseTemplate.
		return nil
	}
	stmt := t.template
	t.template = nil
	t.mu.Unlock()
	return stmt.Close()
}
func (t *table) Destroy() error { return t.Disconnect() }

type cursor struct {
	table *table
	stmt  *sqlite.Stmt
	owns  bool
	rows  driver.Rows
	// hiddenVals is indexed by HIDDEN-column position (0-based: column
	// 0 here = stmt bind position 1). Built at Filter from the args +
	// permutation so Column(out+k) can return the value the user
	// constrained for that HIDDEN column regardless of WHERE-clause
	// order.
	hiddenVals []driver.Value
	row        []driver.Value
	rowID      int64
	eof        bool
}

func (c *cursor) Filter(_ int, idxStr string, args []driver.Value) error {
	if c.rows != nil {
		_ = c.rows.Close()
		c.rows = nil
	}
	if len(args) != c.table.in {
		return fmt.Errorf("statement: expected %d bound arguments, got %d", c.table.in, len(args))
	}
	c.rowID = 0
	c.eof = false

	// idxStr is the byte-slice permutation BestIndex stored: ord[i] is
	// the 1-based stmt bind position for args[i]. Empty when there are
	// no bound parameters at all (out-only SELECT).
	ord := []byte(idxStr)
	if len(ord) != len(args) {
		// Defensive: if the planner re-runs BestIndex with a different
		// shape, fall back to sequential 1..N.
		ord = make([]byte, len(args))
		for i := range ord {
			ord[i] = byte(i + 1)
		}
	}

	// Build NamedValues so each arg is bound to its correct stmt
	// position (by Name for `:pat`/`@pat`/`$pat`, by Ordinal otherwise),
	// and a parallel reverse-lookup so Column can return each HIDDEN
	// column's user-supplied value regardless of WHERE-clause order.
	if c.hiddenVals == nil || len(c.hiddenVals) != c.table.in {
		c.hiddenVals = make([]driver.Value, c.table.in)
	}
	for i := range c.hiddenVals {
		c.hiddenVals[i] = nil
	}
	nv := make([]driver.NamedValue, len(args))
	for i, v := range args {
		stmtPos := int(ord[i])
		if stmtPos >= 1 && stmtPos-1 < len(c.hiddenVals) {
			c.hiddenVals[stmtPos-1] = v
		}
		name := ""
		if stmtPos-1 < len(c.table.bindNames) {
			name = c.table.bindNames[stmtPos-1]
		}
		if name != "" {
			nv[i] = driver.NamedValue{Name: name, Value: v}
		} else {
			nv[i] = driver.NamedValue{Ordinal: stmtPos, Value: v}
		}
	}
	rs, err := c.stmt.QueryContext(context.Background(), nv)
	if err != nil {
		return fmt.Errorf("statement: query: %w", err)
	}
	c.rows = rs
	if c.row == nil || len(c.row) != c.table.out {
		c.row = make([]driver.Value, c.table.out)
	}
	return c.Next()
}

func (c *cursor) Next() error {
	if c.rows == nil {
		c.eof = true
		return nil
	}
	if err := c.rows.Next(c.row); err != nil {
		if errors.Is(err, io.EOF) {
			c.eof = true
			_ = c.rows.Close()
			c.rows = nil
			return nil
		}
		return err
	}
	c.rowID++
	return nil
}

func (c *cursor) Eof() bool { return c.eof }

func (c *cursor) Column(i int) (sqlite.Value, error) {
	switch {
	case i < c.table.out:
		return c.row[i], nil
	case i-c.table.out < len(c.hiddenVals):
		return c.hiddenVals[i-c.table.out], nil
	}
	return nil, nil
}

func (c *cursor) Rowid() (int64, error) { return c.rowID, nil }

func (c *cursor) Close() error {
	var err error
	if c.rows != nil {
		err = c.rows.Close()
		c.rows = nil
	}
	if c.owns && c.stmt != nil {
		if e := c.stmt.Close(); e != nil && err == nil {
			err = e
		}
		c.stmt = nil
		return err
	}
	if e := c.table.releaseTemplate(); e != nil && err == nil {
		err = e
	}
	return err
}

func quote(name string) string { return sqlid.QuoteIdent(name) }

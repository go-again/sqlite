// Package closure implements the `transitive_closure` virtual table:
// a parent-child graph walker that returns every descendant of a
// given node, with optional depth bounds.
//
//	CREATE VIRTUAL TABLE temp.tc USING transitive_closure(
//	    tablename=org, idcolumn=id, parentcolumn=manager);
//
//	-- All reports (direct + indirect) of employee 42, depth ≤ 3:
//	SELECT id, depth FROM temp.tc
//	    WHERE root = 42 AND depth <= 3;
//
// # Schema
//
// The vtab exposes:
//
//	id            INTEGER  -- the descendant rowid
//	depth         INTEGER  -- 0 for root, 1 for direct children, etc.
//	root          HIDDEN   -- the start node, required in every WHERE
//	tablename     HIDDEN   -- override the configured parent table at query time
//	idcolumn      HIDDEN   -- override the id column at query time
//	parentcolumn  HIDDEN   -- override the parent column at query time
//
// The HIDDEN tablename/idcolumn/parentcolumn columns make it possible
// to point one closure vtab at many different graphs at query time;
// if you only ever traverse one graph, set them via the create
// arguments.
//
// # Internals
//
// xFilter runs a single prepared `SELECT idcolumn FROM tablename WHERE
// parentcolumn = ?` against the configured table, then walks BFS from
// the root collecting every reachable id. A visited-set prevents
// infinite recursion on cyclic graphs.
//
// Ported from [ncruces/ext/closure], itself a Go port of the SQLite
// transitive_closure extension by D. Richard Hipp.
//
// [ncruces/ext/closure]: https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/closure
package closure

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/internal/sqlid"
)

// Register installs the `transitive_closure` virtual table on c.
//
// Registration is per-connection. For pool-wide install via
// [sqlite.Driver.ConnectHook], blank-import the auto sub-package:
//
//	import _ "github.com/go-again/sqlite/ext/closure/auto"
func Register(c *sqlite.Conn) error {
	return c.CreateModule("transitive_closure", ctor)
}

// Column indices in the declared schema.
const (
	colID = iota
	colDepth
	colRoot
	colTablename
	colIDColumn
	colParentColumn
)

func ctor(c *sqlite.Conn, _, _, _ string, args []string) (sqlite.VTab, error) {
	t := &table{conn: c}
	seen := map[string]bool{}
	for _, a := range args {
		key, val := sqlid.NamedArg(a)
		if key == "" {
			return nil, fmt.Errorf("closure: argument %q is not a key=value pair", a)
		}
		if seen[key] {
			return nil, fmt.Errorf("closure: duplicate parameter %q", key)
		}
		seen[key] = true
		val = sqlid.Unquote(val)
		switch key {
		case "tablename":
			t.table = val
		case "idcolumn":
			t.idCol = val
		case "parentcolumn":
			t.parentCol = val
		default:
			return nil, fmt.Errorf("closure: unknown parameter %q", key)
		}
	}
	if err := c.DeclareVTab(
		`CREATE TABLE x(id INTEGER, depth INTEGER, root HIDDEN, tablename HIDDEN, idcolumn HIDDEN, parentcolumn HIDDEN)`,
	); err != nil {
		return nil, err
	}
	return t, nil
}

type table struct {
	conn      *sqlite.Conn
	table     string // tablename, may be empty (then must be supplied at query time)
	idCol     string
	parentCol string
}

func (t *table) Disconnect() error { return nil }
func (t *table) Destroy() error    { return nil }

// IdxNum bit-layout: each constraint's argv position is packed into one
// nibble.
//
//	bit 0:        root EQ (required, always arg 0)
//	bits 4-7:     depth LE/LT/EQ arg index (1..)
//	bit 1:        depth is LT (subtract 1 from bound)
//	bits 8-11:    tablename EQ arg index
//	bits 12-15:   idcolumn EQ arg index
//	bits 16-19:   parentcolumn EQ arg index
func (t *table) BestIndex(info *sqlite.IndexInfo) error {
	var plan int
	pos := 0 // next argv slot to assign
	cost := 1e7

	for i, cst := range info.Constraints {
		if !cst.Usable {
			continue
		}
		switch cst.Column {
		case colRoot:
			if cst.Op == sqlite.OpEQ && plan&1 == 0 {
				plan |= 1
				info.Constraints[i].ArgIndex = pos
				info.Constraints[i].Omit = true
				pos++
				cost /= 100
			}
		case colDepth:
			if plan&0xf0 == 0 {
				switch cst.Op {
				case sqlite.OpEQ, sqlite.OpLE, sqlite.OpLT:
					plan |= (pos + 1) << 4
					info.Constraints[i].ArgIndex = pos
					pos++
					cost /= 5
					if cst.Op == sqlite.OpLT {
						plan |= 2
					}
				}
			}
		case colTablename:
			if cst.Op == sqlite.OpEQ && plan&0xf00 == 0 {
				plan |= (pos + 1) << 8
				info.Constraints[i].ArgIndex = pos
				info.Constraints[i].Omit = true
				pos++
			}
		case colIDColumn:
			if cst.Op == sqlite.OpEQ && plan&0xf000 == 0 {
				plan |= (pos + 1) << 12
				info.Constraints[i].ArgIndex = pos
				info.Constraints[i].Omit = true
				pos++
			}
		case colParentColumn:
			if cst.Op == sqlite.OpEQ && plan&0xf0000 == 0 {
				plan |= (pos + 1) << 16
				info.Constraints[i].ArgIndex = pos
				info.Constraints[i].Omit = true
				pos++
			}
		}
	}

	if plan&1 == 0 {
		return errors.New("transitive_closure: root = ? constraint required")
	}
	if t.table == "" && plan&0xf00 == 0 {
		return errors.New("transitive_closure: tablename must be set at create or query time")
	}
	if t.idCol == "" && plan&0xf000 == 0 {
		return errors.New("transitive_closure: idcolumn must be set at create or query time")
	}
	if t.parentCol == "" && plan&0xf0000 == 0 {
		return errors.New("transitive_closure: parentcolumn must be set at create or query time")
	}

	info.IdxNum = int64(plan)
	info.EstimatedCost = cost
	return nil
}

func (t *table) Open() (sqlite.VTabCursor, error) {
	return &cursor{table: t}, nil
}

type cursor struct {
	table     *table
	nodes     []node
	tableName string
	idCol     string
	parentCol string
	idx       int
}

type node struct {
	id    int64
	depth int
}

func (c *cursor) Filter(idxNumInt int, _ string, args []driver.Value) error {
	idxNum := int(idxNumInt)
	if len(args) == 0 {
		return errors.New("transitive_closure: missing root argument")
	}
	root, ok := args[0].(int64)
	if !ok {
		return fmt.Errorf("transitive_closure: root must be INTEGER, got %T", args[0])
	}
	maxDepth := math.MaxInt
	if idxNum&0xf0 != 0 {
		depthArg := args[(idxNum>>4)&0xf-1]
		switch v := depthArg.(type) {
		case int64:
			maxDepth = int(v)
		case float64:
			maxDepth = int(v)
		}
		if idxNum&2 != 0 {
			maxDepth--
		}
	}

	c.tableName = c.table.table
	if idxNum&0xf00 != 0 {
		c.tableName = stringArg(args[(idxNum>>8)&0xf-1])
	}
	c.idCol = c.table.idCol
	if idxNum&0xf000 != 0 {
		c.idCol = stringArg(args[(idxNum>>12)&0xf-1])
	}
	c.parentCol = c.table.parentCol
	if idxNum&0xf0000 != 0 {
		c.parentCol = stringArg(args[(idxNum>>16)&0xf-1])
	}

	sql := fmt.Sprintf(
		`SELECT %s FROM %s WHERE %s = ?`,
		quote(c.idCol), quote(c.tableName), quote(c.parentCol),
	)
	stmt, err := c.table.conn.Prepare(sql)
	if err != nil {
		return fmt.Errorf("transitive_closure: prepare walker: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	c.nodes = c.nodes[:0]
	c.nodes = append(c.nodes, node{id: root, depth: 0})
	c.idx = 0
	visited := map[int64]bool{root: true}

	for i := 0; i < len(c.nodes); i++ {
		curr := c.nodes[i]
		if curr.depth >= maxDepth {
			continue
		}
		rs, err := stmt.(*sqlite.Stmt).QueryContext(context.Background(), []driver.NamedValue{{Ordinal: 1, Value: curr.id}})
		if err != nil {
			return fmt.Errorf("transitive_closure: query children of %d: %w", curr.id, err)
		}
		row := make([]driver.Value, 1)
		for {
			if err := rs.Next(row); err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				_ = rs.Close()
				return err
			}
			child, ok := row[0].(int64)
			if !ok {
				// Skip non-integer ids; closure works on rowid-like graphs.
				continue
			}
			if !visited[child] {
				visited[child] = true
				c.nodes = append(c.nodes, node{id: child, depth: curr.depth + 1})
			}
		}
		_ = rs.Close()
	}
	return nil
}

func (c *cursor) Next() error { c.idx++; return nil }
func (c *cursor) Eof() bool   { return c.idx >= len(c.nodes) }

func (c *cursor) Column(col int) (sqlite.Value, error) {
	switch col {
	case colID:
		return c.nodes[c.idx].id, nil
	case colDepth:
		return int64(c.nodes[c.idx].depth), nil
	case colTablename:
		return c.tableName, nil
	case colIDColumn:
		return c.idCol, nil
	case colParentColumn:
		return c.parentCol, nil
	}
	return nil, nil
}

func (c *cursor) Rowid() (int64, error) { return c.nodes[c.idx].id, nil }
func (c *cursor) Close() error          { return nil }

func quote(ident string) string {
	return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
}

func stringArg(v driver.Value) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	}
	return ""
}

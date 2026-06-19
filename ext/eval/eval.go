// Package eval adds an eval() scalar function that runs dynamic SQL on
// the calling connection and returns the result as text — sqlean's
// distinctive "SQL from SQL" capability, which no other pure-Go SQLite
// driver exposes.
//
//	eval(sql)         -- run sql, return its result values concatenated
//	eval(sql, sep)    -- ...joined by sep (default '')
//
// Example:
//
//	SELECT eval('SELECT 6 * 7');                 -- '42'
//	SELECT eval('SELECT name FROM t', ', ');     -- 'alice, bob, carol'
//	SELECT eval('CREATE TABLE log(msg)');        -- '' (no output, side effect applied)
//
// The SQL runs on the same connection (a re-entrant prepared statement,
// the proven pattern ext/statement uses), so it sees the same tables,
// pragmas, and transaction. One statement per call.
//
// # Trust boundary
//
// eval() executes arbitrary SQL. Never pass untrusted input to it —
// treat it exactly like building a query by string concatenation. It is
// not registered by default for that reason; opt in explicitly per
// connection or via the auto sub-package.
package eval

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	sqlite "gosqlite.org"
)

// Register installs eval() on c, closing over c so the dynamic SQL runs
// on this connection.
//
// Per-connection registration. For pool-wide install, blank-import the
// auto sub-package:
//
//	import _ "gosqlite.org/ext/eval/auto"
func Register(c *sqlite.Conn) error {
	// Not deterministic: it reads (and may mutate) database state.
	return c.RegisterFunc("eval", func(sql string, sep ...string) (any, error) {
		return run(c, sql, sep)
	}, false)
}

func run(c *sqlite.Conn, sql string, sepArg []string) (any, error) {
	sep := ""
	if len(sepArg) > 0 {
		sep = sepArg[0]
	}
	stmt, err := c.Prepare(sql)
	if err != nil {
		return nil, fmt.Errorf("eval: prepare: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	qs, ok := stmt.(driver.StmtQueryContext)
	if !ok {
		return nil, errors.New("eval: statement does not support QueryContext")
	}
	rows, err := qs.QueryContext(context.Background(), nil)
	if err != nil {
		return nil, fmt.Errorf("eval: %w", err)
	}
	defer func() { _ = rows.Close() }()

	cols := rows.Columns()
	if len(cols) == 0 {
		return nil, nil // a statement with no result columns (DDL/DML)
	}
	dest := make([]driver.Value, len(cols))
	var parts []string
	for {
		if err := rows.Next(dest); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("eval: %w", err)
		}
		for _, v := range dest {
			parts = append(parts, valueText(v))
		}
	}
	if len(parts) == 0 {
		return nil, nil
	}
	return strings.Join(parts, sep), nil
}

// valueText renders a driver.Value the way eval concatenates results:
// NULL becomes the empty string, everything else its natural text form.
func valueText(v driver.Value) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []byte:
		return string(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case bool:
		if x {
			return "1"
		}
		return "0"
	default:
		return fmt.Sprint(x)
	}
}

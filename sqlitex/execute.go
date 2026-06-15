package sqlitex

import (
	"context"
	"database/sql"
)

// ExecQuerier is implemented by *sql.DB, *sql.Conn, and *sql.Tx — the two
// methods [Execute] needs (the row-returning and no-result paths).
type ExecQuerier interface {
	Execer
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// ExecOptions configures [Execute].
type ExecOptions struct {
	// Args are positional bind parameters for ? placeholders.
	Args []any

	// Named are named bind parameters (e.g. :id / @id / $id). Each is passed as
	// sql.Named and appended after Args. Bound by name, so map order is
	// irrelevant.
	Named map[string]any

	// ResultFunc, if non-nil, is invoked once per result row with the *sql.Rows
	// positioned on that row — call rows.Scan to read it. Returning an error
	// stops iteration and is returned by Execute. When nil, Execute runs the
	// statement without expecting rows (DDL/DML).
	ResultFunc func(rows *sql.Rows) error
}

// Execute runs query against eq with opts, handling the row lifecycle so callers
// don't repeat the Query/Next/Scan/Err/Close dance. It is the ergonomic
// row-callback helper the crawshaw/zombiezen sqlitex lineage is known for,
// expressed over database/sql:
//
//	err := sqlitex.Execute(ctx, db, "SELECT id, name FROM users WHERE age > ?",
//	    &sqlitex.ExecOptions{
//	        Args: []any{18},
//	        ResultFunc: func(rows *sql.Rows) error {
//	            var id int64
//	            var name string
//	            if err := rows.Scan(&id, &name); err != nil {
//	                return err
//	            }
//	            // … use id, name …
//	            return nil
//	        },
//	    })
//
// With a non-nil ResultFunc it runs as a query; with a nil ResultFunc (or nil
// opts) it runs as a statement via ExecContext, suitable for DDL/DML.
func Execute(ctx context.Context, eq ExecQuerier, query string, opts *ExecOptions) error {
	var args []any
	if opts != nil {
		args = append(args, opts.Args...)
		for k, v := range opts.Named {
			args = append(args, sql.Named(k, v))
		}
	}
	if opts == nil || opts.ResultFunc == nil {
		_, err := eq.ExecContext(ctx, query, args...)
		return err
	}
	rows, err := eq.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := opts.ResultFunc(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// ResultStrings runs query and collects the first column of every row as
// strings — the multi-row companion to [ResultText].
func ResultStrings(ctx context.Context, eq ExecQuerier, query string, args ...any) ([]string, error) {
	var out []string
	err := Execute(ctx, eq, query, &ExecOptions{Args: args, ResultFunc: func(rows *sql.Rows) error {
		var s string
		if err := rows.Scan(&s); err != nil {
			return err
		}
		out = append(out, s)
		return nil
	}})
	return out, err
}

// ResultInts runs query and collects the first column of every row as int64s —
// the multi-row companion to [ResultInt].
func ResultInts(ctx context.Context, eq ExecQuerier, query string, args ...any) ([]int64, error) {
	var out []int64
	err := Execute(ctx, eq, query, &ExecOptions{Args: args, ResultFunc: func(rows *sql.Rows) error {
		var n int64
		if err := rows.Scan(&n); err != nil {
			return err
		}
		out = append(out, n)
		return nil
	}})
	return out, err
}

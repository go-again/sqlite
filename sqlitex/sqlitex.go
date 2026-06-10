package sqlitex

import (
	"context"
	"database/sql"
	"strconv"
	"sync/atomic"
)

// Execer is implemented by *sql.DB, *sql.Conn, and *sql.Tx.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Querier is implemented by *sql.DB, *sql.Conn, and *sql.Tx.
type Querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

var savepointCounter atomic.Uint64

// Save opens a SAVEPOINT on conn and returns a release function for the
// deferred-cleanup idiom:
//
//	release, err := sqlitex.Save(ctx, conn)
//	if err != nil { return err }
//	defer release(&err)
//
// release rolls back to the savepoint when *errp is non-nil or a panic is in
// flight, and always releases it. A nil errp always releases (commit). Nesting
// is safe: each call uses a distinct savepoint name. Use a *sql.Conn (not a
// pooled *sql.DB) so every statement lands on the same connection.
func Save(ctx context.Context, conn *sql.Conn) (release func(errp *error), err error) {
	name := "sqlitex_sp_" + strconv.FormatUint(savepointCounter.Add(1), 10)
	if _, err := conn.ExecContext(ctx, "SAVEPOINT "+name); err != nil {
		return nil, err
	}
	return func(errp *error) {
		recovered := recover()
		if recovered != nil || (errp != nil && *errp != nil) {
			_, _ = conn.ExecContext(ctx, "ROLLBACK TO "+name)
		}
		_, relErr := conn.ExecContext(ctx, "RELEASE "+name)
		if errp != nil && *errp == nil && recovered == nil {
			*errp = relErr
		}
		if recovered != nil {
			panic(recovered)
		}
	}, nil
}

// Transaction runs fn inside a transaction: it commits when fn returns nil and
// rolls back on a non-nil error or a panic (which is re-raised).
func Transaction(ctx context.Context, db *sql.DB, fn func(tx *sql.Tx) error) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback()
			return
		}
		err = tx.Commit()
	}()
	err = fn(tx)
	return err
}

// ImmediateTransaction runs fn inside a BEGIN IMMEDIATE transaction on conn —
// SQLite acquires the write lock up front, avoiding a deadlock-prone
// upgrade-from-read under concurrency. It commits when fn returns nil and
// rolls back on error or panic. fn must run its statements on the same conn.
func ImmediateTransaction(ctx context.Context, conn *sql.Conn, fn func(conn *sql.Conn) error) (err error) {
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
			panic(p)
		}
		if err != nil {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
			return
		}
		_, err = conn.ExecContext(ctx, "COMMIT")
	}()
	err = fn(conn)
	return err
}

// ExecScript runs a multi-statement SQL script (statements separated by ';')
// in a single ExecContext on e.
func ExecScript(ctx context.Context, e Execer, script string) error {
	_, err := e.ExecContext(ctx, script)
	return err
}

// ResultInt runs query (which must return a single row, single column) and
// returns the value as an int64. sql.ErrNoRows is returned for no rows.
func ResultInt(ctx context.Context, q Querier, query string, args ...any) (int64, error) {
	var v int64
	err := q.QueryRowContext(ctx, query, args...).Scan(&v)
	return v, err
}

// ResultText runs a single-row, single-column query and returns the value as
// a string.
func ResultText(ctx context.Context, q Querier, query string, args ...any) (string, error) {
	var v string
	err := q.QueryRowContext(ctx, query, args...).Scan(&v)
	return v, err
}

// ResultFloat runs a single-row, single-column query and returns the value as
// a float64.
func ResultFloat(ctx context.Context, q Querier, query string, args ...any) (float64, error) {
	var v float64
	err := q.QueryRowContext(ctx, query, args...).Scan(&v)
	return v, err
}

// ResultBool runs a single-row, single-column query and returns the value as a
// bool (non-zero / non-empty is true).
func ResultBool(ctx context.Context, q Querier, query string, args ...any) (bool, error) {
	var v bool
	err := q.QueryRowContext(ctx, query, args...).Scan(&v)
	return v, err
}

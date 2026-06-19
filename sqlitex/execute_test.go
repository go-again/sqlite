package sqlitex_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"gosqlite.org/sqlitex"
)

func TestExecute_RowCallbackAndNamed(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)

	// Nil ResultFunc → runs as a statement (DDL/DML).
	if err := sqlitex.Execute(ctx, db, `CREATE TABLE u(id INTEGER, name TEXT)`, nil); err != nil {
		t.Fatalf("create: %v", err)
	}
	for i, name := range []string{"alice", "bob", "carol"} {
		if err := sqlitex.Execute(ctx, db, `INSERT INTO u VALUES (?, ?)`,
			&sqlitex.ExecOptions{Args: []any{int64(i + 1), name}}); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	// Named parameter + row callback.
	var names []string
	if err := sqlitex.Execute(ctx, db, `SELECT name FROM u WHERE id >= :min ORDER BY id`,
		&sqlitex.ExecOptions{
			Named: map[string]any{"min": 2},
			ResultFunc: func(rows *sql.Rows) error {
				var n string
				if err := rows.Scan(&n); err != nil {
					return err
				}
				names = append(names, n)
				return nil
			},
		}); err != nil {
		t.Fatalf("select: %v", err)
	}
	if len(names) != 2 || names[0] != "bob" || names[1] != "carol" {
		t.Errorf("names = %v, want [bob carol]", names)
	}
}

func TestExecute_ResultFuncErrorAborts(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	if err := sqlitex.Execute(ctx, db, `CREATE TABLE n(v)`, nil); err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		if err := sqlitex.Execute(ctx, db, `INSERT INTO n VALUES (?)`,
			&sqlitex.ExecOptions{Args: []any{int64(i)}}); err != nil {
			t.Fatal(err)
		}
	}

	sentinel := errors.New("stop")
	seen := 0
	err := sqlitex.Execute(ctx, db, `SELECT v FROM n ORDER BY v`, &sqlitex.ExecOptions{
		ResultFunc: func(rows *sql.Rows) error {
			seen++
			return sentinel // abort on the first row
		},
	})
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want the sentinel propagated", err)
	}
	if seen != 1 {
		t.Errorf("ResultFunc ran %d times, want 1 (iteration should stop on error)", seen)
	}
}

func TestResultStringsInts(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	if err := sqlitex.Execute(ctx, db, `CREATE TABLE k(id INTEGER, label TEXT)`, nil); err != nil {
		t.Fatal(err)
	}
	for i, l := range []string{"x", "y", "z"} {
		if err := sqlitex.Execute(ctx, db, `INSERT INTO k VALUES (?, ?)`,
			&sqlitex.ExecOptions{Args: []any{int64(i + 1), l}}); err != nil {
			t.Fatal(err)
		}
	}

	labels, err := sqlitex.ResultStrings(ctx, db, `SELECT label FROM k ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	if len(labels) != 3 || labels[0] != "x" || labels[2] != "z" {
		t.Errorf("ResultStrings = %v, want [x y z]", labels)
	}

	ids, err := sqlitex.ResultInts(ctx, db, `SELECT id FROM k ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 || ids[0] != 1 || ids[2] != 3 {
		t.Errorf("ResultInts = %v, want [1 2 3]", ids)
	}
}

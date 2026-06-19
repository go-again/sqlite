package fts_test

import (
	"context"
	"strings"
	"testing"

	"gosqlite.org/fts"
)

// TestSearchSQL_BasicShape pins the SQL emitted with no options.
func TestSearchSQL_BasicShape(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	idx, err := fts.New[int64, string](ctx, db, "docs", fts.Options{
		Columns: []string{"body"},
	})
	if err != nil {
		t.Fatal(err)
	}
	sql, args, err := idx.SearchSQL(fts.Term("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, "SELECT rowid, body FROM `docs` WHERE `docs` MATCH ?") {
		t.Errorf("unexpected SQL: %s", sql)
	}
	if !strings.Contains(sql, "ORDER BY rank") {
		t.Errorf("default ORDER BY missing: %s", sql)
	}
	if len(args) != 1 {
		t.Errorf("args=%d, want 1", len(args))
	}
}

// TestSearchSQL_WithSelectJoinFilter exercises pantry's exact
// FTSSearch shape: project canonical-table columns, JOIN to the
// canonical table, filter by tenant. Executes through db.QueryContext.
func TestSearchSQL_WithSelectJoinFilter(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE items (
		id     INTEGER PRIMARY KEY,
		title  TEXT,
		tenant TEXT
	)`); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		id     int64
		title  string
		tenant string
	}{
		{1, "alpha", "acme"},
		{2, "beta", "acme"},
		{3, "gamma", "other"},
	} {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO items (id, title, tenant) VALUES (?, ?, ?)`,
			row.id, row.title, row.tenant); err != nil {
			t.Fatal(err)
		}
	}
	idx, err := fts.New[int64, string](ctx, db, "items_fts", fts.Options{
		Columns:   []string{"body"},
		Tokenizer: fts.Porter{Base: fts.Unicode61{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	idx.Insert(ctx,
		fts.Attr[int64, string]{Key: 1, Value: "hello acme one"},
		fts.Attr[int64, string]{Key: 2, Value: "hello acme two"},
		fts.Attr[int64, string]{Key: 3, Value: "hello other three"},
	)

	sql, args, err := idx.SearchSQL(fts.Term("hello"),
		fts.WithSelect("items.id, items.title"),
		fts.WithJoin("JOIN items ON items.id = items_fts.rowid"),
		fts.WithFilter("items.tenant = ?", "acme"),
	)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.QueryContext(ctx, sql, args...)
	if err != nil {
		t.Fatalf("QueryContext: %v\nSQL: %s", err, sql)
	}
	defer rows.Close()

	type result struct {
		Rowid int64
		Body  string
		ID    int64
		Title string
	}
	var got []result
	for rows.Next() {
		var r result
		if err := rows.Scan(&r.Rowid, &r.Body, &r.ID, &r.Title); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("rows=%d, want 2 (tenant=other filtered out)", len(got))
	}
	for _, r := range got {
		if r.Title == "" {
			t.Errorf("title not projected: %+v", r)
		}
		if r.ID != r.Rowid {
			t.Errorf("id=%d != rowid=%d", r.ID, r.Rowid)
		}
	}
}

// TestSearchSQL_WithOrderBy overrides the default rank ordering.
func TestSearchSQL_WithOrderBy(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	idx, err := fts.New[int64, string](ctx, db, "docs", fts.Options{Columns: []string{"body"}})
	if err != nil {
		t.Fatal(err)
	}
	sql, _, err := idx.SearchSQL(fts.Term("hello"),
		fts.WithOrderBy("rowid DESC"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, "ORDER BY rowid DESC") {
		t.Errorf("ORDER BY override not honored: %s", sql)
	}
	if strings.Contains(sql, "ORDER BY rank") {
		t.Errorf("default ORDER BY rank leaked through: %s", sql)
	}
}

// TestSearch_RejectsWithSelect confirms Search errors when given
// WithSelect, since the row shape no longer matches Hit[K,V].
func TestSearch_RejectsWithSelect(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	idx, err := fts.New[int64, string](ctx, db, "docs", fts.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = idx.SearchSlice(ctx, fts.Term("x"),
		fts.WithSelect("extra"))
	if err == nil {
		t.Fatal("expected error from Search+WithSelect, got nil")
	}
	if !strings.Contains(err.Error(), "WithSelect") {
		t.Errorf("error %q doesn't mention WithSelect", err.Error())
	}
}

// TestSearch_RejectsWithJoin confirms the same for WithJoin.
func TestSearch_RejectsWithJoin(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	idx, err := fts.New[int64, string](ctx, db, "docs", fts.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = idx.SearchSlice(ctx, fts.Term("x"),
		fts.WithJoin("JOIN other ON other.id = docs.rowid"))
	if err == nil {
		t.Fatal("expected error from Search+WithJoin, got nil")
	}
	if !strings.Contains(err.Error(), "WithJoin") {
		t.Errorf("error %q doesn't mention WithJoin", err.Error())
	}
}

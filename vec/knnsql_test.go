package vec_test

import (
	"context"
	"strings"
	"testing"

	"gosqlite.org/vec"
)

// TestKNNSQL_BasicShape pins the SQL emitted with no options: the
// baseline KNN query without joins, filters, or projections. Callers
// asserting against the string need a stable contract.
func TestKNNSQL_BasicShape(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	tbl, err := vec.Create(ctx, db, "docs", 4, vec.Options{Encoding: vec.Binary})
	if err != nil {
		t.Fatal(err)
	}
	sql, args, err := tbl.KNNSQL([]float32{1, 0, 0, 0}, 5)
	if err != nil {
		t.Fatal(err)
	}
	want := "SELECT rowid, distance FROM `docs` WHERE `embedding` MATCH vec_f32(?) ORDER BY distance LIMIT 5"
	if sql != want {
		t.Errorf("SQL mismatch\n got:  %s\n want: %s", sql, want)
	}
	if len(args) != 1 {
		t.Errorf("args=%d, want 1 (the query embedding)", len(args))
	}
	if _, ok := args[0].([]byte); !ok {
		t.Errorf("args[0] type=%T, want []byte for Binary encoding", args[0])
	}
}

// TestKNNSQL_WithSelectJoinFilter exercises pantry's exact VectorSearch
// shape: project canonical-table columns + EXISTS flag, JOIN to the
// canonical table, filter by tenant. The whole thing executes through
// db.QueryContext using the SQL we hand back.
func TestKNNSQL_WithSelectJoinFilter(t *testing.T) {
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
		{1, "alpha-acme", "acme"},
		{2, "beta-acme", "acme"},
		{3, "gamma-other", "other"},
	} {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO items (id, title, tenant) VALUES (?, ?, ?)`,
			row.id, row.title, row.tenant); err != nil {
			t.Fatal(err)
		}
	}
	tbl, err := vec.Create(ctx, db, "items_vec", 4, vec.Options{Encoding: vec.Binary})
	if err != nil {
		t.Fatal(err)
	}
	if err := tbl.BatchInsert(ctx, []vec.Row{
		{Rowid: 1, Embedding: []float32{1, 0, 0, 0}},
		{Rowid: 2, Embedding: []float32{0.9, 0.1, 0, 0}},
		{Rowid: 3, Embedding: []float32{0, 1, 0, 0}},
	}); err != nil {
		t.Fatal(err)
	}

	sql, args, err := tbl.KNNSQL([]float32{1, 0, 0, 0}, 10,
		vec.WithSelect("items.id, items.title"),
		vec.WithJoin("JOIN items ON items.id = items_vec.rowid"),
		vec.WithFilter("items.tenant = ?", "acme"),
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
		Rowid    int64
		Distance float64
		ID       int64
		Title    string
	}
	var got []result
	for rows.Next() {
		var r result
		if err := rows.Scan(&r.Rowid, &r.Distance, &r.ID, &r.Title); err != nil {
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

// TestKNNSQL_WithOrderBy overrides the default distance ordering.
// Useful when the JOINed canonical table has a created_at column the
// caller wants to sort by instead.
func TestKNNSQL_WithOrderBy(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	tbl, err := vec.Create(ctx, db, "docs", 4, vec.Options{Encoding: vec.Binary})
	if err != nil {
		t.Fatal(err)
	}
	sql, _, err := tbl.KNNSQL([]float32{1, 0, 0, 0}, 5,
		vec.WithOrderBy("rowid DESC"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, "ORDER BY rowid DESC") {
		t.Errorf("ORDER BY override not honored: %s", sql)
	}
	if strings.Contains(sql, "ORDER BY distance") {
		t.Errorf("default ORDER BY distance leaked through: %s", sql)
	}
}

// TestKNN_RejectsWithSelect confirms KNN errors when given WithSelect,
// since the row shape no longer matches Neighbor.
func TestKNN_RejectsWithSelect(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	tbl, err := vec.Create(ctx, db, "docs", 4, vec.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = tbl.KNNSlice(ctx, []float32{1, 0, 0, 0}, 1,
		vec.WithSelect("extra"))
	if err == nil {
		t.Fatal("expected error from KNN+WithSelect, got nil")
	}
	if !strings.Contains(err.Error(), "WithSelect") {
		t.Errorf("error %q doesn't mention WithSelect", err.Error())
	}
}

// TestKNN_RejectsWithJoin confirms the same for WithJoin.
func TestKNN_RejectsWithJoin(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	tbl, err := vec.Create(ctx, db, "docs", 4, vec.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = tbl.KNNSlice(ctx, []float32{1, 0, 0, 0}, 1,
		vec.WithJoin("JOIN other ON other.id = docs.rowid"))
	if err == nil {
		t.Fatal("expected error from KNN+WithJoin, got nil")
	}
	if !strings.Contains(err.Error(), "WithJoin") {
		t.Errorf("error %q doesn't mention WithJoin", err.Error())
	}
}

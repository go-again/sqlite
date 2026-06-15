package vec_test

import (
	"context"
	"database/sql"
	"slices"
	"testing"

	"github.com/go-again/sqlite/vec"
)

func metaTable(t *testing.T, ctx context.Context) (*vec.Table, *sql.DB) {
	t.Helper()
	db := openDB(t)
	tbl, err := vec.Create(ctx, db, "docs", 4, vec.Options{
		Columns: []vec.Column{
			{Name: "tenant", Type: "integer", Kind: vec.Partition},
			{Name: "category", Type: "text", Kind: vec.Metadata},
			{Name: "title", Type: "text", Kind: vec.Auxiliary},
		},
		ChunkSize: 8,
	})
	if err != nil {
		t.Fatalf("Create with columns: %v", err)
	}
	rows := []vec.Row{
		{Rowid: 1, Embedding: []float32{1, 0, 0, 0}, Values: map[string]any{"tenant": 100, "category": "news", "title": "First"}},
		{Rowid: 2, Embedding: []float32{0, 1, 0, 0}, Values: map[string]any{"tenant": 100, "category": "blog", "title": "Second"}},
		{Rowid: 3, Embedding: []float32{1, 0, 0, 0}, Values: map[string]any{"tenant": 200, "category": "news", "title": "Third"}},
	}
	if err := tbl.BatchInsert(ctx, rows); err != nil {
		t.Fatalf("BatchInsert: %v", err)
	}
	return tbl, db
}

// TestTyped_MetadataPartitionFilter: a metadata + partition predicate via
// WithFilter restricts the KNN result set to the matching shard/category.
func TestTyped_MetadataPartitionFilter(t *testing.T) {
	ctx := context.Background()
	tbl, _ := metaTable(t, ctx)

	// category=news AND tenant=100 leaves only rowid 1 (rowid 3 is tenant 200).
	matches, err := tbl.KNNSlice(ctx, []float32{1, 0, 0, 0}, 5,
		vec.WithFilter("category = ? AND tenant = ?", "news", 100))
	if err != nil {
		t.Fatalf("filtered KNN: %v", err)
	}
	if len(matches) != 1 || matches[0].Rowid != 1 {
		t.Errorf("filtered matches = %+v, want exactly rowid 1", matches)
	}

	// category=news alone spans both tenants → rowids 1 and 3.
	matches, err = tbl.KNNSlice(ctx, []float32{1, 0, 0, 0}, 5,
		vec.WithFilter("category = ?", "news"))
	if err != nil {
		t.Fatalf("metadata-only KNN: %v", err)
	}
	got := []int64{}
	for _, m := range matches {
		got = append(got, m.Rowid)
	}
	slices.Sort(got)
	if len(got) != 2 || got[0] != 1 || got[1] != 3 {
		t.Errorf("category=news matches = %v, want [1 3]", got)
	}
}

// TestTyped_AuxColumnRetrieval: the auxiliary payload column is readable via
// WithSelect + KNNSQL even though it is not filterable.
func TestTyped_AuxColumnRetrieval(t *testing.T) {
	ctx := context.Background()
	tbl, db := metaTable(t, ctx)

	sqlStr, args, err := tbl.KNNSQL([]float32{1, 0, 0, 0}, 5,
		vec.WithSelect("title"), vec.WithFilter("category = ?", "news"))
	if err != nil {
		t.Fatalf("KNNSQL: %v", err)
	}
	rows, err := db.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	titles := map[int64]string{}
	for rows.Next() {
		var id int64
		var dist float64
		var title string
		if err := rows.Scan(&id, &dist, &title); err != nil {
			t.Fatal(err)
		}
		titles[id] = title
	}
	if titles[1] != "First" || titles[3] != "Third" {
		t.Errorf("aux titles = %v, want rowid1=First rowid3=Third", titles)
	}
}

// TestTyped_InsertRow: the single-row InsertRow path carries column values.
func TestTyped_InsertRow(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	tbl, err := vec.Create(ctx, db, "docs", 4, vec.Options{
		Columns: []vec.Column{{Name: "category", Type: "text", Kind: vec.Metadata}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tbl.InsertRow(ctx, vec.Row{
		Rowid: 7, Embedding: []float32{1, 0, 0, 0}, Values: map[string]any{"category": "x"},
	}); err != nil {
		t.Fatalf("InsertRow: %v", err)
	}
	matches, err := tbl.KNNSlice(ctx, []float32{1, 0, 0, 0}, 5, vec.WithFilter("category = ?", "x"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Rowid != 7 {
		t.Errorf("matches = %+v, want rowid 7", matches)
	}
}

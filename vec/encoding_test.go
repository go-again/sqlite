package vec_test

import (
	"context"
	"testing"

	"github.com/go-again/sqlite/vec"
)

// TestEncode_JSON_RoundTrip exercises the package-function form
// (vec.Encode) against a real vec0 table. JSON-encoded inputs go in
// via the typed API, then a raw-SQL MATCH using the placeholder +
// value returned by Encode must surface the same rowid.
func TestEncode_JSON_RoundTrip(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	tbl, err := vec.Create(ctx, db, "docs", 4, vec.Options{Encoding: vec.JSON})
	if err != nil {
		t.Fatal(err)
	}
	if err := tbl.Insert(ctx, 1, []float32{1, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	ph, val := vec.Encode([]float32{1, 0, 0, 0}, vec.JSON)
	if ph != "?" {
		t.Fatalf("JSON placeholder=%q, want ?", ph)
	}
	rows, err := db.QueryContext(ctx,
		"SELECT rowid FROM docs WHERE embedding MATCH "+ph+" ORDER BY distance LIMIT 1",
		val)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("no rows")
	}
	var rowid int64
	if err := rows.Scan(&rowid); err != nil {
		t.Fatal(err)
	}
	if rowid != 1 {
		t.Errorf("rowid=%d, want 1", rowid)
	}
}

// TestEncode_Binary_RoundTrip mirrors the JSON variant for the
// Binary encoding, which uses sqlite-vec's vec_f32(?) constructor
// and binds a []byte BLOB.
func TestEncode_Binary_RoundTrip(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	tbl, err := vec.Create(ctx, db, "docs", 4, vec.Options{Encoding: vec.Binary})
	if err != nil {
		t.Fatal(err)
	}
	if err := tbl.Insert(ctx, 7, []float32{0, 1, 0, 0}); err != nil {
		t.Fatal(err)
	}
	ph, val := vec.Encode([]float32{0, 1, 0, 0}, vec.Binary)
	if ph != "vec_f32(?)" {
		t.Fatalf("Binary placeholder=%q, want vec_f32(?)", ph)
	}
	if _, ok := val.([]byte); !ok {
		t.Fatalf("Binary value type=%T, want []byte", val)
	}
	rows, err := db.QueryContext(ctx,
		"SELECT rowid FROM docs WHERE embedding MATCH "+ph+" ORDER BY distance LIMIT 1",
		val)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("no rows")
	}
	var rowid int64
	if err := rows.Scan(&rowid); err != nil {
		t.Fatal(err)
	}
	if rowid != 7 {
		t.Errorf("rowid=%d, want 7", rowid)
	}
}

// TestEncode_PlaceholderShape pins the documented contract: JSON
// returns "?" and a string value; Binary returns "vec_f32(?)" and a
// []byte value. Callers writing raw SQL rely on these exact strings.
func TestEncode_PlaceholderShape(t *testing.T) {
	v := []float32{0.1, 0.2}

	ph, val := vec.Encode(v, vec.JSON)
	if ph != "?" {
		t.Errorf("JSON placeholder=%q, want ?", ph)
	}
	if _, ok := val.(string); !ok {
		t.Errorf("JSON value type=%T, want string", val)
	}

	ph, val = vec.Encode(v, vec.Binary)
	if ph != "vec_f32(?)" {
		t.Errorf("Binary placeholder=%q, want vec_f32(?)", ph)
	}
	if _, ok := val.([]byte); !ok {
		t.Errorf("Binary value type=%T, want []byte", val)
	}
}

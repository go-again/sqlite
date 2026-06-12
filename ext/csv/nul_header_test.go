package csv_test

import (
	"context"
	"testing"
	"testing/fstest"

	"github.com/go-again/sqlite/ext/csv"
)

// TestTable_NULHeaderFallsBackToPositional pins sweep #3 F5: a NUL byte in a
// header cell would otherwise truncate the generated CREATE TABLE at the
// libc.CString boundary. The schema builder now treats such a cell like an
// empty one and uses the positional c<N> name, so Create still succeeds and the
// clean cells keep their names.
func TestTable_NULHeaderFallsBackToPositional(t *testing.T) {
	ctx := context.Background()
	db := openCSVDB(t, fstest.MapFS{"d.csv": {Data: []byte("a\x00b,good\n1,2\n")}})
	tbl, err := csv.Create(ctx, db, "t", csv.WithFilename("d.csv"), csv.WithHeader())
	if err != nil {
		t.Fatalf("Create with a NUL header cell should succeed via fallback: %v", err)
	}
	cols, err := tbl.Columns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 2 || cols[0] != "c1" || cols[1] != "good" {
		t.Errorf("columns = %v, want [c1 good] (NUL cell → positional, clean cell kept)", cols)
	}
}

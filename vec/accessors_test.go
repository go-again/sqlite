package vec_test

import (
	"context"
	"testing"

	"github.com/go-again/sqlite/vec"
)

// TestTable_Accessors pins the public accessor surface (Name / Dim /
// Metric / Encoding) so a refactor that drops or renames a field on
// vec.Table is caught at compile + assertion time. Round 7 added this
// because the round-6 audit found Metric() and Encoding() with zero
// in-tree call sites.
func TestTable_Accessors(t *testing.T) {
	db := openDB(t)
	tbl, err := vec.Create(context.Background(), db, "embeds", 8, vec.Options{
		Metric:   vec.Cosine,
		Encoding: vec.Binary,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := tbl.Name(); got != "embeds" {
		t.Errorf("Name() = %q, want embeds", got)
	}
	if got := tbl.Dim(); got != 8 {
		t.Errorf("Dim() = %d, want 8", got)
	}
	if got := tbl.Metric(); got != vec.Cosine {
		t.Errorf("Metric() = %v, want Cosine", got)
	}
	if got := tbl.Encoding(); got != vec.Binary {
		t.Errorf("Encoding() = %v, want Binary", got)
	}
}

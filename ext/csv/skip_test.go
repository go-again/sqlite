package csv_test

import (
	"context"
	"testing"
)

// TestCSV_SkipRows covers the skip=N parameter: discard leading banner
// rows that sit above the real header.
func TestCSV_SkipRows(t *testing.T) {
	_, sc := withCSV(t, nil)
	ctx := context.Background()
	// Two provenance banners, then a header, then two data rows.
	if _, err := sc.ExecContext(ctx,
		`CREATE VIRTUAL TABLE temp.t USING csv(data='# exported 2026
# source: ledger
id,name
1,alice
2,bob', skip=2, header=on)`); err != nil {
		t.Fatalf("CREATE VIRTUAL TABLE: %v", err)
	}

	var n int
	if err := sc.QueryRowContext(ctx, `SELECT count(*) FROM t`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("count=%d, want 2 (banners skipped, header consumed)", n)
	}
	var name string
	if err := sc.QueryRowContext(ctx, `SELECT name FROM t WHERE id='2'`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "bob" {
		t.Errorf("name=%q, want bob (header columns honored after skip)", name)
	}
}

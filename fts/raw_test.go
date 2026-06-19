package fts_test

import (
	"database/sql"
	"strings"
	"testing"

	sqlite "gosqlite.org"
)

// openRaw returns an in-memory DB pinned to a single connection. FTS5
// virtual tables are per-conn, so the pin is mandatory for the assertions
// to use the same connection that ran CREATE.
func openRaw(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open(sqlite.DriverNameSQLite3, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	return db
}

// seedDocs creates an FTS5 table with the given options and inserts a small
// fixed corpus. The corpus is the same one used by the typed-API tests so a
// reader can diff raw vs typed coverage easily.
func seedDocs(t *testing.T, db *sql.DB, decl string) {
	t.Helper()
	if _, err := db.Exec(`create virtual table docs using fts5(` + decl + `)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := db.Exec(`
insert into docs(rowid, body) values
  (1, 'the quick brown fox jumps over the lazy dog'),
  (2, 'a brown dog barked at the moon'),
  (3, 'pack my box with five dozen liquor jugs'),
  (4, 'running rivers run through ridges')
`); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// TestRaw_UnindexedColumn asserts a UNINDEXED column can be created and
// queried. Documented FTS5 feature for packing payload alongside the
// indexed text without growing the index.
// https://www.sqlite.org/fts5.html#the_unindexed_column_option
func TestRaw_UnindexedColumn(t *testing.T) {
	db := openRaw(t)
	if _, err := db.Exec(`create virtual table t using fts5(body, tag UNINDEXED)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
insert into t(body, tag) values
  ('the quick brown fox', 'mammal'),
  ('the noisy yellow canary', 'avian')`); err != nil {
		t.Fatal(err)
	}
	// 'mammal' is NOT indexed, so matching against it returns no rows.
	var n int
	if err := db.QueryRow(`select count(*) from t where t match 'mammal'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("UNINDEXED column matched %d rows, want 0", n)
	}
	// Tag is still readable as a regular column.
	var tag string
	if err := db.QueryRow(`select tag from t where t match 'canary'`).Scan(&tag); err != nil {
		t.Fatal(err)
	}
	if tag != "avian" {
		t.Errorf("tag=%q, want avian", tag)
	}
}

// TestRaw_ColumnsizeZero asserts the `columnsize=0` option is accepted.
// This disables per-column-size tracking, trading ranking quality for
// space. https://www.sqlite.org/fts5.html#the_columnsize_option
func TestRaw_ColumnsizeZero(t *testing.T) {
	db := openRaw(t)
	if _, err := db.Exec(`create virtual table t using fts5(body, columnsize=0)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := db.Exec(`insert into t(body) values('the quick brown fox')`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`select count(*) from t where t match 'fox'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("match count=%d, want 1", n)
	}
}

// TestRaw_PrefixIndexes asserts the `prefix='N1 N2 ...'` option pre-computes
// a prefix-match index for each listed length. We verify by inserting a row
// and probing prefix queries of the configured lengths.
// https://www.sqlite.org/fts5.html#prefix_indexes
func TestRaw_PrefixIndexes(t *testing.T) {
	db := openRaw(t)
	if _, err := db.Exec(`create virtual table t using fts5(body, prefix='2 3 4')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`insert into t(body) values('quintessential')`); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"qu*", "qui*", "quin*"} {
		var n int
		if err := db.QueryRow(`select count(*) from t where t match ?`, q).Scan(&n); err != nil {
			t.Fatalf("prefix %q: %v", q, err)
		}
		if n != 1 {
			t.Errorf("prefix %q matched %d rows, want 1", q, n)
		}
	}
}

// TestRaw_RankColumn asserts FTS5's auto-exposed `rank` column returns the
// BM25 score directly in a SELECT. Equivalent to bm25() with default weights;
// useful as a shorthand. https://www.sqlite.org/fts5.html#sorting_by_auxiliary_function_results
func TestRaw_RankColumn(t *testing.T) {
	db := openRaw(t)
	seedDocs(t, db, "body")

	rows, err := db.Query(`select rowid, rank from docs where docs match 'brown dog' order by rank`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type entry struct {
		rowid int64
		rank  float64
	}
	var got []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.rowid, &e.rank); err != nil {
			t.Fatal(err)
		}
		got = append(got, e)
	}
	if len(got) < 2 {
		t.Fatalf("rank column: got %d rows, want >=2", len(got))
	}
	// Lower rank = better match in BM25. (FTS5 returns the negative of
	// the conventional BM25 score so ASC order gives best-first.)
	// We assert monotonicity rather than absolute values.
	for i := 1; i < len(got); i++ {
		if got[i].rank < got[i-1].rank {
			t.Errorf("rank not non-decreasing at [%d]: %+v", i, got)
		}
	}
}

// TestRaw_BM25_Weights asserts column weights tilt the ranking. The 2-column
// body+title table is queried twice: once with title weighted 0.1 (so body
// dominates), once with title weighted 10 (so title dominates). Different
// rowid orderings prove the weights took effect.
// https://www.sqlite.org/fts5.html#the_bm25_function
func TestRaw_BM25_Weights(t *testing.T) {
	db := openRaw(t)
	if _, err := db.Exec(`create virtual table d using fts5(body, title)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
insert into d(rowid, body, title) values
  (1, 'fox runs in the meadow', 'the cat'),
  (2, 'the cat naps lazily', 'fox')`); err != nil {
		t.Fatal(err)
	}
	// First query: weight body high → row 1 wins.
	var top1 int64
	if err := db.QueryRow(`
select rowid from d where d match 'fox' order by bm25(d, 10, 0.1) limit 1`).Scan(&top1); err != nil {
		t.Fatal(err)
	}
	// Second: weight title high → row 2 wins.
	var top2 int64
	if err := db.QueryRow(`
select rowid from d where d match 'fox' order by bm25(d, 0.1, 10) limit 1`).Scan(&top2); err != nil {
		t.Fatal(err)
	}
	if top1 != 1 || top2 != 2 {
		t.Errorf("BM25 weights ineffective: top1=%d top2=%d, want 1, 2", top1, top2)
	}
}

// TestRaw_SnippetAndHighlight asserts the documented snippet() and
// highlight() function signatures produce the documented output for a known
// row. https://www.sqlite.org/fts5.html#the_snippet_function
func TestRaw_SnippetAndHighlight(t *testing.T) {
	db := openRaw(t)
	seedDocs(t, db, "body")

	var snip, hi string
	if err := db.QueryRow(`
select snippet(docs, 0, '<b>', '</b>', '…', 4),
       highlight(docs, 0, '[', ']')
from docs where docs match 'fox' limit 1`).Scan(&snip, &hi); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(snip, "<b>fox</b>") {
		t.Errorf("snippet=%q missing <b>fox</b>", snip)
	}
	if !strings.Contains(hi, "[fox]") {
		t.Errorf("highlight=%q missing [fox]", hi)
	}
}

// TestRaw_IntegrityCheck asserts the documented `integrity-check` command is
// callable and returns no error on a healthy index. The command writes no
// rows; success = absence of error. Useful as a corruption canary in long-
// running deployments. https://www.sqlite.org/fts5.html#the_integrity_check_command
func TestRaw_IntegrityCheck(t *testing.T) {
	db := openRaw(t)
	seedDocs(t, db, "body")
	if _, err := db.Exec(`insert into docs(docs) values('integrity-check')`); err != nil {
		t.Errorf("integrity-check: %v", err)
	}
	// Also test the explicit "full" form.
	if _, err := db.Exec(`insert into docs(docs, rank) values('integrity-check', 1)`); err != nil {
		t.Errorf("integrity-check full: %v", err)
	}
}

// TestRaw_Optimize asserts the `optimize` maintenance command is callable.
// It rewrites the index into a single segment. We don't assert any
// observable side effect beyond no-error completion, because checking
// segment count would require parsing fts5_structure().
func TestRaw_Optimize(t *testing.T) {
	db := openRaw(t)
	seedDocs(t, db, "body")
	if _, err := db.Exec(`insert into docs(docs) values('optimize')`); err != nil {
		t.Errorf("optimize: %v", err)
	}
}

// TestRaw_PgszTuning asserts the page-size tuning knob (`pgsz`) is accepted
// via the rank-coded form. Used by power users to trade read amplification
// for write throughput. https://www.sqlite.org/fts5.html#the_pgsz_configuration_option
func TestRaw_PgszTuning(t *testing.T) {
	db := openRaw(t)
	seedDocs(t, db, "body")
	if _, err := db.Exec(`insert into docs(docs, rank) values('pgsz', 4096)`); err != nil {
		t.Errorf("pgsz tuning: %v", err)
	}
}

// TestRaw_UpdateRow asserts UPDATE on an FTS5 table is supported, even
// though our typed API does not surface it. The recommended idiom is
// delete+insert, but raw UPDATE works for backwards-compatible callers.
func TestRaw_UpdateRow(t *testing.T) {
	db := openRaw(t)
	seedDocs(t, db, "body")
	if _, err := db.Exec(`update docs set body = 'jackal jackal jackal' where rowid = 1`); err != nil {
		t.Fatal(err)
	}
	// Old token vanished from row 1; new token landed.
	var n int
	if err := db.QueryRow(`select count(*) from docs where docs match 'fox'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("after UPDATE: 'fox' matches=%d, want 0", n)
	}
	if err := db.QueryRow(`select count(*) from docs where docs match 'jackal'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("after UPDATE: 'jackal' matches=%d, want 1", n)
	}
}

// TestRaw_ContentlessDeleteAll exercises the `delete-all` maintenance
// command, which is only valid on contentless tables. After invocation, the
// index is empty. https://www.sqlite.org/fts5.html#contentless_tables
func TestRaw_ContentlessDeleteAll(t *testing.T) {
	db := openRaw(t)
	if _, err := db.Exec(`create virtual table cl using fts5(body, content='')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`insert into cl(rowid, body) values (1, 'alpha beta gamma')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`insert into cl(cl) values('delete-all')`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`select count(*) from cl where cl match 'alpha'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("after delete-all: matches=%d, want 0", n)
	}
}

// TestRaw_RebuildAndOptimize exercises the rebuild+optimize sequence on a
// regular content table. Rebuild re-derives the index from the content;
// optimize compacts. Combined invocation matches the recommended idiom for
// repairing an index whose data table was bulk-loaded out-of-band.
func TestRaw_RebuildAndOptimize(t *testing.T) {
	db := openRaw(t)
	seedDocs(t, db, "body")
	if _, err := db.Exec(`insert into docs(docs) values('rebuild')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`insert into docs(docs) values('optimize')`); err != nil {
		t.Fatal(err)
	}
	// Sanity: index still queryable post-rebuild.
	var n int
	if err := db.QueryRow(`select count(*) from docs where docs match 'fox'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("post-rebuild 'fox' matches=%d, want 1", n)
	}
}

// TestRaw_AutomergeAndCrisismerge asserts the `automerge` and `crisismerge`
// tuning knobs are accepted. Both control the lazy-merge behavior of the
// FTS5 segment manager. https://www.sqlite.org/fts5.html#the_automerge_configuration_option
func TestRaw_AutomergeAndCrisismerge(t *testing.T) {
	db := openRaw(t)
	seedDocs(t, db, "body")
	if _, err := db.Exec(`insert into docs(docs, rank) values('automerge', 4)`); err != nil {
		t.Errorf("automerge: %v", err)
	}
	if _, err := db.Exec(`insert into docs(docs, rank) values('crisismerge', 16)`); err != nil {
		t.Errorf("crisismerge: %v", err)
	}
}

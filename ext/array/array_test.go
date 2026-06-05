package array_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/array"
	"github.com/go-again/sqlite/internal/testhelp"
)

// openDB opens an in-memory database, pins to MaxOpenConns=1, returns
// the *sqlite.Conn for direct module registration plus the pinned *sql.Conn
// the test uses for SQL execution.
func openDB(t *testing.T) (*sql.DB, *sql.Conn, *sqlite.Conn) {
	t.Helper()
	db, sc := testhelp.OpenPinned(t, "sqlite", ":memory:")
	conn := testhelp.RawConn(t, sc)
	if err := array.Register(conn); err != nil {
		t.Fatalf("array.Register: %v", err)
	}
	return db, sc, conn
}

func TestArray_IntSlice(t *testing.T) {
	_, sc, conn := openDB(t)
	token, release := array.Bind(conn, []int{10, 20, 30})
	defer release()

	rows, err := sc.QueryContext(context.Background(),
		`SELECT value FROM array(?) ORDER BY value`, token)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer rows.Close()
	var got []int64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		got = append(got, v)
	}
	want := []int64{10, 20, 30}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("row[%d]=%d, want %d", i, got[i], want[i])
		}
	}
}

func TestArray_TypedSlices(t *testing.T) {
	_, sc, conn := openDB(t)
	ctx := context.Background()

	cases := []struct {
		name  string
		slice any
		want  []string // stringified row values
	}{
		{"int64", []int64{1, 2, 3}, []string{"1", "2", "3"}},
		{"float64", []float64{1.5, 2.5}, []string{"1.5", "2.5"}},
		{"string", []string{"alpha", "beta", "gamma"}, []string{"alpha", "beta", "gamma"}},
		{"bool", []bool{true, false, true}, []string{"1", "0", "1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			token, release := array.Bind(conn, tc.slice)
			defer release()

			rows, err := sc.QueryContext(ctx,
				`SELECT CAST(value AS TEXT) FROM array(?)`, token)
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			defer rows.Close()
			var got []string
			for rows.Next() {
				var v string
				if err := rows.Scan(&v); err != nil {
					t.Fatalf("Scan: %v", err)
				}
				got = append(got, v)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestArray_AnyHeterogeneous(t *testing.T) {
	_, sc, conn := openDB(t)
	token, release := array.Bind(conn, []any{int64(42), "hello", nil, 3.14, true})
	defer release()

	rows, err := sc.QueryContext(context.Background(),
		`SELECT typeof(value), CAST(value AS TEXT) FROM array(?)`, token)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer rows.Close()
	type row struct {
		ty, val string
	}
	var got []row
	for rows.Next() {
		var ty, val sql.NullString
		if err := rows.Scan(&ty, &val); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		got = append(got, row{ty.String, val.String})
	}
	wantTypes := []string{"integer", "text", "null", "real", "integer"}
	if len(got) != len(wantTypes) {
		t.Fatalf("got %d rows, want %d", len(got), len(wantTypes))
	}
	for i, w := range wantTypes {
		if got[i].ty != w {
			t.Errorf("row[%d] type=%q, want %q", i, got[i].ty, w)
		}
	}
}

func TestArray_RowIDIsOneBased(t *testing.T) {
	// carray semantics: rowid starts at 1, matches upstream so JOINs against
	// other rowid-keyed tables behave intuitively.
	_, sc, conn := openDB(t)
	token, release := array.Bind(conn, []string{"a", "b", "c"})
	defer release()

	rows, err := sc.QueryContext(context.Background(),
		`SELECT rowid, value FROM array(?)`, token)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer rows.Close()
	var i int64
	for rows.Next() {
		var rid int64
		var v string
		if err := rows.Scan(&rid, &v); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		i++
		if rid != i {
			t.Errorf("rowid=%d, want %d", rid, i)
		}
	}
	if i != 3 {
		t.Errorf("scanned %d rows, want 3", i)
	}
}

func TestArray_UnknownTokenError(t *testing.T) {
	_, sc, _ := openDB(t)
	_, err := sc.QueryContext(context.Background(),
		`SELECT value FROM array(?)`, int64(99999))
	if err == nil {
		t.Fatal("expected error from unknown token, got nil")
	}
	if !strings.Contains(err.Error(), "unknown token") {
		t.Errorf("error %q does not mention unknown token", err.Error())
	}
}

func TestArray_ReleaseRemovesBinding(t *testing.T) {
	_, sc, conn := openDB(t)
	token, release := array.Bind(conn, []int{1, 2, 3})
	release()
	// Second release is a no-op (sync.Once).
	release()
	_, err := sc.QueryContext(context.Background(),
		`SELECT value FROM array(?)`, token)
	if err == nil {
		t.Fatal("expected error after release, got nil")
	}
}

func TestArray_MissingConstraintError(t *testing.T) {
	_, sc, _ := openDB(t)
	// Querying the vtab without binding array=? must surface the missing
	// constraint error from BestIndex.
	_, err := sc.QueryContext(context.Background(), `SELECT value FROM array`)
	if err == nil {
		t.Fatal("expected error for missing constraint, got nil")
	}
}

func TestArray_ArrayConstraintIsHidden(t *testing.T) {
	// The `array` column is HIDDEN — `SELECT *` returns just the visible
	// `value` column.
	_, sc, conn := openDB(t)
	token, release := array.Bind(conn, []int{7})
	defer release()
	rows, err := sc.QueryContext(context.Background(),
		`SELECT * FROM array(?)`, token)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatalf("Columns: %v", err)
	}
	if len(cols) != 1 || cols[0] != "value" {
		t.Errorf("cols=%v, want [value]", cols)
	}
}

func TestArray_ModuleName(t *testing.T) {
	if array.ModuleName != "array" {
		t.Errorf("ModuleName=%q, want %q", array.ModuleName, "array")
	}
}

func TestArray_TransparentPointer(t *testing.T) {
	// Transparent path: sqlite.Pointer(slice) instead of Bind/Release.
	// SQLite's destructor frees the binding on stmt finalize — no
	// caller-side cleanup needed.
	_, sc, _ := openDB(t)
	rows, err := sc.QueryContext(context.Background(),
		`SELECT value FROM array(?) ORDER BY value`,
		sqlite.Pointer([]int{42, 7, 99}))
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer rows.Close()
	var got []int64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		got = append(got, v)
	}
	want := []int64{7, 42, 99}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("row[%d]=%d, want %d", i, got[i], want[i])
		}
	}
}

func TestArray_TransparentPointerStringSlice(t *testing.T) {
	_, sc, _ := openDB(t)
	rows, err := sc.QueryContext(context.Background(),
		`SELECT value FROM array(?)`,
		sqlite.Pointer([]string{"alpha", "beta", "gamma"}))
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		got = append(got, s)
	}
	want := []string{"alpha", "beta", "gamma"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestArray_TransparentPointerHeterogeneous exercises a Pointer-bound
// []any with mixed element types. Filter has to walk the slice via
// reflection (not the fast-path type-switch) because []any doesn't match
// the typed cases.
func TestArray_TransparentPointerHeterogeneous(t *testing.T) {
	_, sc, _ := openDB(t)
	rows, err := sc.QueryContext(context.Background(),
		`SELECT typeof(value), CAST(value AS TEXT) FROM array(?)`,
		sqlite.Pointer([]any{int64(42), "hello", nil, 3.14, true}))
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer rows.Close()
	type pair struct{ ty, val string }
	var got []pair
	for rows.Next() {
		var ty, val sql.NullString
		if err := rows.Scan(&ty, &val); err != nil {
			t.Fatal(err)
		}
		got = append(got, pair{ty.String, val.String})
	}
	wantTypes := []string{"integer", "text", "null", "real", "integer"}
	if len(got) != len(wantTypes) {
		t.Fatalf("got %d rows, want %d", len(got), len(wantTypes))
	}
	for i, w := range wantTypes {
		if got[i].ty != w {
			t.Errorf("row[%d] type=%q, want %q", i, got[i].ty, w)
		}
	}
}

// TestArray_TransparentPointerNilSlice pins behavior for a wrapped nil
// slice — should produce zero rows, not error.
func TestArray_TransparentPointerNilSlice(t *testing.T) {
	_, sc, _ := openDB(t)
	rows, err := sc.QueryContext(context.Background(),
		`SELECT value FROM array(?)`, sqlite.Pointer([]int(nil)))
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Error("expected zero rows from nil slice")
	}
}

func TestArray_ErrUnknownTokenSentinel(t *testing.T) {
	// errors.Is should match against the package-level sentinel so callers
	// can branch on the failure mode.
	_, sc, _ := openDB(t)
	_, err := sc.QueryContext(context.Background(),
		`SELECT value FROM array(?)`, int64(42424242))
	// The error wrapping goes through the SQLite C side, which doesn't
	// preserve Go's errors.Is chain. We only pin that the message
	// surfaces the sentinel's text.
	if err == nil || !strings.Contains(err.Error(), array.ErrUnknownToken.Error()) {
		t.Errorf("got %v, want error containing %q", err, array.ErrUnknownToken)
	}
}

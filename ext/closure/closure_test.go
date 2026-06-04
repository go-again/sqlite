package closure_test

import (
	"context"
	"database/sql"
	"slices"
	"testing"

	"github.com/go-again/sqlite/ext/closure"
	"github.com/go-again/sqlite/internal/testhelp"
)

func openDB(t *testing.T) (*sql.DB, *sql.Conn) {
	t.Helper()
	testhelp.WithConnectHook(t, closure.Register)
	db, sc := testhelp.OpenPinned(t, "sqlite", ":memory:")

	ctx := context.Background()
	if _, err := sc.ExecContext(ctx,
		`CREATE TABLE org(id INTEGER PRIMARY KEY, manager INTEGER, name TEXT)`); err != nil {
		t.Fatal(err)
	}
	//   1 (CEO)
	//   ├── 2 (VP-Eng)
	//   │   ├── 4 (Dir-Plat)
	//   │   │   └── 6 (Eng-Plat)
	//   │   └── 5 (Dir-App)
	//   │       └── 7 (Eng-App)
	//   └── 3 (VP-Sales)
	for _, r := range [][2]any{
		{1, nil},
		{2, 1},
		{3, 1},
		{4, 2},
		{5, 2},
		{6, 4},
		{7, 5},
	} {
		if _, err := sc.ExecContext(ctx, `INSERT INTO org(id, manager) VALUES (?, ?)`, r[0], r[1]); err != nil {
			t.Fatal(err)
		}
	}
	return db, sc
}

func collect(t *testing.T, sc *sql.Conn, query string, args ...any) (ids []int64, depths []int) {
	t.Helper()
	rows, err := sc.QueryContext(context.Background(), query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var depth int
		if err := rows.Scan(&id, &depth); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		depths = append(depths, depth)
	}
	return ids, depths
}

func TestClosure_AllDescendants(t *testing.T) {
	_, sc := openDB(t)
	ctx := context.Background()
	if _, err := sc.ExecContext(ctx,
		`CREATE VIRTUAL TABLE temp.tc USING transitive_closure(
			tablename=org, idcolumn=id, parentcolumn=manager)`); err != nil {
		t.Fatal(err)
	}
	ids, _ := collect(t, sc, `SELECT id, depth FROM temp.tc WHERE root = 1 ORDER BY id`)
	slices.Sort(ids)
	want := []int64{1, 2, 3, 4, 5, 6, 7}
	if len(ids) != len(want) {
		t.Errorf("ids=%v, want %v", ids, want)
	}
}

func TestClosure_DepthBound(t *testing.T) {
	_, sc := openDB(t)
	ctx := context.Background()
	if _, err := sc.ExecContext(ctx,
		`CREATE VIRTUAL TABLE temp.tc USING transitive_closure(
			tablename=org, idcolumn=id, parentcolumn=manager)`); err != nil {
		t.Fatal(err)
	}
	// depth <= 1 from root=2 should give 2 (self), 4, 5.
	ids, depths := collect(t, sc,
		`SELECT id, depth FROM temp.tc WHERE root = 2 AND depth <= 1 ORDER BY id`)
	slices.Sort(ids)
	want := []int64{2, 4, 5}
	if len(ids) != len(want) {
		t.Errorf("ids=%v, want %v (depths=%v)", ids, want, depths)
	}
}

func TestClosure_DepthFromRoot(t *testing.T) {
	_, sc := openDB(t)
	ctx := context.Background()
	if _, err := sc.ExecContext(ctx,
		`CREATE VIRTUAL TABLE temp.tc USING transitive_closure(
			tablename=org, idcolumn=id, parentcolumn=manager)`); err != nil {
		t.Fatal(err)
	}
	// From root=1: 1 has depth 0, 2,3 depth 1, 4,5 depth 2, 6,7 depth 3.
	rows, err := sc.QueryContext(context.Background(),
		`SELECT id, depth FROM temp.tc WHERE root = 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[int64]int{}
	for rows.Next() {
		var id int64
		var d int
		if err := rows.Scan(&id, &d); err != nil {
			t.Fatal(err)
		}
		got[id] = d
	}
	expect := map[int64]int{1: 0, 2: 1, 3: 1, 4: 2, 5: 2, 6: 3, 7: 3}
	for k, v := range expect {
		if got[k] != v {
			t.Errorf("depth[%d]=%d, want %d", k, got[k], v)
		}
	}
}

func TestClosure_RequiresRoot(t *testing.T) {
	_, sc := openDB(t)
	if _, err := sc.ExecContext(context.Background(),
		`CREATE VIRTUAL TABLE temp.tc USING transitive_closure(
			tablename=org, idcolumn=id, parentcolumn=manager)`); err != nil {
		t.Fatal(err)
	}
	if _, err := sc.QueryContext(context.Background(), `SELECT * FROM temp.tc`); err == nil {
		t.Error("missing root: want error, got nil")
	}
}

func TestClosure_QueryTimeOverride(t *testing.T) {
	_, sc := openDB(t)
	if _, err := sc.ExecContext(context.Background(),
		`CREATE VIRTUAL TABLE temp.tc USING transitive_closure()`); err != nil {
		t.Fatal(err)
	}
	rows, err := sc.QueryContext(context.Background(),
		`SELECT id, depth FROM temp.tc
		 WHERE root = 1 AND tablename = 'org' AND idcolumn = 'id' AND parentcolumn = 'manager'
		 ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
		var id int64
		var d int
		rows.Scan(&id, &d)
	}
	if count != 7 {
		t.Errorf("got %d rows, want 7", count)
	}
}

// TestClosure_HandlesCycles pins the visited-set semantics promised in
// the docstring. A graph where A reports to B and B reports to A would
// loop forever without the visited-set; cursor must terminate and
// return both nodes exactly once.
func TestClosure_HandlesCycles(t *testing.T) {
	testhelp.WithConnectHook(t, closure.Register)
	_, sc := testhelp.OpenPinned(t, "sqlite", ":memory:")

	ctx := context.Background()
	if _, err := sc.ExecContext(ctx,
		`CREATE TABLE cyc(id INTEGER PRIMARY KEY, parent INTEGER)`); err != nil {
		t.Fatal(err)
	}
	// 1 → 2 → 3 → 1 cycle. Plus 4 unrelated.
	for _, r := range [][2]any{
		{1, 3}, {2, 1}, {3, 2}, {4, nil},
	} {
		if _, err := sc.ExecContext(ctx,
			`INSERT INTO cyc(id, parent) VALUES (?, ?)`, r[0], r[1]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := sc.ExecContext(ctx,
		`CREATE VIRTUAL TABLE temp.tc USING transitive_closure(
			tablename=cyc, idcolumn=id, parentcolumn=parent)`); err != nil {
		t.Fatal(err)
	}
	rows, err := sc.QueryContext(ctx, `SELECT id FROM temp.tc WHERE root = 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[int64]int{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		seen[id]++
	}
	// Every node in the cycle should be visited exactly once; node 4
	// is not in the descendant set.
	for _, want := range []int64{1, 2, 3} {
		if seen[want] != 1 {
			t.Errorf("node %d: visited %d times, want 1 (cycle handling)", want, seen[want])
		}
	}
	if seen[4] != 0 {
		t.Errorf("node 4 should not appear; got %d visits", seen[4])
	}
}

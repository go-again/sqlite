package closure_test

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"testing"

	"github.com/go-again/sqlite/ext/closure"
	"github.com/go-again/sqlite/internal/testhelp"
)

// openGraphDB returns a *sql.DB with the transitive_closure module on
// every connection (via ConnectHook), pinned to one conn, seeded with a
// small org tree in org(id, manager). The typed Graph runs over the
// *sql.DB directly, so the module must be pool-wide.
//
//	1 (CEO) ── 2 (VP-Eng) ── 4 ── 6
//	│          └─────────── 5 ── 7
//	└───────── 3 (VP-Sales)
func openGraphDB(t *testing.T) *sql.DB {
	t.Helper()
	testhelp.WithConnectHook(t, closure.Register)
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	if _, err := db.ExecContext(ctx,
		`CREATE TABLE org(id INTEGER PRIMARY KEY, manager INTEGER, name TEXT)`); err != nil {
		t.Fatalf("create org: %v", err)
	}
	for _, r := range [][2]any{{1, nil}, {2, 1}, {3, 1}, {4, 2}, {5, 2}, {6, 4}, {7, 5}} {
		if _, err := db.ExecContext(ctx, `INSERT INTO org(id, manager) VALUES (?, ?)`, r[0], r[1]); err != nil {
			t.Fatalf("seed org: %v", err)
		}
	}
	return db
}

func nodeIDs(ns []closure.Node) []int64 {
	out := make([]int64, len(ns))
	for i, n := range ns {
		out[i] = n.ID
	}
	slices.Sort(out)
	return out
}

var orgEdge = closure.Over{Table: "org", IDColumn: "id", ParentColumn: "manager"}

func TestGraph_Descendants(t *testing.T) {
	ctx := context.Background()
	db := openGraphDB(t)
	g, err := closure.Create(ctx, db, "tc", orgEdge)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := g.Descendants(ctx, 2)
	if err != nil {
		t.Fatalf("Descendants: %v", err)
	}
	if want := []int64{2, 4, 5, 6, 7}; !slices.Equal(nodeIDs(got), want) {
		t.Errorf("Descendants(2) = %v, want %v (VP-Eng + all reports, incl. root)", nodeIDs(got), want)
	}
}

func TestGraph_WithMaxDepth(t *testing.T) {
	ctx := context.Background()
	db := openGraphDB(t)
	g, _ := closure.Create(ctx, db, "tc", orgEdge)
	got, err := g.Descendants(ctx, 1, closure.WithMaxDepth(1))
	if err != nil {
		t.Fatalf("Descendants: %v", err)
	}
	if want := []int64{1, 2, 3}; !slices.Equal(nodeIDs(got), want) {
		t.Errorf("Descendants(1, MaxDepth 1) = %v, want %v (CEO + direct reports)", nodeIDs(got), want)
	}
}

func TestGraph_WithExactDepth(t *testing.T) {
	ctx := context.Background()
	db := openGraphDB(t)
	g, _ := closure.Create(ctx, db, "tc", orgEdge)
	got, err := g.Descendants(ctx, 1, closure.WithExactDepth(2))
	if err != nil {
		t.Fatalf("Descendants: %v", err)
	}
	if want := []int64{4, 5}; !slices.Equal(nodeIDs(got), want) {
		t.Errorf("Descendants(1, ExactDepth 2) = %v, want %v", nodeIDs(got), want)
	}
}

func TestGraph_Reversed_Ancestors(t *testing.T) {
	ctx := context.Background()
	db := openGraphDB(t)
	// Reversed edge walks id→manager, so Descendants(6) yields 6's ancestor chain.
	g, err := closure.Create(ctx, db, "anc", closure.Reversed(orgEdge))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := g.Descendants(ctx, 6)
	if err != nil {
		t.Fatalf("Descendants: %v", err)
	}
	if want := []int64{1, 2, 4, 6}; !slices.Equal(nodeIDs(got), want) {
		t.Errorf("ancestors of 6 = %v, want %v (6→4→2→1)", nodeIDs(got), want)
	}
}

func TestGraph_OverGraph_PerQueryRetarget(t *testing.T) {
	ctx := context.Background()
	db := openGraphDB(t)
	// Created with no binding; supply the graph per-query.
	g, err := closure.Create(ctx, db, "tc", closure.Over{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := g.Descendants(ctx, 2, closure.OverGraph("org", "id", "manager"))
	if err != nil {
		t.Fatalf("Descendants: %v", err)
	}
	if want := []int64{2, 4, 5, 6, 7}; !slices.Equal(nodeIDs(got), want) {
		t.Errorf("Descendants(2, OverGraph) = %v, want %v", nodeIDs(got), want)
	}
}

func TestGraph_DescendantsSQL_BindArgs(t *testing.T) {
	ctx := context.Background()
	db := openGraphDB(t)
	g, _ := closure.Create(ctx, db, "tc", orgEdge)
	q, args, err := g.DescendantsSQL(2, closure.WithMaxDepth(3))
	if err != nil {
		t.Fatalf("DescendantsSQL: %v", err)
	}
	if len(args) != 2 || args[0] != int64(2) || args[1] != 3 {
		t.Fatalf("args = %v, want [2 3]", args)
	}
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		t.Fatalf("QueryContext(%q): %v", q, err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Error("expected rows from DescendantsSQL output")
	}
}

func TestGraph_Create_WithIfNotExists_Idempotent(t *testing.T) {
	ctx := context.Background()
	db := openGraphDB(t)
	if _, err := closure.Create(ctx, db, "tc", orgEdge, closure.WithIfNotExists()); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := closure.Create(ctx, db, "tc", orgEdge, closure.WithIfNotExists()); err != nil {
		t.Fatalf("second Create with WithIfNotExists: %v", err)
	}
}

func TestGraph_Create_ErrAlreadyExists(t *testing.T) {
	ctx := context.Background()
	db := openGraphDB(t)
	if _, err := closure.Create(ctx, db, "tc", orgEdge); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := closure.Create(ctx, db, "tc", orgEdge); !errors.Is(err, closure.ErrAlreadyExists) {
		t.Errorf("second Create error = %v, want ErrAlreadyExists", err)
	}
}

func TestGraph_Drop(t *testing.T) {
	ctx := context.Background()
	db := openGraphDB(t)
	g, err := closure.Create(ctx, db, "tc", orgEdge)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := g.Drop(ctx); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	if _, err := closure.Create(ctx, db, "tc", orgEdge); err != nil {
		t.Errorf("Create after Drop: %v", err)
	}
}

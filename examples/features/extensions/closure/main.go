// ext-closure: parent-child graph walks with the typed closure.Graph API.
// Descendants of a node, depth bounds, and ancestor walks via the reversed
// edge — no hand-written transitive_closure SQL. Graph mirrors vec.Table /
// fts.Index / spellfix1.Vocab.
//
// Run with:
//
//	just example closure
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	_ "gosqlite.org"
	"gosqlite.org/ext/closure"
	_ "gosqlite.org/ext/closure/auto" // registers the vtab module on every conn
)

func main() {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		log.Fatalf("open: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx := context.Background()

	//   1 CEO ── 2 VP-Eng ── 4 Dir-Plat ── 6 Eng-Plat
	//         │           └─ 5 Dir-App  ── 7 Eng-App
	//         └─ 3 VP-Sales
	if _, err := db.ExecContext(ctx,
		`CREATE TABLE org(id INTEGER PRIMARY KEY, manager INTEGER, name TEXT)`); err != nil {
		log.Fatalf("create org: %v", err)
	}
	name := map[int64]string{}
	for _, r := range []struct {
		id      int64
		manager any
		name    string
	}{
		{1, nil, "CEO"}, {2, 1, "VP-Eng"}, {3, 1, "VP-Sales"},
		{4, 2, "Dir-Plat"}, {5, 2, "Dir-App"}, {6, 4, "Eng-Plat"}, {7, 5, "Eng-App"},
	} {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO org(id, manager, name) VALUES (?, ?, ?)`, r.id, r.manager, r.name); err != nil {
			log.Fatalf("seed: %v", err)
		}
		name[r.id] = r.name
	}

	edge := closure.Over{Table: "org", IDColumn: "id", ParentColumn: "manager"}

	// Typed CREATE VIRTUAL TABLE … USING transitive_closure(...).
	g, err := closure.Create(ctx, db, "tc", edge, closure.WithIfNotExists())
	if err != nil {
		log.Fatalf("create graph: %v", err)
	}

	// Everyone under VP-Eng (id=2), including VP-Eng at depth 0.
	fmt.Println("Reports of VP-Eng (id=2):")
	reports, _ := g.Descendants(ctx, 2)
	for _, n := range reports {
		fmt.Printf("  d=%d  %d %s\n", n.Depth, n.ID, name[n.ID])
	}

	// Direct reports of the CEO (depth <= 1, skipping the CEO itself).
	fmt.Println("\nDirect reports of CEO (id=1, depth <= 1):")
	direct, _ := g.Descendants(ctx, 1, closure.WithMaxDepth(1))
	for _, n := range direct {
		if n.Depth == 0 {
			continue
		}
		fmt.Printf("  %d %s\n", n.ID, name[n.ID])
	}

	// Management chain above an engineer, via the reversed edge.
	fmt.Println("\nManagement chain above Eng-Plat (id=6):")
	up, err := closure.Create(ctx, db, "up", closure.Reversed(edge))
	if err != nil {
		log.Fatalf("create reversed graph: %v", err)
	}
	for _, n := range mustWalk(up.Descendants(ctx, 6)) {
		fmt.Printf("  d=%d  %d %s\n", n.Depth, n.ID, name[n.ID])
	}
}

func mustWalk(ns []closure.Node, err error) []closure.Node {
	if err != nil {
		log.Fatalf("walk: %v", err)
	}
	return ns
}

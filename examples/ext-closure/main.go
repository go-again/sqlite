// ext-closure: walk every descendant of a node in a parent-child
// graph via the transitive_closure vtab. Demonstrates the canonical
// org-chart pattern: "give me the entire reporting tree under person X."
//
// Run with:
//
//	just example ext-closure
package main

import (
	"context"
	"fmt"
	"log"

	sqlite "github.com/go-again/sqlite"
	_ "github.com/go-again/sqlite/ext/closure/auto"
)

func main() {
	db, err := sqlite.Open(sqlite.Config{Path: ":memory:"})
	if err != nil {
		log.Fatalf("Open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	sc, err := db.Conn(ctx)
	if err != nil {
		log.Fatalf("Conn: %v", err)
	}
	defer sc.Close()

	// A simple org chart:
	//   1 CEO  ←  2 VP-Eng  ←  4 Dir-Plat  ←  6 Eng-Plat
	//                       ←  5 Dir-App   ←  7 Eng-App
	//        ←  3 VP-Sales
	if _, err := sc.ExecContext(ctx,
		`CREATE TABLE org(id INTEGER PRIMARY KEY, manager INTEGER, name TEXT)`); err != nil {
		log.Fatalf("CREATE: %v", err)
	}
	rows := [][]any{
		{1, nil, "CEO"},
		{2, 1, "VP-Eng"},
		{3, 1, "VP-Sales"},
		{4, 2, "Dir-Plat"},
		{5, 2, "Dir-App"},
		{6, 4, "Eng-Plat"},
		{7, 5, "Eng-App"},
	}
	for _, r := range rows {
		if _, err := sc.ExecContext(ctx,
			`INSERT INTO org(id, manager, name) VALUES (?, ?, ?)`, r...); err != nil {
			log.Fatalf("INSERT: %v", err)
		}
	}

	if _, err := sc.ExecContext(ctx,
		`CREATE VIRTUAL TABLE temp.tc USING transitive_closure(
		    tablename=org, idcolumn=id, parentcolumn=manager)`); err != nil {
		log.Fatalf("CREATE VTAB: %v", err)
	}

	// Everyone under the VP-Eng (id=2).
	fmt.Println("Reports of VP-Eng (id=2):")
	fmt.Println("depth | id | name")
	fmt.Println("------+----+----------")
	r2, err := sc.QueryContext(ctx, `
		SELECT temp.tc.depth, org.id, org.name
		FROM temp.tc JOIN org ON org.id = temp.tc.id
		WHERE temp.tc.root = 2
		ORDER BY temp.tc.depth, org.id`)
	if err != nil {
		log.Fatalf("Query: %v", err)
	}
	defer r2.Close()
	for r2.Next() {
		var depth int
		var id int64
		var name string
		_ = r2.Scan(&depth, &id, &name)
		fmt.Printf("%-5d | %-2d | %s\n", depth, id, name)
	}

	// Same query, with depth limit.
	fmt.Println()
	fmt.Println("Direct reports of CEO (depth <= 1):")
	r3, err := sc.QueryContext(ctx, `
		SELECT temp.tc.depth, org.id, org.name
		FROM temp.tc JOIN org ON org.id = temp.tc.id
		WHERE temp.tc.root = 1 AND temp.tc.depth <= 1
		ORDER BY temp.tc.depth, org.id`)
	if err != nil {
		log.Fatalf("Query: %v", err)
	}
	defer r3.Close()
	for r3.Next() {
		var depth int
		var id int64
		var name string
		_ = r3.Scan(&depth, &id, &name)
		fmt.Printf("  d=%d %d %s\n", depth, id, name)
	}
}

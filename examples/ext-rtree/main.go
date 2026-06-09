// ext-rtree: spatial indexing with the typed rtree.Table API. Create hides the
// `CREATE VIRTUAL TABLE … USING rtree(…)` column list; InsertPoint / Search /
// SearchCircle drive the index without hand-written SQL. Table mirrors
// vec.Table / fts.Index / csv.Table.
//
// The rtree vtab is built into the library, so create/insert/bounding-box
// Search need no registration. SearchCircle uses the circle geometry, which is
// wired onto every pooled connection by the blank-import below.
//
// Run with:
//
//	just example ext-rtree
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	_ "github.com/go-again/sqlite"                // registers the "sqlite" driver
	"github.com/go-again/sqlite/ext/rtree"        // typed Table API
	_ "github.com/go-again/sqlite/ext/rtree/auto" // circle geometry on every conn
)

func main() {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1) // pin so the geometry registered by the hook is on our conn
	ctx := context.Background()

	// Typed CREATE VIRTUAL TABLE venues USING rtree(id, min0, max0, min1, max1).
	venues, err := rtree.Create(ctx, db, "venues") // 2D by default
	if err != nil {
		log.Fatalf("create: %v", err)
	}

	name := map[int64]string{}
	for _, v := range []struct {
		id   int64
		x, y float64
		name string
	}{
		{1, 1, 1, "Library"},
		{2, 2, 5, "Cafe"},
		{3, 5, 5, "Gym"},
		{4, 8, 1, "Dorm"},
		{5, 5, 9, "Lab"},
	} {
		// Each venue is a point — InsertPoint stores a zero-size box.
		if err := venues.InsertPoint(ctx, v.id, v.x, v.y); err != nil {
			log.Fatalf("insert %s: %v", v.name, err)
		}
		name[v.id] = v.name
	}

	// Bounding-box query: venues in the rectangle x∈[0,3], y∈[0,6].
	fmt.Println("Venues in box x∈[0,3], y∈[0,6]:")
	for _, id := range mustIDs(venues.Search(ctx, 0, 3, 0, 6)) {
		fmt.Printf("  %d %s\n", id, name[id])
	}

	// Radius query via the circle geometry: venues within 4.5 of the Gym.
	fmt.Println("\nVenues within 4.5 of the Gym (5,5):")
	for _, id := range mustIDs(venues.SearchCircle(ctx, 5, 5, 4.5)) {
		fmt.Printf("  %d %s\n", id, name[id])
	}
}

func mustIDs(ids []int64, err error) []int64 {
	if err != nil {
		log.Fatalf("query: %v", err)
	}
	return ids
}

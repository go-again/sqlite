// ext-zorder: 2D / 3D Morton encoding for spatial-ish indexing.
// Run with: just example zorder
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	_ "gosqlite.org"
	_ "gosqlite.org/ext/zorder/auto"
)

func main() {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	fmt.Println("2D round-trip:")
	for _, xy := range [][2]int64{{10, 20}, {100, 200}, {500, 600}, {1024, 2048}} {
		var z, x, y int64
		_ = db.QueryRowContext(ctx,
			`SELECT zorder(?, ?), unzorder(zorder(?, ?), 2, 0), unzorder(zorder(?, ?), 2, 1)`,
			xy[0], xy[1], xy[0], xy[1], xy[0], xy[1]).Scan(&z, &x, &y)
		fmt.Printf("  (%4d, %4d) → z=%d → (%d, %d)\n", xy[0], xy[1], z, x, y)
	}

	fmt.Println("\nLocality property (adjacent coords → close z-values):")
	for _, x := range []int64{100, 101, 102, 103} {
		var z int64
		_ = db.QueryRowContext(ctx, `SELECT zorder(?, 200)`, x).Scan(&z)
		fmt.Printf("  zorder(%d, 200) = %d\n", x, z)
	}
}

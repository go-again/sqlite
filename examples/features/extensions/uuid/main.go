// ext-uuid: RFC 4122 UUID functions. Run with: just example uuid
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	_ "github.com/go-again/sqlite"
	_ "github.com/go-again/sqlite/ext/uuid/auto"
)

func main() {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	for _, ver := range []int64{1, 4, 6, 7} {
		var u string
		_ = db.QueryRowContext(ctx, `SELECT uuid(?)`, ver).Scan(&u)
		var got int64
		_ = db.QueryRowContext(ctx, `SELECT uuid_extract_version(?)`, u).Scan(&got)
		fmt.Printf("uuid(%d) → %s  (version=%d)\n", ver, u, got)
	}

	// Name-based v5 over the DNS namespace is deterministic.
	var v5 string
	_ = db.QueryRowContext(ctx, `SELECT uuid(5, 'dns', 'example.com')`).Scan(&v5)
	fmt.Printf("\nuuid(5, 'dns', 'example.com') → %s (deterministic)\n", v5)

	// Parse + reformat — TEXT in, BLOB out, TEXT back.
	var blob []byte
	_ = db.QueryRowContext(ctx, `SELECT uuid_blob(?)`, v5).Scan(&blob)
	fmt.Printf("uuid_blob: %d bytes\n", len(blob))

	var roundTrip string
	_ = db.QueryRowContext(ctx, `SELECT uuid_str(?)`, blob).Scan(&roundTrip)
	fmt.Printf("uuid_str(blob) → %s\n", roundTrip)
}

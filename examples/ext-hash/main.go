// ext-hash: cryptographic hash SQL functions. Run with: just example ext-hash
package main

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"

	_ "github.com/go-again/sqlite"
	_ "github.com/go-again/sqlite/ext/hash/auto"
)

func main() {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	for _, fn := range []string{"md5", "sha1", "sha256", "sha512", "sha3", "blake2b", "blake3", "xxh64"} {
		var b []byte
		if err := db.QueryRowContext(ctx,
			fmt.Sprintf(`SELECT %s('hello world')`, fn)).Scan(&b); err != nil {
			log.Fatalf("%s: %v", fn, err)
		}
		fmt.Printf("%-8s (%2d bytes) %s\n", fn, len(b), hex.EncodeToString(b))
	}

	// Size variants.
	for _, size := range []int64{224, 256, 384, 512} {
		var b []byte
		_ = db.QueryRowContext(ctx, `SELECT sha3('hello world', ?)`, size).Scan(&b)
		fmt.Printf("sha3-%d  (%2d bytes) %s\n", size, len(b), hex.EncodeToString(b))
	}
}

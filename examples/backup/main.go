// backup: demonstrate (*Conn).Backup (the mattn-compat factory), the
// top-level sqlite.Serialize / sqlite.Deserialize helpers, and the
// round-trip path from a live in-memory DB to a snapshot to a fresh
// in-memory DB.
//
// Run with:
//
//	just example backup
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"

	sqlite "github.com/go-again/sqlite"
)

func main() {
	tmp, err := os.MkdirTemp("", "ga-backup-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(tmp)

	ctx := context.Background()

	// 1. Source DB with some content.
	src, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		log.Fatal(err)
	}
	defer src.Close()
	src.SetMaxOpenConns(1)
	if _, err := src.ExecContext(ctx, `CREATE TABLE t(id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		log.Fatal(err)
	}
	for _, v := range []string{"alpha", "beta", "gamma"} {
		if _, err := src.ExecContext(ctx, `INSERT INTO t(v) VALUES (?)`, v); err != nil {
			log.Fatal(err)
		}
	}

	// 2. (*Conn).Backup: copy source DB into a fresh on-disk DB.
	dstPath := filepath.Join(tmp, "snapshot.db")
	dst, err := sql.Open("sqlite", dstPath)
	if err != nil {
		log.Fatal(err)
	}
	defer dst.Close()
	dst.SetMaxOpenConns(1)

	func() {
		dstC, err := dst.Conn(ctx)
		if err != nil {
			log.Fatal(err)
		}
		defer dstC.Close()
		srcC, err := src.Conn(ctx)
		if err != nil {
			log.Fatal(err)
		}
		defer srcC.Close()

		if err := dstC.Raw(func(dRaw any) error {
			dstConn := dRaw.(*sqlite.Conn)
			return srcC.Raw(func(sRaw any) error {
				srcConn := sRaw.(*sqlite.Conn)
				bk, err := dstConn.Backup("main", srcConn, "main")
				if err != nil {
					return err
				}
				for {
					done, err := bk.Step(100)
					if err != nil {
						return err
					}
					if !done {
						break
					}
				}
				return bk.Finish()
			})
		}); err != nil {
			log.Fatalf("backup: %v", err)
		}
	}()
	fmt.Printf("backup written to %s\n", dstPath)

	// 3. sqlite.Serialize / Deserialize round-trip via a fresh in-memory DB.
	dump, err := sqlite.Serialize(ctx, src)
	if err != nil {
		log.Fatalf("Serialize: %v", err)
	}
	fmt.Printf("serialized snapshot: %d bytes\n", len(dump))

	restored, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		log.Fatal(err)
	}
	defer restored.Close()
	restored.SetMaxOpenConns(1)
	if err := sqlite.Deserialize(ctx, restored, dump); err != nil {
		log.Fatalf("Deserialize: %v", err)
	}
	var count int
	if err := restored.QueryRowContext(ctx, `SELECT COUNT(*) FROM t`).Scan(&count); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("restored DB sees %d rows\n", count)
}

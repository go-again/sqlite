// ext-lines: read a text file as one-row-per-line via the typed
// lines.Table API. lines.Create hides the `USING lines(…)` argument string
// and its quoting; the rows (lineno, line) are queried as SQL, so log
// filtering and aggregation compose normally. Table mirrors vec.Table /
// fts.Index / csv.Table.
//
// Run with:
//
//	just example lines
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"testing/fstest"

	sqlite "gosqlite.org"
	"gosqlite.org/ext/lines"
)

func main() {
	// Bundle a small log fixture in an in-memory fs.FS so the example runs
	// without touching disk. For real workloads, swap in embed.FS,
	// os.DirFS(prefix), or blank-import ext/lines/auto for os.Open access.
	fsys := fstest.MapFS{
		"app.log": {Data: []byte(`INFO  server started
WARN  cache miss for key=42
ERROR connect failed: timeout
INFO  request handled in 12ms
ERROR connect failed: refused
WARN  retry budget low
INFO  shutdown
`)},
	}

	// The typed lines.Table API runs over a *sql.DB, so the lines module
	// must be on every pooled connection. Wire RegisterFS through a
	// connection hook (the auto sub-package registers os-backed access).
	sqlite.DefaultDriver().RegisterConnectionHook(func(c sqlite.ExecQuerierContext, _ string) error {
		return lines.RegisterFS(c.(*sqlite.Conn), fsys)
	})

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	// CREATE VIRTUAL TABLE … USING lines(filename=…), typed.
	logt, err := lines.Create(ctx, db, "applog", lines.WithFilename("app.log"))
	if err != nil {
		log.Fatalf("create applog: %v", err)
	}

	cols, _ := logt.Columns(ctx)
	fmt.Printf("columns: %v\n", cols)

	// Just the ERROR lines, with their line numbers — plain SQL over the
	// vtab, the whole reason to expose a log file as a table.
	fmt.Println("\nERROR lines:")
	rows, _ := db.QueryContext(ctx,
		"SELECT lineno, line FROM "+logt.Name()+" WHERE line LIKE 'ERROR%' ORDER BY lineno")
	for rows.Next() {
		var n int64
		var line string
		_ = rows.Scan(&n, &line)
		fmt.Printf("  %d: %s\n", n, line)
	}
	rows.Close()

	// Count lines by severity (first whitespace-delimited token).
	fmt.Println("\nLines by severity:")
	rows, _ = db.QueryContext(ctx, `
		SELECT substr(line, 1, instr(line || ' ', ' ') - 1) AS level, COUNT(*) AS n
		FROM `+logt.Name()+`
		GROUP BY level ORDER BY n DESC, level`)
	for rows.Next() {
		var level string
		var n int
		_ = rows.Scan(&level, &n)
		fmt.Printf("  %-5s %d\n", level, n)
	}
	rows.Close()
}

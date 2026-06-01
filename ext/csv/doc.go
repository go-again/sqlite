// Package csv exposes RFC 4180 comma-separated values as a SQLite virtual
// table. Read a CSV file as if it were a regular SQL table — JOINs, WHERE
// clauses, aggregates all compose normally.
//
// # Usage
//
//	import (
//	    sqlite "github.com/go-again/sqlite"
//	    "github.com/go-again/sqlite/ext/csv"
//	)
//
//	if err := csv.Register(conn); err != nil { ... }
//
//	// Then in SQL:
//	//   CREATE VIRTUAL TABLE temp.sales USING csv(filename='sales.csv', header=on);
//	//   SELECT region, SUM(amount) FROM temp.sales GROUP BY region;
//
// The vtab needs to be created on the same connection that will query it
// (SQLite virtual tables are per-connection). Either pin a `*sql.Conn`,
// open with `MaxOpenConns=1`, or install via a `Driver.ConnectHook`.
//
// # Module parameters
//
// `CREATE VIRTUAL TABLE name USING csv(named-args)` accepts:
//
//   - filename='path.csv' — file path; opened via the fs.FS handed to
//     [RegisterFS] (defaults to os-backed access via [Register]).
//   - data='inline CSV content' — embed CSV literally in the CREATE.
//     Mutually exclusive with filename.
//   - schema='CREATE TABLE x(a INTEGER, b TEXT, ...)' — column names +
//     affinity hints. Without schema, the vtab declares all columns as
//     TEXT and derives names from the header row (or c1, c2, c3, ...).
//   - header=on — treat the first row as the column-name header. Default
//     off.
//   - columns=N — force a fixed column count; truncates wider rows,
//     pads narrower with NULLs. Default: count from the header (or
//     first data row).
//   - comma=',' — column separator; any single-rune literal. Default ','.
//   - comment='#' — lines starting with this rune are skipped. Default
//     unset.
//
// # Type affinity
//
// When `schema` is provided, the vtab scans each column declaration for
// the keywords INTEGER / INT / REAL / FLOAT / DOUBLE / NUMERIC. Matching
// columns return INTEGER or REAL when their CSV string parses; otherwise
// the value is returned as TEXT (best-effort coercion, matches the
// SQLite-bundled `csv.c` semantics). All other columns return TEXT.
//
// Empty CSV cells in non-TEXT columns return SQL NULL.
//
// # Sandboxed filesystems
//
// Use [RegisterFS] to scope file access to a specific [io/fs.FS]:
//
//   - `embed.FS` — bundle CSV fixtures inside a binary
//   - `fstest.MapFS` — in-memory CSV for tests
//   - `os.DirFS(prefix)` — sandbox to a directory
//
// # Blank-import auto-registration
//
//	import _ "github.com/go-again/sqlite/ext/csv/auto"
//
// Auto-registration uses [Register] (os-backed file access). For
// sandboxed deployments, call [RegisterFS] from your own ConnectHook.
//
// # Acknowledgement
//
// Ported from [ncruces/ext/csv]. Function lineup and named-arg shape
// match upstream; the type-affinity parser is intentionally simpler
// (keyword scan, not a full DDL parser).
//
// [ncruces/ext/csv]: https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/csv
package csv

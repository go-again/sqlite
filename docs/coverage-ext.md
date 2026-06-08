# Coverage — ext/ loadable extensions

Tracks every loadable extension under [`ext/`](../ext/) — Go-native ports of selected ncruces/go-sqlite3 extensions and our own additions. Each row records the upstream reference, ported entry, registration shape, and the test pin.

Status legend:

- ✓ landed — code + tests + docs shipped
- ⚠ partial — code shipped but coverage incomplete, or one feature gated
- ✗ deferred — analyzed and chosen for a later round; reference only
- ✗ skipped — analyzed and intentionally dropped (overlap with existing surface, or upstream-only assumption that does not apply here)

## Vtab-based

| ext | LoC (est) | Upstream | Status | Entry | Test pin |
|---|---|---|---|---|---|
| array | ~250 | [ncruces/ext/array](https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/array) | ✓ landed | `ext/array` + `ext/array/auto` | `ext/array/array_test.go` |
| blobio | ~250 | [ncruces/ext/blobio](https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/blobio) | ✓ landed | `ext/blobio` + `ext/blobio/auto` | `ext/blobio/blobio_test.go` |
| bloom | ~560 | [ncruces/ext/bloom](https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/bloom) | ✓ landed (+ typed `Filter` API) | `ext/bloom` + `ext/bloom/auto` | `ext/bloom/bloom_test.go`, `filter_test.go` |
| closure | ~480 | [ncruces/ext/closure](https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/closure) | ✓ landed (+ typed `Graph` API) | `ext/closure` + `ext/closure/auto` | `ext/closure/closure_test.go`, `graph_test.go` |
| csv | ~600 | [ncruces/ext/csv](https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/csv) | ✓ landed (+ typed `Table` API) | `ext/csv` + `ext/csv/auto` | `ext/csv/csv_test.go`, `table_test.go` |
| fileio | ~330 | [ncruces/ext/fileio](https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/fileio) | ✓ landed | `ext/fileio` + `ext/fileio/auto` | `ext/fileio/fileio_test.go` |
| lines | ~370 | [ncruces/ext/lines](https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/lines) | ✓ landed (+ typed `Table` API) | `ext/lines` + `ext/lines/auto` | `ext/lines/lines_test.go`, `table_test.go` |
| pivot | ~340 | [ncruces/ext/pivot](https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/pivot) | ✓ landed | `ext/pivot` + `ext/pivot/auto` | `ext/pivot/pivot_test.go` |
| statement | ~240 | [ncruces/ext/statement](https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/statement) | ✓ landed | `ext/statement` + `ext/statement/auto` | `ext/statement/statement_test.go` |

`array` supports two binding styles: transparent via `sqlite.Pointer(slice)` (preferred — SQLite's destructor releases on stmt finalize, no caller cleanup needed) and explicit `array.Bind(c, slice) → token, release()` for long-lived bindings or int64-sentinel use cases.

`bloom` persists the bit array to a `<vtab>_storage` shadow table via the incremental BLOB API ([`(*Conn).OpenBlob`](../blob.go)). Filter state survives `db.Close()` / reconnect. Hashes use Kirsch–Mitzenmacher double-hashing over FNV-1a streams seeded with stable salts so bit positions match across process restarts.

`closure`, `pivot`, and `statement` are vtab modules that run nested SQL from inside `xCreate`/`xFilter` against the host `*Conn`. The reentrancy is pinned by `vtab_nested_prepare_test.go` at the root.

### xCreate / xConnect split

`bloom` and `spellfix1` use the [`(*Conn).CreateModuleSplit`](../module.go) two-callback form so they can run distinct logic for the first `CREATE VIRTUAL TABLE` (build the `<vtab>_storage` shadow table, seed the metadata row / index) and every subsequent `xConnect` reopen (declare the schema, fetch persisted params). Modules whose create and connect paths are identical should stick with the simpler `(*Conn).CreateModule`.

`fileio` exposes `readfile` / `writefile` / `lsmode` scalars plus the `fsdir` recursive-walk vtab. Use `fileio.Register(c)` for the os-backed mode (read+write of the local filesystem) or `fileio.RegisterFS(c, fs.FS)` for a sandboxed variant; the latter intentionally omits `writefile` since `fs.FS` is read-only.

`blobio` ships `readblob` / `writeblob` scalars over our incremental BLOB API. The openblob() callback form from upstream isn't ported; callers who want long-lived handles can use `(*Conn).OpenBlob` directly from Go.

`csv` adds a typed `csv.Table` handle (`Create` / `Open` / `Columns` / `Rows` / `Name` / `Drop`, with `WithFilename` / `WithData` / `WithHeader` / `WithComma` / `WithComment` / `WithColumns` / `WithIfNotExists`) that hides the `USING csv(…)` argument string and its single-quote escaping the way `sqlite.Open` hides a DSN. Rows are still queried as SQL — joining and filtering a CSV is the vtab's whole point — so the handle covers create/introspect/drop, not a query DSL. It requires the module pool-wide (blank-import `ext/csv/auto`, or `RegisterFS` from a `ConnectHook` for sandboxed file access).

`lines` mirrors the same typed `lines.Table` (`Create` / `Open` / `Columns` / `Rows` / `Name` / `Drop`, with `WithFilename` / `WithData` / `WithIfNotExists`) over the one-row-per-line vtab — `Create` hides the `USING lines(…)` argument string and its quoting, `Rows` returns `lineno, line` in order. Same pool-wide requirement (`ext/lines/auto`, or `RegisterFS` for a sandbox).

## Scalar UDFs (pure Go)

| ext | LoC (est) | Upstream | Status | Entry | Test pin |
|---|---|---|---|---|---|
| regexp | ~310 | [ncruces/ext/regexp](https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/regexp) | ✓ landed | `ext/regexp` + `ext/regexp/auto` | `ext/regexp/regexp_test.go` |
| uuid | ~330 | [ncruces/ext/uuid](https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/uuid) | ⚠ partial | `ext/uuid` + `ext/uuid/auto` | `ext/uuid/uuid_test.go` |
| hash | ~190 | [ncruces/ext/hash](https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/hash) | ✓ landed | `ext/hash` + `ext/hash/auto` | `ext/hash/hash_test.go` |
| ipaddr | ~150 | [ncruces/ext/ipaddr](https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/ipaddr) | ✓ landed | `ext/ipaddr` + `ext/ipaddr/auto` | `ext/ipaddr/ipaddr_test.go` |
| zorder | ~120 | [ncruces/ext/zorder](https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/zorder) | ✓ landed | `ext/zorder` + `ext/zorder/auto` | `ext/zorder/zorder_test.go` |

`uuid` is ⚠ partial because the DCE Security v2 variant is not yet implemented (rarely used; consumers can open an issue if they need it). v1 / v3 / v4 / v5 / v6 / v7 plus parsing, formatting, version extraction, and timestamp extraction are covered.

## Aggregates / windows (pure Go)

| ext | LoC (est) | Upstream | Status | Entry | Test pin |
|---|---|---|---|---|---|
| stats | ~960 | [ncruces/ext/stats](https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/stats) | ✓ landed | `ext/stats` + `ext/stats/auto` | `ext/stats/stats_test.go` |
| unicode | ~280 | [ncruces/ext/unicode](https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/unicode) | ⚠ partial | `ext/unicode` + `ext/unicode/auto` | `ext/unicode/unicode_test.go` |

`unicode` is ⚠ partial because the REGEXP override is intentionally not registered (would conflict with `ext/regexp`'s richer surface) and the `icu_load_collation` SQL-side helper isn't exposed (collations register through Go-side [unicode.RegisterLocaleCollation] instead). Auto-registration intentionally leaves SQLite's LIKE built-in alone to preserve the LIKE optimization; opt in via [unicode.RegisterLike] = true or [unicode.RegisterLikeOnly].

## Fuzzy text matching

| ext | LoC (est) | Upstream | Status | Entry | Test pin |
|---|---|---|---|---|---|
| spellfix1 | ~780 | [SQLite spellfix1](https://sqlite.org/spellfix1.html) | ✓ landed (Go-native re-implementation, + typed `Vocab` API) | `ext/spellfix1` + `ext/spellfix1/auto` | `ext/spellfix1/spellfix1_test.go`, `vocab_test.go` |

`spellfix1` is a Go-native re-implementation rather than a transpilation of the C `spellfix1.c` — same SQL surface (`CREATE VIRTUAL TABLE x USING spellfix1`, `INSERT INTO x(word [, rank])`, `SELECT word, distance FROM x WHERE word MATCH ?`), simpler internals. Uses Soundex for phonetic grouping and Damerau-Levenshtein with early-exit for distance ranking. Persists vocabulary in a `<vtab>_storage` shadow table (survives `db.Close()` / reconnect), now `UNIQUE` on `word` so inserts dedupe (`INSERT OR IGNORE`). A typed `spellfix1.Vocab` handle (`Create` / `Open` / `Add` / `AddMany` / `Size` / `Correct` / `CorrectSQL` / `Drop`, with `WithIfNotExists` / `WithMaxDistance` / `WithLimit`) mirrors `vec.Table` and `fts.Index` so callers can avoid hand-written SQL; it requires the module pool-wide (blank-import `ext/spellfix1/auto`). What's NOT ported: non-Latin transliteration (Cyrillic / Greek), the `editdist3` custom cost-matrix API, the Russian-language phonetic encoder. Users who need full upstream parity should wait for modernc to transpile spellfix1.c or open an issue.

## Skipped (overlap with existing surface)

| ext | Reason |
|---|---|
| serdes | Root package already exposes `(*Conn).Serialize` / `Deserialize`. |
| vec1 | Sub-package `vec/` over `sqlite-vec` provides a richer typed surface. |

## Registration shape

Two entry styles per extension; consumers pick whichever fits their wiring:

```go
// Explicit — register on a specific conn (the per-conn idiom for hooks).
import "github.com/go-again/sqlite/ext/regexp"
regexp.Register(c)

// Implicit — blank-import auto-wires via ConnectHook on every open.
import _ "github.com/go-again/sqlite/ext/regexp/auto"
```

The explicit form is the canonical entry; the blank-import `auto` sub-package is a thin shim that calls `Register` from a `ConnectHook` so registration survives connection pool churn.

## Adding a new extension

1. Pick the upstream ncruces module from [`ncruces/go-sqlite3/ext/<name>`](https://github.com/ncruces/go-sqlite3/tree/main/ext).
2. Translate to our `(*Conn).RegisterFunc` / `RegisterAggregator` / `RegisterWindowFunction` / vtab helper APIs. Don't copy upstream verbatim; the runtime is different.
3. Ship `ext/<name>/<name>.go` + `<name>_test.go` + `doc.go` + `ext/<name>/auto/auto.go`.
4. Add a row to the relevant table above; flip status to ✓ landed once tests and lint pass.
5. Add a one-line entry to [`llms.txt`](../llms.txt) under "Per-package overviews" so consumer agents find it.
6. Optional: drop a runnable example under `examples/ext-<name>/`.

Last reviewed against ncruces/go-sqlite3 main on 2026-05-29.

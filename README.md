# go-again/sqlite

[![Go Reference](https://pkg.go.dev/badge/github.com/go-again/sqlite.svg)](https://pkg.go.dev/github.com/go-again/sqlite)

A CGo-free SQLite driver for Go, built as a **drop-in replacement** for both
[`github.com/mattn/go-sqlite3`](https://github.com/mattn/go-sqlite3) and the
[glebarez/sqlite](https://github.com/glebarez/sqlite) gorm dialector. Built
on top of [`modernc.org/sqlite`](https://gitlab.com/cznic/sqlite), which
transpiles the SQLite C amalgamation to Go via ccgo.

Native first-class support for **vector search** (sqlite-vec) and
**full-text search** (FTS5).

```go
import (
    "database/sql"
    _ "github.com/go-again/sqlite"
)

db, _ := sql.Open("sqlite3", "file:my.db?_pragma=foreign_keys(1)")
```

## Why

- No C compiler needed. Builds inside `golang:alpine` and on GCP Cloud Build.
- One driver, three personalities: register as `"sqlite3"` (mattn-style) and
  `"sqlite"` (modernc-style) at the same time.
- All the things mattn exposed — `SQLiteDriver.ConnectHook`,
  `Conn.RegisterFunc/RegisterAggregator/RegisterCollation`,
  `RegisterUpdateHook/RegisterAuthorizer/SetTrace`,
  `LoadExtension`, `Backup`, `Serialize/Deserialize`, `GetLimit/SetLimit` —
  but pure Go.
- Typed Go APIs for vector search and FTS5, with `iter.Seq2` streaming and
  optional `log/slog` + Recorder observability.

## Why CGo-free matters

If you've never hit it, this whole section sounds like premature paranoia.
If you have, you know what it cost:

- **Alpine / scratch / distroless images**: mattn requires `gcc` and `musl`
  headers at build time. The Go cross-compile story for those targets is
  hostile to CGo — you either ship a fat base image or move the build into
  a multi-stage Dockerfile and pull in alpine-sdk for the build stage.
  `go-again/sqlite` builds in `FROM golang:1.25-alpine` with no `apk add`.
- **`go build` cross-compile**: `GOOS=linux GOARCH=arm64 go build` from
  macOS just works here. With mattn you need a cross C toolchain (osxcross,
  zig-as-CC, or a remote build runner) and the bug surface from
  cross-linking a vendored amalgamation is its own time sink.
- **CI providers that disallow CGo**: GCP Cloud Build's lightweight tier
  doesn't ship `gcc`. AWS CodeBuild gets confused about `glibc` ABIs when
  CGo is on. Several Lambda runtimes ship without a working linker. This
  package compiles in all of them.
- **`go test -race`**: works the same as any pure-Go package. mattn's race
  detector integration historically broke on each major SQLite bump.
- **Reproducible builds**: the entire driver is Go source. No vendored
  amalgamation to diff, no auto-generated build flags, no ccache surprises.

The cost: a constant-factor perf gap on hot UDF / per-row callback paths
(see Performance below). For 95% of applications this is invisible.

## Package layout

| Import path | What it gives you |
|---|---|
| `github.com/go-again/sqlite` | The driver. Registers `"sqlite"` and `"sqlite3"` names. |
| `github.com/go-again/sqlite/gorm` | `gorm.Dialector` for gorm.io/gorm. |
| `github.com/go-again/sqlite/vec` | sqlite-vec extension + typed `Table` API. |
| `github.com/go-again/sqlite/fts` | Typed FTS5 `Index[K, V]` with tokenizers, query builder, snippet/highlight. |
| `github.com/go-again/sqlite/vfs` | Expose any `io/fs.FS` (incl. `embed.FS`) as a read-only SQLite VFS. |

## Quick starts

### Mattn drop-in

Change one line:

```diff
- import _ "github.com/mattn/go-sqlite3"
+ import _ "github.com/go-again/sqlite"
```

Everything else — `sql.Open("sqlite3", ...)`, the `_*` DSN flags, custom-
driver registration via `&sqlite3.SQLiteDriver{Extensions, ConnectHook}`,
`Conn.RegisterFunc`, `errors.Is(err, sqlite.ErrConstraintUnique)` — works
unchanged.

See [`examples/mattn-compat/`](examples/mattn-compat/main.go).

### gorm

```go
import (
    "gorm.io/gorm"
    sqlite "github.com/go-again/sqlite/gorm"
)

db, _ := gorm.Open(sqlite.Open("file:my.db?_pragma=foreign_keys(1)"), &gorm.Config{})
```

`sqlite.Open(dsn)` and `sqlite.New(sqlite.Config{...})` are both provided so
either glebarez or the official go-gorm/sqlite import-paths can be swapped
in.

See [`examples/gorm/`](examples/gorm/main.go).

### Vector search

```go
import (
    _ "github.com/go-again/sqlite"
    "github.com/go-again/sqlite/vec"
)

tbl, _ := vec.Create(ctx, db, "docs", 8, vec.Options{Metric: vec.Cosine})
tbl.BatchInsert(ctx, items)
for m, err := range tbl.KNN(ctx, query, 5) {
    if err != nil { return err }
    fmt.Println(m.Rowid, m.Distance)
}
```

See [`examples/vec-search/`](examples/vec-search/main.go).

### Full-text search

```go
import (
    _ "github.com/go-again/sqlite"
    "github.com/go-again/sqlite/fts"
)

idx, _ := fts.New[int64, string](ctx, db, "docs", fts.Options{
    Tokenizer: fts.Porter{Base: fts.Unicode61{RemoveDiacritics: 2}},
})
idx.Insert(ctx, fts.Attr[int64, string]{Key: 1, Value: "the quick brown fox"})

matches, _ := idx.SearchSlice(ctx, fts.Term("fox"),
    fts.WithRanking(),
    fts.WithSnippet("value", "[", "]", "…", 8))
```

See [`examples/fts-search/`](examples/fts-search/main.go).

### `embed.FS`-backed read-only databases

```go
import "github.com/go-again/sqlite/vfs"

//go:embed seed.db
var seed embed.FS

name, _, _ := vfs.New(seed)
db, _ := sql.Open("sqlite3", "file:seed.db?vfs="+name+"&mode=ro")
```

See [`examples/vfs-embed/`](examples/vfs-embed/main.go).

## Migration table

If you're coming from:

| Old import | New import | Notes |
|---|---|---|
| `_ "github.com/mattn/go-sqlite3"` | `_ "github.com/go-again/sqlite"` | `sql.Open("sqlite3", ...)` keeps working. |
| `_ "modernc.org/sqlite"` | `_ "github.com/go-again/sqlite"` | `sql.Open("sqlite", ...)` keeps working. |
| `"github.com/glebarez/sqlite"` | `"github.com/go-again/sqlite/gorm"` | `sqlite.Open(dsn)` keeps working. |
| `"github.com/go-gorm/sqlite"` | `"github.com/go-again/sqlite/gorm"` | `sqlite.New(sqlite.Config{...})` keeps working. |

## DSN flag compatibility

Every `_*` DSN flag mattn supported is translated transparently — usually
into the equivalent `PRAGMA`. The `_strict=1` opt-in turns any unknown flag
into an error, helpful during migration to flush typos out.

| Flag (aliases) | Underlying action |
|---|---|
| `_pragma=foo(1)` | `PRAGMA foo=1` (multi-value) |
| `_foreign_keys` / `_fk` | `PRAGMA foreign_keys=` |
| `_busy_timeout` / `_timeout` | `PRAGMA busy_timeout=` |
| `_journal_mode` / `_journal` | `PRAGMA journal_mode=` |
| `_synchronous` / `_sync` | `PRAGMA synchronous=` |
| `_locking_mode` / `_locking` | `PRAGMA locking_mode=` |
| `_secure_delete` | `PRAGMA secure_delete=` |
| `_recursive_triggers` / `_rt` | `PRAGMA recursive_triggers=` |
| `_cache_size` | `PRAGMA cache_size=` |
| `_auto_vacuum` / `_vacuum` | `PRAGMA auto_vacuum=` |
| `_defer_foreign_keys` / `_defer_fk` | `PRAGMA defer_foreign_keys=` |
| `_ignore_check_constraints` | `PRAGMA ignore_check_constraints=` |
| `_case_sensitive_like` / `_cslike` | `PRAGMA case_sensitive_like=` |
| `_query_only` | `PRAGMA query_only=` |
| `_writable_schema` | `PRAGMA writable_schema=` |
| `_loc` | aliased to `_timezone` (auto → Local) |
| `_time_format`, `_time_integer_format`, `_inttotime`, `_texttotime`, `_timezone` | inherited from modernc |
| `_txlock` | sets transaction begin mode |
| `cache`, `mode`, `immutable`, `vfs` | URI-level, passed through |
| `_auth*` | **rejected** — userauth was removed upstream |
| `_strict=1` | unknown flags become hard errors |

## Build-tag mapping

Mattn used build tags to enable SQLite compile-time features. In go-again,
those features are **already enabled by default** (modernc compiles SQLite
with them), so the build tags become no-ops:

| mattn build tag | go-again status |
|---|---|
| `sqlite_fts5` | always on |
| `sqlite_json` (JSON1) | always on |
| `sqlite_math_functions` | always on |
| `sqlite_rtree`, `sqlite_geopoly` | always on |
| `sqlite_dbstat` | always on |
| `sqlite_preupdate_hook` | always on, accessible via `(*Conn).RegisterPreUpdateHook` |
| `sqlite_userauth` | **dropped** (deprecated upstream) |
| `sqlite_unlock_notify` | inherited from modernc |
| `sqlite_vtable` | always on, see [`modernc.org/sqlite/vtab`](https://pkg.go.dev/modernc.org/sqlite/vtab) |

## SQLite version

Inherited from `modernc.org/sqlite` — currently **3.53.1**.

## Performance

The underlying engine is `modernc.org/sqlite`, whose maintainer publishes
benchmark numbers against the major C-bound drivers at
[The SQLite Drivers Benchmarks Game](https://pkg.go.dev/modernc.org/sqlite-bench#readme-tl-dr-scorecard).
Numbers vary by workload, but the broad picture is consistent:

- **Bulk read / scan** paths are within single-digit-percent of mattn.
- **Bulk insert** under WAL is comparable.
- **UDF-heavy** workloads where Go callbacks fire on every row pay a
  measurable constant factor (the ccgo-transpiled call paths go through
  more indirection than mattn's CGo binding). For a no-op authorizer
  installed alongside a tiny SELECT, this package's overhead measures
  ~3% time and +5 allocs/op on Apple M4 — see `BenchmarkAuthorizer_NoOp`
  in `bench_test.go`.
- **Connection open** is faster here than mattn because there's no dlopen
  / dlsym / extension-resolution dance.

If you care about exact numbers for your workload, the
`go test -bench=. -benchmem -count=5` recipe is the right primary source
of truth — micro-benchmarks lifted off someone else's hardware lie often.

The cost-of-doing-business: we cannot beat a hand-tuned C+CGo binding on
hot UDF paths, and we don't try to. For workloads that fit "use SQL,
let SQLite do the heavy lifting," the choice is mostly about deployment
convenience (see "Why CGo-free matters" above), not about throughput.

## Coexistence with mattn/go-sqlite3

By default this package registers `"sqlite3"` — the same name mattn uses.
If you need both drivers in the same binary (gradual migration, fallback
for an extension you only have as a mattn-compiled .so, etc.), register
this one under a custom name and leave `"sqlite3"` to mattn:

```go
import (
    "database/sql"
    _ "github.com/mattn/go-sqlite3" // claims "sqlite3"
    sqlite "github.com/go-again/sqlite"
)

func init() {
    // Pick any name; opens through it route to the pure-Go driver.
    sql.Register("sqlite-pure", &sqlite.SQLiteDriver{})
}
```

Then use `sql.Open("sqlite-pure", dsn)` for routes that should use this
driver, and `sql.Open("sqlite3", dsn)` for routes that should still go
through mattn. There is no shared state, so the choice is per-`*sql.DB`.

The blank-import-only pattern (which auto-registers our driver under
`"sqlite3"` and panics on import-time conflict) **is incompatible** with
co-importing mattn. If you need that combination, drop the blank import
and use the named registration shown above.

See `TestCoexistence_CustomNameAlongsideMattn` in `compat_test.go` for an
executable example.

## Observability

Both the typed sub-packages ship a `Wrap(..., WithLogger, WithRecorder)`
decorator. The `Recorder` interface differs per package (vec records
dimension and k; fts records the FTS5 MATCH expression) — bring your own
metrics/tracing library.

```go
idx := fts.Wrap(rawIndex,
    fts.WithLogger(slog.Default()),
    fts.WithRecorder(myMetricsAdapter))
```

The core driver exposes lower-level hooks for the same purpose:
`(*Conn).SetTrace`, `RegisterUpdateHook`, `RegisterCommitHook`,
`RegisterRollbackHook`, `RegisterPreUpdateHook`, `RegisterAuthorizer`.

## libc version pinning

The transpiled SQLite C in `modernc.org/sqlite/lib` is closely tied to a
specific `modernc.org/libc` version. Your downstream `go.mod` **must use the
same `modernc.org/libc` version pinned by this module**, otherwise the
generated C-side ABI drifts and SQLite behaves erratically. The pin is
maintained automatically when you `go mod tidy` against this module.

To inspect what we pin: `just libc-pin` (or `go list -m modernc.org/libc`).

## Development

A `justfile` ships at the repo root with recipes for the common operations.
[Install `just`](https://just.systems/man/en/) (e.g. `brew install just`)
and then:

```sh
just                  # default: build + test + lint (fast pre-commit gate)
just test             # full suite across every package
just test-one PATTERN # focus on a single test/regex
just test-race        # -race detector
just bench            # all benchmarks
just lint             # vet + staticcheck + golangci-lint
just examples         # smoke-test every example
just cross-build      # compile-only matrix across every CI target
just ci               # full CI sequence locally
just --list           # everything else
```

If you don't want to install `just`, the underlying commands are vanilla
Go tooling — `go test ./...`, `go vet ./...`, `go build ./...`. The
justfile is convenience, not a build dependency.

## Testing

130 tests across the five packages cover:

- driver registration under both names, DSN flag translation matrix
  (including the `_mutex`-honesty path that refuses NOMUTEX rather than
  silently lying)
- reflective UDFs (every Go scalar type, variadics, `(T, error)`),
  aggregators, collations
- update / authorizer / trace / preupdate / commit / rollback hooks
- backup (Step/Remaining/PageCount), serialize/deserialize round-trip
- error code + extended code + `errors.Is`
- gorm: Dialector, Migrator, DDL parser, transaction commit/rollback,
  unique-violation translation, plus integration tests proving the
  side-by-side composition with vfs / vec / fts
- vec: L2 / Cosine metrics (Dot via the SQL path), JSON + binary encoding
  parity, KNN streaming with early break, dim-mismatch validation
- fts: Porter / Unicode61 / Trigram tokenizers, phrase adjacency, BM25
  ranking, snippet/highlight, external-content mode, multi-column index
- both Observable wrappers: Recorder fires once per op, error propagation,
  no-op when no options
- vfs: round-trip from a real on-disk SQLite file into a fstest.MapFS

This is what we run. We do **not** claim to run gorm.io/gorm's full
upstream test suite. The local gorm tests exercise every Dialector /
Migrator path we care about for our dialector.

Run them with `just test` (or `go test ./...`).

## License

BSD-style, matching modernc.org/sqlite. See LICENSE.

## Acknowledgements

- [modernc.org/sqlite](https://gitlab.com/cznic/sqlite) — Jan Mercl's
  pure-Go SQLite transpilation, without which this library would not exist.
- [mattn/go-sqlite3](https://github.com/mattn/go-sqlite3) — the C-based
  driver whose API we mirror.
- [glebarez/sqlite](https://github.com/glebarez/sqlite) — the gorm dialector
  this package's gorm sub-package is ported from.
- [asg017/sqlite-vec](https://github.com/asg017/sqlite-vec) — the vector
  search extension bundled by `modernc.org/sqlite/vec` and re-exported here.
- [zalgonoise/fts](https://github.com/zalgonoise/x/tree/master/fts) — the
  typed FTS5 wrapper shape we expanded on.

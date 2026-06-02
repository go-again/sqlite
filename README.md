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

## Supported Go versions

The project tracks the **two most recent Go releases**. Anything older
is unsupported on purpose. When a new Go minor ships, the just-
superseded release drops out of the support matrix within one cycle;
the actual pin lives in `go.mod`. Downstream consumers who need to
stay on an older Go should pin an older tag of this package.

This is a deliberate stance, not a side-effect:

- **Modern syntax is a feature.** The typed APIs lean on generics,
  `iter.Seq2`, `log/slog`, generic type aliases, `range over int`,
  `sync.WaitGroup.Go`, `strings.SplitSeq`, `reflect.TypeFor`. We adopt
  new idioms as soon as the second-most-recent release picks them up.
  `just lint` runs `gopls modernize` to enforce that contributions
  don't drift to older forms.
- **Code stays small.** `for i := range n` doesn't need a comment;
  `for i := 0; i < n; i++` would. Fewer lines, fewer reviewer-attention
  pixels.
- **Security and toolchain fixes reach you for free.** We aren't going
  to ship a year-old runtime to a downstream just to keep an extra Go
  version on a green build matrix.

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
| `github.com/go-again/sqlite/vec/gorm` | Tag-driven sqlite-vec sidecars on gorm models. |
| `github.com/go-again/sqlite/fts` | Typed FTS5 `Index[K, V]` with tokenizers, query builder, snippet/highlight. |
| `github.com/go-again/sqlite/fts/gorm` | Tag-driven FTS5 sidecars on gorm models. |
| `github.com/go-again/sqlite/fusion` | Rank-fusion helpers — combine `vec.KNN` and `fts.Search` results via Reciprocal Rank Fusion. |
| `github.com/go-again/sqlite/vfs` | Expose any `io/fs.FS` (incl. `embed.FS`) as a read-only SQLite VFS. |
| `github.com/go-again/sqlite/vfs/crypto` | Pure-Go encryption-at-rest VFS — Adiantum or AES-XTS-256, transparent page-level encryption of main DB + journal + WAL + temp files. |
| `github.com/go-again/sqlite/vfs/cksm` | Pure-Go corruption-detection VFS — Fletcher-style 8-byte checksum trailer per page; surfaces silent bit-rot as `SQLITE_IOERR_DATA`. |
| `github.com/go-again/sqlite/vfs/mvcc` | Pure-Go in-memory MVCC VFS — snapshot-isolated reads + atomic-publish writes. Shared (`file:/name`) or private (`file:name`) databases. |
| `github.com/go-again/sqlite/ext/<name>` | Opt-in loadable Go extensions: `array` (bind a slice as a SQL table), `blobio` (incremental BLOB I/O scalars), `bloom` (persistent Bloom-filter vtab), `closure` (transitive_closure graph walker), `csv`, `fileio` (readfile / writefile / lsmode + recursive `fsdir` vtab), `hash`, `ipaddr`, `lines` (line-by-line text-file vtab), `pivot` (three-SELECT cross-tab), `regexp`, `regexp/gorm` (GLOB-prefix + REGEXP gorm helper), `spellfix1` (fuzzy-text vtab — Soundex + Damerau-Levenshtein, persistent), `statement` (parametrized views with `?` and named binds), `stats` (variance/percentile/regr_*/median/mode aggregates + windows), `unicode` (case mapping / normalize / unaccent / collations), `uuid`, `zorder`. Pick per-connection via `<name>.Register(c)` or pool-wide via blank-import of `<name>/auto`. See [docs/coverage-ext.md](docs/coverage-ext.md) for the matrix. |

## Quick starts

### Modern Go config (no DSN strings)

The structured `sqlite.Config` covers Pragmas, encryption, connection pool knobs, and VFS routing in one Go value. Same Config shape feeds both raw `database/sql` and gorm — no per-layer duplication.

```go
import sqlite "github.com/go-again/sqlite"

db, err := sqlite.Open(sqlite.Config{
    Path:    "myapp.db",
    Pragmas: sqlite.RecommendedPragmas(), // WAL + busy_timeout=5s + foreign_keys
    Encryption: &sqlite.Encryption{       // optional
        Key: key,                          // 32 bytes for Adiantum
    },
    MaxOpenConns: 8,
})
if err != nil { ... }
defer db.Close() // drains *sql.DB AND unregisters the encryption VFS
```

For gorm, the same `Config` shape via `sqlitegorm.OpenConfig`:

```go
import (
    sqlite "github.com/go-again/sqlite"
    sqlitegorm "github.com/go-again/sqlite/gorm"
)

db, err := sqlitegorm.OpenConfig(sqlite.Config{
    Path:    "myapp.db",
    Pragmas: sqlite.RecommendedPragmas(),
})
defer db.Close()
db.AutoMigrate(&MyModel{}) // *gorm.DB methods, unchanged
```

The legacy DSN entries (`sql.Open("sqlite", "file:...")`, `sqlitegorm.Open(dsn)`, `sqlitegorm.New(Config{DSN: dsn})`) keep working unchanged.

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

### Coming from ncruces/go-sqlite3

Partial migration only — not a one-line swap. `github.com/ncruces/go-sqlite3` is the other major CGo-free Go SQLite driver (WebAssembly via wazero, vs our ccgo-transpiled Go). Different architecture, different public API for everything beyond `database/sql`.

What works after the import swap:

```diff
- import _ "github.com/ncruces/go-sqlite3/driver"
+ import _ "github.com/go-again/sqlite"
```

- `sql.Open("sqlite3", dsn)` — same driver name.
- URI-form DSNs with repeatable `_pragma=…` — accepted verbatim. You additionally gain the mattn-style short forms (`_fk=1`, `_journal=WAL`, `_busy_timeout=N`, …) that ncruces doesn't support.
- Standard `database/sql` — `db.Query`, `db.Exec`, `db.BeginTx`, prepared statements, all unchanged.

What needs rewriting:

- Anything that imports `github.com/ncruces/go-sqlite3` for type names (`sqlite3.Conn`, `sqlite3.Context`, `sqlite3.Value`, `sqlite3.ScalarFunction`) — those types don't exist here. Move per-conn work to `db.Conn(ctx)` + `conn.Raw(func(dc any) error { c := dc.(*sqlite.Conn); … })`.
- `driver.Open(dsn, func(*sqlite3.Conn) error)` — no equivalent constructor. Use mattn-style `&sqlite.SQLiteDriver{ConnectHook: ...}` registration instead, or install hooks per-conn after `db.Conn(ctx)`.
- UDF / aggregator / collation / window / hook callsites — map as `CreateFunction` → `Conn.RegisterFunc(name, fn, pure)`, `CreateAggregateFunction` → `Conn.RegisterAggregator`, `CreateWindowFunction` → `Conn.RegisterWindowFunction(name, nArg, ctor, pure)`, `CreateCollation` → `Conn.RegisterCollation`, `UpdateHook` → `RegisterUpdateHook`, `SetAuthorizer` → `RegisterAuthorizer`, `Trace` → `SetTrace`. Signatures differ — see [hooks.go](hooks.go), [compat_register.go](compat_register.go), and [windows.go](windows.go).
- `gormlite.Open(dsn)` → `sqlitegorm.Open(dsn)` (textual swap, glebarez-compatible).
- `vfs/readervfs.Create(...)` → `vfs.New(fs.FS) (name, *vfs.FS, error)` — different signature, same intent.
- `ext/vec1` users — our `vec/` wraps [sqlite-vec](https://github.com/asg017/sqlite-vec) (asg017's), not vec1 (SQLite-org's). The vtab name and SQL surface differ; consumers rewrite SQL, not just imports.
- Adiantum / XTS encryption VFSes — different package shape. ncruces exposes `vfs/adiantum` and `vfs/xts`; ours is `vfs/crypto` with both ciphers behind a single `New(Options{Cipher: …})` constructor. Same threat model (confidentiality at rest, no MAC).

If you're a pure-`database/sql` consumer with `_pragma=…` URI DSNs and no custom UDFs, this is a one-line swap. Otherwise budget for it as a per-call-site rewrite — same shape and amount of work as porting from any other Go SQLite driver to mattn.

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

### Hybrid search (semantic + lexical) via fusion

When you want the recall of a `vec.KNN` semantic match AND the precision of an `fts.Index.Search` lexical match, the `fusion` sub-package merges two ranked result sets into one via Reciprocal Rank Fusion (Cormack 2009). No SQL, no extension to load — just Go ranking.

```go
import "github.com/go-again/sqlite/fusion"

vecHits, _ := tbl.KNNSlice(ctx, queryVec, 50)
ftsHits, _ := idx.SearchSlice(ctx, fts.Term("brown fox"), fts.WithLimit(50))

vecKeys := make([]int64, len(vecHits))
for i, h := range vecHits { vecKeys[i] = h.Rowid }
ftsKeys := make([]int64, len(ftsHits))
for i, h := range ftsHits { ftsKeys[i] = h.Key }

top := fusion.RRF([][]int64{vecKeys, ftsKeys}, fusion.WithLimit(20))
for _, r := range top {
    fmt.Println(r.Key, r.Score)
}
```

### Deep gorm integration — tag-driven vec & FTS5

The `vec/gorm` and `fts/gorm` sub-packages bridge gorm models to the
vector / full-text sidecars. Tag a field, register the plugin, and
gorm Create/Update/Delete maintains the sidecar automatically. Typed
`KNN[T]` / `Search[T]` helpers return matching gorm models in
ranking order with distance / rank attached.

```go
import (
    _ "github.com/go-again/sqlite"
    sqlitegorm "github.com/go-again/sqlite/gorm"
    "github.com/go-again/sqlite/fts"
    ftsgorm "github.com/go-again/sqlite/fts/gorm"
    vecgorm "github.com/go-again/sqlite/vec/gorm"
)

type Document struct {
    ID        uint   `gorm:"primaryKey"`
    Title     string `fts5:"tokenize=porter+unicode61"`
    Body      string `fts5:"tokenize=porter+unicode61"`
    Embedding vecgorm.Embedding `vec:"dim=384;metric=cosine"`
}

db, _ := gorm.Open(sqlitegorm.Open("app.db"), &gorm.Config{})
db.Use(vecgorm.Plugin())
db.Use(ftsgorm.Plugin())

vecgorm.Migrate(db, &Document{}) // creates documents + documents_vec
ftsgorm.Migrate(db, &Document{}) // creates documents_fts + triggers

db.Create(&Document{Title: "Hello", Body: "world", Embedding: vec})

// Find documents semantically similar to a query vector:
near, _ := vecgorm.KNN[Document](ctx, db, queryVec, 5)

// Find documents matching a phrase, ranked by BM25:
hits, _ := ftsgorm.Search[Document](ctx, db, fts.Term("world"))
```

Tag-driven features:

- Auto-migrate sidecar tables alongside `db.AutoMigrate`.
- Sync-on-write callbacks (vec) or triggers (FTS5) — including
  `CreateInBatches` in a single transaction per batch.
- Soft-delete awareness: models using `gorm.DeletedAt` get a
  metadata column on the sidecar; KNN/Search excludes them
  automatically. Pass `IncludeDeleted()` to override.
- Typed helpers return `[]Hit[T]` ordered by ranking so callers
  don't have to rebuild the IN-clause + re-sort dance.
- `db.Migrator().DropTable(&Model{})` cascades into the sidecar
  (vec0 table or FTS5 table + triggers) via the dialector's
  `DropTableHook` interface — no manual cleanup needed.
- FTS5 mode is configurable per tag: `external` (default,
  triggers-driven), `external=false` (in-table FTS5), or
  `contentless=true` (index only, no text).

The embedding field type is `vecgorm.Embedding` (a `[]float32`
alias that implements gorm's `GormDataType`); `[]float32` with
`gorm:"-"` also works for callers who prefer not to import the
wrapper.

See [`vec/gorm/`](vec/gorm/) and [`fts/gorm/`](fts/gorm/) for full
package docs and [`examples/gorm-vec-tagged/`](examples/gorm-vec-tagged/)
+ [`examples/gorm-fts-tagged/`](examples/gorm-fts-tagged/) for
runnable end-to-end usage; coverage matrix lives in
[`docs/coverage-gorm.md`](docs/coverage-gorm.md).

### `embed.FS`-backed read-only databases

```go
import "github.com/go-again/sqlite/vfs"

//go:embed seed.db
var seed embed.FS

name, _, _ := vfs.New(seed)
db, _ := sql.Open("sqlite3", "file:seed.db?vfs="+name+"&mode=ro")
```

See [`examples/vfs-embed/`](examples/vfs-embed/main.go).

### Encryption at rest

```go
import "github.com/go-again/sqlite/vfs/crypto"

key := make([]byte, 32) // derive from passphrase / keyring / HSM
name, fs, _ := crypto.New(crypto.Options{Key: key})
defer fs.Close()

db, _ := sql.Open("sqlite", "file:secret.db?vfs="+name)
```

Pure-Go page-level encryption — Adiantum (default, 32-byte key) or AES-XTS-256 (64-byte key). The main DB file, rollback journal, WAL frames, and temp files are all encrypted; the WAL `-shm` index stays plaintext (it's memory-mapped, not disk-resident in practice). No SQLCipher on-disk format compatibility, no MAC — confidentiality only; SQLCipher's per-page HMAC integrity is not what we ship. Overhead measured on Apple M4: ~+27% Adiantum, ~+44% AES-XTS over plaintext on a write-heavy microbenchmark (run `go test -bench=BenchmarkInsert ./vfs/crypto/` to verify on your hardware). Add `Options.Recorder = crypto.NewSlogRecorder(slog.Default())` (or any custom `crypto.Recorder`) for per-IO observability. See [`examples/vfs-crypto/`](examples/vfs-crypto/main.go) and [`vfs/crypto/doc.go`](vfs/crypto/doc.go).

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

Inherited from `modernc.org/sqlite`; the exact build follows whatever
that dependency ships in `go.mod`.

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

## Coverage

Per-surface coverage matrices live in [`docs/`](docs/):

- [`docs/coverage-gorm.md`](docs/coverage-gorm.md) — every method on
  `gorm.Dialector`, `gorm.Migrator`, `gorm.ErrorTranslator`, and
  `gorm.SavePointerDialectorInterface` with status (typed / inherited /
  unsupported) and link to the test that exercises it.
- [`docs/coverage-vec.md`](docs/coverage-vec.md) — every documented
  sqlite-vec column option, SQL helper function, and KNN form.
- [`docs/coverage-fts.md`](docs/coverage-fts.md) — every FTS5 index
  option, query operator, search option, auxiliary function, and
  maintenance command.
- [`docs/coverage-sql.md`](docs/coverage-sql.md) — methodical
  feature-by-feature matrix of the raw SQL surface (SELECT clauses,
  joins, CTEs, window functions, JSON1, datetime, constraints,
  triggers, UPSERT, RETURNING, PRAGMA, etc.) exercised by the
  [`tests/sql/`](tests/sql/) conformance suite.

Read these before filing "does this package support X?" — answer is
in the matrices.

## Testing

Tests live alongside each package and CI runs them on linux / macos /
windows. The default `go test ./...` covers:

- **root package** — driver registration under both names; DSN flag
  translation matrix (including the `_mutex`-honesty path that refuses
  NOMUTEX rather than silently lying); reflective UDFs (every Go scalar
  type, variadics, `(T, error)`), aggregators, collations; update /
  authorizer / trace / preupdate / commit / rollback hooks; backup
  (Step/Remaining/PageCount); serialize/deserialize round-trip; error
  code + extended code + `errors.Is`; time round-trip matrix; BLOB
  semantics including `zeroblob`; WAL concurrent readers + writers;
  context cancellation; prepared-statement LRU cache
- **gorm** — Dialector, Migrator, DDL parser, transaction
  commit/rollback, unique-violation translation, integration tests
  proving side-by-side composition with vfs / vec / fts
- **vec** — L2 / Cosine / Dot metrics, JSON + binary encoding parity,
  KNN streaming with early break, dim-mismatch validation, raw-SQL
  coverage of every documented sqlite-vec helper
- **vec/gorm** — tag parser, plugin lifecycle, sidecar CRUD,
  `vecgorm.Embedding` wrapper, `KNN[T]` typed helper, soft-delete
  semantics, `DropTable` cascade, dim-mismatch warning
- **fts** — Porter / Unicode61 / Trigram tokenizers, phrase adjacency,
  BM25 ranking, snippet/highlight, external-content mode, multi-column
  index, raw-SQL coverage of every documented FTS5 feature
- **fts/gorm** — tag parser, plugin + triggers, `Search[T]` typed
  helper, external / in-table / contentless modes, multi-field shared
  table, soft-delete, `DropTable` cascade
- **vfs** — round-trip from a real on-disk SQLite file into a `fstest.MapFS`
- **tests/sql** — methodical SQL conformance suite organized by
  SQLite Language Reference category (SELECT, JOIN, CTE, window
  functions, JSON1, datetime, constraints, triggers, UPSERT,
  RETURNING, PRAGMA, etc.)

CI also enforces three opt-in upstream-suite lanes:

- **gorm-upstream** — clones `gorm.io/gorm` at the pinned version and
  runs its full integration suite against our dialector via a tiny
  shim. See [`docs/gorm-upstream.md`](docs/gorm-upstream.md).
- **modernc-upstream** — vendored subset of `modernc.org/sqlite`'s
  own test suite runs against this fork under
  `-tags=modernc_upstream`. See
  [`docs/modernc-upstream.md`](docs/modernc-upstream.md).
- **mattn-upstream** — vendored subset of `mattn/go-sqlite3`'s suite
  validates the mattn-compat surface under `-tags=mattn_upstream`.
  See [`docs/mattn-upstream.md`](docs/mattn-upstream.md).

Run the default sweep with `just test` (or `go test ./...`).

## Sponsors

This project is supported by:

- **[ssh2incus](https://ssh2incus.com)** — an open-source SSH server
  that connects directly to [Incus](https://linuxcontainers.org/incus/)
  containers and virtual machines. Runs on the Incus host and routes
  incoming SSH connections to the right instance via the Incus API,
  so individual instances don't need their own SSH server.

- **[MobyDeck](https://github.com/mobydeck)** — a GitHub organization
  publishing open-source developer tools and infrastructure
  utilities across Go, C, TypeScript, shell, and Ruby. Projects
  include the SSH-for-Incus gateway above, container credential
  management, privilege-management system utilities, and status-page
  automation.

If your company benefits from this driver and you'd like to be listed
here, open an issue.

## License

Apache 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).

This project incorporates work from several upstream projects, each
preserved under its original license:

- [modernc.org/sqlite](https://gitlab.com/cznic/sqlite) — BSD-style;
  see [LICENSE.modernc](LICENSE.modernc). We fork the hand-written Go
  wrapper to add per-connection APIs; the transpiled SQLite C code
  remains an external dependency.
- [github.com/mattn/go-sqlite3](https://github.com/mattn/go-sqlite3) —
  MIT; see [LICENSE.mattn](LICENSE.mattn). A subset of mattn's tests is
  vendored under the `mattn_upstream` build tag to validate drop-in
  compatibility.
- [github.com/glebarez/sqlite](https://github.com/glebarez/sqlite) —
  MIT; see [LICENSE.glebarez](LICENSE.glebarez). The `gorm/` sub-package
  is ported from glebarez.

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

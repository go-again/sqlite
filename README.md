# go-again/sqlite

[![Go Reference](https://pkg.go.dev/badge/github.com/go-again/sqlite.svg)](https://pkg.go.dev/github.com/go-again/sqlite)

A CGo-free SQLite **driver + ecosystem** for Go. Drop-in replacement for both [`mattn/go-sqlite3`](https://github.com/mattn/go-sqlite3) (registers as `"sqlite3"`) and the [glebarez/sqlite](https://github.com/glebarez/sqlite) gorm dialector, with first-class typed APIs for vector search, full-text search, encryption at rest, in-memory MVCC, hybrid ranking, and a catalog of loadable Go SQL extensions — all in one module, all pure Go. Built on top of [`modernc.org/sqlite`](https://gitlab.com/cznic/sqlite).

```go
import sqlite "github.com/go-again/sqlite"

db, _ := sqlite.OpenWAL("app.db") // WAL + busy_timeout=5s + foreign_keys=on
defer db.Close()
// db embeds *sql.DB — every database/sql method works.
```

Existing mattn / glebarez / ncruces code keeps working after the import swap; the legacy `sql.Open("sqlite3", "file:...?_pragma=…")` form is still accepted. See [Mattn drop-in](#mattn-drop-in) and [Coming from ncruces](#coming-from-ncrucesgo-sqlite3) for the migration recipes.

## What you get

The driver itself — CGo-free, mattn-API + glebarez-gorm drop-in, registered under both `"sqlite"` and `"sqlite3"` names — is just the floor. Stacked on top, in the same module:

- **Vector search** — typed `vec.Table` over [sqlite-vec](https://github.com/asg017/sqlite-vec): L2 / Cosine / Dot metrics, JSON + binary encoding, streaming `iter.Seq2` KNN, `WithFilter` predicate pushdown, `KNNSQL` escape hatch
- **Full-text search** — typed `fts.Index[K, V]` over FTS5: Porter / Unicode61 / Trigram tokenizers, Go query builder, BM25 ranking, snippet + highlight, external-content / in-table / contentless modes
- **Tag-driven gorm bridges** — `vec/gorm` and `fts/gorm` maintain vector + FTS5 sidecars from a single field tag on your gorm models, with cascading `DropTable`, soft-delete awareness, typed `KNN[T]` / `Search[T]` helpers
- **Hybrid search** — `fusion.RRF` combines `vec.KNN` and `fts.Search` rankings via Reciprocal Rank Fusion (Cormack 2009)
- **Encryption at rest** — pure-Go `vfs/crypto` with Adiantum (default) or AES-XTS-256, transparent page-level encryption of main DB + journal + WAL + temp files; built-in Argon2id key derivation; per-IO `Recorder` observability
- **Corruption detection** — pure-Go `vfs/cksm` with Fletcher-style 8-byte trailer per page (on-disk compatible with SQLite's `cksumvfs`); composes beneath `vfs/crypto` for checksum-then-encrypt stacks
- **In-memory VFSes** — `vfs/mvcc` (snapshot isolation + atomic publish) and `vfs/memdb` (direct page store, no MVCC) for tests + scratch DBs, with shared (`file:/name`) and private (`file:name`) modes
- **`fs.FS` / `io.ReaderAt` as a database** — `vfs.New(fs.FS)` for `embed.FS`-bundled DBs; `vfs.NewReader(io.ReaderAt, size)` for raw-buffer immutable DBs
- **Loadable Go SQL extensions** under `ext/` — SQL scalars / aggregates / collations (`regexp`, `uuid`, `hash`, `ipaddr`, `zorder`, `stats`, `unicode`), virtual tables (`array`, `csv`, `lines`, `statement`, `closure`, `pivot`), specialised stores (`bloom`, `spellfix1`), I/O (`blobio`, `fileio`). Auto-register per conn or pool-wide via blank-import `/auto`; full inventory + status matrix in [`docs/coverage-ext.md`](docs/coverage-ext.md)
- **Hooks** — per-conn update / authorizer / commit / rollback / pre-update / trace; conn-pinning idiom documented and shown in `examples/hooks/`
- **Backup, serialize, deserialize** — mattn-compat `(*Conn).Backup` factory + top-level `sqlite.Serialize` / `Deserialize` for in-memory snapshots
- **Modern Go-typed Config** — `sqlite.Config{Path, Pragmas, Encryption, MaxOpenConns, …}` flows uniformly to both raw `database/sql` and gorm; no DSN-string duplication
- **Modern Go idioms throughout** — generics, `iter.Seq2`, `log/slog`, `range over int`, `sync.WaitGroup.Go`; `gopls modernize` enforced in CI. Requires one of the two most recent Go releases — see [Supported Go versions](#supported-go-versions)

## How it compares

The Go SQLite landscape has three architectural camps: CGo bindings (mattn, zombiezen), pure-Go ccgo transpilation (modernc, this, glebarez), and pure-Go via WebAssembly + wazero (ncruces). Where this module sits:

| Capability | this | mattn | modernc | ncruces | glebarez |
|---|---|---|---|---|---|
| CGo-free | ✓ | ✗ | ✓ | ✓ (wazero) | ✓ |
| Builds in distroless / `golang:alpine` with no `apk add` | ✓ | ✗ | ✓ | ✓ | ✓ |
| `database/sql` driver | ✓ | ✓ | ✓ | ✓ | ✗ (gorm only) |
| Registers as both `"sqlite"` and `"sqlite3"` | ✓ | ✗ (`"sqlite3"`) | ✗ (`"sqlite"`) | ✗ (`"sqlite3"`) | n/a |
| Mattn-compat surface (`ConnectHook`, `RegisterFunc`, hooks, `Backup`, `Serialize`) | ✓ | n/a | partial | partial | n/a |
| gorm dialector in the same module | ✓ | ✗ | ✗ | ✗ | ✓ |
| Typed sqlite-vec API | ✓ | ✗ | ✗ | ✗ | ✗ |
| Typed FTS5 API | ✓ | ✗ | ✗ | ✗ | ✗ |
| Tag-driven gorm bridges (vec + FTS5 sidecars) | ✓ | ✗ | ✗ | ✗ | ✗ |
| Hybrid-search rank fusion | ✓ | ✗ | ✗ | ✗ | ✗ |
| Encryption-at-rest VFS | ✓ (Adiantum + AES-XTS, single constructor) | ✗ (rely on SQLCipher CGo build) | ✗ | ✓ (separate `vfs/adiantum` + `vfs/xts`) | ✗ |
| Page-checksum VFS | ✓ | ✗ | ✗ | ✓ | ✗ |
| In-memory MVCC + direct VFS | ✓ | ✗ | ✗ | ✓ | ✗ |
| `fs.FS` / `io.ReaderAt` VFS | ✓ | ✗ | ✗ | ✓ | ✗ |
| Loadable Go SQL extensions | catalog under `ext/`, per-conn or pool-wide | math/regexp via build-tag | ✗ | similar catalog | ✗ |
| Hot UDF-row throughput | == modernc | fastest (CGo) | baseline | slowest (wazero) | == modernc |

**Where this module is better:**

- **Single module, full stack.** Driver + gorm + vec + FTS5 + fusion + crypto + cksm + the in-memory VFSes + the full `ext/` catalog all release together; no coordinating cadences across `modernc.org/sqlite` + `glebarez/sqlite` + `asg017/sqlite-vec` + whatever encryption / checksum / vtab module you also need.
- **One driver, two SQL names.** `"sqlite"` (modernc-style) and `"sqlite3"` (mattn-style) point at the same registered singleton, so the import-swap migration recipe is one line for users coming from either side.
- **Typed vec + FTS5 + tag-driven gorm sidecars are first-class.** Every other Go SQLite driver requires DIY plumbing for these. Models stay as plain gorm structs; an embedding field with `vec:"dim=N;metric=cosine"` and a text field with `fts5:"tokenize=porter+unicode61"` is enough.
- **`vfs/crypto` consolidates Adiantum + AES-XTS** behind one `New(Options{Cipher: …})`; ncruces ships them as separate `vfs/adiantum` and `vfs/xts` packages.
- **Modern Go-typed `sqlite.Config`** flows uniformly to raw `database/sql` and gorm — no DSN-string duplication between layers.

**Where this module is worse:**

- **Hot UDF / per-row callback throughput.** mattn's CGo binding is still the fastest path through `sqlite3_step` + Go callback. We measure ~3% slower with +5 allocs/op vs mattn on a no-op authorizer + tiny SELECT (`BenchmarkAuthorizer_NoOp`). For workloads that fit "let SQLite do the heavy lifting in SQL", this is invisible; for million-rows-per-second UDF pipelines, mattn still wins.
- **Two-Go-release support window.** The project tracks only the two newest Go releases (see [Supported Go versions](#supported-go-versions)). Downstreams pinned to an older Go can't use this module — bring the toolchain up to one of the two supported lines.
- **No SQLCipher format compatibility.** `vfs/crypto` is confidentiality-only — no MAC, no on-disk format compatibility with SQLCipher. If you need integrity-tagged ciphertext, pair with disk-level integrity (LUKS dm-integrity, ZFS) or stay on a SQLCipher-CGo build.

## Supported Go versions

The project tracks the **two most recent Go releases**. Anything older is unsupported on purpose; when a new Go minor ships, the just-superseded release drops out within one cycle. The actual pin lives in `go.mod`.

The stance is deliberate. The typed APIs lean on idioms (generics, `iter.Seq2`, `log/slog`, `range over int`, `sync.WaitGroup.Go`, `strings.SplitSeq`, `reflect.TypeFor`) that read cleaner without a polyfill layer, and `just lint` runs `gopls modernize` to enforce that contributions don't drift to older forms. Security and toolchain fixes reach downstreams for free; we won't ship a year-old runtime just to keep an extra Go version on a green build matrix.

Downstreams pinned to a Go release older than the two-newest window can't use this module — there's no older-tag fallback to recommend. Bring the toolchain up to one of the supported releases, or stay on whichever pure-Go SQLite driver you're currently using.

## Why CGo-free matters

If you've never hit it, this whole section sounds like premature paranoia.
If you have, you know what it cost:

- **Alpine / scratch / distroless images**: mattn requires `gcc` and `musl`
  headers at build time. The Go cross-compile story for those targets is
  hostile to CGo — you either ship a fat base image or move the build into
  a multi-stage Dockerfile and pull in alpine-sdk for the build stage.
  `go-again/sqlite` builds in `FROM golang:alpine` with no `apk add`.
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
| `github.com/go-again/sqlite/vfs/memdb` | Pure-Go in-memory VFS with no snapshot isolation — direct per-page store under `sync.RWMutex`. Smaller-surface alternative to `vfs/mvcc` for tests and scratch DBs; writes are visible to readers immediately. |
| `github.com/go-again/sqlite/ext/<name>` | Opt-in loadable Go extensions covering scalars, aggregates, collations, virtual tables, specialised stores, and sandboxed I/O. See the [Extensions](#extensions) section below for usage patterns and [`docs/coverage-ext.md`](docs/coverage-ext.md) for the per-package status matrix. |

## Quick starts

### Open shortcuts

Four single-arg constructors cover the cases most consumers want, each with a symmetric gorm-side helper. None of them needs a `Config` literal:

| Root | gorm | Equivalent to | Use case |
|---|---|---|---|
| `sqlite.OpenInMemory()` | `sqlitegorm.OpenInMemory()` | `Config{Path: sqlite.InMemory}` | Tests, REPLs, scratch DBs (per-conn private). |
| `sqlite.OpenWAL(path)` | `sqlitegorm.OpenWAL(path)` | `Config{Path: path, Pragmas: RecommendedPragmas()}` | Production preset — WAL + busy_timeout=5s + foreign_keys=on. |
| `sqlite.OpenReadOnly(path)` | `sqlitegorm.OpenReadOnly(path)` | `Config{Path: path, Mode: ModeReadOnly}` | Shipped seed DBs, replica reads. Refuses to create the file if missing; refuses writes. |
| `sqlite.OpenShared(name)` | `sqlitegorm.OpenShared(name)` | `Config{Path: name, Mode: ModeMemory, Cache: CacheShared}` | Multi-conn in-memory tests — every open against the same name sees the same rows. |

```go
import sqlite "github.com/go-again/sqlite"

db, _ := sqlite.OpenWAL("app.db")
defer db.Close()
// db is a *sqlite.DB embedding *sql.DB — all database/sql methods work.
```

```go
import (
    "gorm.io/gorm"
    sqlitegorm "github.com/go-again/sqlite/gorm"
)

db, _ := gorm.Open(sqlitegorm.OpenWAL("app.db"), &gorm.Config{})
```

If you prefer to keep the DSN string form, `sqlite.InMemory` is the typed constant for `":memory:"` — `sql.Open("sqlite", sqlite.InMemory)` and `sqlitegorm.Open(sqlite.InMemory)` both work. Need richer isolation than `OpenShared` provides? The [`vfs/memdb`](#in-memory-databases-mvcc-and-direct) (no MVCC) and [`vfs/mvcc`](#in-memory-databases-mvcc-and-direct) (snapshot isolation) sub-packages are the next step up.

#### Typed Pragma values

The `Pragmas` struct's string-valued fields (`JournalMode`, `Synchronous`, `TempStore`) and the new `Config.Cache` field accept typed string-derived enums rather than free-form strings. Autocomplete-friendly and typo-proof:

```go
db, _ := sqlite.Open(sqlite.Config{
    Path: "app.db",
    Pragmas: sqlite.Pragmas{
        JournalMode: sqlite.JournalWAL,
        Synchronous: sqlite.SynchronousNormal,
        TempStore:   sqlite.TempStoreMemory,
        BusyTimeout: 5 * time.Second,
        ForeignKeys: true,
    },
})
```

The constants live in the root package: `sqlite.JournalWAL` / `JournalDelete` / …, `sqlite.SynchronousNormal` / …, `sqlite.TempStoreMemory` / …, `sqlite.CacheShared` / `CachePrivate`. String literals (`JournalMode: "WAL"`) still compile — the typed forms are an additive type-safety win, not a breaking change.

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

top, err := fusion.RRF([][]int64{vecKeys, ftsKeys}, fusion.WithLimit(20))
if err != nil { log.Fatal(err) }
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

Pure-Go page-level encryption — Adiantum (default, 32-byte key) or AES-XTS-256 (64-byte key). The main DB file, rollback journal, WAL frames, and temp files are all encrypted; the WAL `-shm` index stays plaintext (it's memory-mapped, not disk-resident in practice).

**What to know.** Confidentiality only — no SQLCipher on-disk format compatibility, no MAC. SQLCipher's per-page HMAC integrity is not what we ship; for active-tamper threats pair with disk-level integrity (LUKS dm-integrity, ZFS). Overhead on a write-heavy microbenchmark is in the tens of percent; the exact factor depends on cipher and platform (Adiantum is faster than AES-XTS on most ARM, often the reverse on AES-NI-capable x86). Run `go test -bench=BenchmarkInsert ./vfs/crypto/` to measure on your hardware.

**Composing.** Add `Options.Recorder = crypto.NewSlogRecorder(slog.Default())` (or any custom `crypto.Recorder`) for per-IO observability. Stack `vfs/cksm` underneath via `Options.WrapVFS` for checksum-then-encrypt protection. See [`examples/vfs-crypto/`](examples/vfs-crypto/main.go) for the standalone shape and [`examples/gorm-crypto/`](examples/gorm-crypto/main.go) for an end-to-end stack with gorm + vec + fts + fusion on top of encryption + Argon2id key derivation. Package docs in [`vfs/crypto/doc.go`](vfs/crypto/doc.go).

### Corruption detection at rest

```go
import (
    "github.com/go-again/sqlite/vfs/cksm"
    sqlite "github.com/go-again/sqlite"
)

name, fs, _ := cksm.New(cksm.Options{})
defer fs.Close()

db, _ := sql.Open("sqlite", "file:journal.db?vfs="+name)
sc, _ := db.Conn(ctx)
sc.Raw(func(d any) error { return d.(*sqlite.Conn).EnableChecksums("main") })
```

Pure-Go page-level Fletcher-style checksum trailer (8 bytes per page). `(*sqlite.Conn).EnableChecksums("main")` sets `reserved_bytes=8` on the schema and `VACUUM`s so every existing page is rewritten with the trailer.

**What to know.** On reopens the VFS detects byte 20 == 8 in the SQLite header and auto-activates verification; a flipped bit surfaces as `SQLITE_IOERR_DATA` on read instead of silent corruption. On-disk compatible with SQLite's `cksumvfs` extension, so a database written here is readable by stock SQLite + cksumvfs.

**Composing.** Both `cksm.Options` and `crypto.Options` accept `WrapVFS` to stack — register cksm first, then point crypto at it for checksum-on-the-inside / encrypt-on-the-outside. See [`examples/vfs-cksm/`](examples/vfs-cksm/main.go) for a corrupt-a-byte-and-watch-the-error demo.

### In-memory databases (MVCC and direct)

Two sibling VFSes for in-memory data, picked by the isolation contract you want:

```go
// Snapshot-isolation reads + atomic-publish writes.
import "github.com/go-again/sqlite/vfs/mvcc"
name, fs, _ := mvcc.New(mvcc.Options{})
defer fs.Close()
db, _ := sql.Open("sqlite", "file:/shared.db?vfs="+name)   // SHARED
db2, _ := sql.Open("sqlite", "file:scratch.db?vfs="+name)  // PRIVATE
```

```go
// Direct per-page store, no MVCC — writes visible to readers instantly.
import "github.com/go-again/sqlite/vfs/memdb"
name, fs, _ := memdb.New(memdb.Options{})
defer fs.Close()
db, _ := sql.Open("sqlite", "file:/cache.db?vfs="+name)
```

`vfs/mvcc` gives reader snapshots at lock-acquire time and republishes the snapshot atomically on commit; pair it with the standard SQLite busy-timeout retry for concurrent writers. `vfs/memdb` is the smaller-surface alternative for tests and scratch DBs — no snapshot copy on commit, no MVCC bookkeeping; concurrent writers see each other's writes immediately. Both VFSes use the leading-`/` convention (`file:/name`) for shared stores vs no-leading-slash (`file:name`) for per-handle private stores. See [`examples/vfs-mvcc/`](examples/vfs-mvcc/main.go) and [`examples/vfs-memdb/`](examples/vfs-memdb/main.go).

### io.ReaderAt-backed databases

`vfs.New(fs.FS)` is the general adapter; when you already have a `[]byte` or `io.ReaderAt`, use the lighter `vfs.NewReader`:

```go
import "github.com/go-again/sqlite/vfs"

bs, _ := os.ReadFile("seed.db")
name, fs, _ := vfs.NewReader(bytes.NewReader(bs), int64(len(bs)))
defer fs.Close()

db, _ := sql.Open("sqlite", "file:db?vfs="+name+"&mode=ro")
```

Same constructor shape as `vfs.New`; the file name inside the VFS is always `db`. Useful for shipping a sealed read-only DB as a Go variable, or for mmap-backed buffers that don't want to round-trip through `fs.FS`.

### Lower-level driver capabilities

**Hooks.** Per-connection update / authorizer / commit / rollback / pre-update / trace callbacks. gorm's pool fan-out makes "install once" fragile — pin one conn and use it for everything:

```go
db.SetMaxOpenConns(1)
sc, _ := db.Conn(ctx)
sc.Raw(func(dc any) error {
    c := dc.(*sqlite.Conn)
    c.RegisterUpdateHook(func(op int, dbName, table string, rowid int64) { ... })
    c.RegisterAuthorizer(func(op int, a1, a2, dbName, trig string) int { return sqlite.SQLITE_OK })
    return c.SetTrace(&sqlite.TraceConfig{EventMask: sqlite.TraceStmt, Callback: ...})
})
// Drive traffic through sc.ExecContext, NOT db.Exec.
```

See [`examples/hooks/`](examples/hooks/main.go) for the full update / authorizer / commit / trace round-trip.

**Backups.** The mattn-compat `(*Conn).Backup(destSchema, srcConn, srcSchema)` factory drives `sqlite3_backup_*` against an open destination connection (caller-owned). See [`examples/backup/`](examples/backup/main.go).

**Serialize / deserialize.** Top-level `sqlite.Serialize(ctx, db) []byte` snapshots an in-memory database into a byte buffer; `sqlite.Deserialize(ctx, db, buf)` restores it. Useful for ship-a-DB-as-state and for state-machine tests.

## Extensions

The `ext/` tree adds loadable SQL features as Go sub-packages. Each one is independent — pick what you need and leave the rest off your import graph. Two registration shapes per extension:

```go
// Explicit — register on a specific *sqlite.Conn (the per-conn idiom).
import "github.com/go-again/sqlite/ext/regexp"
regexp.Register(conn)

// Implicit — blank-import the /auto variant; every conn the driver
// opens picks the extension up via Driver.ConnectHook.
import _ "github.com/go-again/sqlite/ext/regexp/auto"
```

Beyond that, extensions fall into three patterns based on what they expose:

### Pattern 1 — scalar / aggregate / collation (auto-import + plain SQL)

These add SQL functions, operators, and collations. Blank-import the `/auto` package and they show up in any `Where` / `Order` / `Select`:

```go
import (
    _ "github.com/go-again/sqlite/ext/hash/auto"
    _ "github.com/go-again/sqlite/ext/regexp/auto"
    _ "github.com/go-again/sqlite/ext/uuid/auto"
)

db.Where("email REGEXP ?", `^.*@example\.com$`).Find(&users)
db.Select(`hex(sha256(body)) AS digest`).Find(&logs)
db.Where("ipcontains(?, src_ip)", "10.0.0.0/8").Find(&events)
```

Covered packages: [`regexp`](ext/regexp/) (RE2 operator + `regexp_*` scalars), [`uuid`](ext/uuid/) (v1/4/5/6/7 + `uuid_str`/`uuid_blob`), [`hash`](ext/hash/) (md5/sha1/sha2/sha3/blake2/ripemd), [`ipaddr`](ext/ipaddr/) (`ipcontains`/`ipfamily`/`ipnetwork` over `net/netip`), [`zorder`](ext/zorder/) (Morton encoding for 2-24 dimensions), [`stats`](ext/stats/) (variance/percentile/regr_*/median/mode aggregates + sliding-window inverses), [`unicode`](ext/unicode/) (Unicode-aware `upper`/`lower`/`normalize`/`unaccent` + preset `NOCASE_UNICODE` collation + locale-tagged collators). End-to-end demo composing seven of them against one gorm model: [`examples/gorm-ext-scalars/`](examples/gorm-ext-scalars/main.go).

### Pattern 2 — virtual tables (CREATE VIRTUAL TABLE + bind / Pointer)

These expose data through `CREATE VIRTUAL TABLE name USING module(...)`; some accept Go data via `sqlite.Pointer(slice)`:

```go
import (
    sqlite "github.com/go-again/sqlite"
    _ "github.com/go-again/sqlite/ext/array/auto"
)

// Bind a Go slice as a SQL table:
db.Where(`id IN (SELECT value FROM array(?))`,
    sqlite.Pointer([]int64{1, 5, 17})).Find(&users)

// Or DDL-based vtabs (lines, csv, statement, closure, bloom, spellfix1):
db.Exec(`CREATE VIRTUAL TABLE temp.log USING lines(data='INFO ok\nERROR …')`)
db.Exec(`CREATE VIRTUAL TABLE recent USING statement('SELECT name FROM users WHERE id >= ?')`)
db.Exec(`CREATE VIRTUAL TABLE dict USING spellfix1`)
```

Covered packages: [`array`](ext/array/) (slice-as-table; supports the transparent `sqlite.Pointer` path), [`lines`](ext/lines/) (split a text blob into rows), [`csv`](ext/csv/) (CSV files as tables; sandbox via `csv.RegisterFS(c, fsys)`), [`statement`](ext/statement/) (parametrized views with `?N` / `:name` HIDDEN bind columns), [`closure`](ext/closure/) (`transitive_closure` graph walker with optional depth bounds), [`bloom`](ext/bloom/) (persistent Bloom filter; survives `db.Close`), [`spellfix1`](ext/spellfix1/) (Soundex + Damerau-Levenshtein fuzzy lookup; vocabulary persists), [`pivot`](ext/pivot/) (three-SELECT cross-tab — rows × columns × cell aggregate). End-to-end demo composing seven vtabs against one gorm DB: [`examples/gorm-ext-vtabs/`](examples/gorm-ext-vtabs/main.go).

### Pattern 3 — `fs.FS`-bound (connect-hook idiom)

`csv`, `lines`, `fileio`, `blobio` all accept a sandbox `fs.FS` at registration time, which the `/auto` package can't carry. Install a connect-hook on the singleton driver before `gorm.Open` so every pooled conn picks it up:

```go
import (
    sqlite "github.com/go-again/sqlite"
    "github.com/go-again/sqlite/ext/csv"
)

sqlite.DefaultDriver().RegisterConnectionHook(
    func(c sqlite.ExecQuerierContext, _ string) error {
        return csv.RegisterFS(c.(*sqlite.Conn), myFsys)
    })
// Now gorm.Open(...) opens conns that all have csv sandboxed to myFsys.
```

Same shape works for `lines.RegisterFS`, `fileio.RegisterFS`, `blobio.RegisterFS`. [`examples/gorm-ext-vtabs/`](examples/gorm-ext-vtabs/main.go) shows the recipe end-to-end with csv.

### Window functions and custom UDFs

`(*Conn).RegisterWindowFunction(name, nArg, ctor, pure)` is the typed window-function entry. See [`examples/window-function/`](examples/window-function/main.go). For plain scalar / aggregate UDFs use `Conn.RegisterFunc` / `RegisterAggregator` (mattn-compatible) or the top-level `sqlite.RegisterFunction`.

### Status matrix

Every `ext/` package is tracked with status (✓ landed / ⚠ partial / ✗ deferred), upstream reference, and test pin in [`docs/coverage-ext.md`](docs/coverage-ext.md).

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
  in the low single-digit-percent range with a handful of extra
  allocs/op; run `BenchmarkAuthorizer_NoOp` in `bench_test.go` to
  measure on your hardware.
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

This package and mattn both claim the `"sqlite3"` driver name by default, so a blank-import of both in one binary panics at init. The fix is to skip the blank import on one side and register that driver under a custom name; the section below covers the recipe when mattn keeps `"sqlite3"`.

If you need both drivers in the same binary (gradual migration, fallback for an extension you only have as a mattn-compiled .so, etc.), register this one under a custom name and leave `"sqlite3"` to mattn:

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
- [`docs/coverage-ext.md`](docs/coverage-ext.md) — every `ext/` loadable
  extension with status (✓ landed / ⚠ partial / ✗ deferred), upstream
  reference, and test pin.
- [`docs/coverage-vfs.md`](docs/coverage-vfs.md) — every `vfs/`
  sub-package (crypto, cksm, mvcc, memdb, fs.FS / ReaderAt adapters)
  with status, upstream reference, and test pin.

Read these before filing "does this package support X?" — answer is
in the matrices.

## Testing

Tests live alongside each package; `go test ./...` runs the full suite,
which CI exercises on linux / macos / windows. The per-package coverage
is itemized in the [coverage matrices](#coverage); behaviour you'd
expect of a SQLite driver — driver registration, DSN translation, every
UDF / hook / backup / serialize round-trip, WAL concurrency, context
cancellation, the prepared-statement LRU — is exercised by name.
`tests/sql/` adds a SQL conformance suite organised by SQLite Language
Reference category.

CI also enforces three opt-in upstream-suite lanes:

- **gorm-upstream** — clones `gorm.io/gorm` at the pinned version and
  runs its full integration suite against our dialector via a tiny
  shim. See [`docs/gorm-upstream.md`](docs/gorm-upstream.md).
- **modernc-upstream** — vendored subset of `modernc.org/sqlite`'s
  own test suite runs against this driver under
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

- **[mobydeck](https://github.com/mobydeck)** — a GitHub organization
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
  see [LICENSE.modernc](LICENSE.modernc). The hand-written Go wrapper
  layer was vendored into this repo's root (with attribution) so we
  can add per-connection APIs that the upstream wrapper doesn't
  expose; the transpiled SQLite C code remains an upstream dependency.
- [github.com/mattn/go-sqlite3](https://github.com/mattn/go-sqlite3) —
  MIT; see [LICENSE.mattn](LICENSE.mattn). A subset of mattn's tests is
  vendored under the `mattn_upstream` build tag to validate drop-in
  compatibility.
- [github.com/glebarez/sqlite](https://github.com/glebarez/sqlite) —
  MIT; see [LICENSE.glebarez](LICENSE.glebarez). The `gorm/` sub-package
  is ported from glebarez.
- [github.com/ncruces/go-sqlite3](https://github.com/ncruces/go-sqlite3) —
  MIT; see [LICENSE.ncruces](LICENSE.ncruces). Several `ext/` sub-packages
  port loadable extensions from ncruces — re-implemented against this
  driver's `(*Conn).RegisterFunc` / vtab helper APIs rather than copied
  verbatim, with a credit header in each ported package's doc.

## Acknowledgements

- [modernc.org/sqlite](https://gitlab.com/cznic/sqlite) — Jan Mercl's
  pure-Go SQLite transpilation, without which this library would not exist.
- [mattn/go-sqlite3](https://github.com/mattn/go-sqlite3) — the C-based
  driver whose API we mirror.
- [glebarez/sqlite](https://github.com/glebarez/sqlite) — the gorm dialector
  this package's gorm sub-package is ported from.
- [ncruces/go-sqlite3](https://github.com/ncruces/go-sqlite3) — Nuno Cruces's
  CGo-free (WASM/wazero) driver, whose loadable-extension lineup several
  `ext/` sub-packages are ported from.
- [asg017/sqlite-vec](https://github.com/asg017/sqlite-vec) — the vector
  search extension bundled by `modernc.org/sqlite/vec` and re-exported here.
- [zalgonoise/fts](https://github.com/zalgonoise/x/tree/master/fts) — the
  typed FTS5 wrapper shape we expanded on.

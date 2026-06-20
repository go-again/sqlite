<div align="center">

<img src="https://github.com/gosqlite/.github/raw/main/profile/logo.png" alt="gosqlite" width="128" />

# gosqlite

[**Website**](https://gosqlite.com) &nbsp;·&nbsp; [**Docs**](https://gosqlite.com/docs) &nbsp;·&nbsp; [**Get started**](https://gosqlite.com/docs/getting-started/) &nbsp;·&nbsp; [**API reference**](https://pkg.go.dev/gosqlite.org)

[![Go Reference](https://pkg.go.dev/badge/gosqlite.org.svg)](https://pkg.go.dev/gosqlite.org)

</div>

**The comprehensive, AI-ready, CGo-free SQLite stack for Go.** One module replaces [`mattn/go-sqlite3`](https://github.com/mattn/go-sqlite3) (registers as `"sqlite3"`), [`modernc.org/sqlite`](https://gitlab.com/cznic/sqlite) (`"sqlite"`), and the [`glebarez/sqlite`](https://github.com/glebarez/sqlite) gorm dialector — then adds first-class **typed** APIs for vector search, full-text search, encryption at rest, in-memory MVCC, hybrid ranking, a user-implementable VFS, a bounded page cache, and a catalog of loadable SQL extensions. Designed so that **migrating to CGo-free and adopting advanced SQLite features is a one-line change**, and so your **AI coding agent gets it right the first time** — the repo ships [Agent Skills](skills/) that teach assistants the real API. All pure Go.

```go
import sqlite "gosqlite.org"

db, _ := sqlite.OpenWAL("app.db") // WAL + busy_timeout=5s + foreign_keys=on
defer db.Close()
// db embeds *sql.DB — every database/sql method works.
```

Existing mattn / modernc / glebarez / ncruces code keeps working after the import swap; the legacy `sql.Open("sqlite3", "file:...?_pragma=…")` form is still accepted. Your ORM works too — the [gorm](docs/guides/gorm.md) dialector is a companion module (`gosqlite.org/gorm`), and [xorm](docs/guides/migrating.md#with-xorm) uses the driver as-is. See the **[migration guide](docs/guides/migrating.md)**.

## Why gosqlite

- **🤖 AI / LLM-ready — your coding agent writes correct code on the first try.** The repo ships [**Agent Skills**](skills/): task-scoped instructions (vector search, FTS5, the gorm dialector, LiteORM, custom VFS, migration, pitfalls) an AI assistant loads on demand, so it reaches for the *real* API instead of hallucinating one — drop `skills/` into your agent and the trial-and-error loop largely disappears. It's backed by structured, task-oriented [docs](docs/) ("I want X → do Y"), an [`AGENTS.md`](AGENTS.md) for agents working *in* the codebase, and `doc.go` on every package for pkg.go.dev. **This is the fastest way to build on SQLite with an AI pair-programmer.**

- **📦 Comprehensive — the whole stack, one project.** Driver + typed sqlite-vec + FTS5 + rank fusion + encryption + checksums + in-memory & custom VFSes + a bounded page cache + the `ext/` catalog all ship and version **together** under `gosqlite.org`. The gorm dialector lives in a companion `gosqlite.org/gorm` module so the core stays dependency-free for everyone who isn't on gorm. No stitching `modernc.org/sqlite` + `glebarez/sqlite` + `asg017/sqlite-vec` + a separate encryption/vtab module and reconciling four release cadences — and your agent reasons about *one* surface, not four.

- **🔀 CGo-free migration in one line.** Swap the import; keep your `sql.Open("sqlite3", …)` calls and `_*` DSN flags (translated transparently). Two driver names (`"sqlite"` + `"sqlite3"`) mean code from either lineage compiles unchanged. You drop the C toolchain, cross-compile with plain `GOOS=… go build`, and ship static distroless/alpine binaries.

- **⚡ Advanced SQLite features without the DIY plumbing.** Vector + full-text search, encryption at rest, a writable custom VFS, sessions/changesets, custom Go FTS5 tokenizers, a bounded page cache — **typed and first-class** here, where every other Go SQLite driver makes you build them yourself (or doesn't offer them at all). Want that search inside an ORM? [LiteORM](https://liteorm.org) layers declarative `vec:` / `fts:` model tags on top of this driver — `AutoMigrate` provisions the sidecars, typed helpers return ranked results.

## Documentation

- **[gosqlite.com](https://gosqlite.com)** — the documentation site. Start at **[gosqlite.com/docs](https://gosqlite.com/docs)** ([Getting started](https://gosqlite.com/docs/getting-started/) · [Guides](https://gosqlite.com/docs) · [Extensions](https://gosqlite.com/docs/extensions/)).
- **[pkg.go.dev/gosqlite.org](https://pkg.go.dev/gosqlite.org)** — the Go API reference.
- Browsing the repo directly? The same docs live under [`docs/`](docs/) — [Getting started](docs/getting-started.md) · [Guides](docs/index.md) · [Extensions](docs/extensions/index.md).
- **[`skills/`](skills/)** — task recipes for AI agents *using* the package; **[`AGENTS.md`](AGENTS.md)** for agents *developing* it.

## What you get

The driver itself — CGo-free, mattn-API + glebarez-gorm drop-in, registered under both `"sqlite"` and `"sqlite3"` — is just the floor. Stacked on top, in the same module:

- **[Vector search](docs/guides/vector-search.md)** — typed `vec.Table` over [sqlite-vec](https://github.com/asg017/sqlite-vec): L2 / Cosine / Dot / Hamming, JSON + binary + quantized `int8` / `bit` encodings, metadata / partition / auxiliary columns, streaming `iter.Seq2` KNN, predicate pushdown.
- **[Full-text search](docs/guides/full-text-search.md)** — typed `fts.Index[K, V]` over FTS5: Porter / Unicode61 / Trigram tokenizers plus **custom Go tokenizers**, a Go query builder, BM25 ranking, snippet + highlight.
- **[Hybrid search](docs/guides/hybrid-search.md)** — `fusion.RRF` combines `vec.KNN` and `fts.Search` rankings via Reciprocal Rank Fusion.
- **[Encryption at rest](docs/guides/encryption.md)** — pure-Go `vfs/crypto`, Adiantum or AES-XTS-256, transparent page-level encryption of DB + journal + WAL + temp; Argon2id key derivation; per-IO observability.
- **[Corruption detection](docs/guides/checksums.md)** — pure-Go `vfs/cksm`, Fletcher-style trailer per page (compatible with SQLite's `cksumvfs`); composes beneath `vfs/crypto`.
- **[In-memory & embedded](docs/guides/in-memory.md)** — `vfs/mvcc` (snapshot isolation) and `vfs/memdb` (direct), plus `vfs.New(fs.FS)` / `vfs.NewReader` for `embed.FS` and raw-buffer databases.
- **[User-implementable VFS](docs/guides/custom-vfs.md)** — back a writable database with arbitrary Go storage via `vfs.Register` + the `vfs.VFS` / `vfs.File` interfaces; WAL via the optional `vfs.ShmFile`; `vfs.Wrap` instrumentation.
- **[Bounded page cache](docs/guides/page-cache.md)** — `pcache.InstallBoundedLRU(maxPages)` bounds SQLite's page-cache heap with hit / miss / eviction / live-page counters.
- **[Loadable Go SQL extensions](docs/extensions/index.md)** under `ext/` — scalars / aggregates / collations (`regexp`, `uuid`, `hash`, `stats`, `unicode`, `fuzzy`, `decimal`, `money`, `time`, `eval`, …), virtual tables (`array`, `csv`, `lines`, `rtree`, `series`, …), stores (`bloom`, `spellfix1`), I/O (`blobio`, `fileio`) — many with a typed Go handle. Auto-register per conn or pool-wide.
- **[Sessions / changesets](docs/guides/sessions.md)** — record changes into a changeset/patchset, `ApplyChangeset` to a replica, `InvertChangeset`, `ConcatChangesets`. Offline sync, audit logs, replication — **no other pure-Go SQLite driver exposes this**.
- **[Hooks, backup & introspection](docs/guides/hooks.md)** — per-conn update / authorizer / commit / trace hooks, `Backup` + `Serialize`/`Deserialize`, column metadata + runtime telemetry, the `*FunctionContext` aux-data substrate.
- **[Modern Go-typed Config](docs/guides/configuration.md)** — `sqlite.Config{Path, Pragmas, Encryption, …}` flows uniformly to raw `database/sql` and gorm; plus typed pragma enums and one-line open shortcuts.
- **[sqlitex ergonomics](docs/guides/sqlitex.md)** — `Save`, `Transaction`, `Execute`, scalar reads, and an `embed.FS` migration runner.

## Declarative models with native search: LiteORM

Want declarative models on top of all this? [**LiteORM**](https://liteorm.org) integrates with this driver more deeply than any other Go SQLite stack: its **vector, full-text, and hybrid (RRF) search** are built directly on this driver's `vec` / `fts` / `fusion` packages, and encryption rides on `vfs/crypto` — search isn't bolted on, it's the same engine.

- **Declare, don't wire.** Put `vec:` / `fts:` tags (or a `SearchIndexes()` method) on a model; `AutoMigrate` provisions the FTS5 + vec0 sidecars and keeps them in sync on every write.
- **Typed, ranked results.** `search.For[T](db).Vector / .FullText / .Hybrid` return your models in score order — no manual KNN SQL.
- **Same driver, no walls.** Open with the same `gosqlite.Config`; gosqlite's `ext/` SQL functions work through the query builder with no glue (e.g. `REGEXP` via `sqlite.WhereRegex`, which range-scans an index for left-anchored patterns); and `sqlite.Conn(db)` hands back the raw `*gosqlite.DB` for sessions, backup, or the typed `vec`/`fts` handles anytime.

It's a separate module (`liteorm.org`), so the gosqlite core stays dependency-free. Runnable end-to-end demo: [`examples/liteorm/`](examples/liteorm/); agent recipe: [`skills/liteorm`](skills/liteorm/SKILL.md). (For `gorm.io/gorm` specifically, the [`gosqlite.org/gorm`](docs/guides/gorm.md) dialector is the drop-in — it covers the dialector contract; ORM-level search is its domain.)

## How it compares

The Go SQLite landscape has three camps: CGo bindings (mattn, zombiezen), pure-Go ccgo transpilation (modernc, this, glebarez), and pure-Go via WebAssembly + wazero (ncruces). Where this module sits:

| Capability | this | mattn | modernc | ncruces | glebarez |
|---|---|---|---|---|---|
| Ships AI Agent Skills + task-oriented docs | ✓ | ✗ | ✗ | ✗ | ✗ |
| Whole stack (driver + vec + FTS5 + fusion + VFS + ext) in one module | ✓ | ✗ | ✗ | ✗ | ✗ |
| CGo-free | ✓ | ✗ | ✓ | ✓ (wazero) | ✓ |
| Builds in distroless / `golang:alpine`, no `apk add` | ✓ | ✗ | ✓ | ✓ | ✓ |
| `database/sql` driver | ✓ | ✓ | ✓ | ✓ | ✗ (gorm only) |
| Registers as both `"sqlite"` and `"sqlite3"` | ✓ | ✗ | ✗ | ✗ | n/a |
| Mattn-compat surface (hooks, `Backup`, `Serialize`, …) | ✓ | n/a | partial | partial | n/a |
| ORM with native vector / FTS / hybrid search (via LiteORM) | ✓ | ✗ | ✗ | ✗ | ✗ |
| CGo-free gorm dialector (companion module) | ✓ | ✗ | ✗ | ✗ | ✓ |
| Typed sqlite-vec API | ✓ | ✗ | ✗ | ✗ | ✗ |
| Typed FTS5 API + custom Go tokenizers | ✓ | ✗ | ✗ | ✗ | ✗ |
| Hybrid-search rank fusion | ✓ | ✗ | ✗ | ✗ | ✗ |
| Encryption-at-rest VFS | ✓ (Adiantum + AES-XTS, one constructor) | ✗ | ✗ | ✓ (two packages) | ✗ |
| Page-checksum / in-memory / `fs.FS` VFSes | ✓ | ✗ | ✗ | ✓ | ✗ |
| User-implementable VFS (pure Go) | ✓ (rollback-journal + WAL via `ShmFile`) | ✗ | ✗ | ✓ | ✗ |
| Bounded / observable page cache (pure Go) | ✓ | ✗ | ✗ | ✗ | ✗ |
| Changesets / session sync | ✓ | ✗ | ✗ | ✗ | ✗ |
| Loadable Go SQL extensions | catalog under `ext/` | math/regexp via build-tag | ✗ | similar catalog | ✗ |
| Hot UDF-row throughput | == modernc | fastest (CGo) | baseline | slowest (wazero) | == modernc |

**Better here:** it's the only Go SQLite package that ships AI Agent Skills + task-oriented docs, so an AI assistant builds against it correctly without trial and error; one module ships the whole stack (driver + vec + FTS5 + fusion + crypto + cksm + VFSes + page cache + `ext/`) on one release cadence; one driver under two SQL names makes migration a one-line swap; typed vec/FTS5 are first-class where every other driver needs DIY plumbing; custom Go FTS5 tokenizers and the typed `sqlite.Config` are unique to this module; and [LiteORM](https://liteorm.org), built on this driver, is the only Go SQLite data layer with native vector / full-text / hybrid search wired straight in.

**The trade-offs are narrow:** mattn's CGo binding is ~3% faster on hot per-row UDF paths (a few extra allocs/op on a no-op authorizer — invisible once you let SQL do the work); the support window tracks the two newest Go releases; and `vfs/crypto` is confidentiality-only (no SQLCipher on-disk format or per-page MAC). None is a dealbreaker for typical use; details in [Performance](docs/reference/performance.md).

## Quick start

```go
import sqlite "gosqlite.org"

// One-line presets, no DSN string:
db, _ := sqlite.OpenWAL("app.db")        // production: WAL + busy_timeout + foreign_keys
mem, _ := sqlite.OpenInMemory()          // tests / scratch

// Or the typed Config (pragmas, encryption, pool tuning in one value):
db2, _ := sqlite.Open(sqlite.Config{
	Path:    "myapp.db",
	Pragmas: sqlite.RecommendedPragmas(),
	MaxOpenConns: 8,
})
```

Plain `database/sql` works too: `sql.Open("sqlite", "file:app.db")`. The full setup story — open shortcuts, the typed `Config`, typed pragma enums — is in **[Configuration](docs/guides/configuration.md)**; start-to-finish in **[Getting started](docs/getting-started.md)**.

## Packages

| Import path | What it gives you |
|---|---|
| `gosqlite.org` | the driver (registers `"sqlite"` + `"sqlite3"`) + `Config` + hooks + backup/serialize |
| `gosqlite.org/gorm` | `gorm.Dialector` drop-in (glebarez / go-gorm) — **separate module** |
| `gosqlite.org/vec` | typed sqlite-vec `Table` (L2 / Cosine / Dot / Hamming, quantized encodings) |
| `gosqlite.org/fts` | typed FTS5 `Index[K, V]` + custom Go tokenizers |
| `gosqlite.org/fusion` | RRF rank-fusion helpers (combine vec + fts results) |
| `gosqlite.org/vfs` (+ `crypto`, `cksm`, `mvcc`, `memdb`) | `fs.FS`/`io.ReaderAt` adapters, the user-implementable VFS, encryption, checksums, in-memory |
| `gosqlite.org/pcache` | application-controlled bounded page cache |
| `gosqlite.org/sqlitex` | ergonomic `database/sql` helpers + `embed.FS` migrations |
| `gosqlite.org/ext/<name>` (+ `/auto`) | opt-in loadable Go extensions — see the [catalog](docs/extensions/index.md) |

## Migrating

| Coming from | Change |
|---|---|
| `_ "github.com/mattn/go-sqlite3"` | → `_ "gosqlite.org"`; `sql.Open("sqlite3", ...)` keeps working |
| `_ "modernc.org/sqlite"` | → `_ "gosqlite.org"`; `sql.Open("sqlite", ...)` keeps working |
| `"github.com/glebarez/sqlite"` / `"github.com/go-gorm/sqlite"` | → `"gosqlite.org/gorm"`; `sqlite.Open(dsn)` keeps working |
| `ncruces/go-sqlite3` | partial — `database/sql` + `_pragma` DSNs swap cleanly; custom UDFs/types need a rewrite |

Full per-package recipes, the `_*` DSN-flag table, and build-tag mapping: **[Migrating](docs/guides/migrating.md)**, **[DSN flags](docs/reference/dsn-flags.md)**, **[Build tags](docs/reference/build-tags.md)**. Runnable: [`examples/migrating/`](examples/migrating/).

## Why CGo-free

Because SQLite is transpiled to Go (via `modernc.org/sqlite`) rather than C-bound, you get: builds in `golang:alpine` / distroless with no `apk add`; `GOOS=… GOARCH=… go build` cross-compilation that just works; clean `go test -race`; reproducible builds with no vendored amalgamation; and CI on providers that disallow CGo. The cost is a constant-factor gap on hot UDF/per-row paths — invisible for most applications. More in [Getting started](docs/getting-started.md#why-cgo-free) and [Performance](docs/reference/performance.md).

## Examples

Runnable, smoke-tested programs under [`examples/`](examples/), grouped by audience: [`migrating/`](examples/migrating/) (drop-in recipes), [`getting-started/`](examples/getting-started/) (the modern on-ramp), and [`features/`](examples/features/) (one per capability — search, VFS, the `ext/` catalog, hooks, sessions, …). `just example <name>` runs one; `just examples` smoke-tests all.

## Development & contributing

`just` recipes drive everything: `just` (build + test + lint), `just test`, `just test-race`, `just lint`, `just examples`, `just bench`, `just ci`. The underlying commands are vanilla `go test ./...` etc. Architecture, invariants, conventions, and where-to-look live in **[`AGENTS.md`](AGENTS.md)**; maintainer reference (coverage matrices, upstream-suite recipes, perf baselines) in [`dev/`](dev/).

## Supported Go

The two most recent Go releases (the pin lives in `go.mod`). The typed APIs use modern idioms freely and `gopls modernize` is enforced in CI. See [Supported Go](docs/reference/supported-go.md).

## Sponsors

This project is supported by:

- **[ssh2incus](https://ssh2incus.com)** — an open-source SSH server that connects directly to [Incus](https://linuxcontainers.org/incus/) containers and virtual machines, routing incoming SSH connections to the right instance via the Incus API.
- **[mobydeck](https://github.com/mobydeck)** — a GitHub organization publishing open-source developer tools and infrastructure utilities across Go, C, TypeScript, shell, and Ruby.

If your company benefits from this driver and you'd like to be listed here, open an issue.

## License

Apache 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE). This project incorporates work from [modernc.org/sqlite](https://gitlab.com/cznic/sqlite) (BSD-style; the Go wrapper layer is vendored with attribution), [mattn/go-sqlite3](https://github.com/mattn/go-sqlite3) (MIT; a test subset is vendored to validate drop-in compatibility), [glebarez/sqlite](https://github.com/glebarez/sqlite) (MIT; the `gorm/` sub-package), and [ncruces/go-sqlite3](https://github.com/ncruces/go-sqlite3) (MIT; several `ext/` ports). Each is preserved under its original license — see the `LICENSE.*` files.

## Acknowledgements

- [modernc.org/sqlite](https://gitlab.com/cznic/sqlite) — Jan Mercl's pure-Go SQLite transpilation, without which this library would not exist.
- [mattn/go-sqlite3](https://github.com/mattn/go-sqlite3) — the C-based driver whose API we mirror.
- [glebarez/sqlite](https://github.com/glebarez/sqlite) — the gorm dialector our gorm sub-package is ported from.
- [ncruces/go-sqlite3](https://github.com/ncruces/go-sqlite3) — Nuno Cruces's CGo-free (WASM/wazero) driver, whose loadable-extension lineup several `ext/` sub-packages are ported from.
- [asg017/sqlite-vec](https://github.com/asg017/sqlite-vec) — the vector-search extension bundled by `modernc.org/sqlite/vec` and re-exported here.
- [zalgonoise/fts](https://github.com/zalgonoise/x/tree/master/fts) — the typed FTS5 wrapper shape we expanded on.

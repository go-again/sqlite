# AGENTS.md

Onboarding for AI agents and humans **developing** this repository. Read it top-to-bottom to be useful within ~5 minutes. (Agents *using* the package as a dependency want [`skills/`](skills/) instead; end users want [`README.md`](README.md) and [`docs/`](docs/).)

This file is canonical; `CLAUDE.md` is a pointer to it. Deep C-ABI internals live in [`dev/architecture.md`](dev/architecture.md).

---

## What this package is

`gosqlite.org` is a **CGo-free** SQLite driver for Go, a drop-in replacement for:

- `github.com/mattn/go-sqlite3` — the C-bound driver; we register as `"sqlite3"`.
- `modernc.org/sqlite` — the upstream CGo-free driver this fork builds on; we register as `"sqlite"`.
- `github.com/glebarez/sqlite` and `github.com/go-gorm/sqlite` — gorm dialectors; ours is the `gorm/` sub-package.

Plus first-class typed Go APIs for **sqlite-vec** vector search and **FTS5** full-text search, encryption-at-rest, a user-implementable VFS, a bounded page cache, and a catalog of loadable Go SQL extensions.

Supported Go: the **two most recent releases** (the pin lives in `go.mod`; don't name versions in prose). Modern syntax is a feature — generics, `iter.Seq2`, `log/slog`, generic type aliases, range-over-int, `sync.WaitGroup.Go`, `strings.SplitSeq`, `reflect.TypeFor` are all in use. `just lint` runs `gopls modernize` to enforce the policy.

## Architecture in one paragraph

We **fork modernc's hand-written Go wrapper** (the root `*.go` files) so we can add per-conn methods and own the `Driver` type; the transpiled C (`lib/`, `vec/`, `vfs/c/`) stays an untouched external dependency. The wrapper talks to that C through a `uintptr`/`unsafe.Pointer` function-pointer dance centralized in `internal/cabi`. The weird-looking patterns (uintptr arithmetic, empty mutex critical sections, named-field struct literals) are the contract, not bugs — **don't restyle them**. Full rationale, the cabi primitives, and the struct-drift discipline: [`dev/architecture.md`](dev/architecture.md).

---

## Repository layout

The high-signal map is the root-package fork surface; each sub-package documents its contract in a `doc.go`.

**Root (modernc-derived + our additions):**

```
sqlite.go               init(), driver registration ("sqlite" + "sqlite3")
driver.go               *Driver with Extensions / ConnectHook
conn.go                 *conn (alias *Conn); most of the work happens here
stmt.go, rows.go, result.go, tx.go
error.go                *Error with Code() / ExtendedCode()
convert.go              SQLite ↔ Go value coercion
backup.go / backup_factory.go   *Backup + (*Conn).Backup + Serialize/Deserialize
blob.go                 *Blob + (*Conn).OpenBlob — incremental BLOB I/O
extension.go            LoadExtension / EnableLoadExtension
limits.go               GetLimit / SetLimit
hooks.go                Update / Authorizer / Trace hooks
rtree.go                (*Conn).RegisterRTreeGeometry / RegisterRTreeQuery
session.go              SESSION ext — CreateSession/ApplyChangeset/Invert/Concat + *Session
pre_update_hook.go      RegisterPreUpdateHook / Commit / Rollback
fcntl.go                file-control helpers (incl. EnableChecksums)
wal.go                  WALCheckpoint / WALAutoCheckpoint / RegisterWALHook
snapshot.go             GetSnapshot / OpenSnapshot / SnapshotRecover + *Snapshot
control.go              SetProgressHandler / SetDBConfig / QueryDBConfig
vtab.go / module.go     virtual-table trampolines + CreateModule / CreateModuleSplit
pointer.go              sqlite.Pointer for binding Go values into SQL params
mutex.go                unlock_notify mutex wrapper
dsn.go                  mattn `_*` DSN-flag translator
constants.go            SQLITE_* re-exports + ErrNo / ErrNoExtended
compat_*.go             type aliases + reflective RegisterFunc / RegisterAggregator + conversion
stmt_cache.go           per-conn prepared-stmt LRU + StmtCacheStats
introspect.go           TableColumnMetadata / Status / TxnState + (*Stmt).Readonly / Status
config.go / open_config.go   sqlite.Config / Pragmas / Encryption + sqlite.Open(Config)
doc.go                  package doc (pkg.go.dev landing)
```

**Sub-packages** (each has a `doc.go`):

- `gorm/` — gorm dialector, a **separate module** (`gosqlite.org/gorm`, its own `go.mod`); the core module does not depend on `gorm.io/gorm`. Originally from glebarez; diverged: RETURNING always-on, OpenConfig, error translator. `DropTableHook` (a public extension point for third-party plugins) is defined here. Examples under `gorm/examples/`.
- `vec/` — sqlite-vec typed `Table`.
- `fts/` — FTS5 typed `Index[K, V]`.
- `fusion/` — RRF / RRF2 rank-fusion helpers (pure Go, no SQLite dep).
- `sqlitex/` — ergonomic `database/sql` helpers (Save, Transaction, ExecScript, Execute, Result*, Migrate) in the zombiezen/crawshaw lineage.
- `blobstore/` — large, growable, randomly-writable byte objects over a managed chunk table (`io.ReaderAt`/`io.WriterAt`, sparse holes, truncate); built on `Conn.OpenBlob`, conn-per-op. A **separate module** (`gosqlite.org/blobstore`, its own `go.mod`) so its codec dependency stays out of the root graph; example under `blobstore/example/`. Coverage: [`dev/coverage/blobstore.md`](dev/coverage/blobstore.md).
- `vfs/` — `vfs.New(fs.FS)` + `vfs.NewReader`; the public user-implementable VFS (`vfs.Register` with `vfs.VFS`/`vfs.File`, optional `vfs.ShmFile` for WAL, `vfs.NoLock`, `vfs.Wrap` instrumentation; dispatcher in `register.go`/`iomethods.go`/`shm.go`); `vfs/cksm` (page checksums), `vfs/mvcc` (snapshot-isolation in-memory), `vfs/memdb` (plain in-memory). `vfs/crypto` (encryption + `crypto.Open`) is a **separate module** (`gosqlite.org/vfs/crypto`, its own `go.mod` + `replace gosqlite.org => ../..`) so its adiantum / x/crypto deps stay out of the root graph; example under `vfs/crypto/example/`. The root package no longer imports it — encryption is opened via `crypto.Open`, and the root `Config.VFSCloser` seam lets any VFS module bundle teardown into `db.Close()`.
- `pcache/` — application-controlled page cache (`InstallBoundedLRU` over `SQLITE_CONFIG_PCACHE2`, off-heap blocks + the 11 PCACHE2 trampolines via `internal/cabi`).
- `internal/` — `cabi/` (the C-ABI primitives), `sqlid/` (SQL-identifier toolkit), `obs/` (slog level-dispatch), `raceskip/`, `testhelp/`.
- `ext/` — loadable Go extensions, one sub-package per ext, each with an `auto/` blank-import. Inventory + status: [`dev/coverage/ext.md`](dev/coverage/ext.md). `ext/internal/filevtab/` holds the file-vtab scaffolding shared by `ext/csv` + `ext/lines`.
- `tests/sql/` — SQL conformance suite, organized by SQLite Language Reference category.
- `examples/` — runnable examples grouped by reader intent: `migrating/`, `getting-started/`, `features/{search,vfs,extensions,advanced}/`. `examples/README.md` is the router. Smoke-tested by `just examples`; run one with `just example <leaf-or-subpath>`. (gorm examples live in the `gorm/` module under `gorm/examples/`.)

Dot-prefixed top-level dirs (e.g. `.plans/`) are local-only working state, gitignored; nothing in the module references them by name.

---

## Fragile invariants you must not break

1. **libc version pin.** `modernc.org/sqlite/lib` is transpiled C tied to a specific `modernc.org/libc` version — bumping one without the other breaks the ABI. Use `just bump-modernc vX.Y.Z` (libc follows via `go mod tidy`); inspect with `just libc-pin`. The single most likely source of "behaves erratically after a bump."
2. **Two driver names, one singleton.** `sql.Register("sqlite", drv)` and `sql.Register("sqlite3", drv)` register the same `*Driver`; `RegisterFunction` / `RegisterConnectionHook` once affects both. Never register two separate instances under the two names.
3. **The C-ABI boundary** (uintptr↔unsafe.Pointer, `internal/cabi`, named-field struct literals, the bump-time field-list recheck) — see [`dev/architecture.md`](dev/architecture.md). Don't restyle the casts; re-check the struct field lists by hand on every modernc bump.
4. **database/sql pool semantics.** Hooks (Update/Authorizer/Trace/Commit/Rollback/PreUpdate) are **per-connection**; `db.Exec`/`db.Query` may pick any pooled conn. Tests installing a hook must pin the pool — `internal/testhelp.OpenPinned(t, dsn)` + `testhelp.RawConn(sc, fn)` is the canonical fixture.
5. **sqlite-vec quirks.** `INSERT OR REPLACE` is not honored by vec0 (use `(*Table).Update`); vec0's column parser rejects quoted identifiers (we validate via `validIdent`); `LIMIT`/`k` must be inlined as a literal (the planner needs it visible alongside MATCH); metric keywords are `l1`/`l2`/`cosine` (`Dot` aliases L1); `modernc.org/sqlite/vec` isn't transpiled for every GOOS/GOARCH (CI tolerates `vec/` build failures).
6. **SQLite version** is whatever `modernc.org/sqlite` ships — we don't pin or fork SQLite itself.
7. **Userauth is dropped** upstream; we reject `_auth*` DSN flags with a clear error. Don't reintroduce it.

---

## Conventions

- **Lint directives — two flavors.** `staticcheck` honors `//lint:ignore`; `golangci-lint` honors `//nolint:staticcheck`. Where both are needed, use both.
- **errcheck path-scoped suppression.** `.golangci.yml` disables errcheck for the modernc-derived files (`conn|driver|stmt|rows|tx|backup|backup_factory|sqlite|vtab|pre_update_hook|fcntl`), the `gorm/` port, tests, and examples. **New code in new files is fully checked** — don't smuggle new logic into an excluded file to dodge errcheck.
- **`interface{}` is `any`.** Always.
- **Test fixtures.** `internal/testhelp.OpenPinned` + `RawConn` is the canonical pinned-conn helper; per-sub-package fixtures (`vec/table_test.go::openDB`, `fts/fts_test.go::openDB`, `vfs/crypto/crypto_test.go::freshKey`, `tests/sql/helper_test.go::openDB`, …) handle domain seeding. Reuse them.
- **Comments: WHY not WHAT.** A well-named identifier already says what; comments explain the non-obvious choice, the invariant preserved, or the upstream contract honored.
- **Markdown:** never hard-wrap prose (single long lines per paragraph; the renderer decides width). No version numbers in prose. No "Recent additions" / "Unreleased" holding sections — new rows go into their feature-section home.

---

## Common tasks

| Task | Command |
|---|---|
| Build / test / lint | `just build` · `just test` · `just lint` |
| One named test | `just test-one TestBLOB_` |
| Race detector | `just test-race` |
| Format check / apply | `just fmt-check` / `just fmt` |
| Run / smoke-test examples | `just example <name>` / `just examples` |
| Cross-build CI targets | `just cross-build` |
| Full CI locally | `just ci` |
| Benchmarks | `just bench` |
| Bump modernc / inspect libc pin | `just bump-modernc vX.Y.Z` / `just libc-pin` |
| List recipes | `just --list` |

`just` is convenience over vanilla `go test ./...`, not a build dependency.

---

## When asked to add a new feature

First: **does the typed API or the raw SQL path own this?**

- **Raw SQL / conn-level** (DSN flag, hook, `Conn.Raw` method) → root package; touches modernc-derived files, be conservative.
- **Vector** → `vec/` (typed `Table`). **Full-text** → `fts/`. **gorm** dialector/Migrator → the separate `gosqlite.org/gorm` module. **Rank fusion** → `fusion/` (pure Go). ORM-level vector/FTS search lives in the **liteorm** project, not here.
- **Encryption / VFS** → `vfs/crypto/` or the public `vfs/` interface; do not patch the transpilation pipeline; honor the struct-drift discipline.
- **Loadable extension** → `ext/<name>/` with a `Register(*Conn) error` + a sibling `ext/<name>/auto/` blank-import; track status in [`dev/coverage/ext.md`](dev/coverage/ext.md).
- **SQL conformance tests** → `tests/sql/`. **Observability** → `Wrap(...)` decorators (the per-package `Recorder` shape difference is intentional).

Always:

1. Add tests in the package's `*_test.go` (prefer integration tests over the public API). Run `just lint` and `just test` before reporting done. If you touched modernc-derived files, confirm the non-modernc packages still build and pass.
2. Don't quote test counts in user-facing docs — describe behavior, not numbers.
3. **Update every doc the change touches, in the same change.** Doc drift is the #1 failure mode here. The set:
   - `doc.go` for the package whose API moved (pkg.go.dev surface).
   - `README.md` only if the change affects the landing page (feature bullet, comparison table, overview table) — keep README lean; deep content lives in `docs/`.
   - `docs/<section>/<page>.md` — the user-facing guide/reference/extension page for the feature (this is where most narrative belongs now).
   - **`skills/<name>/SKILL.md`** — the agent-usage recipe. **Skills ship to consumers and go stale silently; treat updating them as part of the feature, not optional.** Add a new skill folder when a feature is a distinct task an agent would do.
   - `dev/coverage/<area>.md` for any vec / fts / gorm / vfs / ext / raw-SQL surface change (status flips, new test pins).
   - Never link `ARTICLE-EN.md` / `ARTICLE-RU.md` from any onboarding/consumer doc; never touch `ARTICLE-RU.md`.

---

## When asked to bump dependencies

- **`modernc.org/sqlite`** → `just bump-modernc vX.Y.Z`, then `just test` + `just cross-build`; the libc pin follows via `go mod tidy`. Re-check the struct field lists ([`dev/architecture.md`](dev/architecture.md)).
- **`gorm.io/gorm`** → `go get` directly, then `go test ./gorm/...`; major bumps occasionally need `Dialector.Initialize` tweaks (we gate `RETURNING` on the SQLite feature-introduction version in `gorm/sqlite.go`).
- **Anything else** (standalone `modernc.org/libc`, `golang.org/x/sys`) — don't bump independently if implied by a modernc upgrade; the libc pin is the most fragile part of the graph.

---

## Where to look for what

| Question | File |
|---|---|
| DSN flag translation | `dsn.go::translateMattnDSN` |
| `RegisterFunc` / `RegisterAggregator` | `compat_register.go` + `compat_convert.go` |
| C→Go callback trampolines | `hooks.go`, `pre_update_hook.go`, `vtab.go` |
| The Go↔C function-pointer dance | `internal/cabi/funcptr.go` (`FuncPointer` / `AsFunc`) + `callx.go`; token/pointer maps in `registry.go` / `ptrmap.go` |
| Encryption-at-rest / corruption detection | `vfs/crypto/` / `vfs/cksm/` (+ their `doc.go`); `(*Conn).EnableChecksums` in `fcntl.go` |
| Incremental BLOB I/O | `blob.go::OpenBlob` + `*Blob` |
| WAL / snapshots / progress / db_config | `wal.go`, `snapshot.go`, `control.go` |
| Changesets (SESSION) | `session.go` — `CreateSession` → `*Session`, `ApplyChangeset`, `InvertChangeset`, `ConcatChangesets`. Example: `examples/features/advanced/session` |
| Custom R-Tree geometry/query | `rtree.go` (`RegisterRTreeGeometry`/`RegisterRTreeQuery`); `ext/rtree` ships a `circle` geometry |
| Stmt introspection (Column*/Bind*) | `stmt.go::ColumnCount` (for `ext/statement` + `ext/pivot`) |
| Honor `sqlite3_interrupt` mid-vtab-loop | `(*Conn).IsInterrupted()` in `conn.go` (used by `ext/closure`/`spellfix1`/`pivot`) |
| Stmt-cache telemetry | `(*Conn).StmtCacheStats()` in `stmt_cache.go` |
| Column metadata / runtime stats / txn state | `introspect.go` + `(*Stmt).Readonly`/`Status` in `stmt.go` |
| cksm/crypto chaining | `vfs/crypto/crypto.go::Options.WrapVFS` + per-package `fileMap` (`cabi.PtrMap[FS]`) |
| vtab xCreate/xConnect split | `module.go::CreateModuleSplit` (used by `ext/bloom`, `ext/spellfix1`) |
| Shared SQL-identifier toolkit | `internal/sqlid/sqlid.go` |
| gorm Dialector / AutoMigrate | `gorm/sqlite.go::Dialector` / `gorm/migrator.go::recreateTable` |
| Embedding serialization / FTS SQL | `vec/encoding.go` / `fts/fts.go::buildSearchSQL` |
| vtab calling back into SQL on its host conn | `vtab_nested_prepare_test.go` pins it; `ext/closure`/`pivot`/`statement` use it |
| io.ReaderAt VFS / in-memory VFSes | `vfs/vfs.go::NewReader` / `vfs/mvcc/` / `vfs/memdb/` |

---

## Things that look broken but aren't

If you find yourself "fixing" any of these, stop and re-read — each is a deliberate, already-debated choice:

- **`mutex.Lock(); mutex.Unlock()` with no body** (conn.go) — the documented `sqlite3_unlock_notify` handshake.
- **`driver.Execer` / `driver.Queryer`** marked deprecated — we implement both deprecated and Context variants so `database/sql` finds them on any Go version. Keep both.
- **nil collapse on empty BLOB reads** — modernc returns `nil` for `bytes==0`; we surface a `len==0` `[]byte` (`blob_test.go::TestBLOB_EmptyBLOB`).
- **No `INSERT OR REPLACE` in `vec.Insert`** — vec0 rejects it; use `Update`. **`vec.KNN` inlines LIMIT as a literal** — the planner needs it.
- **`TestLoadExtension_*` skipped under `-race` / darwin / windows** — modernc's `_sqlite3LoadExtension` pointer arithmetic trips checkptr; libc's dlopen/LoadLibraryW shims abort with "TODOTODO" before our error path. Opt-outs via `internal/raceskip` + `platform_test.go`. linux is fine.
- **CI `build_all_targets` swallows a `vec/` build failure** — `modernc.org/sqlite/vec` isn't transpiled for every arch; the fallback lets us catch real regressions in the rest of the module.

---

## Coverage matrices (does it support X?)

Living tables under [`dev/coverage/`](dev/coverage/): `gorm.md`, `vec.md`, `fts.md`, `sql.md`, `ext.md`, `vfs.md`, `conn.md`, `session.md` — each records status (✓/⚠/✗) and the test that exercises it. Upstream-suite reproduction recipes: [`dev/upstream/`](dev/upstream/). Each file has a "last reviewed" footer; re-walk the affected matrix when you bump a dep. The `⚠ inherited` cells are honest gaps — flipping one to ✓ is natural next-step work.

## Last words

When in doubt, find an existing parallel feature and mirror it. The `Observable` wrappers (`vec/observability.go` / `fts/observability.go`) and the `compat_*.go` shim layer are the canonical templates.

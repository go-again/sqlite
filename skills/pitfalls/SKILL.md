---
name: pitfalls
description: Use when debugging surprising behaviour with gosqlite, or as a pre-ship checklist — the gotchas around per-conn hooks, the connection pool, vec quirks, bind limits, and what is not supported.
---

# Pitfalls & gotchas

## Connection-pool surprises

- **Per-conn state needs a pinned conn.** Hooks (update/authorizer/commit/trace/pre-update), custom functions registered via `Conn.Raw`, and `CREATE VIRTUAL TABLE` module registrations live on ONE connection. `db.Exec`/`db.Query` may pick any pooled conn, so "install once on `db`" is fragile. Fix: `db.SetMaxOpenConns(1)` + `db.Conn(ctx)` + `sc.Raw(...)`, then drive traffic through `sc`. For pool-wide function/vtab registration, use the `/auto` blank-import or `Driver.RegisterConnectionHook`.
- **`:memory:` is private per connection.** Each pooled conn gets its own empty database. For a shared in-memory DB use `sqlite.OpenShared(name)` or `db.SetMaxOpenConns(1)`.

## sqlite-vec

- **No `INSERT OR REPLACE`** on vec0 — use `(*Table).Update`.
- **Metadata/partition columns reject NULL** (only auxiliary columns allow it).
- **`KNN inside a gorm.Transaction` is not supported** — run it outside the transaction.
- `bit[N]` needs `N%8==0`; int8 quantization needs values in [-1, 1].

## BLOBs

- **`col || zeroblob(delta)` silently truncates.** SQLite drops `zeroblob` operands under `||` (`x'0102' || zeroblob(10)` has length 2, not 12); `cast`/`substr` don't materialize it either. You cannot grow a value that way — you get a shorter blob and no error. To pre-size, `INSERT … VALUES (zeroblob(N))` or `UPDATE SET col = zeroblob(N)`, then write with `Conn.OpenBlob` / `writeblob`.
- **`Blob` (`OpenBlob`) is fixed-size.** `WriteAt` past `Size()` errors; growth means re-sizing the row. For an unbounded, growable, randomly-writable byte stream (files, uploads), use **`gosqlite.org/blobstore`** instead of hand-rolling chunk tables.

## Other

- **Bind values cap at ~2 GiB** (one BLOB/TEXT parameter < INT_MAX bytes) — surfaced as an error.
- **`_auth*` DSN flags are rejected** — userauth was removed upstream. Remove them when migrating from mattn.
- **libc version pin:** your downstream `go.mod` must use the `modernc.org/libc` version this module pins, or the C ABI drifts; `go mod tidy` against this module maintains it.
- **Tokenizer transforms are symmetric** — a reversible FTS5 tokenizer transform is invisible to MATCH (applied to both doc and query). Inspect indexed terms via fts5vocab.
- **Coexisting with mattn** in one binary requires registering one driver under a custom name (both claim `"sqlite3"`); blank-importing both panics at init.
- **A query is unexpectedly slow?** Inspect its plan without re-preparing: `(*Stmt).Explain(sqlite.ExplainQueryPlan)` flips a prepared statement to EXPLAIN QUERY PLAN mode (bound parameters carry over); `ExplainOff` flips it back to run normally.

## Not supported

Userauth; SQLCipher on-disk format / MAC (encryption is confidentiality-only); cross-process WAL on a custom (non-platform) VFS; beating mattn on hot per-row UDF throughput.

When something behaves unexpectedly, check this list first, then the relevant feature skill, then [`docs/`](../../docs/).

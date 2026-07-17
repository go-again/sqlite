# Advanced — connection-level controls

Lower-level capabilities. Several use raw SQL on a pinned connection because the feature *is* connection-level — that's the right tool here, not legacy style.

| example | what it shows |
|---|---|
| [`hooks`](hooks/) | per-conn update / authorizer / commit / trace hooks, with the `MaxOpenConns(1)` + `db.Conn` + `sc.Raw` pinning idiom |
| [`session`](session/) | the SESSION extension — record changes into a changeset, replay onto a replica with `ApplyChangeset`, undo via `InvertChangeset` (offline sync / replication, pure Go) |
| [`backup`](backup/) | the `(*Conn).Backup` factory + top-level `sqlite.Serialize` / `Deserialize` round-trip |
| [`pcache`](pcache/) | `pcache.InstallBoundedLRU(maxPages)` — bound and observe SQLite's page-cache heap (hit / miss / eviction / live counters) |
| [`busy-handler`](busy-handler/) | `RegisterBusyHandler` with exponential back-off under real lock contention |
| [`collation-needed`](collation-needed/) | `CollationNeeded` (lazy collation registration) + `AnyCollationNeeded` |
| [`udf-context`](udf-context/) | `FunctionContext` aux-data — cache a compiled regexp across rows in a scalar UDF |
| [`sqlitex`](sqlitex/) | the `sqlitex` helpers — `embed.FS` migrations, `Transaction`, deferred savepoints, `Execute`, scalar reads |
| [`window-function`](window-function/) | a custom Go window function via `Conn.RegisterWindowFunction` |
| [`stmt-explain`](stmt-explain/) | `(*Stmt).Explain` / `IsExplain` — read a query's EXPLAIN QUERY PLAN and then run it, from one prepared statement |
| [`vtab-planning`](vtab-planning/) | `VTabDistinct` from a virtual table's `BestIndex` — read how far the query relaxes ordering/duplication so the module can skip work |
| [`vtab-overload`](vtab-overload/) | `VTabFunctionFinder` (xFindFunction) + `Conn.OverloadFunction` — a virtual table overrides a SQL function on its own columns |
| [`blobstore`](blobstore/) | large, growable byte objects (`gosqlite.org/blobstore`, a separate module) — `io.WriterAt`/`io.ReaderAt`, sparse holes, truncate, optional transparent compression |

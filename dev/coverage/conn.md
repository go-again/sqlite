# Coverage — connection-level methods (WAL / snapshot / introspection / config)

Typed `(*Conn)` / `(*Stmt)` methods that bind SQLite C-API surface beyond the `database/sql` contract. These complement the SQL-feature matrix in [coverage-sql.md](sql.md) (which is scoped to SQL clauses / PRAGMAs) and the SESSION matrix in [coverage-session.md](session.md). All are thin binds over already-compiled symbols.

## Introspection & telemetry — [`introspect.go`](../../introspect.go), [`stmt.go`](../../stmt.go)

| Method | C symbol | What it returns | Test |
|---|---|---|---|
| `(*Conn).TableColumnMetadata` | `sqlite3_table_column_metadata` | decltype / collation / notnull / pk / autoinc, no SELECT needed | `TestTableColumnMetadata`, `TestTableColumnMetadata_SpecialModes` |
| `(*Conn).Status` | `sqlite3_db_status` | per-conn cache/lookaside counters (current + high-water) | `TestConnStatus`, `TestConnStatus_Reset` |
| `(*Conn).TxnState` | `sqlite3_txn_state` | none / read / write | `TestConnTxnState`, `TestConnTxnState_Read` |
| `(*Stmt).Readonly` | `sqlite3_stmt_readonly` | does the stmt write? (read/write routing) | `TestStmtReadonly` |
| `(*Stmt).Status` | `sqlite3_stmt_status` | per-stmt VM-step / sort / fullscan counters | `TestStmtStatus` |
| `(*Stmt).Explain` / `IsExplain` | `sqlite3_stmt_explain` / `_isexplain` | flip a prepared stmt to EXPLAIN / EXPLAIN QUERY PLAN at runtime (no re-prepare, params carry over) and read the current mode | `TestStmtExplain` |
| `(*Conn).Filename` | `sqlite3_db_filename` | on-disk path of a schema ("" for in-memory/temp) | `TestConn_FilenameAndAutoCommit` |
| `(*Conn).AutoCommit` | `sqlite3_get_autocommit` | true at rest, false inside a transaction | `TestConn_FilenameAndAutoCommit` |
| `(*Conn).ErrorOffset` | `sqlite3_error_offset` | byte offset of the last parse error's token (-1 if none) | `TestConn_ErrorOffset` |
| `(*Conn).CacheFlush` | `sqlite3_db_cacheflush` | flush dirty pages mid-transaction (no PRAGMA equivalent) | `TestConn_CacheFlushAndFileControl` |
| `(*Conn).SetFileControlInt` / `ResetCache` | `sqlite3_file_control` (int ops / `RESET_CACHE`) | generic int file-control escape hatch; drop page cache | `TestConn_CacheFlushAndFileControl` |

### Package-level C-API helpers — [`runtime.go`](../../runtime.go)

Process-global, connection-independent (serialized through one mutex-guarded TLS).

| Function | C symbol | Notes | Test |
|---|---|---|---|
| `KeywordCount` / `KeywordName` / `IsKeyword` | `sqlite3_keyword_count` / `_name` / `_check` | the build's reserved-word set (authoritative for identifier quoting) | `TestRuntime_Keywords` |
| `CompileOptionUsed` / `CompileOptionGet` | `sqlite3_compileoption_used` / `_get` | probe the build's feature flags | `TestRuntime_CompileOptions` |
| `StrGlob` / `StrLike` / `Complete` | `sqlite3_strglob` / `_strlike` / `_complete` | exact GLOB/LIKE without a query; statement-boundary check | `TestRuntime_StringUtils` |

Not wrapped: `sqlite3_memory_used` / heap-limit / `status64` — modernc disables SQLite's memstat (they return 0); use Go's `runtime/metrics` instead.

## Virtual-table authoring helpers — [`module.go`](../../module.go), [`vtab.go`](../../vtab.go)

Beyond `CreateModule` / `CreateEponymousModule` / `CreateModuleSplit` / `DeclareVTab`, the advanced hooks a Go-implemented module can use:

| Symbol | C symbol | What it does | Test |
|---|---|---|---|
| `(*Conn).OverloadFunction` | `sqlite3_overload_function` | declare a stub function so a module's xFindFunction can give a name (e.g. `MATCH`) a table-specific meaning; the name is then accepted at prepare time | `TestOverloadFunction` |
| `VTabFunctionFinder` (optional `VTab` interface) | `xFindFunction` | a module overrides a SQL function applied to its columns — return a Go scalar impl + an op (0 = plain override; ≥150 = an indexable-constraint operator like `MATCH`). Registered ids are cached per (name, nArg) and freed on disconnect | `TestVTabFindFunction`, `examples/features/advanced/vtab-overload` |
| `VTabDistinct` / `VTabDistinctMode` | `sqlite3_vtab_distinct` | called from `BestIndex`, reports how much the query relaxes row ordering/duplication so the module can skip work (grouped / distinct / unordered) | `TestVTabDistinct` |

`⚠ vtab_rhs_value` (constraint RHS at plan time) is ready to add on the same `bestIndexRawCtx` plumbing; the `vtab_in` batch-`IN` family and `vtab_nochange` need a vtab-API extension (raw values inside Filter/Column) — tracked in `.plans/plan-gosqlite-feature-backlog.md`.

## WAL control — [`wal.go`](../../wal.go)

| Method | C symbol | Notes | Test |
|---|---|---|---|
| `(*Conn).WALCheckpoint` | `sqlite3_wal_checkpoint_v2` | mode (PASSIVE/FULL/RESTART/TRUNCATE) + returned log/checkpointed frame counts | `TestWALCheckpoint` |
| `(*Conn).WALAutoCheckpoint` | `sqlite3_wal_autocheckpoint` | auto-checkpoint frame threshold | `TestWALCheckpoint` |
| `(*Conn).RegisterWALHook` | `sqlite3_wal_hook` | post-commit callback; non-nil error fails the commit; replaces auto-checkpoint | `TestWALHook`, `TestWALHook_Error` |

## Snapshot (WAL point-in-time) — [`snapshot.go`](../../snapshot.go)

| Method | C symbol | Test |
|---|---|---|
| `(*Conn).GetSnapshot` | `sqlite3_snapshot_get` | `TestSnapshot`, `TestSnapshot_CmpAndGuards` |
| `(*Conn).OpenSnapshot` | `sqlite3_snapshot_open` | `TestSnapshot_OpenAndRecover`, `TestSnapshot_CmpAndGuards` (closed/nil guards) |
| `(*Conn).SnapshotRecover` | `sqlite3_snapshot_recover` | `TestSnapshot_OpenAndRecover` |
| `(*Snapshot).Cmp` / `Close` | `sqlite3_snapshot_cmp` / `_free` | `TestSnapshot_CmpAndGuards` |

## Progress & config — [`control.go`](../../control.go)

| Method | C symbol | Notes | Test |
|---|---|---|---|
| `(*Conn).SetProgressHandler` | `sqlite3_progress_handler` | periodic VM-instruction callback; return true to interrupt | `TestProgressHandler` |
| `(*Conn).RegisterBusyHandler` | `sqlite3_busy_handler` | programmable lock-contention retry (adaptive/jittered back-off); the callback alternative to a fixed busy_timeout | `TestRegisterBusyHandler` |
| `(*Conn).SetDBConfig` / `QueryDBConfig` | `sqlite3_db_config` | boolean flags incl. security-only DEFENSIVE / TRUSTED_SCHEMA / WRITABLE_SCHEMA (no PRAGMA equivalent), via `libc.VaList` | `TestDBConfig`, `TestDBConfig_Effect` |

## Lazy collation registration — [`collation_needed.go`](../../collation_needed.go)

| Method | C symbol | Notes | Test |
|---|---|---|---|
| `(*Conn).CollationNeeded` | `sqlite3_collation_needed` | fires when a statement references an undefined collation; handler defines it on demand (typically via `RegisterCollation`) | `TestCollationNeeded_Custom` |
| `(*Conn).AnyCollationNeeded` | (built on the above) | defines every unknown collation as byte-wise/BINARY so foreign schemas open/ATTACH/restore without "no such collation sequence" | `TestCollationNeeded_AnyFakesBinary`, `TestCollationNeeded_DrainOnClose` |

## UDF function-context substrate — [`function_context.go`](../../function_context.go)

Methods on `*FunctionContext` (the value passed to scalar / aggregate / window callbacks).

| Method | C symbol | What it does | Test |
|---|---|---|---|
| `(*FunctionContext).ResultSubtype` | `sqlite3_result_subtype` | tag the result value's subtype (applied after the result is set) | `TestFunctionContext_Subtype` |
| `(*FunctionContext).ValueSubtype` | `sqlite3_value_subtype` | read an argument's subtype | `TestFunctionContext_Subtype` |
| `(*FunctionContext).SetAuxData` / `GetAuxData` | `sqlite3_set_auxdata` / `sqlite3_get_auxdata` | cache a Go value against a constant argument and reuse it across rows; auto-released (destructor drains the registry) on finalize | `TestFunctionContext_AuxData` |

## Custom FTS5 tokenizer — [`fts5_tokenizer.go`](../../fts5_tokenizer.go)

| Method | C symbol | Notes | Test |
|---|---|---|---|
| `(*Conn).RegisterFTS5Tokenizer` | `fts5_api.xCreateTokenizer` | registers a Go `FTS5Tokenizer` (the `SELECT fts5(?1)` / `bind_pointer` handshake gets the api); reference it as `tokenize='name'`. Per-connection. No other pure-Go driver exposes this. | `TestRegisterFTS5Tokenizer`, `TestRegisterFTS5Tokenizer_DrainOnClose` |

All per-connection callbacks (WAL hook, progress handler, busy handler, rtree geometry/query, collation-needed, FTS5 tokenizer factory) are stored in process-global registries keyed by `c.db` / a minted id and drained in `(*conn).dropHookHandlers` on Close — see [`hooks.go`](../../hooks.go).

Last reviewed against the transpiled SQLite conn-method surface on 2026-06-13.

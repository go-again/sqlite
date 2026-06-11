# Coverage — connection-level methods (WAL / snapshot / introspection / config)

Typed `(*Conn)` / `(*Stmt)` methods that bind SQLite C-API surface beyond the `database/sql` contract. These complement the SQL-feature matrix in [coverage-sql.md](coverage-sql.md) (which is scoped to SQL clauses / PRAGMAs) and the SESSION matrix in [coverage-session.md](coverage-session.md). All are thin binds over already-compiled symbols.

## Introspection & telemetry — [`introspect.go`](../introspect.go), [`stmt.go`](../stmt.go)

| Method | C symbol | What it returns | Test |
|---|---|---|---|
| `(*Conn).TableColumnMetadata` | `sqlite3_table_column_metadata` | decltype / collation / notnull / pk / autoinc, no SELECT needed | `TestTableColumnMetadata`, `TestTableColumnMetadata_SpecialModes` |
| `(*Conn).Status` | `sqlite3_db_status` | per-conn cache/lookaside counters (current + high-water) | `TestConnStatus`, `TestConnStatus_Reset` |
| `(*Conn).TxnState` | `sqlite3_txn_state` | none / read / write | `TestConnTxnState`, `TestConnTxnState_Read` |
| `(*Stmt).Readonly` | `sqlite3_stmt_readonly` | does the stmt write? (read/write routing) | `TestStmtReadonly` |
| `(*Stmt).Status` | `sqlite3_stmt_status` | per-stmt VM-step / sort / fullscan counters | `TestStmtStatus` |

## WAL control — [`wal.go`](../wal.go)

| Method | C symbol | Notes | Test |
|---|---|---|---|
| `(*Conn).WALCheckpoint` | `sqlite3_wal_checkpoint_v2` | mode (PASSIVE/FULL/RESTART/TRUNCATE) + returned log/checkpointed frame counts | `TestWALCheckpoint` |
| `(*Conn).WALAutoCheckpoint` | `sqlite3_wal_autocheckpoint` | auto-checkpoint frame threshold | `TestWALCheckpoint` |
| `(*Conn).RegisterWALHook` | `sqlite3_wal_hook` | post-commit callback; non-nil error fails the commit; replaces auto-checkpoint | `TestWALHook`, `TestWALHook_Error` |

## Snapshot (WAL point-in-time) — [`snapshot.go`](../snapshot.go)

| Method | C symbol | Test |
|---|---|---|
| `(*Conn).GetSnapshot` | `sqlite3_snapshot_get` | `TestSnapshot`, `TestSnapshot_CmpAndGuards` |
| `(*Conn).OpenSnapshot` | `sqlite3_snapshot_open` | `TestSnapshot_OpenAndRecover`, `TestSnapshot_CmpAndGuards` (closed/nil guards) |
| `(*Conn).SnapshotRecover` | `sqlite3_snapshot_recover` | `TestSnapshot_OpenAndRecover` |
| `(*Snapshot).Cmp` / `Close` | `sqlite3_snapshot_cmp` / `_free` | `TestSnapshot_CmpAndGuards` |

## Progress & config — [`control.go`](../control.go)

| Method | C symbol | Notes | Test |
|---|---|---|---|
| `(*Conn).SetProgressHandler` | `sqlite3_progress_handler` | periodic VM-instruction callback; return true to interrupt | `TestProgressHandler` |
| `(*Conn).SetDBConfig` / `QueryDBConfig` | `sqlite3_db_config` | boolean flags incl. security-only DEFENSIVE / TRUSTED_SCHEMA / WRITABLE_SCHEMA (no PRAGMA equivalent), via `libc.VaList` | `TestDBConfig`, `TestDBConfig_Effect` |

All per-connection callbacks (WAL hook, progress handler, rtree geometry/query) are stored in process-global registries keyed by `c.db` / a minted id and drained in `(*conn).dropHookHandlers` on Close — see [`hooks.go`](../hooks.go).

Last reviewed against the transpiled SQLite conn-method surface on 2026-06-12.

---
title: Hooks, backup & introspection
description: Per-connection hooks (update/authorizer/commit/trace), backup, serialize/deserialize, and runtime introspection.
sidebar:
  order: 14
---

# Hooks, backup & introspection

Lower-level driver capabilities. Most are connection-level — install them on a **pinned** connection, because `database/sql`'s pool fan-out makes "install once on `db`" fragile.

## Hooks

Per-connection update / authorizer / commit / rollback / pre-update / trace callbacks:

```go
db.SetMaxOpenConns(1)
sc, _ := db.Conn(ctx)
sc.Raw(func(dc any) error {
	c := dc.(*sqlite.Conn)
	c.RegisterUpdateHook(func(op int, dbName, table string, rowid int64) { /* ... */ })
	c.RegisterAuthorizer(func(op int, a1, a2, dbName, trig string) int { return sqlite.SQLITE_OK })
	return c.SetTrace(&sqlite.TraceConfig{EventMask: sqlite.TraceStmt, Callback: /* ... */})
})
// Drive traffic through sc.ExecContext, NOT db.Exec.
```

Runnable: [`examples/features/advanced/hooks/`](../../examples/features/advanced/hooks/main.go) (full update / authorizer / commit / trace round-trip with the conn-pinning idiom).

## Backup, serialize, deserialize

- `(*Conn).Backup(destSchema, srcConn, srcSchema)` — the mattn-compat factory driving `sqlite3_backup_*` against an open destination connection (caller-owned).
- `sqlite.Serialize(ctx, db) []byte` snapshots an in-memory database into a byte buffer; `sqlite.Deserialize(ctx, db, buf)` restores it. Useful for ship-a-DB-as-state and state-machine tests.

Runnable: [`examples/features/advanced/backup/`](../../examples/features/advanced/backup/main.go).

## Introspection & telemetry

On `*sqlite.Conn`: `TableColumnMetadata` (decltype / collation / PK / autoinc without a SELECT), `Status` (SQLite cache/lookaside counters), `TxnState`, `StmtCacheStats` (prepared-statement LRU hit/miss/eviction), `CollationNeeded` / `AnyCollationNeeded` (define collations lazily). On `*sqlite.Stmt`: `Readonly`, `Status` (VM-step / sort / fullscan counters). The `*FunctionContext` passed to a UDF exposes result/argument subtypes and per-argument aux-data caching (`SetAuxData` / `GetAuxData`) — see [`examples/features/advanced/udf-context/`](../../examples/features/advanced/udf-context/main.go).

WAL control (`WALCheckpoint`, `WALAutoCheckpoint`, `RegisterWALHook`), snapshots (`GetSnapshot` / `OpenSnapshot` / `SnapshotRecover`), the progress handler, and `db_config` security flags round out the conn-level surface. Full matrix: [`dev/coverage/conn.md`](../../dev/coverage/conn.md).

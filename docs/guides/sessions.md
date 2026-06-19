---
title: Sessions & changesets
description: Record database changes into changesets/patchsets, apply them to a replica, invert and concat — the offline-sync primitive.
sidebar:
  order: 13
---

# Sessions & changesets (SESSION extension)

The SESSION extension records the changes made on a database into a **changeset** (or **patchset**), which you can serialize, ship, and replay onto another database — the foundation for offline sync, audit logs, and lightweight replication. `SQLITE_ENABLE_SESSION` is compiled into the lib; no other pure-Go SQLite driver exposes this.

```go
sc, _ := db.Conn(ctx)
sc.Raw(func(dc any) error {
	c := dc.(*sqlite.Conn)
	sess, _ := c.CreateSession("main")
	defer sess.Close()
	sess.Attach("")            // track all tables (or a specific table name)

	// ... run INSERT/UPDATE/DELETE on c ...

	cs, _ := sess.Changeset()  // []byte; or sess.Patchset()
	// ship cs to a replica, then on the replica's conn:
	return replicaConn.ApplyChangeset(cs,
		sqlite.WithConflictHandler(myHandler),
		sqlite.WithTableFilter(myFilter))
})
```

API surface (on `*sqlite.Conn`): `CreateSession` → `*Session` (`Attach` / `Enable` / `Changeset` / `Patchset` / `Diff` / `Close`); `ApplyChangeset` (with `WithConflictHandler` / `WithTableFilter`), `InvertChangeset` (undo), `ConcatChangesets` (merge).

Runnable: [`examples/features/advanced/session/`](../../examples/features/advanced/session/main.go) — record on a primary, replay onto a replica, then undo via `InvertChangeset`.

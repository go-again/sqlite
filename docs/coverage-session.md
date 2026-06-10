# Coverage — SESSION extension (changesets / patchsets)

The SQLite [SESSION extension](https://sqlite.org/sessionintro.html) records changes to a database and serializes them as changeset/patchset blobs that can be inverted, concatenated, and applied to another database with conflict resolution. `SQLITE_ENABLE_SESSION` is compiled into the transpiled lib; this module exposes it as a typed Go API in [`session.go`](../session.go). No other pure-Go SQLite driver (modernc, ncruces) exposes it.

The API lives in the root package because it needs the connection's unexported handles; the apply callbacks dispatch through static trampolines + an id registry, the same shape as the R-Tree and scalar-UDF machinery.

## Surface

| Capability | API | C symbol | Status | Test |
|---|---|---|---|---|
| Start recording | `(*Conn).CreateSession(schema)` | `sqlite3session_create` | ✓ | `TestSession_CaptureAndApply` |
| Attach tables | `(*Session).Attach(table)` (""=all) | `sqlite3session_attach` | ✓ | `captureUsers` |
| Enable/disable | `(*Session).Enable` / `IsEnabled` | `sqlite3session_enable` | ✓ | `TestSession_EnableDisable` |
| Empty check | `(*Session).IsEmpty` | `sqlite3session_isempty` | ✓ | `TestSession_EnableDisable` |
| Serialize changeset | `(*Session).Changeset()` | `sqlite3session_changeset` | ✓ | `TestSession_CaptureAndApply` |
| Serialize patchset | `(*Session).Patchset()` | `sqlite3session_patchset` | ✓ | `TestSession_Patchset` |
| Diff two databases | `(*Session).Diff(fromSchema, table)` | `sqlite3session_diff` | ✓ (shipped; integration test deferred) | — |
| Close | `(*Session).Close()` | `sqlite3session_delete` | ✓ | all |
| Invert | `(*Conn).InvertChangeset(cs)` | `sqlite3changeset_invert` | ✓ | `TestSession_Invert` |
| Concat | `(*Conn).ConcatChangesets(a, b)` | `sqlite3changeset_concat` | ✓ | — |
| Apply (+ conflict handler, table filter) | `(*Conn).ApplyChangeset(cs, opts…)` | `sqlite3changeset_apply_v2` | ✓ | `TestSession_CaptureAndApply` / `ConflictReplace` / `ConflictAbortByDefault` / `TableFilter` |

Conflict types (`ConflictData` / `NotFound` / `Conflict` / `Constraint` / `ForeignKey`) and actions (`ChangesetOmit` / `Replace` / `Abort`) are typed. With no `WithConflictHandler`, conflicts abort and roll back (the safe default).

Example: [`examples/session`](../examples/session/main.go) — record changes on a primary, replay onto a replica, undo via the inverse.

## Deferred (follow-ups)

- **Changegroup** (`sqlite3changegroup_*`) — merge many changesets into one before applying.
- **Rebaser** (`sqlite3rebaser_*`) — rebase local changes against an applied remote changeset (3-way-merge-style conflict carry-over).
- **Streaming** (`_strm` variants) — produce/consume changesets via callbacks instead of a single in-memory blob, for changesets too large to buffer.
- **Richer conflict inspection** — surface the per-change iterator (`sqlite3changeset_op` / `_old` / `_new` / `_conflict` / `_pk`) to the `ConflictHandler` so it can read the operation, table, and conflicting values, not just the conflict type.

Last reviewed against the transpiled SQLite SESSION surface on 2026-06-12.

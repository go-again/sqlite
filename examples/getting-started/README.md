# Getting started (the modern way)

If you're new here, start at the top and work down. These use the modern `"sqlite"` driver name and the typed `sqlite.Config` entry — no legacy DSN flags.

| example | what it shows |
|---|---|
| [`database-sql`](database-sql/) | the idiomatic foundation — plain `database/sql`: open, parameterized writes, `rows.Next/Scan`, transactions. Raw SQL here is the standard-library way, not "old". |
| [`config`](config/) | `sqlite.Open(sqlite.Config{…})` — structured setup (pragmas, encryption, pool tuning) as one Go value, with no DSN-string assembly. Plain and encrypted variants. |

For gorm, the dialector is the separate `gosqlite.org/gorm` module — see its examples under `gorm/examples/`.

From here, [`../features/`](../features/) has a reference example for every capability (vector / FTS5 search, custom VFS, the `ext/` catalog, hooks, sessions, …). For the higher-level typed search surfaces specifically, see [`../features/search/`](../features/search/) (vec / FTS5).

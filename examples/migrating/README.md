# Migrating from another SQLite package

One example per source package. The theme is the same throughout: **change the import, keep your code.** This driver registers under both `"sqlite"` (modernc-compatible) and `"sqlite3"` (mattn-compatible), so existing blank imports and DSN strings keep working.

| example | coming from | what changes |
|---|---|---|
| [`from-mattn`](from-mattn/) | `github.com/mattn/go-sqlite3` | blank-import swap; keep the `"sqlite3"` driver name and your `_`-prefixed DSN flags (translated automatically). `_auth*` userauth flags are rejected — that feature was dropped upstream. |
| [`from-modernc`](from-modernc/) | `modernc.org/sqlite` | blank-import swap; same `"sqlite"` driver name and DSN — nothing else. |
| [`from-glebarez`](from-glebarez/) | `glebarez/sqlite` or `gorm.io/driver/sqlite` | swap the gorm dialector import to `github.com/go-again/sqlite/gorm`; the `sqlite.Open(dsn)` call signature is identical, and `TranslateError` behaves the same. |

Once you're running, see [`../getting-started/`](../getting-started/) to move from DSN strings to the typed `sqlite.Config` and the typed search / gorm surfaces — at your own pace, file by file.

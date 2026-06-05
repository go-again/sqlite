# Coverage: gorm

Last reviewed against `gorm.io/gorm v1.31.1` on 2026-05-26.

The version is pinned in this module's `go.mod`. When you bump it, walk
this file top to bottom; the public interfaces are stable but methods do
get added across minor releases. Compile-time satisfaction is enforced by
`gorm/interfaces_test.go`; if gorm grows a method we haven't implemented,
that test fails to build with the method name in the error.

## Status legend

- **✓ typed** — implemented in this module's `gorm/` package and exercised
  by a test in `gorm/*_test.go`.
- **⚠ inherited** — implemented upstream in `gorm.io/gorm/migrator`, used
  unchanged by the embedded `migrator.Migrator` field in our `Migrator`.
  Works because the upstream code works. Not exercised by a test we own.
- **✗** — not implemented; calling it returns whatever the embedded
  upstream default returns (typically an error).

## gorm.Dialector

The primary contract. Every method below is implemented on our `Dialector`
type in `gorm/sqlite.go`.

| Method | Status | Test | Notes |
|---|---|---|---|
| `Name() string` | ✓ typed | `TestDialector` | Returns `"sqlite"`. |
| `Initialize(*DB) error` | ✓ typed | `TestDialector`, `TestGorm_AutoMigrate_CreatesTable` | Registers default callbacks; gates `RETURNING`-aware clauses on the SQLite release that introduced the clause. |
| `Migrator(*DB) Migrator` | ✓ typed | `TestGorm_AutoMigrate_CreatesTable` | Returns our `Migrator{migrator.Migrator{...}}` wrapper. |
| `DataTypeOf(*schema.Field) string` | ✓ typed | `TestGorm_AutoMigrate_CreatesTable` | bool→numeric, int/uint→integer (auto-increment becomes `integer PRIMARY KEY AUTOINCREMENT`), float→real, string→text, time→datetime, bytes→blob. |
| `DefaultValueOf(*schema.Field) clause.Expression` | ✓ typed | covered transitively via AutoMigrate | `NULL` for auto-increment, `DEFAULT` otherwise. |
| `BindVarTo(clause.Writer, *Statement, any)` | ✓ typed | covered transitively | Always emits `?`. |
| `QuoteTo(clause.Writer, string)` | ✓ typed | covered transitively | Backtick-quoted identifiers with `.`-aware delimiting. |
| `Explain(string, ...any) string` | ✓ typed | covered transitively | Delegates to `logger.ExplainSQL` with `"` quote char. |

## gorm.Migrator

Returned by `Dialector.Migrator(db)`. We embed `gorm.io/gorm/migrator.Migrator`
to inherit the long tail of methods and override the SQLite-specific ones.
Tests live in `gorm/integration_test.go`.

### Schema

| Method | Status | Test |
|---|---|---|
| `AutoMigrate(dst ...any) error` | ⚠ inherited | `TestGorm_AutoMigrate_CreatesTable`, `TestGorm_NewWithConfig`, `TestGorm_SQLite3DriverName` |
| `CurrentDatabase() string` | ✓ typed | covered transitively | Overridden to use `PRAGMA database_list`. |
| `FullDataTypeOf(*schema.Field) clause.Expr` | ⚠ inherited | — |
| `GetTypeAliases(string) []string` | ⚠ inherited | — |

### Tables

| Method | Status | Test |
|---|---|---|
| `CreateTable(dst ...any) error` | ⚠ inherited | covered transitively via AutoMigrate |
| `DropTable(dst ...any) error` | ✓ typed | — | Overridden to drop in dependency order with foreign keys off. |
| `HasTable(dst any) bool` | ✓ typed | `TestGorm_AutoMigrate_CreatesTable` | Reads `sqlite_master`. |
| `RenameTable(oldName, newName any) error` | ⚠ inherited | — |
| `GetTables() ([]string, error)` | ✓ typed | — | Reads `sqlite_master WHERE type='table'`. |
| `TableType(dst any) (TableType, error)` | ⚠ inherited | — |

### Columns

| Method | Status | Test |
|---|---|---|
| `AddColumn(dst any, field string) error` | ⚠ inherited | — |
| `DropColumn(dst any, field string) error` | ✓ typed | — | Overridden; uses recreate-table approach (older SQLite releases lacked DROP COLUMN). |
| `AlterColumn(dst any, field string) error` | ✓ typed | — | Recreate-table with new column definition. |
| `MigrateColumn(dst any, *schema.Field, ColumnType) error` | ⚠ inherited | — |
| `MigrateColumnUnique(dst any, *schema.Field, ColumnType) error` | ⚠ inherited | — |
| `HasColumn(dst any, field string) bool` | ✓ typed | — | Reads `sqlite_master.sql`. |
| `RenameColumn(dst any, oldName, field string) error` | ⚠ inherited | — |
| `ColumnTypes(dst any) ([]ColumnType, error)` | ✓ typed | — | Parses DDL via `ddlmod.go`. |

### Views

| Method | Status | Test |
|---|---|---|
| `CreateView(name string, ViewOption) error` | ⚠ inherited | — |
| `DropView(name string) error` | ⚠ inherited | — |

### Constraints

| Method | Status | Test |
|---|---|---|
| `CreateConstraint(dst any, name string) error` | ✓ typed | — | Recreate-table approach. |
| `DropConstraint(dst any, name string) error` | ✓ typed | — | Recreate-table approach. |
| `HasConstraint(dst any, name string) bool` | ✓ typed | — | Regex against `sqlite_master.sql`. |

### Indexes

| Method | Status | Test |
|---|---|---|
| `CreateIndex(dst any, name string) error` | ✓ typed | `TestGorm_AutoMigrate_CreatesTable` (via uniqueIndex tag) |
| `DropIndex(dst any, name string) error` | ✓ typed | — |
| `HasIndex(dst any, name string) bool` | ✓ typed | `TestGorm_AutoMigrate_CreatesTable` |
| `RenameIndex(dst any, oldName, newName string) error` | ✓ typed | — | DROP + recreate with renamed-in-source SQL. |
| `GetIndexes(dst any) ([]Index, error)` | ✓ typed | — | Uses `PRAGMA index_list` + `PRAGMA index_info`. |

## gorm.ErrorTranslator

Optional interface. Activated by `gorm.Config{TranslateError: true}`.

| Method | Status | Test |
|---|---|---|
| `Translate(err error) error` | ✓ typed | `TestErrorTranslator`, `TestGorm_UniqueViolation_TranslatedToErrDuplicatedKey` |

Translation table:

| `*Error.ExtendedCode()` | maps to |
|---|---|
| `SQLITE_CONSTRAINT_UNIQUE` (2067) | `gorm.ErrDuplicatedKey` |
| `SQLITE_CONSTRAINT_PRIMARYKEY` (1555) | `gorm.ErrDuplicatedKey` |
| `SQLITE_CONSTRAINT_FOREIGNKEY` (787) | `gorm.ErrForeignKeyViolated` |
| Everything else | passes through unchanged |

Other `SQLITE_CONSTRAINT_*` codes (CHECK, NOTNULL, TRIGGER, ROWID) intentionally
do not map because gorm has no equivalent sentinel. Surface as the original
`*sqlite.Error`.

## gorm.SavePointerDialectorInterface

Optional interface. Activated when gorm needs nested transactions.

| Method | Status | Test |
|---|---|---|
| `SavePoint(*DB, name string) error` | ✓ typed | covered transitively via `db.Transaction` |
| `RollbackTo(*DB, name string) error` | ✓ typed | covered transitively via `db.Transaction` |

## Optional interfaces we don't currently implement

- `gorm.ParamsFilter` — for filtering query parameters; rare, not needed.
- `gorm.MigratorIndexInterface` — doesn't exist in v1.31.1 as a separate
  interface; index methods are part of `gorm.Migrator`.

## Known behaviour differences from glebarez

- `gorm.ErrorTranslator.Translate` prefers `interface{ ExtendedCode() int }`
  (this module) over `interface{ Code() int }` (glebarez). Both produce
  `gorm.ErrDuplicatedKey` for unique violations; glebarez does it via the
  full extended code, we do it via the same code. End-state is identical.
- Receiver name typo fix: `SavePoint` and `RollbackTo` had a `dialectopr`
  receiver in glebarez; we corrected to `dialector` per `ST1016`.

## Verification recipe

```sh
# Compile-time interface drift catcher.
go test -c -o /dev/null ./gorm/

# Local-tests pass.
just test
```

If gorm bumps and the first command fails, the error message names the
method that was added; decide whether to implement, stub, or document the
gap.

## Upstream suite

Beyond the per-method matrix above, the full `gorm.io/gorm/tests`
integration suite runs against this dialector via a thin shim,
pinned to the same gorm version go.mod
depends on. The setup, reproduction recipe, and reasoning for each
shim flag live in [gorm-upstream.md](gorm-upstream.md). The same
recipe is enforced by the `gorm-upstream` CI job.

## Deep integration: `vec/gorm` and `fts/gorm`

Tag-driven sidecar packages live under `github.com/go-again/sqlite/vec/gorm`
and `github.com/go-again/sqlite/fts/gorm`. They register as gorm
plugins and own the full lifecycle of the sidecar (vec0 virtual table /
FTS5 external-content table + triggers).

### Tag syntax — vec

| Key | Required | Meaning |
|---|---|---|
| `dim=N` | yes | Embedding dimension. |
| `metric=l2 \| cosine \| dot` | no | Distance metric. Default `l2`. |
| `encoding=json \| binary` | no | Wire encoding. Default `binary`. |
| `table=NAME` | no | Override sidecar table name. Default `<source>_vec`. |
| `column=NAME` | no | Override embedding column. Default `embedding`. |

The tagged field's type must be either `vecgorm.Embedding`
(recommended) or `[]float32` with `gorm:"-"` alongside. The wrapper
type implements gorm's `GormDataType` interface so the schema parser
accepts it; the plugin then sets `IgnoreMigration=true` so no column
lands on the source table.

### Tag syntax — fts5

| Key | Required | Meaning |
|---|---|---|
| `tokenize=NAME[+args]` | no | FTS5 tokenize option. Spaces escaped as `+`. |
| `prefix=N1,N2,...` | no | Pre-computed prefix-match index sizes. |
| `column=NAME` | no | Override FTS5 column name (default = lowercase field). |
| `table=NAME` | no | Override FTS5 table. Default `<source>_fts`. |
| `detail=full \| column \| none` | no | FTS5 detail= option. |
| `external=true \| false` | no | External-content mode (default true). false → in-table FTS5 manages text itself. |
| `contentless=true` | no | Contentless FTS5 (index only, no text). Snippet/highlight are rejected at search time. Mutually exclusive with `external=true`. |

Multiple `fts5:`-tagged fields on one model share **one** FTS5 table.
Conflicting table-level keys across fields are rejected at parse time.

### Lifecycle matrix

| Event | vec/gorm behavior | fts/gorm behavior |
|---|---|---|
| Plugin install | `db.Use(vecgorm.Plugin())` | `db.Use(ftsgorm.Plugin())` |
| AutoMigrate | `vecgorm.Migrate(db, &T{})` creates source + sidecar | `ftsgorm.Migrate(db, &T{})` creates source + FTS5 table + triggers |
| Create | AfterCreate callback `BatchInsert` (single tx) | AFTER INSERT trigger writes to FTS5 |
| Save/Update | AfterUpdate callback `(*vec.Table).Update` | AFTER UPDATE trigger refreshes index |
| Delete (hard) | AfterDelete callback `(*vec.Table).Delete` | AFTER DELETE trigger emits FTS5 `'delete'` |
| Delete (soft, via `gorm.DeletedAt`) | Sidecar `deleted` flag flipped to 1 | FTS5's UNINDEXED `deleted_at` mirror set by trigger |
| KNN / Search read-side | Typed `KNN[T]` returns materialized models; soft-deleted excluded by default, `IncludeDeleted()` overrides | Typed `Search[T]` ditto; soft-delete filter is `deleted_at IS NULL` (external) or `deleted = 0` (in-table/contentless) |
| Multi-embedding models | `vecgorm.WithField("Embedding")` picks which sidecar to query | n/a (FTS5 columns share one index) |
| Custom projection / JOIN | `vecgorm.KNNSQL[T]` + `WithSelect` / `WithJoin` / `WithOrderBy` returns `(sql, args, err)` for `db.Raw(...).Scan(&custom)` | `ftsgorm.SearchSQL[T]` same shape |
| DropSidecar | Drops sidecar table | Drops FTS5 table + all three triggers (for external mode) |
| Source DropTable | Cascades into sidecar via DropTableHook on our gorm Dialector | Cascades into FTS5 table + triggers |
| dim mismatch on re-migrate | Logged warning, existing sidecar left alone | n/a |

### Tests

| File | Notes |
|---|---|
| `vec/gorm/vecgorm_test.go` | Basic create/update/delete, KNN ranking, BatchInsert single-tx, soft-delete, Embedding wrapper |
| `vec/gorm/lifecycle_test.go` | DropTable cascade, DropSidecar, composite PK rejection, tag validation, WithFilter, dim mismatch |
| `fts/gorm/ftsgorm_test.go` | Migrate creates index + triggers, search/snippet/highlight, ranking, soft-delete, backfill |
| `fts/gorm/lifecycle_test.go` | Conflicting tags, non-string fields, composite PK, LIMIT/OFFSET, no-plugin error, DropTable cascade |
| `fts/gorm/mode_test.go` | external/in-table/contentless modes, conflicting modes rejected, contentless rejects snippet, in-table soft-delete |
| `vec/gorm/multifield_test.go` | Multi-embedding models: `WithField` dispatch, unknown field rejected, single-field WithField ignored |
| `vec/gorm/knnsql_test.go` | `KNNSQL` preserves soft-delete filter; `IncludeDeleted` strips it; WithJoin/Filter stack |
| `fts/gorm/searchsql_test.go` | `SearchSQL` preserves external-mode `deleted_at IS NULL`; `IncludeDeleted` strips it; WithJoin executes via `db.Raw` |

---

Last reviewed against gorm.io/gorm on 2026-05-29.

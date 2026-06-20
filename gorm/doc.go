// Package sqlite provides a gorm Dialector backed by the CGo-free
// gosqlite.org driver. It is a drop-in replacement for
// both gorm.io/driver/sqlite (the official mattn-based dialector) and
// github.com/glebarez/sqlite (the modernc-based fork): existing code
// that does
//
//	import "gorm.io/driver/sqlite"
//
// keeps working when the import is repointed at
//
//	import sqlite "gosqlite.org/gorm"
//
// with no other source changes — the exported names (Open, New,
// Config, Dialector, DriverName) match.
//
// # Quick start (recommended — Go-typed Config, no DSN)
//
//	import (
//	    sqlite "gosqlite.org"
//	    sqlitegorm "gosqlite.org/gorm"
//	)
//
//	db, err := sqlitegorm.OpenConfig(sqlite.Config{
//	    Path:    "app.db",
//	    Pragmas: sqlite.RecommendedPragmas(), // WAL + busy_timeout=5s + foreign_keys
//	})
//	if err != nil { ... }
//	defer db.Close() // drains gorm pool AND unregisters any encryption VFS
//
//	type User struct {
//	    gorm.Model
//	    Email string `gorm:"uniqueIndex"`
//	}
//	db.AutoMigrate(&User{})
//	db.Create(&User{Email: "a@example.com"})
//
// Same [sqlite.Config] type the root package exposes — set
// [sqlite.Encryption] for transparent encryption at rest, or
// MaxOpenConns / MaxIdleConns / ConnMaxLifetime to tune the pool.
// See examples/getting-started for the plain + encrypted demos.
//
// # Quick start (legacy DSN, still supported)
//
//	db, err := gorm.Open(sqlitegorm.Open("file:app.db?_pragma=foreign_keys(1)"),
//	    &gorm.Config{TranslateError: true})
//
// # What this package implements
//
//   - Dialector: full gorm.Dialector contract including the optional
//     gorm.ErrorTranslator (maps SQLITE_CONSTRAINT_UNIQUE /
//     PRIMARYKEY → gorm.ErrDuplicatedKey, FOREIGNKEY →
//     gorm.ErrForeignKeyViolated) and SavePointerDialectorInterface
//     (nested transactions).
//   - Migrator: an embedded gorm.io/gorm/migrator.Migrator with the
//     SQLite-specific overrides for HasTable, DropTable, AlterColumn,
//     DropColumn, CreateConstraint, DropConstraint, HasConstraint,
//     GetIndexes, and a custom CurrentDatabase that reads
//     PRAGMA database_list.
//   - DDL parser (ddl.go) feeding the recreate-table approach SQLite
//     requires for column type / constraint changes: SQLite's ALTER TABLE
//     cannot change a column's type or its constraints in place, so the
//     table is rebuilt — an inherent SQLite limitation, not a version concern.
//   - DropTableHook interface: third-party gorm plugins that own a sidecar
//     table or triggers tied to a gorm-managed source table can implement
//     DropTableHook to cascade their cleanup when callers run
//     db.Migrator().DropTable(&Model{}). No second helper call needed.
//
// # Virtual-table extensions (ext/)
//
// The ext/ vtab modules — csv, lines, closure, bloom, spellfix1, array,
// statement — work through this dialector once they are registered on
// the pool. Blank-import the module's auto sub-package (or install a
// Driver.ConnectHook before Open, e.g. for csv.RegisterFS with a
// sandboxed fs.FS), pin the pool with sqlDB.SetMaxOpenConns(1), then
// drive the vtab with ordinary gorm calls: db.Exec for the CREATE
// VIRTUAL TABLE, db.Raw / db.Table(...).Scan / Where for reads. gorm's
// AutoMigrate can't emit CREATE VIRTUAL TABLE … USING …, so create the
// vtab out-of-band — raw db.Exec, or the typed csv.Create / lines.Create
// over db.DB(). There is deliberately no csv/gorm or lines/gorm sidecar
// plugin: those vtabs are the table, not a companion index, so there is
// nothing to keep in sync. See examples/ext-vtabs for csv / lines /
// closure / bloom / spellfix1 each driven through gorm.
//
// # Configuration knobs
//
//   - sqlitegorm.OpenConfig(sqlite.Config, ...*gorm.Config): the modern
//     Go-typed entry. Pragmas, Encryption, pool knobs, and VFS routing
//     are struct fields — no DSN string assembly. Returns a *DB that
//     embeds *gorm.DB and bundles the encryption VFS lifecycle.
//   - sqlitegorm.Open(dsn): the gorm-standard constructor. dsn is passed
//     verbatim to the driver, so every DSN flag the underlying
//     gosqlite.org supports is available
//     (_foreign_keys, _busy_timeout, _journal_mode, _pragma, _txlock,
//     _time_format, _texttotime, _stmt_cache_size, …).
//   - sqlitegorm.New(sqlitegorm.Config{...}): for callers who want to
//     inject a custom DriverName (e.g. one with pre-registered UDFs)
//     or reuse an existing gorm.ConnPool.
//
// On every open, the dialector injects _texttotime=1 into the DSN if
// the caller hasn't already specified it. This makes ColumnTypeScanType
// return time.Time for DATE / DATETIME / TIMESTAMP columns, matching
// mattn's behavior — without it, gorm's Table(...).Find(&map) reads
// would return an RFC3339 string instead of a time.Time. Opt out
// explicitly with _texttotime=0 if you want the raw-string behavior.
//
// # Compatibility surface
//
// The gorm.io/gorm/tests integration suite runs against this dialector
// on every CI push via a thin shim re-exporting our types under
// gorm.io/driver/sqlite. See dev/upstream/gorm.md
// in the repo for the reproduction recipe.
//
// # See also
//
//   - gosqlite.org — the root driver: the shared sqlite.Config type and
//     the ext/ catalog of loadable SQL functions and virtual tables.
//   - gosqlite.org/vfs — io/fs.FS-backed read-only databases (e.g. shipping
//     seed data inside an embed.FS), usable under this dialector.
//   - examples/ in this module — getting-started, from-glebarez, crypto,
//     vfs, ext-scalars, ext-vtabs.
package sqlite

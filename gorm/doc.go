// Package sqlite provides a gorm Dialector backed by the CGo-free
// github.com/go-again/sqlite driver. It is a drop-in replacement for
// both gorm.io/driver/sqlite (the official mattn-based dialector) and
// github.com/glebarez/sqlite (the modernc-based fork): existing code
// that does
//
//	import "gorm.io/driver/sqlite"
//
// keeps working when the import is repointed at
//
//	import sqlite "github.com/go-again/sqlite/gorm"
//
// with no other source changes — the exported names (Open, New,
// Config, Dialector, DriverName) match.
//
// # Quick start
//
//	import (
//	    "gorm.io/gorm"
//	    sqlite "github.com/go-again/sqlite/gorm"
//	)
//
//	db, err := gorm.Open(sqlite.Open("file:app.db?_pragma=foreign_keys(1)"),
//	    &gorm.Config{TranslateError: true})
//	if err != nil { ... }
//
//	type User struct {
//	    gorm.Model
//	    Email string `gorm:"uniqueIndex"`
//	}
//	db.AutoMigrate(&User{})
//	db.Create(&User{Email: "a@example.com"})
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
//   - DDL parser (ddlmod.go) used by the recreate-table approach
//     SQLite needs for column drops / alters / constraint changes
//     prior to SQLite 3.35.
//   - DropTableHook interface: third-party gorm plugins (this module's
//     vec/gorm and fts/gorm bridges, for example) can implement
//     DropTableHook to cascade their sidecar cleanup when callers run
//     db.Migrator().DropTable(&Model{}). No second helper call needed.
//
// # Configuration knobs
//
//   - sqlite.Open(dsn): the gorm-standard constructor. dsn is passed
//     verbatim to the driver, so every DSN flag the underlying
//     github.com/go-again/sqlite supports is available
//     (_foreign_keys, _busy_timeout, _journal_mode, _pragma, _txlock,
//     _time_format, _texttotime, _stmt_cache_size, …).
//   - sqlite.New(sqlite.Config{...}): for callers who want to inject
//     a custom DriverName (e.g. one with pre-registered UDFs) or
//     reuse an existing gorm.ConnPool.
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
// gorm.io/driver/sqlite. See docs/gorm-upstream.md
// in the repo for the reproduction recipe.
//
// # See also
//
//   - github.com/go-again/sqlite/vec/gorm — tag-driven sqlite-vec
//     vector-search sidecars wired into gorm models.
//   - github.com/go-again/sqlite/fts/gorm — tag-driven FTS5 search
//     indexes wired into gorm models, with external / in-table /
//     contentless modes.
//   - github.com/go-again/sqlite/vfs — io/fs.FS-backed read-only
//     databases (e.g. shipping seed data inside an embed.FS).
package sqlite

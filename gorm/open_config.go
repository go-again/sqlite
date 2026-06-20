package sqlite

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	rootsqlite "gosqlite.org"
)

// DB wraps *gorm.DB so the caller can `defer db.Close()` without reaching for
// the underlying *sql.DB. The embedded *gorm.DB means every gorm method works
// unchanged:
//
//	db, err := sqlitegorm.OpenConfig(sqlite.Config{Path: "x.db"})
//	if err != nil { ... }
//	defer db.Close()
//
//	db.AutoMigrate(&Model{})  // *gorm.DB methods
//	db.Use(myPlugin)          // gorm plugins compose normally
//
// The gorm dialector has no encryption path; for an encrypted database with an
// ORM, use LiteORM (https://liteorm.org), which is built on this driver.
type DB struct {
	*gorm.DB
}

// OpenConfig is the modern Go-typed entry for gorm + SQLite. Takes the same
// [sqlite.Config] the root package exposes — one Config type for raw
// database/sql AND gorm.
//
// PRAGMAs ride in via DSN `_pragma=` URL flags (same encoding the root
// [sqlite.Open] uses), so every connection in gorm's pool gets the requested
// settings — not just the one [database/sql] happens to pick for the first
// Exec. A pre-registered VFS is routed via cfg.VFS; managing its lifecycle is
// the caller's job.
//
// Backward-compat: [Open] (taking a DSN string) and [New] (taking the
// gorm-style Config{DSN: ...}) both keep working unchanged. OpenConfig is the
// new, recommended path; it's strictly additive.
func OpenConfig(cfg rootsqlite.Config, gormCfg ...*gorm.Config) (*DB, error) {
	if cfg.Path == "" {
		return nil, errors.New("sqlitegorm: Config.Path is required")
	}

	// Build the DSN through the root package so PRAGMAs ride via `_pragma=`
	// URL flags — applied per connection by the driver. cfg.VFS is honored.
	dsn := rootsqlite.BuildDSN(cfg)

	var resolved *gorm.Config
	if len(gormCfg) > 0 && gormCfg[0] != nil {
		resolved = gormCfg[0]
	} else {
		resolved = &gorm.Config{}
	}

	gormDB, err := gorm.Open(Open(dsn), resolved)
	if err != nil {
		return nil, fmt.Errorf("sqlitegorm: gorm.Open: %w", err)
	}

	sqlDB, err := gormDB.DB()
	if err != nil {
		return nil, fmt.Errorf("sqlitegorm: get *sql.DB: %w", err)
	}

	if cfg.MaxOpenConns > 0 {
		sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	}
	if cfg.MaxIdleConns > 0 {
		sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	}
	if cfg.ConnMaxLifetime > 0 {
		sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	}

	// Force the first connection so any PRAGMA error surfaces here rather than
	// during the caller's first query.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("sqlitegorm: open first connection: %w", err)
	}

	return &DB{DB: gormDB}, nil
}

// Close drains the gorm pool (which drains the *sql.DB). Idempotent.
func (d *DB) Close() error {
	if d == nil || d.DB == nil {
		return nil
	}
	sqlDB, err := d.DB.DB()
	d.DB = nil
	if err != nil {
		return fmt.Errorf("sqlitegorm: get *sql.DB for Close: %w", err)
	}
	if sqlDB != nil {
		if err := sqlDB.Close(); err != nil {
			return fmt.Errorf("sqlitegorm: close *sql.DB: %w", err)
		}
	}
	return nil
}

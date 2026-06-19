package sqlite

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	rootsqlite "gosqlite.org"
	"gosqlite.org/vfs/crypto"
)

// DB wraps *gorm.DB so the caller can `defer db.Close()` without
// thinking about *sql.DB or VFS lifecycle. The embedded *gorm.DB
// means every gorm method works unchanged:
//
//	db, err := sqlitegorm.OpenConfig(sqlite.Config{Path: "x.db"})
//	if err != nil { ... }
//	defer db.Close()
//
//	db.AutoMigrate(&Model{})  // *gorm.DB methods
//	db.Use(vecgorm.Plugin())  // plugins compose normally
type DB struct {
	*gorm.DB
	fs *crypto.FS // nil unless Encryption was set
}

// OpenConfig is the modern Go-typed entry for gorm + SQLite. Takes
// the same [sqlite.Config] the root package exposes — one Config
// type for raw database/sql AND gorm.
//
// PRAGMAs ride in via DSN `_pragma=` URL flags (same encoding the
// root [sqlite.Open] uses), so every connection in gorm's pool gets
// the requested settings — not just the one [database/sql] happens
// to pick for the first Exec.
//
// Backward-compat: [Open] (taking a DSN string) and [New] (taking
// the gorm-style Config{DSN: ...}) both keep working unchanged.
// OpenConfig is the new, recommended path; it's strictly additive.
//
// Lifecycle: a single defer db.Close() drains the gorm pool and
// unregisters any VFS that OpenConfig registered for encryption.
func OpenConfig(cfg rootsqlite.Config, gormCfg ...*gorm.Config) (*DB, error) {
	if cfg.Path == "" {
		return nil, errors.New("sqlitegorm: Config.Path is required")
	}
	if cfg.Encryption != nil && cfg.VFS != "" {
		return nil, errors.New("sqlitegorm: Encryption and VFS are mutually exclusive")
	}
	if cfg.Encryption != nil && (cfg.Path == ":memory:" || cfg.Mode == rootsqlite.ModeMemory) {
		return nil, errors.New("sqlitegorm: Encryption requires an on-disk path (refusing :memory: / mode=memory)")
	}

	var fs *crypto.FS
	vfsName := cfg.VFS
	if cfg.Encryption != nil {
		name, handle, err := crypto.New(crypto.Options{
			Key:      cfg.Encryption.Key,
			Cipher:   cfg.Encryption.Cipher,
			PageSize: cfg.Encryption.PageSize,
			Recorder: cfg.Encryption.Recorder,
		})
		if err != nil {
			return nil, fmt.Errorf("sqlitegorm: register encryption VFS: %w", err)
		}
		fs = handle
		vfsName = name
	}

	// Build the DSN through the root package so PRAGMAs ride via
	// `_pragma=` URL flags — applied per connection by the driver.
	dsnCfg := cfg
	dsnCfg.VFS = vfsName
	dsn := rootsqlite.BuildDSN(dsnCfg)

	var resolved *gorm.Config
	if len(gormCfg) > 0 && gormCfg[0] != nil {
		resolved = gormCfg[0]
	} else {
		resolved = &gorm.Config{}
	}

	gormDB, err := gorm.Open(Open(dsn), resolved)
	if err != nil {
		if fs != nil {
			_ = fs.Close()
		}
		return nil, fmt.Errorf("sqlitegorm: gorm.Open: %w", err)
	}

	sqlDB, err := gormDB.DB()
	if err != nil {
		if fs != nil {
			_ = fs.Close()
		}
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

	// Force the first connection so any PRAGMA error surfaces here
	// rather than during the caller's first query.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		if fs != nil {
			_ = fs.Close()
		}
		return nil, fmt.Errorf("sqlitegorm: open first connection: %w", err)
	}

	return &DB{DB: gormDB, fs: fs}, nil
}

// Close drains the gorm pool (which drains the *sql.DB) and
// unregisters the encryption VFS if one was registered.
// Idempotent. Order matters per [vfs/crypto/doc.go]: pool first,
// VFS second.
func (d *DB) Close() error {
	if d == nil {
		return nil
	}
	var errs []error
	if d.DB != nil {
		sqlDB, err := d.DB.DB()
		if err != nil {
			errs = append(errs, fmt.Errorf("sqlitegorm: get *sql.DB for Close: %w", err))
		} else if sqlDB != nil {
			if err := sqlDB.Close(); err != nil {
				errs = append(errs, fmt.Errorf("sqlitegorm: close *sql.DB: %w", err))
			}
		}
		d.DB = nil
	}
	if d.fs != nil {
		if err := d.fs.Close(); err != nil {
			errs = append(errs, fmt.Errorf("sqlitegorm: close encryption VFS: %w", err))
		}
		d.fs = nil
	}
	return errors.Join(errs...)
}

// VFSName returns the registered encryption VFS name, or empty
// when no encryption was set. Useful for opening a second sql.DB
// pool against the same encrypted file.
func (d *DB) VFSName() string {
	if d == nil || d.fs == nil {
		return ""
	}
	return d.fs.Name()
}

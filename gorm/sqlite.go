package sqlite

import (
	"database/sql"
	"strconv"
	"strings"

	"gorm.io/gorm/callbacks"

	rootsqlite "gosqlite.org"
	sqlite3 "modernc.org/sqlite/lib"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/migrator"
	"gorm.io/gorm/schema"
)

// applyGormDefaults injects DSN flags that match gorm's expectations of the
// SQLite driver. mattn/go-sqlite3 advertises time.Time as the ScanType for
// DATETIME columns; modernc's wrapper returns string unless _texttotime is
// set. Without the flag, gorm's map-mode reads (`DB.Table(...).Find(&m)`)
// hand callers an RFC3339 string instead of a time.Time, which breaks
// equality checks in the upstream gorm/tests suite.
func applyGormDefaults(dsn string) string {
	if dsn == "" || strings.Contains(dsn, "_texttotime") {
		return dsn
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "_texttotime=1"
}

// DriverName is the default driver name for SQLite. Since
// gosqlite.org registers under both "sqlite" (modernc-style)
// and "sqlite3" (mattn-style), either name resolves to the same singleton.
const DriverName = "sqlite"

// Config is the configuration object accepted by New. It mirrors the field
// set of the official go-gorm/sqlite driver so existing code that does
// sqlite.New(sqlite.Config{DSN: "..."}) continues to compile after switching
// the import path to gosqlite.org/gorm.
type Config struct {
	DriverName string
	DSN        string
	Conn       gorm.ConnPool
}

type Dialector struct {
	DriverName string
	DSN        string
	Conn       gorm.ConnPool
}

// Open returns a gorm.Dialector for the given DSN, using the default driver
// name. Equivalent to glebarez/sqlite's sqlite.Open.
func Open(dsn string) gorm.Dialector {
	return &Dialector{DSN: dsn}
}

// OpenInMemory returns a gorm.Dialector wired to a private in-memory
// SQLite database — equivalent to [Open](":memory:"). Convenient for
// gorm tests and scratch databases that don't need a file path:
//
//	db, _ := gorm.Open(sqlitegorm.OpenInMemory(), &gorm.Config{})
//
// For an in-memory DB shared across multiple connections, see
// [OpenShared].
func OpenInMemory() gorm.Dialector {
	return Open(rootsqlite.InMemory)
}

// OpenWAL returns a gorm.Dialector wired to a file-backed database
// with the [rootsqlite.RecommendedPragmas] preset — WAL journaling,
// 5-second busy timeout, foreign keys enforced. The shortest path to
// a production-shaped gorm open:
//
//	db, _ := gorm.Open(sqlitegorm.OpenWAL("app.db"), &gorm.Config{})
//
// For finer-grained control, use [OpenConfig] with the full
// [rootsqlite.Config] shape.
func OpenWAL(path string) gorm.Dialector {
	return Open(rootsqlite.BuildDSN(rootsqlite.Config{
		Path:    path,
		Pragmas: rootsqlite.RecommendedPragmas(),
	}))
}

// OpenReadOnly returns a gorm.Dialector wired to an existing
// file-backed database in read-only mode. Refuses to create the file
// if missing. Equivalent to opening the DSN
// `file:path?mode=ro` via [Open]:
//
//	db, _ := gorm.Open(sqlitegorm.OpenReadOnly("seed.db"), &gorm.Config{})
func OpenReadOnly(path string) gorm.Dialector {
	return Open(rootsqlite.BuildDSN(rootsqlite.Config{
		Path: path,
		Mode: rootsqlite.ModeReadOnly,
	}))
}

// OpenShared returns a gorm.Dialector wired to a named in-memory
// database that every connection in the same process pointing at the
// same name shares — the standard SQLite recipe for multi-conn
// in-memory tests. Equivalent to opening
// `file:NAME?mode=memory&cache=shared` via [Open]:
//
//	db, _ := gorm.Open(sqlitegorm.OpenShared("testdb"), &gorm.Config{})
func OpenShared(name string) gorm.Dialector {
	return Open(rootsqlite.BuildDSN(rootsqlite.Config{
		Path:  name,
		Mode:  rootsqlite.ModeMemory,
		Cache: rootsqlite.CacheShared,
	}))
}

// New returns a gorm.Dialector configured by the given Config. Equivalent to
// the official go-gorm/sqlite sqlite.New, provided so consumers can migrate
// without code changes.
func New(cfg Config) gorm.Dialector {
	return &Dialector{
		DriverName: cfg.DriverName,
		DSN:        cfg.DSN,
		Conn:       cfg.Conn,
	}
}

func (dialector Dialector) Name() string {
	return "sqlite"
}

func (dialector Dialector) Initialize(db *gorm.DB) (err error) {
	if dialector.DriverName == "" {
		dialector.DriverName = DriverName
	}

	if dialector.Conn != nil {
		db.ConnPool = dialector.Conn
	} else {
		conn, err := sql.Open(dialector.DriverName, applyGormDefaults(dialector.DSN))
		if err != nil {
			return err
		}
		db.ConnPool = conn
	}

	// RETURNING is always available — our pure-Go driver bundles a modern SQLite.
	callbacks.RegisterDefaultCallbacks(db, &callbacks.Config{
		CreateClauses:        []string{"INSERT", "VALUES", "ON CONFLICT", "RETURNING"},
		UpdateClauses:        []string{"UPDATE", "SET", "FROM", "WHERE", "RETURNING"},
		DeleteClauses:        []string{"DELETE", "FROM", "WHERE", "RETURNING"},
		LastInsertIDReversed: true,
	})

	for k, v := range dialector.ClauseBuilders() {
		db.ClauseBuilders[k] = v
	}
	return
}

func (dialector Dialector) ClauseBuilders() map[string]clause.ClauseBuilder {
	return map[string]clause.ClauseBuilder{
		"INSERT": func(c clause.Clause, builder clause.Builder) {
			if insert, ok := c.Expression.(clause.Insert); ok {
				if stmt, ok := builder.(*gorm.Statement); ok {
					stmt.WriteString("INSERT ")
					if insert.Modifier != "" {
						stmt.WriteString(insert.Modifier)
						stmt.WriteByte(' ')
					}

					stmt.WriteString("INTO ")
					if insert.Table.Name == "" {
						stmt.WriteQuoted(stmt.Table)
					} else {
						stmt.WriteQuoted(insert.Table)
					}
					return
				}
			}

			c.Build(builder)
		},
		"LIMIT": func(c clause.Clause, builder clause.Builder) {
			if limit, ok := c.Expression.(clause.Limit); ok {
				var lmt = -1
				if limit.Limit != nil && *limit.Limit >= 0 {
					lmt = *limit.Limit
				}
				if lmt >= 0 || limit.Offset > 0 {
					builder.WriteString("LIMIT ")
					builder.WriteString(strconv.Itoa(lmt))
				}
				if limit.Offset > 0 {
					builder.WriteString(" OFFSET ")
					builder.WriteString(strconv.Itoa(limit.Offset))
				}
			}
		},
		"FOR": func(c clause.Clause, builder clause.Builder) {
			if _, ok := c.Expression.(clause.Locking); ok {
				// SQLite3 does not support row-level locking.
				return
			}
			c.Build(builder)
		},
	}
}

func (dialector Dialector) DefaultValueOf(field *schema.Field) clause.Expression {
	if field.AutoIncrement {
		return clause.Expr{SQL: "NULL"}
	}

	// doesn't work, will raise error
	return clause.Expr{SQL: "DEFAULT"}
}

func (dialector Dialector) Migrator(db *gorm.DB) gorm.Migrator {
	return Migrator{migrator.Migrator{Config: migrator.Config{
		DB:                          db,
		Dialector:                   dialector,
		CreateIndexAfterCreateTable: true,
	}}}
}

func (dialector Dialector) BindVarTo(writer clause.Writer, stmt *gorm.Statement, v any) {
	writer.WriteByte('?')
}

func (dialector Dialector) QuoteTo(writer clause.Writer, str string) {
	var (
		underQuoted, selfQuoted bool
		continuousBacktick      int8
		shiftDelimiter          int8
	)

	for _, v := range []byte(str) {
		switch v {
		case '`':
			continuousBacktick++
			if continuousBacktick == 2 {
				writer.WriteString("``")
				continuousBacktick = 0
			}
		case '.':
			if continuousBacktick > 0 || !selfQuoted {
				shiftDelimiter = 0
				underQuoted = false
				continuousBacktick = 0
				writer.WriteString("`")
			}
			writer.WriteByte(v)
			continue
		default:
			if shiftDelimiter-continuousBacktick <= 0 && !underQuoted {
				writer.WriteString("`")
				underQuoted = true
				if selfQuoted = continuousBacktick > 0; selfQuoted {
					continuousBacktick -= 1
				}
			}

			for ; continuousBacktick > 0; continuousBacktick -= 1 {
				writer.WriteString("``")
			}

			writer.WriteByte(v)
		}
		shiftDelimiter++
	}

	if continuousBacktick > 0 && !selfQuoted {
		writer.WriteString("``")
	}
	writer.WriteString("`")
}

func (dialector Dialector) Explain(sql string, vars ...any) string {
	return logger.ExplainSQL(sql, nil, `"`, vars...)
}

func (dialector Dialector) DataTypeOf(field *schema.Field) string {
	switch field.DataType {
	case schema.Bool:
		return "numeric"
	case schema.Int, schema.Uint:
		if field.AutoIncrement {
			// doesn't check `PrimaryKey`, to keep backward compatibility
			// https://www.sqlite.org/autoinc.html
			return "integer PRIMARY KEY AUTOINCREMENT"
		} else {
			return "integer"
		}
	case schema.Float:
		return "real"
	case schema.String:
		return "text"
	case schema.Time:
		// Distinguish between schema.Time and tag time
		if val, ok := field.TagSettings["TYPE"]; ok {
			return val
		} else {
			return "datetime"
		}
	case schema.Bytes:
		return "blob"
	}

	return string(field.DataType)
}

func (dialector Dialector) SavePoint(tx *gorm.DB, name string) error {
	tx.Exec("SAVEPOINT " + name)
	return nil
}

func (dialector Dialector) RollbackTo(tx *gorm.DB, name string) error {
	tx.Exec("ROLLBACK TO SAVEPOINT " + name)
	return nil
}

func (dialector Dialector) Translate(err error) error {
	// Prefer the extended code so we can distinguish, e.g.,
	// SQLITE_CONSTRAINT_UNIQUE from SQLITE_CONSTRAINT_CHECK. The driver's
	// *sqlite.Error implements both ExtendedCode() and Code(); we type-switch
	// on the more specific accessor first.
	switch terr := err.(type) {
	case interface{ ExtendedCode() int }:
		switch terr.ExtendedCode() {
		case sqlite3.SQLITE_CONSTRAINT_UNIQUE, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY:
			return gorm.ErrDuplicatedKey
		case sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY:
			return gorm.ErrForeignKeyViolated
		}
	case interface{ Code() int }:
		// Fallback for drivers that only expose a single Code() returning the
		// extended code value directly (e.g. older modernc.org/sqlite).
		switch terr.Code() {
		case sqlite3.SQLITE_CONSTRAINT_UNIQUE, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY:
			return gorm.ErrDuplicatedKey
		case sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY:
			return gorm.ErrForeignKeyViolated
		}
	}
	return err
}

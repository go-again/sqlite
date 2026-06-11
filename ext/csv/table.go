package csv

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/go-again/sqlite/internal/sqlid"
)

// Table is a typed handle to a csv virtual table — a CSV file (or inline
// string) presented as a SQL table. Create hides the `USING csv(…)`
// argument string and its quoting (the value worth abstracting here, the
// way sqlite.Open hides DSN strings); the rows are queried as SQL, since a
// CSV is schemaless. Construct it with Create or Open. Safe for concurrent
// use as long as the *sql.DB is.
//
// The csv module must be registered on every connection db hands out:
// blank-import "github.com/go-again/sqlite/ext/csv/auto", or call
// [Register] / [RegisterFS] on a pinned *sqlite.Conn. File access (os vs a
// sandbox fs.FS) is fixed at registration; WithFilename resolves against
// whatever was registered. Without the module, Create fails "no such
// module: csv".
type Table struct {
	db   *sql.DB
	name string
}

type createConfig struct {
	filename    string
	data        string
	hasData     bool
	header      bool
	comma       rune
	comment     rune
	columns     int
	ifNotExists bool
}

// CreateOption configures [Create].
type CreateOption func(*createConfig)

// WithFilename names the CSV file to read (resolved against the registered
// fs.FS). Mutually exclusive with WithData; exactly one is required.
func WithFilename(path string) CreateOption { return func(c *createConfig) { c.filename = path } }

// WithData supplies inline CSV content instead of a file. Mutually
// exclusive with WithFilename.
func WithData(content string) CreateOption {
	return func(c *createConfig) { c.data = content; c.hasData = true }
}

// WithHeader treats the first row as column names. Without it, columns are
// named c0, c1, … (or use WithColumns to fix the count).
func WithHeader() CreateOption { return func(c *createConfig) { c.header = true } }

// WithComma sets the field delimiter (default ',').
func WithComma(r rune) CreateOption { return func(c *createConfig) { c.comma = r } }

// WithComment sets a line-comment rune; lines starting with it are skipped.
func WithComment(r rune) CreateOption { return func(c *createConfig) { c.comment = r } }

// WithColumns fixes the column count when the file has no header row.
func WithColumns(n int) CreateOption { return func(c *createConfig) { c.columns = n } }

// WithIfNotExists makes Create idempotent.
func WithIfNotExists() CreateOption { return func(c *createConfig) { c.ifNotExists = true } }

// ErrAlreadyExists wraps the error returned by Create when the named
// virtual table already exists and WithIfNotExists was not passed.
var ErrAlreadyExists = errors.New("csv: virtual table already exists")

// Create runs CREATE VIRTUAL TABLE name USING csv(…) with the supplied
// options, properly quoting each value, and returns a handle. Exactly one
// of WithFilename / WithData is required.
func Create(ctx context.Context, db *sql.DB, name string, opts ...CreateOption) (*Table, error) {
	if !sqlid.ValidIdent(name) {
		return nil, fmt.Errorf("csv.Create: %q is not a valid SQL identifier", name)
	}
	cfg := &createConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	if (cfg.filename != "") == cfg.hasData {
		return nil, errors.New(`csv.Create: exactly one of WithFilename or WithData is required`)
	}
	var params []string
	if cfg.filename != "" {
		params = append(params, "filename="+sqlid.QuoteString(cfg.filename))
	}
	if cfg.hasData {
		params = append(params, "data="+sqlid.QuoteString(cfg.data))
	}
	if cfg.header {
		params = append(params, "header=on")
	}
	if cfg.comma != 0 {
		params = append(params, "comma="+sqlid.QuoteString(string(cfg.comma)))
	}
	if cfg.comment != 0 {
		params = append(params, "comment="+sqlid.QuoteString(string(cfg.comment)))
	}
	if cfg.columns > 0 {
		params = append(params, fmt.Sprintf("columns=%d", cfg.columns))
	}
	ifNotExists := ""
	if cfg.ifNotExists {
		ifNotExists = "IF NOT EXISTS "
	}
	stmt := fmt.Sprintf("CREATE VIRTUAL TABLE %s%s USING %s(%s)",
		ifNotExists, sqlid.QuoteIdent(name), ModuleName, strings.Join(params, ", "))
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		if sqlid.IsAlreadyExistsErr(err) {
			return nil, fmt.Errorf("csv.Create %q: %w", name, ErrAlreadyExists)
		}
		return nil, fmt.Errorf("csv.Create %q: %w", name, err)
	}
	return &Table{db: db, name: name}, nil
}

// Open returns a handle for a csv vtab that already exists in db. It does
// no I/O and does not validate existence.
func Open(db *sql.DB, name string) (*Table, error) {
	if !sqlid.ValidIdent(name) {
		return nil, fmt.Errorf("csv.Open: %q is not a valid SQL identifier", name)
	}
	return &Table{db: db, name: name}, nil
}

// Name returns the underlying SQLite vtab name.
func (t *Table) Name() string { return t.name }

// Columns returns the table's column names — the CSV header when
// WithHeader was set, otherwise c0, c1, ….
func (t *Table) Columns(ctx context.Context) ([]string, error) {
	rows, err := t.db.QueryContext(ctx, fmt.Sprintf("SELECT * FROM %s LIMIT 0", sqlid.QuoteIdent(t.name)))
	if err != nil {
		return nil, fmt.Errorf("csv.Columns: %w", err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("csv.Columns: %w", err)
	}
	return cols, nil
}

// Rows runs SELECT * FROM name and returns the rows; the caller owns and
// must Close the returned *sql.Rows. For joins or filters, use Name() and
// write the SQL — querying a CSV is SQL's job, which is the whole point of
// the vtab.
func (t *Table) Rows(ctx context.Context) (*sql.Rows, error) {
	rows, err := t.db.QueryContext(ctx, fmt.Sprintf("SELECT * FROM %s", sqlid.QuoteIdent(t.name)))
	if err != nil {
		return nil, fmt.Errorf("csv.Rows: %w", err)
	}
	return rows, nil
}

// Drop removes the vtab. The underlying file is untouched.
func (t *Table) Drop(ctx context.Context) error {
	if _, err := t.db.ExecContext(ctx, "DROP TABLE IF EXISTS "+sqlid.QuoteIdent(t.name)); err != nil {
		return fmt.Errorf("csv.Drop: %w", err)
	}
	return nil
}

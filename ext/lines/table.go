package lines

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/go-again/sqlite/internal/sqlid"
	"github.com/go-again/sqlite/internal/vtabx"
)

// Table is a typed handle to a lines virtual table — a text file (or
// inline string) presented as one row per line, with columns lineno
// INTEGER and line TEXT. Create hides the `USING lines(…)` argument
// string and its quoting (the way sqlite.Open hides DSN strings); the
// rows are queried as SQL. Construct it with Create or Open. Safe for
// concurrent use as long as the *sql.DB is.
//
// The lines module must be registered on every connection db hands out:
// blank-import "github.com/go-again/sqlite/ext/lines/auto", or call
// [Register] / [RegisterFS] on a pinned *sqlite.Conn. File access (os vs a
// sandbox fs.FS) is fixed at registration; WithFilename resolves against
// whatever was registered. Without the module, Create fails "no such
// module: lines".
type Table struct {
	db   *sql.DB
	name string
}

type createConfig struct {
	filename    string
	data        string
	hasData     bool
	ifNotExists bool
}

// CreateOption configures [Create].
type CreateOption func(*createConfig)

// WithFilename names the text file to read (resolved against the
// registered fs.FS). Mutually exclusive with WithData; exactly one is
// required.
func WithFilename(path string) CreateOption { return func(c *createConfig) { c.filename = path } }

// WithData supplies inline text content instead of a file. Mutually
// exclusive with WithFilename.
func WithData(content string) CreateOption {
	return func(c *createConfig) { c.data = content; c.hasData = true }
}

// WithIfNotExists makes Create idempotent.
func WithIfNotExists() CreateOption { return func(c *createConfig) { c.ifNotExists = true } }

// ErrAlreadyExists wraps the error returned by Create when the named
// virtual table already exists and WithIfNotExists was not passed.
var ErrAlreadyExists = errors.New("lines: virtual table already exists")

// Create runs CREATE VIRTUAL TABLE name USING lines(…) with the supplied
// options, properly quoting each value, and returns a handle. Exactly one
// of WithFilename / WithData is required.
func Create(ctx context.Context, db *sql.DB, name string, opts ...CreateOption) (*Table, error) {
	cfg := &createConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	if (cfg.filename != "") == cfg.hasData {
		return nil, errors.New(`lines.Create: exactly one of WithFilename or WithData is required`)
	}
	var params []string
	if cfg.filename != "" {
		params = append(params, "filename="+sqlid.QuoteString(cfg.filename))
	}
	if cfg.hasData {
		params = append(params, "data="+sqlid.QuoteString(cfg.data))
	}
	if err := vtabx.Create(ctx, db, name, ModuleName, params, cfg.ifNotExists, ErrAlreadyExists); err != nil {
		return nil, err
	}
	return &Table{db: db, name: name}, nil
}

// Open returns a handle for a lines vtab that already exists in db. It
// does no I/O and does not validate existence.
func Open(db *sql.DB, name string) (*Table, error) {
	if !sqlid.ValidIdent(name) {
		return nil, fmt.Errorf("lines.Open: %q is not a valid SQL identifier", name)
	}
	return &Table{db: db, name: name}, nil
}

// Name returns the underlying SQLite vtab name.
func (t *Table) Name() string { return t.name }

// Columns returns the table's column names — `lineno`, `line` for the
// default schema.
func (t *Table) Columns(ctx context.Context) ([]string, error) {
	rows, err := t.db.QueryContext(ctx, fmt.Sprintf("SELECT * FROM %s LIMIT 0", sqlid.QuoteIdent(t.name)))
	if err != nil {
		return nil, fmt.Errorf("lines.Columns: %w", err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("lines.Columns: %w", err)
	}
	return cols, nil
}

// Rows runs SELECT lineno, line FROM name in line order and returns the
// rows; the caller owns and must Close the returned *sql.Rows. For
// filters (e.g. WHERE line LIKE …) use Name() and write the SQL.
func (t *Table) Rows(ctx context.Context) (*sql.Rows, error) {
	rows, err := t.db.QueryContext(ctx,
		fmt.Sprintf("SELECT lineno, line FROM %s ORDER BY lineno", sqlid.QuoteIdent(t.name)))
	if err != nil {
		return nil, fmt.Errorf("lines.Rows: %w", err)
	}
	return rows, nil
}

// Drop removes the vtab. The underlying file is untouched.
func (t *Table) Drop(ctx context.Context) error {
	return vtabx.Drop(ctx, t.db, t.name)
}

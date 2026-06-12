package rtree

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/go-again/sqlite/internal/sqlid"
	"github.com/go-again/sqlite/internal/vtabx"
)

// Table is a typed handle to a SQLite R-Tree virtual table — a spatial index
// over N-dimensional bounding boxes. Create hides the
// `CREATE VIRTUAL TABLE … USING rtree(…)` column list; Insert / Search /
// Delete drive it without hand-written SQL. Construct it with Create or Open.
// Safe for concurrent use as long as the *sql.DB is.
//
// The rtree module is compiled into the library, so — unlike the file-backed
// vtab extensions — Table needs no pool-wide registration to Create, Insert,
// Delete, or run a bounding-box Search. Only SearchCircle (and any other MATCH
// geometry) needs a geometry registered per connection: blank-import
// "github.com/go-again/sqlite/ext/rtree/auto" and pin the pool, or call
// [Register] on a pinned *sqlite.Conn.
type Table struct {
	db   *sql.DB
	name string
	dims int
}

type createConfig struct {
	dims        int
	int32Coords bool
	ifNotExists bool
}

// CreateOption configures [Create].
type CreateOption func(*createConfig)

// WithDimensions sets the number of spatial dimensions (1–5). Default 2.
func WithDimensions(n int) CreateOption { return func(c *createConfig) { c.dims = n } }

// WithInt32Coords stores coordinates as 32-bit integers (the rtree_i32 module)
// instead of the default single-precision floats.
func WithInt32Coords() CreateOption { return func(c *createConfig) { c.int32Coords = true } }

// WithIfNotExists makes Create idempotent.
func WithIfNotExists() CreateOption { return func(c *createConfig) { c.ifNotExists = true } }

// ErrAlreadyExists wraps the error returned by Create when the named table
// already exists and WithIfNotExists was not passed.
var ErrAlreadyExists = errors.New("rtree: virtual table already exists")

// Create runs CREATE VIRTUAL TABLE name USING rtree(id, min0, max0, …) with
// two coordinate columns per dimension and returns a handle.
func Create(ctx context.Context, db *sql.DB, name string, opts ...CreateOption) (*Table, error) {
	cfg := &createConfig{dims: 2}
	for _, opt := range opts {
		opt(cfg)
	}
	if cfg.dims < 1 || cfg.dims > 5 {
		return nil, fmt.Errorf("rtree.Create: dimensions must be 1..5, got %d", cfg.dims)
	}
	cols := make([]string, 0, 1+2*cfg.dims)
	cols = append(cols, "id")
	for i := range cfg.dims {
		cols = append(cols, "min"+strconv.Itoa(i), "max"+strconv.Itoa(i))
	}
	module := "rtree"
	if cfg.int32Coords {
		module = "rtree_i32"
	}
	if err := vtabx.Create(ctx, db, name, "rtree", module, cols, cfg.ifNotExists, ErrAlreadyExists); err != nil {
		return nil, err
	}
	return &Table{db: db, name: name, dims: cfg.dims}, nil
}

// Open returns a handle to an rtree vtab that already exists in db, detecting
// its dimension count from the id + min/max column layout.
func Open(ctx context.Context, db *sql.DB, name string) (*Table, error) {
	if !sqlid.ValidIdent(name) {
		return nil, fmt.Errorf("rtree.Open: %q is not a valid SQL identifier", name)
	}
	rows, err := db.QueryContext(ctx, fmt.Sprintf("SELECT * FROM %s LIMIT 0", sqlid.QuoteIdent(name)))
	if err != nil {
		return nil, fmt.Errorf("rtree.Open: %w", err)
	}
	defer rows.Close()
	cnames, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("rtree.Open: %w", err)
	}
	// id + two coordinate columns per dimension; auxiliary columns are not
	// modelled here.
	if len(cnames) < 3 || (len(cnames)-1)%2 != 0 {
		return nil, fmt.Errorf("rtree.Open: %q has %d columns, not an id + min/max layout", name, len(cnames))
	}
	return &Table{db: db, name: name, dims: (len(cnames) - 1) / 2}, nil
}

// Name returns the underlying SQLite vtab name.
func (t *Table) Name() string { return t.name }

// Dimensions returns the number of spatial dimensions.
func (t *Table) Dimensions() int { return t.dims }

// Insert stores a bounding box, replacing any existing row with the same id.
// coords holds the box as [min0, max0, min1, max1, …] — two values per
// dimension, so len(coords) must be 2*Dimensions(). Each min must not exceed
// its max.
func (t *Table) Insert(ctx context.Context, id int64, coords ...float64) error {
	if len(coords) != 2*t.dims {
		return fmt.Errorf("rtree.Insert: got %d coords, want %d (2 per dimension)", len(coords), 2*t.dims)
	}
	args := make([]any, 0, 1+len(coords))
	args = append(args, id)
	for _, c := range coords {
		args = append(args, c)
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(args)), ", ")
	stmt := fmt.Sprintf("INSERT OR REPLACE INTO %s VALUES (%s)", sqlid.QuoteIdent(t.name), placeholders)
	if _, err := t.db.ExecContext(ctx, stmt, args...); err != nil {
		return fmt.Errorf("rtree.Insert: %w", err)
	}
	return nil
}

// InsertPoint stores a zero-size box at a single point: point holds one
// coordinate per dimension (len == Dimensions()), expanded to min==max on each
// axis.
func (t *Table) InsertPoint(ctx context.Context, id int64, point ...float64) error {
	if len(point) != t.dims {
		return fmt.Errorf("rtree.InsertPoint: got %d coords, want %d (1 per dimension)", len(point), t.dims)
	}
	coords := make([]float64, 0, 2*t.dims)
	for _, p := range point {
		coords = append(coords, p, p)
	}
	return t.Insert(ctx, id, coords...)
}

// Delete removes the row with the given id; a missing id is a no-op.
func (t *Table) Delete(ctx context.Context, id int64) error {
	if _, err := t.db.ExecContext(ctx,
		fmt.Sprintf("DELETE FROM %s WHERE id = ?", sqlid.QuoteIdent(t.name)), id); err != nil {
		return fmt.Errorf("rtree.Delete: %w", err)
	}
	return nil
}

// Search returns the ids of every stored box that overlaps the query box.
// query holds the box as [min0, max0, …] (len == 2*Dimensions()). The query
// runs against the R-Tree index, not as a full scan.
func (t *Table) Search(ctx context.Context, query ...float64) ([]int64, error) {
	if len(query) != 2*t.dims {
		return nil, fmt.Errorf("rtree.Search: got %d coords, want %d (2 per dimension)", len(query), 2*t.dims)
	}
	conds := make([]string, 0, t.dims)
	args := make([]any, 0, 2*t.dims)
	for i := range t.dims {
		// Boxes overlap on axis i ⇔ max_i >= qmin_i AND min_i <= qmax_i.
		conds = append(conds, fmt.Sprintf("max%d >= ? AND min%d <= ?", i, i))
		args = append(args, query[2*i], query[2*i+1])
	}
	stmt := fmt.Sprintf("SELECT id FROM %s WHERE %s ORDER BY id",
		sqlid.QuoteIdent(t.name), strings.Join(conds, " AND "))
	return t.queryIDs(ctx, stmt, args...)
}

// SearchCircle returns the ids of every box overlapping the circle of radius r
// centred at (cx, cy). It is valid only for a 2-dimensional table and requires
// the circle geometry registered on the connection — blank-import
// "github.com/go-again/sqlite/ext/rtree/auto" and pin the pool, or call
// [Register] on a pinned *sqlite.Conn. For other shapes, register a custom
// geometry via (*sqlite.Conn).RegisterRTreeGeometry and query MATCH directly.
func (t *Table) SearchCircle(ctx context.Context, cx, cy, r float64) ([]int64, error) {
	if t.dims != 2 {
		return nil, fmt.Errorf("rtree.SearchCircle: requires a 2D table, have %dD", t.dims)
	}
	stmt := fmt.Sprintf("SELECT id FROM %s WHERE id MATCH circle(?, ?, ?) ORDER BY id", sqlid.QuoteIdent(t.name))
	return t.queryIDs(ctx, stmt, cx, cy, r)
}

func (t *Table) queryIDs(ctx context.Context, stmt string, args ...any) ([]int64, error) {
	rows, err := t.db.QueryContext(ctx, stmt, args...)
	if err != nil {
		return nil, fmt.Errorf("rtree: query: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("rtree: scan: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Drop removes the vtab and its backing shadow tables.
func (t *Table) Drop(ctx context.Context) error {
	return vtabx.Drop(ctx, t.db, "rtree", t.name)
}

package vec

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"iter"
	"strings"

	"github.com/go-again/sqlite/internal/sqlid"
)

// Table is a typed handle to a sqlite-vec virtual table backed by an
// embedding column of fixed dimension. Construct one with Create (which runs
// the CREATE VIRTUAL TABLE statement) or Open (which assumes the table
// already exists).
//
// Table is safe for concurrent use as long as the underlying *sql.DB is — it
// holds no per-conn state.
type Table struct {
	db        *sql.DB
	name      string
	embedding string // column name; defaults to "embedding"
	dim       int
	metric    Metric
	encoding  Encoding
}

// QuoteIdent returns name in backticks, escaping any embedded backticks.
// Used for SQL identifier interpolation outside the vec0 constructor —
// SQLite treats table/column names as identifiers, not bind parameters.
// Thin re-export of [internal/sqlid.QuoteIdentBacktick] so the
// internals share one implementation.
//
// Note: vec0's CREATE VIRTUAL TABLE column-argument parser does NOT
// accept quoted identifiers — only bare names — so identifiers fed
// into that constructor must pass ValidIdent and be interpolated raw.
func QuoteIdent(name string) string { return sqlid.QuoteIdentBacktick(name) }

// ValidIdent reports whether name is a safe SQL identifier — the
// conservative ASCII subset: leading letter or underscore, then
// letters, digits, or underscores. Used to guard against SQL injection
// at the API boundary when callers pass arbitrary strings as table or
// column names. Thin re-export of [internal/sqlid.ValidIdent].
func ValidIdent(s string) bool { return sqlid.ValidIdent(s) }

// quote / validIdent are the legacy private aliases — kept so the rest
// of the vec package compiles unchanged.
func quote(name string) string { return QuoteIdent(name) }
func validIdent(s string) bool { return ValidIdent(s) }

// ErrAlreadyExists wraps the error returned by Create when the named
// virtual table already exists and WithIfNotExists was not passed.
// Match via errors.Is to branch between create-or-open without
// duplicating the existence check.
//
// Note that ErrAlreadyExists does NOT signal a schema mismatch — if
// the existing table was created with a different dim, metric, or
// encoding, you'll still get this error. Use Open to verify the
// schema you expect.
var ErrAlreadyExists = errors.New("vec: virtual table already exists")

// CreateOption configures a single Create call. Compose via the
// variadic Create(ctx, db, name, dim, opts, createOpts...) tail.
type CreateOption func(*createConfig)

type createConfig struct {
	ifNotExists bool
}

// WithIfNotExists makes Create idempotent: if the table already exists,
// Create returns a Table handle for it instead of erroring with
// ErrAlreadyExists. The existing table's schema is NOT validated against
// the dim / metric / encoding you pass — if those differ from what the
// table was created with, Insert / KNN may fail at runtime. Use Open
// instead when you want strict schema-match semantics on an existing
// table.
//
// Typical use is migrate-on-startup where you want the create to be
// a no-op on subsequent runs:
//
//	tbl, err := vec.Create(ctx, db, "docs", 384, vec.Options{Metric: vec.Cosine},
//	    vec.WithIfNotExists())
func WithIfNotExists() CreateOption {
	return func(c *createConfig) { c.ifNotExists = true }
}

// Create runs `CREATE VIRTUAL TABLE name USING vec0(embedding float[dim])`
// with the supplied options and returns a Table handle. By default the
// call errors with [ErrAlreadyExists] (wrapped) if name is already a
// virtual table; pass [WithIfNotExists] to make the call idempotent.
//
// dim is required and must be positive. opts may be the zero value, in
// which case Metric defaults to L2 and Encoding defaults to JSON.
func Create(ctx context.Context, db *sql.DB, name string, dim int, opts Options, createOpts ...CreateOption) (*Table, error) {
	if dim <= 0 {
		return nil, fmt.Errorf("vec.Create: dim must be > 0, got %d", dim)
	}
	if !validIdent(name) {
		return nil, fmt.Errorf("vec.Create: %q is not a valid SQL identifier", name)
	}
	cfg := &createConfig{}
	for _, opt := range createOpts {
		opt(cfg)
	}
	col := "embedding"
	// vec0's column-argument parser is strict: bare identifiers only, with
	// options space-separated. We assemble the column declaration without
	// backticks; the table name itself goes through quote() because the
	// surrounding CREATE VIRTUAL TABLE keyword accepts quoted identifiers.
	ifNotExists := ""
	if cfg.ifNotExists {
		ifNotExists = "IF NOT EXISTS "
	}
	stmt := fmt.Sprintf(
		"CREATE VIRTUAL TABLE %s%s USING vec0(%s float[%d] distance=%s)",
		ifNotExists, quote(name), col, dim, opts.Metric.Keyword(),
	)
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		if isAlreadyExistsErr(err) {
			return nil, fmt.Errorf("vec.Create %q: %w", name, ErrAlreadyExists)
		}
		return nil, fmt.Errorf("vec.Create %q: %w", name, err)
	}
	return &Table{
		db:        db,
		name:      name,
		embedding: col,
		dim:       dim,
		metric:    opts.Metric,
		encoding:  opts.Encoding,
	}, nil
}

// isAlreadyExistsErr reports whether err carries SQLite's "table X
// already exists" signal. SQLite returns SQLITE_ERROR (no extended
// code) for this; we string-match the engine's stable message
// fragment. The match is lowercased for safety against future-version
// case changes.
func isAlreadyExistsErr(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "already exists")
}

// Open returns a Table handle for a vec0 virtual table that already exists in
// db. It does not validate the table's schema — the dim/encoding/metric
// arguments must match what was declared when the table was created. Use
// Create instead when you want the package to issue CREATE for you.
func Open(db *sql.DB, name string, dim int, opts Options) (*Table, error) {
	if dim <= 0 {
		return nil, fmt.Errorf("vec.Open: dim must be > 0, got %d", dim)
	}
	if name == "" {
		return nil, errors.New("vec.Open: name is required")
	}
	return &Table{
		db:        db,
		name:      name,
		embedding: "embedding",
		dim:       dim,
		metric:    opts.Metric,
		encoding:  opts.Encoding,
	}, nil
}

// Name returns the table name as known to SQLite.
func (t *Table) Name() string { return t.name }

// Dim returns the vector dimension this table was created with.
func (t *Table) Dim() int { return t.dim }

// Metric returns the distance metric this table uses.
func (t *Table) Metric() Metric { return t.metric }

// Encoding returns the wire encoding used for vector values.
func (t *Table) Encoding() Encoding { return t.encoding }

// Insert adds a single row keyed by rowid. Returns an error if the rowid
// already exists — sqlite-vec's vec0 INSERT does not honor SQLite's
// OR REPLACE conflict resolution, so we report the conflict honestly. Use
// Update to overwrite an existing row.
//
// The embedding length must match the table's dim.
func (t *Table) Insert(ctx context.Context, rowid int64, embedding []float32) error {
	if len(embedding) != t.dim {
		return fmt.Errorf("vec.Insert: embedding length %d != dim %d", len(embedding), t.dim)
	}
	q := fmt.Sprintf(
		"INSERT INTO %s (rowid, %s) VALUES (?, %s)",
		quote(t.name), quote(t.embedding), matchPlaceholder(t.encoding),
	)
	_, err := t.db.ExecContext(ctx, q, rowid, encodeValue(embedding, t.encoding))
	return err
}

// Row is a single (rowid, embedding) pair consumed by BatchInsert.
type Row struct {
	Rowid     int64
	Embedding []float32
}

// BatchInsert inserts every item in a single transaction. Each rowid must be
// unique within the table; conflicts surface as errors (sqlite-vec's vec0
// INSERT does not honor OR REPLACE).
func (t *Table) BatchInsert(ctx context.Context, items []Row) error {
	tx, err := t.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	q := fmt.Sprintf(
		"INSERT INTO %s (rowid, %s) VALUES (?, %s)",
		quote(t.name), quote(t.embedding), matchPlaceholder(t.encoding),
	)
	stmt, err := tx.PrepareContext(ctx, q)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()
	for i, it := range items {
		if len(it.Embedding) != t.dim {
			tx.Rollback()
			return fmt.Errorf("vec.BatchInsert[%d]: embedding length %d != dim %d", i, len(it.Embedding), t.dim)
		}
		if _, err := stmt.ExecContext(ctx, it.Rowid, encodeValue(it.Embedding, t.encoding)); err != nil {
			tx.Rollback()
			return fmt.Errorf("vec.BatchInsert[%d]: %w", i, err)
		}
	}
	return tx.Commit()
}

// Update replaces the embedding for an existing rowid via a real UPDATE
// statement. If the rowid doesn't exist, Update is a no-op and returns nil
// (matching SQL's UPDATE-without-matching-row semantics).
//
// Use Insert to add a new rowid. Callers wanting upsert behavior should
// dispatch on a prior existence check or fall back from Update to Insert
// based on rows-affected.
func (t *Table) Update(ctx context.Context, rowid int64, embedding []float32) error {
	if len(embedding) != t.dim {
		return fmt.Errorf("vec.Update: embedding length %d != dim %d", len(embedding), t.dim)
	}
	q := fmt.Sprintf(
		"UPDATE %s SET %s = %s WHERE rowid = ?",
		quote(t.name), quote(t.embedding), matchPlaceholder(t.encoding),
	)
	_, err := t.db.ExecContext(ctx, q, encodeValue(embedding, t.encoding), rowid)
	return err
}

// Delete removes the row with the given rowid. Returns nil if the row didn't
// exist.
func (t *Table) Delete(ctx context.Context, rowid int64) error {
	_, err := t.db.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE rowid = ?", quote(t.name)), rowid)
	return err
}

// Drop removes the underlying virtual table. After calling Drop, the Table
// handle is no longer usable.
func (t *Table) Drop(ctx context.Context) error {
	_, err := t.db.ExecContext(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", quote(t.name)))
	return err
}

// Neighbor is one row returned by KNN: the matched rowid and the
// distance scored by the table's [Metric].
type Neighbor struct {
	Rowid    int64
	Distance float64
}

// KNN issues an approximate k-nearest-neighbour query for the given vector
// and returns an iter.Seq2 cursor over the results in ascending-distance
// order. Yielding stops at k rows or on error.
//
// Optional QueryOptions filter the result set. WithFilter appends a
// custom WHERE conjunct (e.g. "rowid IN (1, 2, 3)" to restrict to known
// IDs); see [WithFilter] for details.
//
// The yielded error is always nil except for the final iteration after a
// row-scan failure, where it carries the scan error and the Neighbor is
// the zero value. Iterating with `for m, err := range tbl.KNN(...)`
// follows the idiomatic Go-1.23 range-over-func convention.
func (t *Table) KNN(ctx context.Context, query []float32, k int, opts ...QueryOption) iter.Seq2[Neighbor, error] {
	cfg := &queryConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	return func(yield func(Neighbor, error) bool) {
		if cfg.selectExtra != "" || cfg.joinClause != "" {
			yield(Neighbor{}, errors.New(
				"vec.KNN: WithSelect / WithJoin change the row shape; use Table.KNNSQL "+
					"with db.QueryContext (or gorm db.Raw(...).Scan) to consume custom projections"))
			return
		}
		sql, args, err := t.buildKNNSQL(query, k, cfg)
		if err != nil {
			yield(Neighbor{}, err)
			return
		}
		if sql == "" {
			// k <= 0 — no error, no rows. Mirrors the prior behavior.
			return
		}
		rows, err := t.db.QueryContext(ctx, sql, args...)
		if err != nil {
			yield(Neighbor{}, err)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var m Neighbor
			if err := rows.Scan(&m.Rowid, &m.Distance); err != nil {
				yield(Neighbor{}, err)
				return
			}
			if !yield(m, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(Neighbor{}, err)
		}
	}
}

// KNNSlice is a convenience wrapper that collects the first k matches into a
// slice. Use it when you don't need streaming behavior. Accepts the same
// QueryOptions as KNN; WithSelect / WithJoin are not honored (use
// [Table.KNNSQL] instead).
func (t *Table) KNNSlice(ctx context.Context, query []float32, k int, opts ...QueryOption) ([]Neighbor, error) {
	// Cap the initial slice capacity so a caller passing a pathological
	// k (millions) doesn't pre-allocate gigabytes before the query
	// runs, and clamp negative k to 0 so make() doesn't panic. The
	// streaming KNN iter already returns no rows for k<=0; mirror that.
	capHint := min(max(k, 0), 1024)
	out := make([]Neighbor, 0, capHint)
	for m, err := range t.KNN(ctx, query, k, opts...) {
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// KNNSQL returns the SQL statement and bound arguments that KNN would
// execute, without actually running it. Pair with [database/sql.DB.QueryContext]
// or gorm's `db.Raw(sql, args...).Scan(&out)` when you want to extend
// the projection (via [WithSelect]) or join companion data (via
// [WithJoin]) and scan rows into a custom struct.
//
// The returned SQL is parameterized: the query embedding is bound as
// the first argument (after any [WithRanking]-style positional args
// upstream of MATCH — there are none today, but the contract is
// "args in the order they appear in the SQL"). The vector encoding
// matches the Table's configured Encoding.
//
// Example with [WithJoin] + [WithSelect]:
//
//	sql, args, err := tbl.KNNSQL(query, 10,
//	    vec.WithSelect("items.id, items.title"),
//	    vec.WithJoin("JOIN items ON items.id = items_vec.rowid"),
//	    vec.WithFilter("items.tenant = ?", "acme"),
//	)
//	if err != nil { return err }
//	rows, _ := db.QueryContext(ctx, sql, args...)
//
// k=0 returns an empty SQL string and no args; callers should
// short-circuit. Negative k is treated the same.
func (t *Table) KNNSQL(query []float32, k int, opts ...QueryOption) (string, []any, error) {
	cfg := &queryConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	return t.buildKNNSQL(query, k, cfg)
}

// buildKNNSQL composes the SQL + bind args for KNN / KNNSlice / KNNSQL.
// Returns an empty string with nil args when k <= 0 so callers can
// short-circuit without iterating an empty Rows.
func (t *Table) buildKNNSQL(query []float32, k int, cfg *queryConfig) (string, []any, error) {
	if len(query) != t.dim {
		return "", nil, fmt.Errorf("vec.KNN: query length %d != dim %d", len(query), t.dim)
	}
	if k <= 0 {
		return "", nil, nil
	}
	var b strings.Builder
	b.WriteString("SELECT rowid, distance")
	if cfg.selectExtra != "" {
		b.WriteString(", ")
		b.WriteString(cfg.selectExtra)
	}
	b.WriteString(" FROM ")
	b.WriteString(quote(t.name))
	if cfg.joinClause != "" {
		b.WriteString(" ")
		b.WriteString(cfg.joinClause)
	}
	b.WriteString(" WHERE ")
	b.WriteString(quote(t.embedding))
	b.WriteString(" MATCH ")
	b.WriteString(matchPlaceholder(t.encoding))
	// k = N is required when there's a JOIN — sqlite-vec's planner can't
	// extract a `LIMIT N` constraint through a join boundary, but the
	// `k = N` predicate is a vec0-recognized vtab hint that survives.
	// When no join is in play, `LIMIT N` is fine (and produces simpler
	// EXPLAIN output).
	joining := cfg.joinClause != "" || cfg.selectExtra != ""
	if joining {
		fmt.Fprintf(&b, " AND k = %d", k)
	}
	if cfg.whereSQL != "" {
		b.WriteString(" AND (")
		b.WriteString(cfg.whereSQL)
		b.WriteString(")")
	}
	if cfg.orderByExpr != "" {
		b.WriteString(" ORDER BY ")
		b.WriteString(cfg.orderByExpr)
	} else {
		b.WriteString(" ORDER BY distance")
	}
	// LIMIT is inlined as a literal integer (no injection risk; k is a
	// Go int controlled by the caller). For the join case we still
	// emit a LIMIT so the outer SELECT bounds rows correctly when
	// the join expands cardinality.
	fmt.Fprintf(&b, " LIMIT %d", k)

	args := make([]any, 0, 1+len(cfg.whereArgs))
	args = append(args, encodeValue(query, t.encoding))
	args = append(args, cfg.whereArgs...)
	return b.String(), args, nil
}

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
	columns   []Column // declared metadata / partition / auxiliary columns

	// Pre-rendered SQL strings for the per-row Insert / Update /
	// Delete paths. They depend only on (name, embedding, encoding,
	// columns) which are immutable after Create / Open, so caching at
	// construction avoids re-running fmt.Sprintf on every call.
	insertSQL string
	updateSQL string
	deleteSQL string
}

// buildSQL prepares the cached SQL strings; called from Create and
// Open after every Table field is populated. When the table declares extra
// columns (metadata / partition / auxiliary), the INSERT carries them after
// the embedding; UPDATE still touches only the embedding (change column values
// with a raw UPDATE or delete+insert).
func (t *Table) buildSQL() {
	cols := []string{"rowid", quote(t.embedding)}
	phs := []string{"?", t.encoding.Placeholder()}
	for _, c := range t.columns {
		cols = append(cols, quote(c.Name))
		phs = append(phs, "?")
	}
	t.insertSQL = fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s)",
		quote(t.name), strings.Join(cols, ", "), strings.Join(phs, ", "),
	)
	t.updateSQL = fmt.Sprintf(
		"UPDATE %s SET %s = %s WHERE rowid = ?",
		quote(t.name), quote(t.embedding), t.encoding.Placeholder(),
	)
	t.deleteSQL = fmt.Sprintf("DELETE FROM %s WHERE rowid = ?", quote(t.name))
}

// insertArgs builds the positional bind args for insertSQL: rowid, the encoded
// embedding, then each declared column's value from values (nil when absent, so
// the simple Insert path can pass a nil map).
func (t *Table) insertArgs(rowid int64, embedding []float32, values map[string]any) []any {
	args := make([]any, 0, 2+len(t.columns))
	args = append(args, rowid, t.encoding.Encode(embedding))
	for _, c := range t.columns {
		args = append(args, values[c.Name])
	}
	return args
}

// columnDDL renders the vec0 column-declaration fragment for c (validating the
// name). Partition keys get the "partition key" suffix; auxiliary columns the
// "+" prefix; metadata columns are bare "name type".
func columnDDL(c Column) (string, error) {
	if !validIdent(c.Name) {
		return "", fmt.Errorf("vec.Create: column %q is not a valid SQL identifier", c.Name)
	}
	if !validIdent(c.Type) {
		return "", fmt.Errorf("vec.Create: column %q has invalid type %q", c.Name, c.Type)
	}
	switch c.Kind {
	case Partition:
		return fmt.Sprintf("%s %s partition key", c.Name, c.Type), nil
	case Auxiliary:
		return fmt.Sprintf("+%s %s", c.Name, c.Type), nil
	default:
		return fmt.Sprintf("%s %s", c.Name, c.Type), nil
	}
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

// Package-local short aliases for the exported helpers — kept because
// every SQL-emitting helper in this package interpolates identifiers
// inline, and using the short forms keeps the formatted SQL readable.
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
	// The Bit encoding (bit[N] column) ranks by Hamming distance only; force
	// it so a stray L2/Cosine default doesn't produce an invalid DDL.
	metric := opts.Metric
	if opts.Encoding == Bit {
		metric = Hamming
	}
	// vec0's column-argument parser is strict: bare identifiers only, with
	// options space-separated. We assemble the column declaration without
	// backticks; the table name itself goes through quote() because the
	// surrounding CREATE VIRTUAL TABLE keyword accepts quoted identifiers. The
	// column storage type (float / int8 / bit) comes from the encoding.
	ifNotExists := ""
	if cfg.ifNotExists {
		ifNotExists = "IF NOT EXISTS "
	}
	// bit[N] columns rank by Hamming implicitly and reject a distance= clause;
	// every other column type takes the explicit metric keyword.
	colDecl := fmt.Sprintf("%s %s[%d]", col, opts.Encoding.columnType(), dim)
	if opts.Encoding != Bit {
		colDecl += " distance=" + metric.Keyword()
	}
	// Assemble the column list: embedding first, then declared metadata /
	// partition / auxiliary columns, then chunk_size (verified to parse in this
	// order against sqlite-vec).
	specs := []string{colDecl}
	for _, c := range opts.Columns {
		decl, err := columnDDL(c)
		if err != nil {
			return nil, err
		}
		specs = append(specs, decl)
	}
	if opts.ChunkSize > 0 {
		specs = append(specs, fmt.Sprintf("chunk_size=%d", opts.ChunkSize))
	}
	stmt := fmt.Sprintf("CREATE VIRTUAL TABLE %s%s USING vec0(%s)", ifNotExists, quote(name), strings.Join(specs, ", "))
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		if isAlreadyExistsErr(err) {
			return nil, fmt.Errorf("vec.Create %q: %w", name, ErrAlreadyExists)
		}
		return nil, fmt.Errorf("vec.Create %q: %w", name, err)
	}
	t := &Table{
		db:        db,
		name:      name,
		embedding: col,
		dim:       dim,
		metric:    metric,
		encoding:  opts.Encoding,
		columns:   opts.Columns,
	}
	t.buildSQL()
	return t, nil
}

// isAlreadyExistsErr is a thin local alias for [internal/sqlid.IsAlreadyExistsErr]
// — both vec and fts need the same upstream-message-fragment match, so the
// implementation lives in the shared internal/sqlid helper.
func isAlreadyExistsErr(err error) bool { return sqlid.IsAlreadyExistsErr(err) }

// Open returns a Table handle for a vec0 virtual table that already exists in
// db. It does not validate the table's schema — the dim/encoding/metric
// arguments must match what was declared when the table was created. Use
// Create instead when you want the package to issue CREATE for you.
func Open(db *sql.DB, name string, dim int, opts Options) (*Table, error) {
	if dim <= 0 {
		return nil, fmt.Errorf("vec.Open: dim must be > 0, got %d", dim)
	}
	// Use the same admission rule as Create: ValidIdent rejects names
	// that vec0 would mishandle anyway, AND blocks string-builder
	// injection through this entry point. The previous empty-string
	// check let `Open(db, "items; DROP", …)` past the door.
	if !validIdent(name) {
		return nil, fmt.Errorf("vec.Open: %q is not a valid SQL identifier", name)
	}
	metric := opts.Metric
	if opts.Encoding == Bit {
		metric = Hamming
	}
	t := &Table{
		db:        db,
		name:      name,
		embedding: "embedding",
		dim:       dim,
		metric:    metric,
		encoding:  opts.Encoding,
		columns:   opts.Columns,
	}
	t.buildSQL()
	return t, nil
}

// Close is a no-op kept for API symmetry with [fts.Index.Close] and
// io.Closer-shaped consumer code. Table holds no per-instance resource
// (the *sql.DB is caller-owned), so `defer tbl.Close()` is safe to add
// without any teardown cost. Always returns nil.
func (t *Table) Close() error { return nil }

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
	// nil values map: declared columns (if any) bind NULL. Use InsertRow /
	// BatchInsert with Row.Values to set them.
	_, err := t.db.ExecContext(ctx, t.insertSQL, t.insertArgs(rowid, embedding, nil)...)
	return err
}

// Row is a single (rowid, embedding) pair consumed by BatchInsert and
// InsertRow. Values supplies the declared metadata / partition / auxiliary
// column values keyed by column name; a column absent from the map binds NULL.
type Row struct {
	Rowid     int64
	Embedding []float32
	Values    map[string]any
}

// InsertRow inserts a single row including its declared column values (Row.Values).
// Use it instead of Insert when the table has metadata / partition / auxiliary
// columns. Like Insert, a duplicate rowid is an error (vec0 INSERT ignores
// OR REPLACE).
func (t *Table) InsertRow(ctx context.Context, row Row) error {
	if len(row.Embedding) != t.dim {
		return fmt.Errorf("vec.InsertRow: embedding length %d != dim %d", len(row.Embedding), t.dim)
	}
	_, err := t.db.ExecContext(ctx, t.insertSQL, t.insertArgs(row.Rowid, row.Embedding, row.Values)...)
	return err
}

// BatchInsert inserts every item in a single transaction. Each rowid must be
// unique within the table; conflicts surface as errors (sqlite-vec's vec0
// INSERT does not honor OR REPLACE).
func (t *Table) BatchInsert(ctx context.Context, items []Row) error {
	tx, err := t.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, t.insertSQL)
	if err != nil {
		return errors.Join(err, tx.Rollback())
	}
	defer stmt.Close()
	for i, it := range items {
		if len(it.Embedding) != t.dim {
			return errors.Join(
				fmt.Errorf("vec.BatchInsert[%d]: embedding length %d != dim %d", i, len(it.Embedding), t.dim),
				tx.Rollback(),
			)
		}
		if _, err := stmt.ExecContext(ctx, t.insertArgs(it.Rowid, it.Embedding, it.Values)...); err != nil {
			return errors.Join(fmt.Errorf("vec.BatchInsert[%d]: %w", i, err), tx.Rollback())
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
	_, err := t.db.ExecContext(ctx, t.updateSQL, t.encoding.Encode(embedding), rowid)
	return err
}

// Delete removes the row with the given rowid. Returns nil if the row didn't
// exist.
func (t *Table) Delete(ctx context.Context, rowid int64) error {
	_, err := t.db.ExecContext(ctx, t.deleteSQL, rowid)
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
	b.WriteString(t.encoding.Placeholder())
	// k = N is required ONLY when there's a JOIN: the LIMIT then bounds the
	// outer (post-join) result, so the vtab still needs the vec0-recognized
	// `k = N` hint to bound its KNN. On a direct vtab scan (no join — even with
	// WithSelect adding projection columns) emitting both k= and LIMIT is
	// rejected by sqlite-vec ("Only LIMIT or 'k =?' can be provided, not
	// both"), so we rely on LIMIT alone there.
	if cfg.joinClause != "" {
		fmt.Fprintf(&b, " AND k = %d", k)
	}
	emitWhereArgs := false
	if cfg.whereSQL != "" {
		b.WriteString(" AND (")
		b.WriteString(cfg.whereSQL)
		b.WriteString(")")
		emitWhereArgs = true
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

	// Bind args mirror placeholders: 1 for MATCH, then the WHERE args
	// only when the WHERE clause was actually emitted. WithFilter("", x, y)
	// stripping the SQL but still binding x, y would smuggle extra
	// positional binds into the prepared statement; gate on the boolean
	// so the two move together.
	argCap := 1
	if emitWhereArgs {
		argCap += len(cfg.whereArgs)
	}
	args := make([]any, 0, argCap)
	args = append(args, t.encoding.Encode(query))
	if emitWhereArgs {
		args = append(args, cfg.whereArgs...)
	}
	return b.String(), args, nil
}

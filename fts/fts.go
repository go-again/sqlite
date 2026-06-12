package fts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"iter"
	"strings"

	"github.com/go-again/sqlite/internal/sqlid"
)

// Attr is a single (key, value) attribute. K is the rowid mapping (typically
// int64 or string) and V is the searchable text (string or []byte).
//
// For multi-column tables the Extras map carries additional column values
// keyed by column name; nil/empty Extras means "no extra columns".
type Attr[K, V SQLType] struct {
	Key    K
	Value  V
	Extras map[string]any
}

// Hit is a single search hit returned by [Index.Search].
type Hit[K, V SQLType] struct {
	// Key mirrors the rowid stored at insert time.
	Key K

	// Value is the primary column's value. When the index has multiple
	// columns, Extras carries the rest.
	Value V

	// Rank is FTS5's bm25 score for the match. Smaller is better — FTS5's
	// bm25() returns negative scores by convention. Zero when WithRanking
	// is not requested.
	Rank float64

	// Snippet is FTS5's snippet() output when WithSnippet is set on the
	// query.
	Snippet string

	// Highlight is FTS5's highlight() output when WithHighlight is set.
	Highlight string

	// Extras carries the remaining column values, keyed by column name, when
	// the index has more than one user column.
	Extras map[string]any
}

// Index is a typed handle to an FTS5 virtual table.
//
// K is the type of the key column (FTS5's hidden rowid in the default
// configuration). V is the type of the primary value column.
//
// Index is safe for concurrent use insofar as the underlying *sql.DB is.
type Index[K, V SQLType] struct {
	db      *sql.DB
	name    string
	columns []string
	ext     *External

	// Pre-rendered SQL for the per-row Insert / Delete paths. Computed
	// once from (name, columns) at New/Open time; saves repeating the
	// fmt.Sprintf + strings.Repeat dance on every call.
	insertSQL string
	deleteSQL string
}

// buildSQL pre-renders the per-row SQL strings for Insert/Delete.
func (i *Index[K, V]) buildSQL() {
	cols := append([]string{"rowid"}, i.columns...)
	placeholders := strings.Repeat("?, ", len(cols))
	placeholders = placeholders[:len(placeholders)-2]
	i.insertSQL = fmt.Sprintf(
		"INSERT OR REPLACE INTO %s (%s) VALUES (%s)",
		quote(i.name), strings.Join(cols, ", "), placeholders,
	)
	i.deleteSQL = fmt.Sprintf("DELETE FROM %s WHERE rowid = ?", quote(i.name))
}

// ErrAlreadyExists wraps the error returned by New when the named FTS5
// virtual table already exists and WithIfNotExists was not passed.
// Match via errors.Is to branch between create-or-open without
// duplicating the existence check.
//
// Note that ErrAlreadyExists does NOT signal a schema mismatch — if
// the existing table was created with different columns, a different
// tokenizer, or in a different mode, you'll still get this error. Use
// Open to verify the schema you expect.
var ErrAlreadyExists = errors.New("fts: virtual table already exists")

// CreateOption configures a single New call. Compose via the variadic
// New(ctx, db, name, opts, createOpts...) tail.
type CreateOption func(*createConfig)

type createConfig struct {
	ifNotExists bool
}

// WithIfNotExists makes New idempotent: if the table already exists,
// New returns an Index handle for it instead of erroring with
// ErrAlreadyExists. The existing table's schema is NOT validated
// against the columns / tokenizer / mode you pass — if those differ
// from what the table was created with, Insert / Search may surface
// errors at runtime. Use Open instead when you want strict schema-
// match semantics on an existing table.
//
// Typical use is migrate-on-startup where you want the create to be a
// no-op on subsequent runs.
func WithIfNotExists() CreateOption {
	return func(c *createConfig) { c.ifNotExists = true }
}

// New creates an FTS5 virtual table named `name` configured by opts
// and returns a typed Index handle. The CREATE VIRTUAL TABLE statement
// is executed against db immediately.
//
// By default the call errors with [ErrAlreadyExists] (wrapped) if name
// already exists; pass [WithIfNotExists] to make the call idempotent.
// To re-attach to an existing table with full schema validation, use
// [Open] instead.
func New[K, V SQLType](ctx context.Context, db *sql.DB, name string, opts Options, createOpts ...CreateOption) (*Index[K, V], error) {
	if !validIdent(name) {
		return nil, fmt.Errorf("fts.New: %q is not a valid SQL identifier", name)
	}
	// External + Contentless are documented as mutually exclusive
	// (see Options.External / Options.Contentless docstrings) but the
	// downstream CREATE-VIRTUAL-TABLE SQL would merely error with an
	// opaque FTS5 message. Fail loud here with the caller's API names
	// so the bug is obvious.
	if opts.External != nil && opts.Contentless {
		return nil, fmt.Errorf("fts.New: External and Contentless are mutually exclusive")
	}
	// Tokenizer args are escaped for the SQL literal (wrapTokenize), but a NUL
	// would still truncate the generated SQL at the C-string boundary, so refuse
	// it here where we still have an error channel.
	if opts.Tokenizer != nil {
		if err := validateTokenizer(opts.Tokenizer); err != nil {
			return nil, fmt.Errorf("fts.New: %w", err)
		}
	}
	// Columns vs ColumnsRich silently shadow per columnSpecs() (rich
	// wins); a non-empty bare Columns alongside non-empty ColumnsRich
	// usually means a mid-edit typo. Error so the caller picks one.
	if len(opts.Columns) > 0 && len(opts.ColumnsRich) > 0 {
		return nil, fmt.Errorf("fts.New: set Options.Columns OR Options.ColumnsRich, not both")
	}
	specs := opts.columnSpecs()
	cols := make([]string, len(specs))
	for i, c := range specs {
		if !validIdent(c.Name) {
			return nil, fmt.Errorf("fts.New: column %q is not a valid SQL identifier", c.Name)
		}
		cols[i] = c.Name
	}
	// External content-table / content-rowid get interpolated into the
	// content='…' option as single-quoted strings; without identifier
	// validation a name containing `'` would inject SQL into the
	// CREATE VIRTUAL TABLE statement.
	if opts.External != nil {
		if !validIdent(opts.External.ContentTable) {
			return nil, fmt.Errorf("fts.New: External.ContentTable %q is not a valid SQL identifier",
				opts.External.ContentTable)
		}
		if opts.External.ContentRowid != "" && !validIdent(opts.External.ContentRowid) {
			return nil, fmt.Errorf("fts.New: External.ContentRowid %q is not a valid SQL identifier",
				opts.External.ContentRowid)
		}
	}
	cfg := &createConfig{}
	for _, opt := range createOpts {
		opt(cfg)
	}

	var parts []string
	for _, c := range specs {
		if c.Unindexed {
			parts = append(parts, c.Name+" UNINDEXED")
		} else {
			parts = append(parts, c.Name)
		}
	}
	for _, expr := range []string{
		opts.tokenizerExpr(),
		opts.prefixExpr(),
		opts.externalExpr(),
		opts.detailExpr(),
		opts.contentlessDeleteExpr(),
	} {
		if expr != "" {
			parts = append(parts, expr)
		}
	}

	ifNotExists := ""
	if cfg.ifNotExists {
		ifNotExists = "IF NOT EXISTS "
	}
	stmt := fmt.Sprintf("CREATE VIRTUAL TABLE %s%s USING fts5(%s)", ifNotExists, quote(name), strings.Join(parts, ", "))
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		if isAlreadyExistsErr(err) {
			return nil, fmt.Errorf("fts.New %q: %w", name, ErrAlreadyExists)
		}
		return nil, fmt.Errorf("fts.New %q: %w", name, err)
	}

	// External-content sync triggers, if requested. The columns the
	// triggers reference are the FTS5 columns; they must exist on the
	// content table with matching names.
	if opts.External != nil && opts.External.SyncTriggers != 0 {
		if err := installSyncTriggers(ctx, db, name,
			opts.External.ContentTable, opts.External.ContentRowid,
			cols, opts.External.SyncTriggers); err != nil {
			return nil, fmt.Errorf("fts.New %q: %w", name, err)
		}
	}

	idx := &Index[K, V]{db: db, name: name, columns: cols, ext: opts.External}
	idx.buildSQL()
	return idx, nil
}

// isAlreadyExistsErr is a thin local alias for [internal/sqlid.IsAlreadyExistsErr]
// — both vec and fts need the same upstream-message-fragment match, so the
// implementation lives in the shared internal/sqlid helper.
func isAlreadyExistsErr(err error) bool { return sqlid.IsAlreadyExistsErr(err) }

// Open returns a typed handle to an FTS5 table that already exists. It does
// not validate the table's schema; the caller asserts that cols matches the
// actual user-visible columns. When cols is empty, the default ["value"]
// is assumed.
func Open[K, V SQLType](db *sql.DB, name string, cols ...string) (*Index[K, V], error) {
	if !validIdent(name) {
		return nil, fmt.Errorf("fts.Open: %q is not a valid SQL identifier", name)
	}
	if len(cols) == 0 {
		cols = []string{"value"}
	}
	for _, c := range cols {
		if !validIdent(c) {
			return nil, fmt.Errorf("fts.Open: column %q is not a valid SQL identifier", c)
		}
	}
	idx := &Index[K, V]{db: db, name: name, columns: cols}
	idx.buildSQL()
	return idx, nil
}

// Name returns the underlying table name.
func (i *Index[K, V]) Name() string { return i.name }

// Columns returns the user-visible column names in declaration order.
func (i *Index[K, V]) Columns() []string {
	out := make([]string, len(i.columns))
	copy(out, i.columns)
	return out
}

// Insert adds (or replaces) one or more rows in a single transaction. Use
// the Attr.Extras map to supply values for non-primary columns.
//
// For single-column indexes the Value field maps to the column named in
// Options.Columns (default "value"). For multi-column indexes the primary
// column receives Value and the rest receive Extras[colName].
func (i *Index[K, V]) Insert(ctx context.Context, items ...Attr[K, V]) error {
	if len(items) == 0 {
		return nil
	}

	tx, err := i.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	prep, err := tx.PrepareContext(ctx, i.insertSQL)
	if err != nil {
		return errors.Join(err, tx.Rollback())
	}
	defer prep.Close()
	for n, a := range items {
		args := make([]any, 0, 1+len(i.columns))
		args = append(args, a.Key)
		// Primary column = Value; remaining columns drawn from Extras.
		for k, col := range i.columns {
			if k == 0 {
				args = append(args, a.Value)
				continue
			}
			args = append(args, a.Extras[col])
		}
		if _, err := prep.ExecContext(ctx, args...); err != nil {
			return errors.Join(fmt.Errorf("fts.Insert[%d]: %w", n, err), tx.Rollback())
		}
	}
	return tx.Commit()
}

// Delete removes the rows with the given keys. Returns nil if none of them
// existed.
func (i *Index[K, V]) Delete(ctx context.Context, keys ...K) error {
	if len(keys) == 0 {
		return nil
	}
	tx, err := i.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	prep, err := tx.PrepareContext(ctx, i.deleteSQL)
	if err != nil {
		return errors.Join(err, tx.Rollback())
	}
	defer prep.Close()
	for _, k := range keys {
		if _, err := prep.ExecContext(ctx, k); err != nil {
			return errors.Join(err, tx.Rollback())
		}
	}
	return tx.Commit()
}

// Rebuild reconstructs an external-content FTS5 index from the source table.
// No-op for ordinary FTS5 tables, though FTS5 still accepts it; the call
// returns nil there too.
func (i *Index[K, V]) Rebuild(ctx context.Context) error {
	_, err := i.db.ExecContext(ctx, fmt.Sprintf("INSERT INTO %s(%s) VALUES('rebuild')", quote(i.name), quote(i.name)))
	return err
}

// Optimize tells FTS5 to merge segments and reclaim space.
func (i *Index[K, V]) Optimize(ctx context.Context) error {
	_, err := i.db.ExecContext(ctx, fmt.Sprintf("INSERT INTO %s(%s) VALUES('optimize')", quote(i.name), quote(i.name)))
	return err
}

// Merge runs a single-step merge of up to `pages` pages. Use 0 to let FTS5
// pick. See https://www.sqlite.org/fts5.html section 7.
func (i *Index[K, V]) Merge(ctx context.Context, pages int) error {
	stmt := fmt.Sprintf("INSERT INTO %s(%s, rank) VALUES('merge', ?)", quote(i.name), quote(i.name))
	_, err := i.db.ExecContext(ctx, stmt, pages)
	return err
}

// Drop removes the FTS5 virtual table. The Index handle is invalid after Drop
// returns.
func (i *Index[K, V]) Drop(ctx context.Context) error {
	_, err := i.db.ExecContext(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", quote(i.name)))
	return err
}

// Close releases any resources owned by the Index. The current implementation
// is a no-op because *Index holds no per-instance handles — the underlying
// *sql.DB is owned by the caller and Index keeps only configuration.
//
// Provided for API symmetry with similar libraries (e.g. zalgonoise/fts) so
// `defer idx.Close()` is harmless. Callers should still Close the *sql.DB
// they passed to New / Open.
func (i *Index[K, V]) Close() error {
	return nil
}

// SearchOption configures a single Search call.
type SearchOption func(*searchConfig)

type searchConfig struct {
	limit              int
	offset             int
	withRank           bool
	bm25Weights        []float64
	snippetColumn      string
	snippetBefore      string
	snippetAfter       string
	snippetEllipsis    string
	snippetTokens      int
	snippetRequested   bool
	highlightColumn    string
	highlightBefore    string
	highlightAfter     string
	highlightRequested bool
	whereSQL           string
	whereArgs          []any
	selectExtra        string
	joinClause         string
	orderByExpr        string
}

// WithLimit caps the number of returned rows. Zero or negative means no
// limit (default).
func WithLimit(n int) SearchOption {
	return func(c *searchConfig) { c.limit = n }
}

// WithOffset skips n rows before returning.
func WithOffset(n int) SearchOption {
	return func(c *searchConfig) { c.offset = n }
}

// WithRanking enables BM25 ranking; weights, if supplied, are per column in
// declaration order. Without WithRanking, Hit.Rank is zero.
func WithRanking(weights ...float64) SearchOption {
	return func(c *searchConfig) {
		c.withRank = true
		c.bm25Weights = weights
	}
}

// WithSnippet enables FTS5's snippet() function for the named column. before
// and after wrap each matched term; ellipsis is inserted at snippet
// boundaries; tokens is the maximum number of tokens in the snippet.
//
// See https://www.sqlite.org/fts5.html section 5.
func WithSnippet(column, before, after, ellipsis string, tokens int) SearchOption {
	return func(c *searchConfig) {
		c.snippetRequested = true
		c.snippetColumn = column
		c.snippetBefore = before
		c.snippetAfter = after
		c.snippetEllipsis = ellipsis
		c.snippetTokens = tokens
	}
}

// WithFilter appends a custom WHERE conjunct to the FTS5 search. The SQL
// fragment is AND'd with the MATCH clause; bind parameters in args bind
// in declaration order. Use this for per-tenant, per-user, or other
// column-level filtering without dropping to raw SQL.
//
// # Trust model
//
// The fragment is **caller-trusted raw SQL**, interpolated into the
// query as-is. Values passed via args... are bound as parameters and
// are safe; the fragment text is not validated and not escaped. Same
// trust contract as [vec.WithFilter] and [gorm.DB.Where]. Callers
// MUST:
//
//   - validate any identifier they interpolate into the fragment via
//     [ValidIdent] before passing the fragment in,
//   - route every literal through args... rather than building it into
//     the fragment string,
//   - never pass user-controlled SQL through here.
//
// The fragment must reference columns the FTS5 virtual table has —
// rowid is always available; declared columns appear under their bare
// names. External-content FTS5 tables CAN filter on their declared
// columns (the column list mirrors the source), but filtering on
// columns that exist ONLY on the source table requires JOINing — use
// [SearchSQL] together with [WithJoin] for that pattern.
//
// Example:
//
//	idx.SearchSlice(ctx, fts.Term("hello"),
//	    fts.WithFilter("tenant = ?", "acme"))
//
// The args slice is variadic; pass values inline.
func WithFilter(sql string, args ...any) SearchOption {
	return func(c *searchConfig) {
		c.whereSQL = sql
		c.whereArgs = args
	}
}

// WithSelect appends extra projected columns to the SELECT list of a
// Search query. The default projection is "rowid, <Options.Columns>"
// plus any optional rank/snippet/highlight aliases. Pair with
// [WithJoin] to source the extra columns from another table.
//
// # Trust model
//
// extraCols is **caller-trusted raw SQL** — interpolated as-is.
// Validate identifiers via [ValidIdent] before interpolating. Same
// contract as [WithFilter] / [WithJoin] / [WithOrderBy].
//
// IMPORTANT: WithSelect changes the row shape, so [Index.Search] and
// [Index.SearchSlice] cannot scan the result. Use [Index.SearchSQL]
// with [database/sql.DB.QueryContext] or gorm's `db.Raw(sql,
// args...).Scan(&out)` to consume the projected shape. Calling
// Search / SearchSlice with WithSelect set returns an error.
func WithSelect(extraCols string) SearchOption {
	return func(c *searchConfig) { c.selectExtra = extraCols }
}

// WithJoin inserts a JOIN clause after "FROM <fts table>". The
// fragment must include the JOIN keyword and the ON predicate.
//
//	fts.WithJoin("JOIN items ON items.id = items_fts.rowid")
//
// # Trust model
//
// joinClause is **caller-trusted raw SQL** — interpolated as-is. Same
// rules as [WithFilter] / [WithSelect] / [WithOrderBy].
//
// IMPORTANT: WithJoin (like WithSelect) is honored only by
// [Index.SearchSQL]; passing it to [Index.Search] or
// [Index.SearchSlice] returns an error.
func WithJoin(joinClause string) SearchOption {
	return func(c *searchConfig) { c.joinClause = joinClause }
}

// WithOrderBy replaces the default ORDER BY clause with the given
// expression. Without WithOrderBy the query orders by the bm25 rank
// (with [WithRanking]) or FTS5's internal rank otherwise.
//
// # Trust model
//
// expr is **caller-trusted raw SQL** — interpolated as-is. Validate
// identifiers via [ValidIdent]. Same contract as [WithFilter] /
// [WithSelect] / [WithJoin].
//
// IMPORTANT: WithOrderBy is honored by all three of [Index.Search],
// [Index.SearchSlice], and [Index.SearchSQL] — it does not change the
// row shape, only the order.
func WithOrderBy(expr string) SearchOption {
	return func(c *searchConfig) { c.orderByExpr = expr }
}

// SearchSQL returns the SQL statement and bound arguments that Search
// would execute, without actually running it. Pair with
// [database/sql.DB.QueryContext] or gorm's `db.Raw(sql, args...).Scan(&out)`
// when you want to extend the projection (via [WithSelect]) or join
// companion data (via [WithJoin]) and scan rows into a custom struct.
//
// The bound args appear in declaration order: any [WithRanking] /
// [WithSnippet] / [WithHighlight] arguments first, then the MATCH
// expression, then any [WithFilter] arguments.
//
// Example with WithJoin + WithSelect (mirrors the typical "join the
// FTS5 table to the canonical row table" pattern):
//
//	sql, args, err := idx.SearchSQL(fts.Term("hello"),
//	    fts.WithSelect("items.id, items.title"),
//	    fts.WithJoin("JOIN items ON items.id = docs_fts.rowid"),
//	    fts.WithFilter("items.tenant = ?", "acme"),
//	    fts.WithLimit(10),
//	)
//	if err != nil { return err }
//	rows, _ := db.QueryContext(ctx, sql, args...)
func (i *Index[K, V]) SearchSQL(q Query, opts ...SearchOption) (string, []any, error) {
	cfg := &searchConfig{}
	for _, o := range opts {
		o(cfg)
	}
	return i.buildSearchSQL(q, cfg)
}

// WithHighlight enables FTS5's highlight() function for the named column.
// before/after wrap each matched term in the returned text.
func WithHighlight(column, before, after string) SearchOption {
	return func(c *searchConfig) {
		c.highlightRequested = true
		c.highlightColumn = column
		c.highlightBefore = before
		c.highlightAfter = after
	}
}

// Search executes the given Query and returns an iter.Seq2 over matching
// rows in BM25 order. Stops early when the consumer breaks the range loop.
//
// Hit.Rank is populated only when WithRanking is passed. Hit.Snippet
// and Hit.Highlight are populated only when their respective options are
// requested.
//
// WithSelect and WithJoin are not honored here — they change the row
// shape and the typed Hit[K, V] scanner can't consume the result. Use
// [Index.SearchSQL] instead for custom projections; calling Search
// with WithSelect or WithJoin set surfaces an error on the first
// iteration.
func (i *Index[K, V]) Search(ctx context.Context, q Query, opts ...SearchOption) iter.Seq2[Hit[K, V], error] {
	cfg := &searchConfig{}
	for _, o := range opts {
		o(cfg)
	}
	return func(yield func(Hit[K, V], error) bool) {
		if cfg.selectExtra != "" || cfg.joinClause != "" {
			yield(Hit[K, V]{}, errors.New(
				"fts.Search: WithSelect / WithJoin change the row shape; use Index.SearchSQL "+
					"with db.QueryContext (or gorm db.Raw(...).Scan) to consume custom projections"))
			return
		}
		stmt, args, err := i.buildSearchSQL(q, cfg)
		if err != nil {
			yield(Hit[K, V]{}, err)
			return
		}
		rows, err := i.db.QueryContext(ctx, stmt, args...)
		if err != nil {
			yield(Hit[K, V]{}, err)
			return
		}
		defer rows.Close()

		colNames, err := rows.Columns()
		if err != nil {
			yield(Hit[K, V]{}, err)
			return
		}
		// Hoist scanTargets/holders above the loop so they're allocated
		// once per Search instead of once per row. Reuse is safe here
		// because FTS5 columns are TEXT / INTEGER / REAL — when scanned
		// into *any, those land as string / int64 / float64 values
		// stored fully inside the interface header, so a subsequent
		// Scan that overwrites a slot doesn't alias prior values that
		// makeMatch stashed into a Hit's Extras map. If FTS5 ever
		// supports BLOB columns and the driver returns a reused []byte
		// buffer, makeMatch will need to copy before stashing.
		scanTargets := make([]any, len(colNames))
		holders := make([]any, len(colNames))
		for i := range scanTargets {
			holders[i] = &scanTargets[i]
		}
		for rows.Next() {
			if err := rows.Scan(holders...); err != nil {
				yield(Hit[K, V]{}, err)
				return
			}
			m, err := i.makeMatch(colNames, scanTargets, cfg)
			if err != nil {
				yield(Hit[K, V]{}, err)
				return
			}
			if !yield(m, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(Hit[K, V]{}, err)
		}
	}
}

// SearchSlice is a convenience around Search that collects all matches into a
// slice. Use Search when you need streaming behavior.
//
// The output slice is pre-sized from the parsed [WithLimit], clamped at
// 1024 so a pathological caller can't drive a huge make() request before
// any hit lands. Mirrors [vec.Table.KNNSlice]'s capacity discipline so
// the two parallel sub-packages stay symmetric on the hot path.
func (i *Index[K, V]) SearchSlice(ctx context.Context, q Query, opts ...SearchOption) ([]Hit[K, V], error) {
	var cfg searchConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	capHint := min(max(cfg.limit, 0), 1024)
	out := make([]Hit[K, V], 0, capHint)
	for m, err := range i.Search(ctx, q, opts...) {
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// buildSearchSQL composes the SELECT statement and bound arguments. We keep
// the assembly here (not in Search) so observability decorators can wrap a
// pre-built statement view if they ever want to.
func (i *Index[K, V]) buildSearchSQL(q Query, cfg *searchConfig) (string, []any, error) {
	if q == nil {
		return "", nil, errors.New("fts.Search: nil query")
	}
	// When a JOIN is in play, the SELECT list must qualify rowid (and
	// the columns) so SQLite doesn't see ambiguity with the joined
	// table's own rowid alias.
	joining := cfg.joinClause != "" || cfg.selectExtra != ""
	tableQuoted := quote(i.name)
	var b strings.Builder
	if joining {
		b.WriteString("SELECT ")
		b.WriteString(tableQuoted)
		b.WriteString(".rowid")
	} else {
		b.WriteString("SELECT rowid")
	}
	for _, c := range i.columns {
		b.WriteString(", ")
		if joining {
			b.WriteString(tableQuoted)
			b.WriteString(".")
		}
		b.WriteString(c)
	}
	// Cap hint covers the typical max: bm25 weights, 4 snippet args,
	// 1 highlight col, 1 MATCH placeholder, and the user-supplied
	// whereArgs. Slight over-allocation is cheaper than the regrowth
	// dance of starting at capacity 0.
	args := make([]any, 0, len(cfg.bm25Weights)+4+1+1+len(cfg.whereArgs))

	if cfg.withRank {
		if len(cfg.bm25Weights) == 0 {
			b.WriteString(", bm25(" + quote(i.name) + ") AS __rank")
		} else {
			b.WriteString(", bm25(" + quote(i.name))
			for _, w := range cfg.bm25Weights {
				b.WriteString(", ?")
				args = append(args, w)
			}
			b.WriteString(") AS __rank")
		}
	}
	if cfg.snippetRequested {
		col := cfg.snippetColumn
		if col == "" {
			col = i.columns[0]
		}
		idx := columnIndex(i.columns, col)
		if idx < 0 {
			return "", nil, fmt.Errorf("fts.WithSnippet: unknown column %q", col)
		}
		b.WriteString(", snippet(")
		b.WriteString(quote(i.name))
		fmt.Fprintf(&b, ", %d, ?, ?, ?, ?", idx)
		b.WriteString(") AS __snippet")
		args = append(args, cfg.snippetBefore, cfg.snippetAfter, cfg.snippetEllipsis, cfg.snippetTokens)
	}
	if cfg.highlightRequested {
		col := cfg.highlightColumn
		if col == "" {
			col = i.columns[0]
		}
		idx := columnIndex(i.columns, col)
		if idx < 0 {
			return "", nil, fmt.Errorf("fts.WithHighlight: unknown column %q", col)
		}
		b.WriteString(", highlight(")
		b.WriteString(quote(i.name))
		fmt.Fprintf(&b, ", %d, ?, ?", idx)
		b.WriteString(") AS __highlight")
		args = append(args, cfg.highlightBefore, cfg.highlightAfter)
	}

	if cfg.selectExtra != "" {
		b.WriteString(", ")
		b.WriteString(cfg.selectExtra)
	}

	b.WriteString(" FROM ")
	b.WriteString(quote(i.name))
	if cfg.joinClause != "" {
		b.WriteString(" ")
		b.WriteString(cfg.joinClause)
	}
	b.WriteString(" WHERE ")
	b.WriteString(quote(i.name))
	b.WriteString(" MATCH ?")
	args = append(args, q.Build())

	if cfg.whereSQL != "" {
		b.WriteString(" AND (")
		b.WriteString(cfg.whereSQL)
		b.WriteString(")")
		args = append(args, cfg.whereArgs...)
	}

	switch {
	case cfg.orderByExpr != "":
		b.WriteString(" ORDER BY ")
		b.WriteString(cfg.orderByExpr)
	case cfg.withRank:
		b.WriteString(" ORDER BY __rank")
	default:
		// Without explicit ranking, fall back to FTS5's internal rank order
		// (which is rowid order for default indexes — still deterministic).
		b.WriteString(" ORDER BY rank")
	}
	if cfg.limit > 0 {
		fmt.Fprintf(&b, " LIMIT %d", cfg.limit)
	}
	if cfg.offset > 0 {
		fmt.Fprintf(&b, " OFFSET %d", cfg.offset)
	}
	return b.String(), args, nil
}

// makeMatch decodes a single scanned row into a typed Hit[K, V].
func (i *Index[K, V]) makeMatch(colNames []string, vals []any, cfg *searchConfig) (Hit[K, V], error) {
	var m Hit[K, V]
	for j, name := range colNames {
		raw := vals[j]
		switch name {
		case "rowid":
			k, err := assignSQLType[K](raw)
			if err != nil {
				return m, fmt.Errorf("rowid: %w", err)
			}
			m.Key = k
		case "__rank":
			if f, ok := raw.(float64); ok {
				m.Rank = f
			}
		case "__snippet":
			m.Snippet = toString(raw)
		case "__highlight":
			m.Highlight = toString(raw)
		default:
			// User column. The first declared column maps to Value; the rest
			// land in Extras.
			if name == i.columns[0] {
				v, err := assignSQLType[V](raw)
				if err != nil {
					return m, fmt.Errorf("column %s: %w", name, err)
				}
				m.Value = v
			} else {
				if m.Extras == nil {
					m.Extras = map[string]any{}
				}
				m.Extras[name] = raw
			}
		}
	}
	return m, nil
}

// columnIndex returns the 0-based position of name within cols, or -1 if not
// found.
func columnIndex(cols []string, name string) int {
	for i, c := range cols {
		if c == name {
			return i
		}
	}
	return -1
}

package ftsgorm

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/go-again/sqlite/fts"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// Hit pairs a typed model T with the FTS5 rank score and the optional
// snippet/highlight strings that the caller may request via the
// matching Option.
type Hit[T any] struct {
	Model     T
	Rank      float64
	Snippet   string
	Highlight string
}

type options struct {
	limit      int
	offset     int
	weights    []float64
	includeDel bool

	extraWhere string
	extraArgs  []any

	snippet *snippetCfg
	hilite  *hiliteCfg
}

type snippetCfg struct {
	column                  string
	before, after, ellipsis string
	tokens                  int
}
type hiliteCfg struct {
	column        string
	before, after string
}

// Option mutates the search behavior. Compose multiple via the
// Search(...opts...) variadic.
type Option func(*options)

// WithLimit caps the result set size.
func WithLimit(n int) Option { return func(o *options) { o.limit = n } }

// WithOffset skips the first n matches.
func WithOffset(n int) Option { return func(o *options) { o.offset = n } }

// WithRanking applies per-column BM25 weights. len(weights) must match
// the FTS5 column count or it's silently ignored by SQLite.
func WithRanking(weights ...float64) Option {
	return func(o *options) { o.weights = weights }
}

// WithSnippet asks SQLite to compute snippet text for a column.
func WithSnippet(column, before, after, ellipsis string, tokens int) Option {
	return func(o *options) {
		o.snippet = &snippetCfg{column: column, before: before, after: after, ellipsis: ellipsis, tokens: tokens}
	}
}

// WithHighlight asks SQLite to compute highlighted-match text for a column.
func WithHighlight(column, before, after string) Option {
	return func(o *options) {
		o.hilite = &hiliteCfg{column: column, before: before, after: after}
	}
}

// IncludeDeleted disables the default `deleted = 0` filter. Has no
// effect on models without gorm.DeletedAt.
func IncludeDeleted() Option { return func(o *options) { o.includeDel = true } }

// WithFilter adds an extra WHERE conjunct to the FTS5 search (joined
// with AND). Useful for "this user's documents only", "rows tagged
// before :ts", etc. The fragment is concatenated into the FTS5 SELECT
// and so must reference columns the FTS5 table has — rowid plus any
// UNINDEXED columns declared on the model.
//
// Mirrors vec/gorm.WithFilter; for gorm-side scopes / preloads, chain
// db.Where(...) on the returned slice instead.
func WithFilter(sqlFragment string, args ...any) Option {
	return func(o *options) {
		o.extraWhere = sqlFragment
		o.extraArgs = args
	}
}

// Search runs an FTS5 query against the model T's shared FTS5 table
// and returns matching gorm models in rank order. Reads:
//
//   - rowids from the FTS5 table via MATCH
//   - models from the source table via gorm's Find (so scopes /
//     preloads chained on db apply)
//   - snippet/highlight columns directly from the FTS5 query if requested
//
// k=0 returns all matches subject to FTS5's row limit; otherwise
// LIMIT k OFFSET 0 unless WithOffset is supplied.
func Search[T any](ctx context.Context, db *gorm.DB, q fts.Query, opts ...Option) ([]Hit[T], error) {
	p, err := pluginFrom(db)
	if err != nil {
		return nil, err
	}
	var zero T
	mm, err := p.registerSchema(db, &zero)
	if err != nil {
		return nil, err
	}
	if len(mm.Fields) == 0 {
		return nil, fmt.Errorf("ftsgorm: Search: %T has no fields tagged with fts5", zero)
	}

	var o options
	for _, opt := range opts {
		opt(&o)
	}

	if mm.Mode == ModeContentless && (o.snippet != nil || o.hilite != nil) {
		return nil, fmt.Errorf(
			"ftsgorm: %s uses contentless mode; snippet() and highlight() are unavailable — "+
				"remove WithSnippet/WithHighlight or switch the model to external (default) or in-table mode",
			mm.Table)
	}

	pool, err := activePool(db)
	if err != nil {
		return nil, err
	}
	matchExpr := q.Build()

	// Build the FTS5 SELECT. We always select rowid + rank;
	// snippet/highlight follow as optional columns.
	selects := []string{"rowid"}
	if len(o.weights) > 0 {
		var w []string
		for _, x := range o.weights {
			w = append(w, fmt.Sprintf("%v", x))
		}
		selects = append(selects, fmt.Sprintf("bm25(%s, %s) AS rank_", quoteIdent(mm.Table), strings.Join(w, ", ")))
	} else {
		selects = append(selects, "rank AS rank_")
	}
	if o.snippet != nil {
		s := o.snippet
		colIdx := columnIndex(mm, s.column)
		selects = append(selects, fmt.Sprintf(
			"snippet(%s, %d, %s, %s, %s, %d) AS snippet_",
			quoteIdent(mm.Table), colIdx,
			sqlString(s.before), sqlString(s.after), sqlString(s.ellipsis), s.tokens))
	} else {
		selects = append(selects, "'' AS snippet_")
	}
	if o.hilite != nil {
		h := o.hilite
		colIdx := columnIndex(mm, h.column)
		selects = append(selects, fmt.Sprintf(
			"highlight(%s, %d, %s, %s) AS highlight_",
			quoteIdent(mm.Table), colIdx,
			sqlString(h.before), sqlString(h.after)))
	} else {
		selects = append(selects, "'' AS highlight_")
	}

	wheres := []string{quoteIdent(mm.Table) + " MATCH ?"}
	args := []any{matchExpr}
	if mm.SoftDelete && !o.includeDel {
		// External mode mirrors source's deleted_at; in-table /
		// contentless modes use an owned `deleted` flag.
		if mm.Mode == ModeExternal {
			wheres = append(wheres, "deleted_at IS NULL")
		} else {
			wheres = append(wheres, "deleted = 0")
		}
	}
	if o.extraWhere != "" {
		wheres = append(wheres, "("+o.extraWhere+")")
		args = append(args, o.extraArgs...)
	}

	query := fmt.Sprintf(
		"SELECT %s FROM %s WHERE %s ORDER BY rank_",
		strings.Join(selects, ", "),
		quoteIdent(mm.Table),
		strings.Join(wheres, " AND "),
	)
	if o.limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", o.limit)
	}
	if o.offset > 0 {
		query += fmt.Sprintf(" OFFSET %d", o.offset)
	}

	rows, err := pool.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("ftsgorm: search %s: %w", mm.Table, err)
	}
	defer rows.Close()

	type match struct {
		rowid     int64
		rank      float64
		snippet   string
		highlight string
	}
	var matches []match
	for rows.Next() {
		var m match
		if err := rows.Scan(&m.rowid, &m.rank, &m.snippet, &m.highlight); err != nil {
			return nil, err
		}
		matches = append(matches, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, nil
	}

	// Fetch source rows via gorm so scopes/preloads chained on db apply.
	rowids := make([]any, len(matches))
	for i, m := range matches {
		rowids[i] = m.rowid
	}
	models := reflect.New(reflect.SliceOf(reflect.TypeOf(zero))).Interface()
	if err := db.WithContext(ctx).
		Where(fmt.Sprintf("%s IN ?", quoteIdent(mm.PKField.DBName)), rowids).
		Find(models).Error; err != nil {
		return nil, fmt.Errorf("ftsgorm: fetch models: %w", err)
	}
	indexed := map[int64]T{}
	sliceVal := reflect.ValueOf(models).Elem()
	for i := 0; i < sliceVal.Len(); i++ {
		row := sliceVal.Index(i)
		pk, ok := pkAsInt64(mm.PKField, row)
		if !ok {
			continue
		}
		indexed[pk] = row.Interface().(T)
	}

	results := make([]Hit[T], 0, len(matches))
	for _, m := range matches {
		model, ok := indexed[m.rowid]
		if !ok {
			continue
		}
		results = append(results, Hit[T]{
			Model:     model,
			Rank:      m.rank,
			Snippet:   m.snippet,
			Highlight: m.highlight,
		})
	}
	return results, nil
}

// columnIndex returns the zero-based ordinal of an FTS5 column on the
// model. Snippets and highlights identify the source column by
// position, not name. Returns -1 if the column is missing — caller
// should treat that as user error; the resulting SQL will surface the
// SQLite-side complaint.
func columnIndex(mm *modelMeta, name string) int {
	for i, f := range mm.Fields {
		if f.Column == name || f.FieldName == name {
			return i
		}
	}
	return -1
}

// pkAsInt64 reads the PK as int64 off a row. Mirrors the vec/gorm
// helper of the same name.
func pkAsInt64(f *schema.Field, row reflect.Value) (int64, bool) {
	v := row.FieldByIndex(f.StructField.Index)
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return 0, false
		}
		v = v.Elem()
	}
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return int64(v.Uint()), true
	}
	return 0, false
}

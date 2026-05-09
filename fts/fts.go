// Copyright 2026 The go-again/sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be found
// in the LICENSE file.

package fts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"iter"
	"strings"
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

// Match is a single search hit.
type Match[K, V SQLType] struct {
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
}

// New creates an FTS5 virtual table named `name` configured by opts and
// returns a typed Index handle. The CREATE VIRTUAL TABLE statement is
// executed against db immediately.
//
// To re-attach to an existing table, use Open instead.
func New[K, V SQLType](ctx context.Context, db *sql.DB, name string, opts Options) (*Index[K, V], error) {
	if !validIdent(name) {
		return nil, fmt.Errorf("fts.New: %q is not a valid SQL identifier", name)
	}
	cols := opts.columnList()
	for _, c := range cols {
		if !validIdent(c) {
			return nil, fmt.Errorf("fts.New: column %q is not a valid SQL identifier", c)
		}
	}

	var parts []string
	if opts.External != nil {
		// External-content tables declare the user columns as UNINDEXED
		// references — the content lives in the source table.
		parts = append(parts, cols...)
	} else {
		parts = append(parts, cols...)
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

	stmt := fmt.Sprintf("CREATE VIRTUAL TABLE %s USING fts5(%s)", quote(name), strings.Join(parts, ", "))
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		return nil, fmt.Errorf("fts.New %q: %w", name, err)
	}
	return &Index[K, V]{db: db, name: name, columns: cols, ext: opts.External}, nil
}

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
	return &Index[K, V]{db: db, name: name, columns: cols}, nil
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
	cols := append([]string{"rowid"}, i.columns...)
	placeholders := strings.Repeat("?, ", len(cols))
	placeholders = placeholders[:len(placeholders)-2]
	stmt := fmt.Sprintf(
		"INSERT OR REPLACE INTO %s (%s) VALUES (%s)",
		quote(i.name), strings.Join(cols, ", "), placeholders,
	)

	tx, err := i.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	prep, err := tx.PrepareContext(ctx, stmt)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer prep.Close()
	for n, a := range items {
		args := make([]any, 0, len(cols))
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
			tx.Rollback()
			return fmt.Errorf("fts.Insert[%d]: %w", n, err)
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
	stmt := fmt.Sprintf("DELETE FROM %s WHERE rowid = ?", quote(i.name))
	tx, err := i.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	prep, err := tx.PrepareContext(ctx, stmt)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer prep.Close()
	for _, k := range keys {
		if _, err := prep.ExecContext(ctx, k); err != nil {
			tx.Rollback()
			return err
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
// declaration order. Without WithRanking, Match.Rank is zero.
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
// Match.Rank is populated only when WithRanking is passed. Match.Snippet
// and Match.Highlight are populated only when their respective options are
// requested.
func (i *Index[K, V]) Search(ctx context.Context, q Query, opts ...SearchOption) iter.Seq2[Match[K, V], error] {
	cfg := &searchConfig{}
	for _, o := range opts {
		o(cfg)
	}
	return func(yield func(Match[K, V], error) bool) {
		stmt, args, err := i.buildSearchSQL(q, cfg)
		if err != nil {
			yield(Match[K, V]{}, err)
			return
		}
		rows, err := i.db.QueryContext(ctx, stmt, args...)
		if err != nil {
			yield(Match[K, V]{}, err)
			return
		}
		defer rows.Close()

		colNames, err := rows.Columns()
		if err != nil {
			yield(Match[K, V]{}, err)
			return
		}
		for rows.Next() {
			scanTargets := make([]any, len(colNames))
			holders := make([]any, len(colNames))
			for i := range scanTargets {
				holders[i] = &scanTargets[i]
			}
			if err := rows.Scan(holders...); err != nil {
				yield(Match[K, V]{}, err)
				return
			}
			m, err := i.makeMatch(colNames, scanTargets, cfg)
			if err != nil {
				yield(Match[K, V]{}, err)
				return
			}
			if !yield(m, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(Match[K, V]{}, err)
		}
	}
}

// SearchSlice is a convenience around Search that collects all matches into a
// slice. Use Search when you need streaming behavior.
func (i *Index[K, V]) SearchSlice(ctx context.Context, q Query, opts ...SearchOption) ([]Match[K, V], error) {
	var out []Match[K, V]
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
	var b strings.Builder
	b.WriteString("SELECT rowid")
	for _, c := range i.columns {
		b.WriteString(", ")
		b.WriteString(c)
	}
	args := []any{}

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
		b.WriteString(fmt.Sprintf(", %d, ?, ?, ?, ?", idx))
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
		b.WriteString(fmt.Sprintf(", %d, ?, ?", idx))
		b.WriteString(") AS __highlight")
		args = append(args, cfg.highlightBefore, cfg.highlightAfter)
	}

	b.WriteString(" FROM ")
	b.WriteString(quote(i.name))
	b.WriteString(" WHERE ")
	b.WriteString(quote(i.name))
	b.WriteString(" MATCH ?")
	args = append(args, q.build())

	if cfg.withRank {
		b.WriteString(" ORDER BY __rank")
	} else {
		// Without explicit ranking, fall back to FTS5's internal rank order
		// (which is rowid order for default indexes — still deterministic).
		b.WriteString(" ORDER BY rank")
	}
	if cfg.limit > 0 {
		b.WriteString(fmt.Sprintf(" LIMIT %d", cfg.limit))
	}
	if cfg.offset > 0 {
		b.WriteString(fmt.Sprintf(" OFFSET %d", cfg.offset))
	}
	return b.String(), args, nil
}

// makeMatch decodes a single scanned row into a typed Match[K, V].
func (i *Index[K, V]) makeMatch(colNames []string, vals []any, cfg *searchConfig) (Match[K, V], error) {
	var m Match[K, V]
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

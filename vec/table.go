// Copyright 2026 The go-again/sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be found
// in the LICENSE file.

package vec

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"iter"
	"strings"
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

// quote returns name in backticks, escaping any embedded backticks. We rely
// on this for identifier interpolation outside the vec0 constructor since
// SQLite treats table/column names as identifiers, not bind parameters.
//
// Note: vec0's CREATE VIRTUAL TABLE column-argument parser does NOT accept
// quoted identifiers — only bare names — so we validate identifiers used in
// the constructor with validIdent below and never call quote on them.
func quote(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

// validIdent guards against SQL injection for callers passing arbitrary
// strings as table or column names. A valid identifier here is the
// conservative ASCII subset: leading letter or underscore, then letters,
// digits, or underscores. Any other input must be rejected at the API
// boundary rather than blindly interpolated.
func validIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_':
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

// Create runs `CREATE VIRTUAL TABLE name USING vec0(embedding float[dim])`
// with the supplied options and returns a Table handle. Use IF NOT EXISTS via
// the standard SQLite semantics — pass a name that may or may not exist and
// inspect the returned error if you need to detect re-create attempts.
//
// dim is required and must be positive. opts may be the zero value, in which
// case Metric defaults to L2 and Encoding defaults to JSON.
func Create(ctx context.Context, db *sql.DB, name string, dim int, opts Options) (*Table, error) {
	if dim <= 0 {
		return nil, fmt.Errorf("vec.Create: dim must be > 0, got %d", dim)
	}
	if !validIdent(name) {
		return nil, fmt.Errorf("vec.Create: %q is not a valid SQL identifier", name)
	}
	col := "embedding"
	// vec0's column-argument parser is strict: bare identifiers only, with
	// options space-separated. We assemble the column declaration without
	// backticks; the table name itself goes through quote() because the
	// surrounding CREATE VIRTUAL TABLE keyword accepts quoted identifiers.
	stmt := fmt.Sprintf(
		"CREATE VIRTUAL TABLE %s USING vec0(%s float[%d] distance=%s)",
		quote(name), col, dim, metricKeyword(opts.Metric),
	)
	if _, err := db.ExecContext(ctx, stmt); err != nil {
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

// Item is a single (rowid, embedding) pair consumed by BatchInsert.
type Item struct {
	Rowid     int64
	Embedding []float32
}

// BatchInsert inserts every item in a single transaction. Each rowid must be
// unique within the table; conflicts surface as errors (sqlite-vec's vec0
// INSERT does not honor OR REPLACE).
func (t *Table) BatchInsert(ctx context.Context, items []Item) error {
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

// metricKeyword maps a Metric to the keyword sqlite-vec accepts in vec0's
// distance= option. sqlite-vec supports exactly three: l1, l2, cosine.
// Our Dot constant is kept as a name alias for L1 for plan/historical
// reasons — see types.go.
func metricKeyword(m Metric) string {
	switch m {
	case Cosine:
		return "cosine"
	case Dot:
		return "l1"
	}
	// L2 / unknown
	return "l2"
}

// Match is one row returned by KNN.
type Match struct {
	Rowid    int64
	Distance float64
}

// KNN issues an approximate k-nearest-neighbour query for the given vector
// and returns an iter.Seq2 cursor over the results in ascending-distance
// order. Yielding stops at k rows or on error.
//
// Optional QueryOptions filter the result set. WithWhere appends a custom
// WHERE conjunct (e.g. "rowid IN (1, 2, 3)" to restrict to known IDs); see
// vec.WithWhere for details.
//
// The yielded error is always nil except for the final iteration after a
// row-scan failure, where it carries the scan error and the Match is the
// zero value. Iterating with `for m, err := range tbl.KNN(...)` follows the
// idiomatic Go-1.23 range-over-func convention.
func (t *Table) KNN(ctx context.Context, query []float32, k int, opts ...QueryOption) iter.Seq2[Match, error] {
	cfg := &queryConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	return func(yield func(Match, error) bool) {
		if len(query) != t.dim {
			yield(Match{}, fmt.Errorf("vec.KNN: query length %d != dim %d", len(query), t.dim))
			return
		}
		if k <= 0 {
			return
		}
		var b strings.Builder
		b.WriteString("SELECT rowid, distance FROM ")
		b.WriteString(quote(t.name))
		b.WriteString(" WHERE ")
		b.WriteString(quote(t.embedding))
		b.WriteString(" MATCH ")
		b.WriteString(matchPlaceholder(t.encoding))
		// User-provided filter, AND'd onto MATCH.
		if cfg.whereSQL != "" {
			b.WriteString(" AND (")
			b.WriteString(cfg.whereSQL)
			b.WriteString(")")
		}
		// LIMIT is inlined as a literal integer (no injection risk; k is a
		// Go int controlled by the caller) because sqlite-vec's vec0 module
		// requires a literal LIMIT or a `k = ?` constraint to identify the
		// nearest-neighbour cap, and a parameterized LIMIT can confuse its
		// planner when other WHERE conjuncts are present.
		b.WriteString(" ORDER BY distance LIMIT ")
		fmt.Fprintf(&b, "%d", k)

		args := make([]any, 0, 1+len(cfg.whereArgs))
		args = append(args, encodeValue(query, t.encoding))
		args = append(args, cfg.whereArgs...)

		rows, err := t.db.QueryContext(ctx, b.String(), args...)
		if err != nil {
			yield(Match{}, err)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var m Match
			if err := rows.Scan(&m.Rowid, &m.Distance); err != nil {
				yield(Match{}, err)
				return
			}
			if !yield(m, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(Match{}, err)
		}
	}
}

// KNNSlice is a convenience wrapper that collects the first k matches into a
// slice. Use it when you don't need streaming behavior. Accepts the same
// QueryOptions as KNN.
func (t *Table) KNNSlice(ctx context.Context, query []float32, k int, opts ...QueryOption) ([]Match, error) {
	out := make([]Match, 0, k)
	for m, err := range t.KNN(ctx, query, k, opts...) {
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

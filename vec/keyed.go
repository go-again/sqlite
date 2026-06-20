package vec

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"iter"
	"reflect"
	"strings"
)

// Key is the primary-key type a [KeyedTable] supports: a 64-bit integer or a
// string (e.g. a UUID or slug). Named types with those underlying kinds are
// accepted too.
type Key interface {
	~int64 | ~string
}

// KeyedTable is a sqlite-vec table whose rows are addressed by an explicit
// primary-key column of type K, instead of the implicit int64 rowid that
// [Table] uses. Use it when your keys are strings (UUIDs, slugs) — the form an
// ORM needs for models with non-int64 primary keys.
//
// It mirrors [Table]'s lifecycle and KNN surface (and the same [QueryOption]s)
// but its Insert / KNN take and return K-typed keys. Construct it with
// [CreateKeyed] or [OpenKeyed]. Safe for concurrent use as long as the *sql.DB
// is. Metadata / partition / auxiliary columns are available on the rowid
// [Table]; KeyedTable carries only the key and embedding.
type KeyedTable[K Key] struct {
	db        *sql.DB
	name      string
	keyCol    string
	embedding string
	dim       int
	metric    Metric
	encoding  Encoding

	insertSQL string
	updateSQL string
	deleteSQL string
}

// keyColumnType returns the vec0 column type for K: "text" for strings,
// "integer" for int64s.
func keyColumnType[K Key]() string {
	if reflect.TypeOf(*new(K)).Kind() == reflect.String {
		return "text"
	}
	return "integer"
}

func (t *KeyedTable[K]) buildSQL() {
	t.insertSQL = fmt.Sprintf("INSERT INTO %s (%s, %s) VALUES (?, %s)",
		quote(t.name), quote(t.keyCol), quote(t.embedding), t.encoding.Placeholder())
	t.updateSQL = fmt.Sprintf("UPDATE %s SET %s = %s WHERE %s = ?",
		quote(t.name), quote(t.embedding), t.encoding.Placeholder(), quote(t.keyCol))
	t.deleteSQL = fmt.Sprintf("DELETE FROM %s WHERE %s = ?", quote(t.name), quote(t.keyCol))
}

// CreateKeyed runs CREATE VIRTUAL TABLE name USING vec0(id <type> primary key,
// embedding <enc>[dim] distance=<metric>) and returns a handle. The key column
// is named "id"; override it with [WithKeyColumn]. By default the call wraps
// [ErrAlreadyExists] if name already exists; pass [WithIfNotExists].
func CreateKeyed[K Key](ctx context.Context, db *sql.DB, name string, dim int, opts Options, createOpts ...CreateOption) (*KeyedTable[K], error) {
	if dim <= 0 {
		return nil, fmt.Errorf("vec.CreateKeyed: dim must be > 0, got %d", dim)
	}
	if !validIdent(name) {
		return nil, fmt.Errorf("vec.CreateKeyed: %q is not a valid SQL identifier", name)
	}
	if opts.Encoding == Bit && dim%8 != 0 {
		return nil, fmt.Errorf("vec.CreateKeyed: bit encoding requires dim divisible by 8, got %d", dim)
	}
	cfg := &createConfig{}
	for _, opt := range createOpts {
		opt(cfg)
	}
	keyCol := cfg.keyColumn
	if keyCol == "" {
		keyCol = "id"
	}
	if !validIdent(keyCol) {
		return nil, fmt.Errorf("vec.CreateKeyed: key column %q is not a valid SQL identifier", keyCol)
	}
	metric := opts.Metric
	if opts.Encoding == Bit {
		metric = Hamming
	}

	embDecl := fmt.Sprintf("embedding %s[%d]", opts.Encoding.columnType(), dim)
	if opts.Encoding != Bit {
		embDecl += " distance=" + metric.Keyword()
	}
	ifNotExists := ""
	if cfg.ifNotExists {
		ifNotExists = "IF NOT EXISTS "
	}
	stmt := fmt.Sprintf("CREATE VIRTUAL TABLE %s%s USING vec0(%s %s primary key, %s)",
		ifNotExists, quote(name), keyCol, keyColumnType[K](), embDecl)
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		if isAlreadyExistsErr(err) {
			return nil, fmt.Errorf("vec.CreateKeyed %q: %w", name, ErrAlreadyExists)
		}
		return nil, fmt.Errorf("vec.CreateKeyed %q: %w", name, err)
	}
	t := &KeyedTable[K]{db: db, name: name, keyCol: keyCol, embedding: "embedding", dim: dim, metric: metric, encoding: opts.Encoding}
	t.buildSQL()
	return t, nil
}

// OpenKeyed returns a handle to a keyed vec0 table that already exists. It does
// not validate the schema; the dim / encoding / metric / key column must match
// what the table was created with (override the key column via [WithKeyColumn]).
func OpenKeyed[K Key](db *sql.DB, name string, dim int, opts Options, createOpts ...CreateOption) (*KeyedTable[K], error) {
	if dim <= 0 {
		return nil, fmt.Errorf("vec.OpenKeyed: dim must be > 0, got %d", dim)
	}
	if !validIdent(name) {
		return nil, fmt.Errorf("vec.OpenKeyed: %q is not a valid SQL identifier", name)
	}
	cfg := &createConfig{}
	for _, opt := range createOpts {
		opt(cfg)
	}
	keyCol := cfg.keyColumn
	if keyCol == "" {
		keyCol = "id"
	}
	if !validIdent(keyCol) {
		return nil, fmt.Errorf("vec.OpenKeyed: key column %q is not a valid SQL identifier", keyCol)
	}
	metric := opts.Metric
	if opts.Encoding == Bit {
		metric = Hamming
	}
	t := &KeyedTable[K]{db: db, name: name, keyCol: keyCol, embedding: "embedding", dim: dim, metric: metric, encoding: opts.Encoding}
	t.buildSQL()
	return t, nil
}

// Name / Dim / Metric / Encoding / KeyColumn report the table's configuration.
func (t *KeyedTable[K]) Name() string       { return t.name }
func (t *KeyedTable[K]) Dim() int           { return t.dim }
func (t *KeyedTable[K]) Metric() Metric     { return t.metric }
func (t *KeyedTable[K]) Encoding() Encoding { return t.encoding }
func (t *KeyedTable[K]) KeyColumn() string  { return t.keyCol }

// Close is a no-op kept for API symmetry with [Table.Close].
func (t *KeyedTable[K]) Close() error { return nil }

// Insert adds a single row keyed by key. A duplicate key is an error (vec0's
// INSERT does not honor OR REPLACE) — use Update to overwrite.
func (t *KeyedTable[K]) Insert(ctx context.Context, key K, embedding []float32) error {
	if len(embedding) != t.dim {
		return fmt.Errorf("vec.Insert: embedding length %d != dim %d", len(embedding), t.dim)
	}
	_, err := t.db.ExecContext(ctx, t.insertSQL, key, t.encoding.Encode(embedding))
	return err
}

// KeyedRow is a single (key, embedding) pair for [KeyedTable.BatchInsert].
type KeyedRow[K Key] struct {
	Key       K
	Embedding []float32
}

// BatchInsert inserts every item in a single transaction.
func (t *KeyedTable[K]) BatchInsert(ctx context.Context, items []KeyedRow[K]) error {
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
				tx.Rollback())
		}
		if _, err := stmt.ExecContext(ctx, it.Key, t.encoding.Encode(it.Embedding)); err != nil {
			return errors.Join(fmt.Errorf("vec.BatchInsert[%d]: %w", i, err), tx.Rollback())
		}
	}
	return tx.Commit()
}

// Update replaces the embedding for an existing key. A missing key is a no-op.
func (t *KeyedTable[K]) Update(ctx context.Context, key K, embedding []float32) error {
	if len(embedding) != t.dim {
		return fmt.Errorf("vec.Update: embedding length %d != dim %d", len(embedding), t.dim)
	}
	_, err := t.db.ExecContext(ctx, t.updateSQL, t.encoding.Encode(embedding), key)
	return err
}

// Delete removes the row with the given key. A missing key is a no-op.
func (t *KeyedTable[K]) Delete(ctx context.Context, key K) error {
	_, err := t.db.ExecContext(ctx, t.deleteSQL, key)
	return err
}

// Drop removes the vtab and its shadow storage.
func (t *KeyedTable[K]) Drop(ctx context.Context) error {
	_, err := t.db.ExecContext(ctx, "DROP TABLE IF EXISTS "+quote(t.name))
	return err
}

// KeyedNeighbor is one KNN result: a key and its distance to the query.
type KeyedNeighbor[K Key] struct {
	Key      K
	Distance float64
}

// KNN issues a k-nearest-neighbour query, yielding results in ascending-distance
// order. Accepts the same [QueryOption]s as [Table.KNN] (WithFilter, …);
// WithSelect / WithJoin require [KeyedTable.KNNSQL].
func (t *KeyedTable[K]) KNN(ctx context.Context, query []float32, k int, opts ...QueryOption) iter.Seq2[KeyedNeighbor[K], error] {
	cfg := &queryConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	return func(yield func(KeyedNeighbor[K], error) bool) {
		if cfg.selectExtra != "" || cfg.joinClause != "" {
			yield(KeyedNeighbor[K]{}, errors.New(
				"vec.KNN: WithSelect / WithJoin change the row shape; use KeyedTable.KNNSQL with db.QueryContext"))
			return
		}
		q, args, err := t.buildKNNSQL(query, k, cfg)
		if err != nil {
			yield(KeyedNeighbor[K]{}, err)
			return
		}
		if q == "" {
			return
		}
		rows, err := t.db.QueryContext(ctx, q, args...)
		if err != nil {
			yield(KeyedNeighbor[K]{}, err)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var n KeyedNeighbor[K]
			if err := rows.Scan(&n.Key, &n.Distance); err != nil {
				yield(KeyedNeighbor[K]{}, err)
				return
			}
			if !yield(n, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(KeyedNeighbor[K]{}, err)
		}
	}
}

// KNNSlice collects the first k matches into a slice.
func (t *KeyedTable[K]) KNNSlice(ctx context.Context, query []float32, k int, opts ...QueryOption) ([]KeyedNeighbor[K], error) {
	out := make([]KeyedNeighbor[K], 0, min(max(k, 0), 1024))
	for n, err := range t.KNN(ctx, query, k, opts...) {
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

// KNNSQL returns the SQL + bind args KNN would execute, for the WithSelect /
// WithJoin escape hatch (run it through db.QueryContext and scan your own shape).
func (t *KeyedTable[K]) KNNSQL(query []float32, k int, opts ...QueryOption) (string, []any, error) {
	cfg := &queryConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	return t.buildKNNSQL(query, k, cfg)
}

// buildKNNSQL mirrors Table.buildKNNSQL but selects the explicit key column. See
// that method for the k= / LIMIT rationale.
func (t *KeyedTable[K]) buildKNNSQL(query []float32, k int, cfg *queryConfig) (string, []any, error) {
	if len(query) != t.dim {
		return "", nil, fmt.Errorf("vec.KNN: query length %d != dim %d", len(query), t.dim)
	}
	if k <= 0 {
		return "", nil, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "SELECT %s, distance", quote(t.keyCol))
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
	// k = N is needed whenever there's a JOIN (LIMIT lands on the post-join
	// result) OR an additional WHERE predicate: vec0 cannot extract a LIMIT as
	// the KNN k through a non-MATCH constraint such as a key-column equality
	// ("A LIMIT or 'k = ?' constraint is required"). A bare scan uses LIMIT.
	hasJoin := cfg.joinClause != ""
	useK := hasJoin || cfg.whereSQL != ""
	if useK {
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
	// Emit LIMIT for a bare scan, or alongside k= only for a join (where it
	// bounds the outer result, not the vtab scan). Never both on a direct scan.
	if !useK || hasJoin {
		fmt.Fprintf(&b, " LIMIT %d", k)
	}

	args := make([]any, 0, 1+len(cfg.whereArgs))
	args = append(args, t.encoding.Encode(query))
	if emitWhereArgs {
		args = append(args, cfg.whereArgs...)
	}
	return b.String(), args, nil
}

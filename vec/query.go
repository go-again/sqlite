package vec

// QueryOption configures a single KNN call. Compose with KNN and KNNSlice.
type QueryOption func(*queryConfig)

type queryConfig struct {
	whereSQL  string
	whereArgs []any
}

// WithFilter appends a custom WHERE conjunct to the KNN query. The SQL
// fragment is AND'd with the MATCH clause; bind parameters in args bind
// in declaration order. This is the supported way to do filtered KNN
// with sqlite-vec — restricting to "this user's documents", "items
// priced under $50", etc.
//
// The fragment is not validated — callers are responsible for ensuring
// it parses against the underlying vec0 table's columns (rowid is
// always available; user-declared metadata columns appear under their
// names).
//
// Example:
//
//	tbl.KNN(ctx, query, 5, vec.WithFilter("rowid > ?", lastSeenID))
//
// The args slice is variadic; pass values inline.
func WithFilter(sql string, args ...any) QueryOption {
	return func(c *queryConfig) {
		c.whereSQL = sql
		c.whereArgs = args
	}
}

// WithWhere is the previous name of [WithFilter] and remains as a
// thin wrapper for backward compatibility. New code should call
// [WithFilter] directly; the names were unified with vec/gorm's
// matching option in v0.2.x.
//
// Deprecated: use [WithFilter].
func WithWhere(sql string, args ...any) QueryOption {
	return WithFilter(sql, args...)
}

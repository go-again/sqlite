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
// # Trust model
//
// The fragment is **caller-trusted raw SQL**, interpolated into the
// query as-is. Values passed via args... are bound as parameters and
// are safe; the fragment text is not validated and not escaped. Same
// trust contract as gorm.Where(fragment, args...). Callers MUST:
//
//   - validate any identifier they interpolate into the fragment via
//     [ValidIdent] before passing the fragment in,
//   - route every literal through args... rather than building it into
//     the fragment string,
//   - never pass user-controlled SQL through here.
//
// The fragment must reference columns the vec0 table actually has —
// rowid is always available; user-declared metadata columns appear
// under their bare names.
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

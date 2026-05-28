package vec

// QueryOption configures a single KNN call. Compose with KNN, KNNSlice,
// and KNNSQL.
type QueryOption func(*queryConfig)

type queryConfig struct {
	whereSQL    string
	whereArgs   []any
	selectExtra string
	joinClause  string
	orderByExpr string
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

// WithSelect appends extra projected columns to the SELECT list of a
// KNN query. The default projection is "rowid, distance"; passing
// "items.title, items.tenant" yields "SELECT rowid, distance,
// items.title, items.tenant ...". Pair with [WithJoin] to source the
// extra columns from another table.
//
// # Trust model
//
// extraCols is **caller-trusted raw SQL** — interpolated as-is.
// Validate identifiers via [ValidIdent] before interpolating; never
// pass user-controlled SQL through here. Same contract as
// [WithFilter] / [WithJoin] / [WithOrderBy].
//
// IMPORTANT: WithSelect changes the row shape returned by the query,
// which means [Table.KNN] and [Table.KNNSlice] cannot scan the
// result — they expect exactly (rowid int64, distance float64). Use
// [Table.KNNSQL] together with [database/sql.DB.QueryContext] or
// gorm's `db.Raw(sql, args...).Scan(&out)` to scan the projected
// shape. Calling KNN / KNNSlice with WithSelect set returns an error.
func WithSelect(extraCols string) QueryOption {
	return func(c *queryConfig) { c.selectExtra = extraCols }
}

// WithJoin inserts a JOIN clause after "FROM <table>". The fragment
// must include the JOIN keyword and the ON predicate. Example:
//
//	vec.WithJoin("JOIN items ON items.id = items_vec.rowid")
//
// # Trust model
//
// joinClause is **caller-trusted raw SQL** — interpolated as-is. Same
// rules as [WithFilter] / [WithSelect] / [WithOrderBy].
//
// IMPORTANT: WithJoin (like WithSelect) is only honored by
// [Table.KNNSQL]; passing it to [Table.KNN] or [Table.KNNSlice]
// returns an error.
func WithJoin(joinClause string) QueryOption {
	return func(c *queryConfig) { c.joinClause = joinClause }
}

// WithOrderBy replaces the default "ORDER BY distance" with the given
// expression. Use it when you want to order results by something other
// than vec0 distance — e.g. a sidecar table's created_at after JOINing.
//
// # Trust model
//
// expr is **caller-trusted raw SQL** — interpolated as-is. Validate
// identifiers via [ValidIdent] before interpolating. Same contract as
// [WithFilter] / [WithSelect] / [WithJoin].
//
// IMPORTANT: WithOrderBy is honored by all three of [Table.KNN],
// [Table.KNNSlice], and [Table.KNNSQL] — it does not change the row
// shape, only the order.
func WithOrderBy(expr string) QueryOption {
	return func(c *queryConfig) { c.orderByExpr = expr }
}

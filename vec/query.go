// Copyright 2026 The go-again/sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be found
// in the LICENSE file.

package vec

// QueryOption configures a single KNN call. Compose with KNN and KNNSlice.
type QueryOption func(*queryConfig)

type queryConfig struct {
	whereSQL  string
	whereArgs []any
}

// WithWhere appends a custom WHERE conjunct to the KNN query. The provided
// SQL fragment is AND'd with the MATCH clause; bind parameters in args go
// in declaration order. This is the supported way to do filtered KNN with
// sqlite-vec — restricting to "this user's documents", "items priced under
// $50", etc.
//
// The SQL fragment is not validated — callers are responsible for ensuring
// it parses against the underlying vec0 table's columns (rowid is always
// available; user-declared metadata columns appear under their names).
//
// Example:
//
//	tbl.KNN(ctx, query, 5, vec.WithWhere("rowid > ?", lastSeenID))
//
// The args slice is variadic; pass values inline.
func WithWhere(sql string, args ...any) QueryOption {
	return func(c *queryConfig) {
		c.whereSQL = sql
		c.whereArgs = args
	}
}

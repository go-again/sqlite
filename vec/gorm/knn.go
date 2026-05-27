package vecgorm

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/go-again/sqlite/vec"
	"gorm.io/gorm"
)

// Hit pairs a typed gorm model with the vec distance returned by KNN.
// Distance is in the metric declared on the field tag (L2 squared,
// cosine, or L1).
type Hit[T any] struct {
	Model    T
	Distance float64
}

// Result is the previous name of [Hit] and remains as a generic type
// alias so existing call sites (`vecgorm.Result[Model]`) keep
// compiling. New code should use [Hit].
//
// Deprecated: use [Hit].
type Result[T any] = Hit[T]

// options controls KNN behavior. See WithFilter and IncludeDeleted.
type options struct {
	extraWhere string
	extraArgs  []any
	includeDel bool
}

// Option mutates options. Apply via the KNN(...opts) variadic.
type Option func(*options)

// WithFilter adds an extra WHERE conjunct to the KNN query on the
// sidecar (joined with AND). Useful for "this user's documents only",
// "embeddings inserted after :ts", etc.
//
// The fragment is concatenated into the sidecar's SELECT and so must
// reference columns the sidecar has — rowid plus any metadata columns
// the model's tag declared. It must NOT reference gorm-side columns;
// for that, chain a db.Where(...) on the returned slice instead.
func WithFilter(sqlFragment string, args ...any) Option {
	return func(o *options) {
		o.extraWhere = sqlFragment
		o.extraArgs = args
	}
}

// IncludeDeleted disables the default `deleted = 0` filter. Has no
// effect on models without gorm.DeletedAt.
func IncludeDeleted() Option {
	return func(o *options) { o.includeDel = true }
}

// KNN performs a nearest-neighbour search against the sidecar for model
// T and returns matching T rows in ranking order with distances
// attached. Calls db's row-fetch to materialize the gorm models, so
// any scopes / preloads chained on db apply.
//
// k is the maximum number of matches to return.
func KNN[T any](
	ctx context.Context,
	db *gorm.DB,
	query []float32,
	k int,
	opts ...Option,
) ([]Hit[T], error) {
	if k <= 0 {
		return nil, nil
	}
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
		return nil, fmt.Errorf(
			"vecgorm: KNN: %T has no fields tagged with vec",
			zero)
	}
	if len(mm.Fields) > 1 {
		// Multi-embedding models exist in principle but KNN[T] doesn't
		// disambiguate which one to query. Add a WithField option in
		// v1.1; for now reject.
		return nil, fmt.Errorf(
			"vecgorm: KNN: %T has %d vec-tagged fields; multi-field KNN is not yet supported",
			zero, len(mm.Fields))
	}
	m := mm.Fields[0]

	var o options
	for _, opt := range opts {
		opt(&o)
	}

	tbl, err := openSidecar(db, m)
	if err != nil {
		return nil, err
	}

	var queryOpts []vec.QueryOption
	whereParts := []string{}
	whereArgs := []any{}
	if m.SoftDelete && !o.includeDel {
		whereParts = append(whereParts, "deleted = 0")
	}
	if o.extraWhere != "" {
		whereParts = append(whereParts, "("+o.extraWhere+")")
		whereArgs = append(whereArgs, o.extraArgs...)
	}
	if len(whereParts) > 0 {
		queryOpts = append(queryOpts, vec.WithFilter(strings.Join(whereParts, " AND "), whereArgs...))
	}

	matches, err := tbl.KNNSlice(ctx, query, k, queryOpts...)
	if err != nil {
		return nil, fmt.Errorf("vecgorm: KNN %s: %w", m.Table, err)
	}
	if len(matches) == 0 {
		return nil, nil
	}

	// Materialize the gorm models. Use the caller's db so any scopes,
	// preloads, and session config carry through.
	rowids := make([]any, len(matches))
	for i, mt := range matches {
		rowids[i] = mt.Rowid
	}

	// We must SELECT in the order KNN returned, but gorm's `IN` clause
	// won't preserve order on SQLite. Fetch as a map then reassemble.
	models := reflect.New(reflect.SliceOf(reflect.TypeOf(zero))).Interface()
	if err := db.WithContext(ctx).
		Where(fmt.Sprintf("%s IN ?", quoteIdent(mm.PKField.DBName)), rowids).
		Find(models).Error; err != nil {
		return nil, fmt.Errorf("vecgorm: fetch models: %w", err)
	}

	indexed := make(map[int64]T)
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
	for _, mt := range matches {
		model, ok := indexed[mt.Rowid]
		if !ok {
			// Model not found in gorm fetch — typically means the
			// sidecar has a stale row (e.g. source was deleted but
			// sidecar wasn't cleaned up). Skip rather than panic.
			continue
		}
		results = append(results, Hit[T]{Model: model, Distance: mt.Distance})
	}
	return results, nil
}

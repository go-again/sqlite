package vecgorm

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-again/sqlite/internal/gormbridge"
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

// options controls KNN behavior. See WithFilter, IncludeDeleted,
// WithField, WithSelect, WithJoin, WithOrderBy.
type options struct {
	extraWhere  string
	extraArgs   []any
	includeDel  bool
	fieldName   string
	selectExtra string
	joinClause  string
	orderByExpr string
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
//
// # Trust model
//
// The sqlFragment is caller-trusted raw SQL (parameters in args... are
// bound, fragment text interpolates as-is) — same contract as
// [gorm.DB.Where] and [vec.WithFilter]. Validate identifiers with
// [vec.ValidIdent] before interpolating; pass literals through args.
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

// WithField selects which vec-tagged field on T to query against, for
// models that declare more than one. fieldName must match the Go
// struct field name (e.g. "Embedding", "ImageEmbedding") — case-
// sensitive, matching what gorm's schema parser sees.
//
// For models with exactly one vec-tagged field, WithField is
// unnecessary but still validated: an empty string or a name matching
// the single field is fine, any other name errors with the available
// names listed. Silently accepting wrong names would mask a typo as a
// query against the wrong embedding.
//
// For models with more than one, WithField is required; without it
// KNN[T] errors with a clear message naming the available fields.
//
// Multimodal models are the headline use case:
//
//	type Document struct {
//	    ID    uint              `gorm:"primaryKey"`
//	    Text  vecgorm.Embedding `vec:"dim=384;metric=cosine"`
//	    Image vecgorm.Embedding `vec:"dim=512;metric=cosine"`
//	}
//
//	textHits, _  := vecgorm.KNN[Document](ctx, db, textVec,  10, vecgorm.WithField("Text"))
//	imageHits, _ := vecgorm.KNN[Document](ctx, db, imageVec, 10, vecgorm.WithField("Image"))
func WithField(fieldName string) Option {
	return func(o *options) { o.fieldName = fieldName }
}

// WithSelect appends extra projected columns to the sidecar's SELECT
// list. Pair with [WithJoin] to source the extra columns from another
// table — typically the canonical row table the sidecar references by
// rowid. Trust contract matches [vec.WithSelect] and [WithFilter].
//
// IMPORTANT: WithSelect is honored by [KNNSQL] only. [KNN] errors if
// it sees WithSelect because the typed [Hit[T]] scanner can't consume
// custom projections.
func WithSelect(extraCols string) Option {
	return func(o *options) { o.selectExtra = extraCols }
}

// WithJoin inserts a JOIN clause after "FROM <sidecar>" so callers can
// project canonical-table columns alongside KNN distances in a single
// query. Same trust contract as [vec.WithJoin].
//
// IMPORTANT: WithJoin is honored by [KNNSQL] only. [KNN] errors if it
// sees WithJoin because the typed [Hit[T]] scanner can't consume rows
// whose shape depends on the joined table.
func WithJoin(joinClause string) Option {
	return func(o *options) { o.joinClause = joinClause }
}

// WithOrderBy replaces the default "ORDER BY distance" with a custom
// expression — useful when JOINing canonical data and sorting by one
// of its columns. Honored by [KNN], [KNNSlice], and [KNNSQL]; does not
// change the row shape.
//
// Same trust contract as [vec.WithOrderBy].
func WithOrderBy(expr string) Option {
	return func(o *options) { o.orderByExpr = expr }
}

// pickField selects the meta for the field the caller wants to query.
// Single-field models accept an empty fieldName OR the single field's
// name; any other name errors so a typo can't masquerade as a
// successful query against the only available field. Multi-field
// models require an explicit fieldName and error loudly otherwise,
// listing the available fields to make the recovery path obvious.
func pickField(fields []meta, fieldName string, zero any) (meta, error) {
	if len(fields) == 1 && fieldName == "" {
		return fields[0], nil
	}
	if len(fields) > 1 && fieldName == "" {
		names := make([]string, len(fields))
		for i, f := range fields {
			names[i] = f.FieldName
		}
		return meta{}, fmt.Errorf(
			"vecgorm: KNN: %T has %d vec-tagged fields (%s); pass vecgorm.WithField(\"<name>\") to pick one",
			zero, len(fields), strings.Join(names, ", "))
	}
	for _, f := range fields {
		if f.FieldName == fieldName {
			return f, nil
		}
	}
	names := make([]string, len(fields))
	for i, f := range fields {
		names[i] = f.FieldName
	}
	return meta{}, fmt.Errorf(
		"vecgorm: KNN: %T has no vec-tagged field named %q (have %s)",
		zero, fieldName, strings.Join(names, ", "))
}

// KNN performs a nearest-neighbour search against the sidecar for model
// T and returns matching T rows in ranking order with distances
// attached. Calls db's row-fetch to materialize the gorm models, so
// any scopes / preloads chained on db apply.
//
// k is the maximum number of matches to return.
//
// Transaction semantics: KNN reads the sidecar through *sql.DB (not
// the active *sql.Tx on db.Statement.ConnPool), so it sees the latest
// committed state — not writes made earlier in the same
// gorm.Transaction. Calling KNN inside a Transaction is therefore not
// supported in general, and will deadlock under pools pinned to one
// connection (the typical SQLite test setup), since the parent tx
// holds that conn. To query uncommitted state, issue raw SQL through
// tx.Raw against the sidecar table directly.
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

	var o options
	for _, opt := range opts {
		opt(&o)
	}

	m, err := pickField(mm.Fields, o.fieldName, zero)
	if err != nil {
		return nil, err
	}

	if o.selectExtra != "" || o.joinClause != "" {
		return nil, fmt.Errorf(
			"vecgorm.KNN: WithSelect / WithJoin change the row shape; use vecgorm.KNNSQL " +
				"with gorm.DB.Raw(sql, args...).Scan(&out) to consume custom projections")
	}

	// String-PK models read through a vec.KeyedTable[string]; the rowid path
	// below is for integer PKs.
	if m.KeyIsText {
		return knnTextKeys[T](ctx, db, mm, m, query, k, o)
	}

	tbl, err := openSidecar(db, m)
	if err != nil {
		return nil, err
	}

	queryOpts := buildVecQueryOpts(o, m)
	matches, err := tbl.KNNSlice(ctx, query, k, queryOpts...)
	if err != nil {
		return nil, fmt.Errorf("vecgorm: KNN %s: %w", m.Table, err)
	}
	if len(matches) == 0 {
		return nil, nil
	}

	// Materialize the gorm models via the caller's db so any scopes,
	// preloads, and session config carry through. KNN returns matches in
	// rank order, but gorm's `IN` clause won't preserve it on SQLite, so
	// we fetch into a PK-keyed map then reassemble below.
	rowids := make([]any, len(matches))
	for i, mt := range matches {
		rowids[i] = mt.Rowid
	}
	indexed, err := gormbridge.MaterializeByRowid[T](ctx, db, mm.PKField, rowids)
	if err != nil {
		return nil, fmt.Errorf("vecgorm: fetch models: %w", err)
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

// knnTextKeys is the string-PK analogue of the rowid KNN path: it reads through
// a vec.KeyedTable[string] and materializes models by their string primary key.
func knnTextKeys[T any](ctx context.Context, db *gorm.DB, mm modelMeta, m meta, query []float32, k int, o options) ([]Hit[T], error) {
	tbl, err := openKeyedSidecar(db, m)
	if err != nil {
		return nil, err
	}
	matches, err := tbl.KNNSlice(ctx, query, k, buildVecQueryOpts(o, m)...)
	if err != nil {
		return nil, fmt.Errorf("vecgorm: KNN %s: %w", m.Table, err)
	}
	if len(matches) == 0 {
		return nil, nil
	}
	keys := make([]any, len(matches))
	for i, mt := range matches {
		keys[i] = mt.Key
	}
	indexed, err := gormbridge.MaterializeByKey[string, T](ctx, db, mm.PKField, keys, gormbridge.PKAsString)
	if err != nil {
		return nil, fmt.Errorf("vecgorm: fetch models: %w", err)
	}
	results := make([]Hit[T], 0, len(matches))
	for _, mt := range matches {
		model, ok := indexed[mt.Key]
		if !ok {
			continue // stale sidecar row; skip
		}
		results = append(results, Hit[T]{Model: model, Distance: mt.Distance})
	}
	return results, nil
}

// KNNSQL returns the SQL statement and bound arguments the bridge
// would execute, without running it. Use it to extend the projection
// via [WithSelect] or join companion data via [WithJoin], then plug
// the SQL into `gorm.DB.Raw(sql, args...).Scan(&customStruct)` (or
// `db.QueryContext`) for a single round-trip.
//
// The sidecar's soft-delete filter (`deleted = 0`) is included when
// the model uses gorm.DeletedAt, exactly as [KNN] would — pass
// [IncludeDeleted] to disable it. [WithFilter] AND's in your own
// conjunct.
//
// Unlike [KNN], KNNSQL is safe to call inside a gorm.Transaction
// because it doesn't open a *vec.Table connection of its own; the
// caller executes the SQL through whatever conn pool they choose.
func KNNSQL[T any](
	db *gorm.DB,
	query []float32,
	k int,
	opts ...Option,
) (string, []any, error) {
	if k <= 0 {
		return "", nil, nil
	}
	p, err := pluginFrom(db)
	if err != nil {
		return "", nil, err
	}
	var zero T
	mm, err := p.registerSchema(db, &zero)
	if err != nil {
		return "", nil, err
	}
	if len(mm.Fields) == 0 {
		return "", nil, fmt.Errorf(
			"vecgorm: KNNSQL: %T has no fields tagged with vec", zero)
	}

	var o options
	for _, opt := range opts {
		opt(&o)
	}
	m, err := pickField(mm.Fields, o.fieldName, zero)
	if err != nil {
		return "", nil, err
	}

	tbl, err := openSidecar(db, m)
	if err != nil {
		return "", nil, err
	}
	return tbl.KNNSQL(query, k, buildVecQueryOpts(o, m)...)
}

// buildVecQueryOpts translates bridge-level options into the raw
// [vec.QueryOption] list. The soft-delete filter and the bridge's own
// [WithFilter] are stacked into a single [vec.WithFilter] so the
// generated SQL has at most one user-WHERE conjunct; [WithSelect],
// [WithJoin], [WithOrderBy] pass through 1:1.
func buildVecQueryOpts(o options, m meta) []vec.QueryOption {
	var out []vec.QueryOption
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
		out = append(out, vec.WithFilter(strings.Join(whereParts, " AND "), whereArgs...))
	}
	if o.selectExtra != "" {
		out = append(out, vec.WithSelect(o.selectExtra))
	}
	if o.joinClause != "" {
		out = append(out, vec.WithJoin(o.joinClause))
	}
	if o.orderByExpr != "" {
		out = append(out, vec.WithOrderBy(o.orderByExpr))
	}
	return out
}

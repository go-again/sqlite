package vec

import (
	"context"
	"iter"
	"log/slog"
	"time"

	"github.com/go-again/sqlite/internal/obs"
)

// Recorder is the metrics/tracing hook surface for vec.Observable. Each
// method is called after the corresponding Table operation has returned;
// the err argument is the operation's result. Implementations should be
// cheap — heavy work inside a Recorder method will block the calling
// goroutine.
//
// The shape mirrors fts.Recorder but with vec-native fields (vector
// dimension on inserts, k on KNN queries) so the per-package detail is
// preserved without a generic super-interface.
type Recorder interface {
	OnInsert(ctx context.Context, table string, n int, dim int, d time.Duration, err error)
	OnDelete(ctx context.Context, table string, n int, d time.Duration, err error)
	OnKNN(ctx context.Context, table string, dim, k int, d time.Duration, err error)
}

type obsConfig struct {
	logger   *slog.Logger
	recorder Recorder
}

// Option configures Wrap.
type Option func(*obsConfig)

// WithLogger attaches an slog.Logger that receives Info-level events for
// every successful operation and Error-level events for failures. The
// logger is invoked synchronously.
func WithLogger(l *slog.Logger) Option {
	return func(c *obsConfig) { c.logger = l }
}

// WithRecorder attaches a Recorder for metrics/tracing.
func WithRecorder(r Recorder) Option {
	return func(c *obsConfig) { c.recorder = r }
}

// Observable wraps a Table with optional logging and metrics hooks. It
// exposes the same surface so callers can drop it in without changing call
// sites: Insert / BatchInsert / Delete / KNN / KNNSlice all delegate to the
// underlying Table after firing the configured hooks.
//
// Wrap without Options returns a pass-through wrapper — useful for tests
// that toggle observability via a build-time flag.
type Observable struct {
	inner *Table
	cfg   obsConfig
}

// Wrap returns an Observable view of tbl that fires the supplied hooks on
// every operation. Passing no Option makes Wrap a no-op pass-through.
func Wrap(tbl *Table, opts ...Option) *Observable {
	o := &Observable{inner: tbl}
	for _, opt := range opts {
		opt(&o.cfg)
	}
	return o
}

// Inner returns the underlying Table. Useful for accessing methods not
// surfaced through Observable (e.g. Drop, accessors) without losing the
// wrapped instance.
func (o *Observable) Inner() *Table { return o.inner }

// Name returns the underlying table name.
func (o *Observable) Name() string { return o.inner.Name() }

// Dim returns the underlying table's vector dimension.
func (o *Observable) Dim() int { return o.inner.Dim() }

// Insert records the operation and delegates.
func (o *Observable) Insert(ctx context.Context, rowid int64, embedding []float32) error {
	start := time.Now()
	err := o.inner.Insert(ctx, rowid, embedding)
	dur := time.Since(start)
	if o.cfg.logger != nil {
		o.log(ctx, "vec.insert", err,
			slog.String("table", o.inner.Name()),
			slog.Int64("rowid", rowid),
			slog.Int("dim", len(embedding)),
			slog.Duration("dur", dur))
	}
	if o.cfg.recorder != nil {
		o.cfg.recorder.OnInsert(ctx, o.inner.Name(), 1, len(embedding), dur, err)
	}
	return err
}

// BatchInsert records the operation and delegates.
func (o *Observable) BatchInsert(ctx context.Context, items []Row) error {
	start := time.Now()
	err := o.inner.BatchInsert(ctx, items)
	dur := time.Since(start)
	dim := 0
	if len(items) > 0 {
		dim = len(items[0].Embedding)
	}
	if o.cfg.logger != nil {
		o.log(ctx, "vec.batch_insert", err,
			slog.String("table", o.inner.Name()),
			slog.Int("count", len(items)),
			slog.Int("dim", dim),
			slog.Duration("dur", dur))
	}
	if o.cfg.recorder != nil {
		o.cfg.recorder.OnInsert(ctx, o.inner.Name(), len(items), dim, dur, err)
	}
	return err
}

// Delete records the operation and delegates.
func (o *Observable) Delete(ctx context.Context, rowid int64) error {
	start := time.Now()
	err := o.inner.Delete(ctx, rowid)
	dur := time.Since(start)
	if o.cfg.logger != nil {
		o.log(ctx, "vec.delete", err,
			slog.String("table", o.inner.Name()),
			slog.Int64("rowid", rowid),
			slog.Duration("dur", dur))
	}
	if o.cfg.recorder != nil {
		o.cfg.recorder.OnDelete(ctx, o.inner.Name(), 1, dur, err)
	}
	return err
}

// KNN wraps the underlying iter.Seq2 so the recorder/logger fire exactly
// once per KNN invocation, after the iterator is fully drained or cleaned
// up by an early break. Forwards QueryOptions to the underlying Table.KNN.
func (o *Observable) KNN(ctx context.Context, query []float32, k int, opts ...QueryOption) iter.Seq2[Neighbor, error] {
	start := time.Now()
	dim := len(query)
	inner := o.inner.KNN(ctx, query, k, opts...)
	return func(yield func(Neighbor, error) bool) {
		var firstErr error
		defer func() {
			dur := time.Since(start)
			if o.cfg.logger != nil {
				o.log(ctx, "vec.knn", firstErr,
					slog.String("table", o.inner.Name()),
					slog.Int("dim", dim),
					slog.Int("k", k),
					slog.Duration("dur", dur))
			}
			if o.cfg.recorder != nil {
				o.cfg.recorder.OnKNN(ctx, o.inner.Name(), dim, k, dur, firstErr)
			}
		}()
		for m, err := range inner {
			if err != nil && firstErr == nil {
				firstErr = err
			}
			if !yield(m, err) {
				return
			}
		}
	}
}

// KNNSlice mirrors Table.KNNSlice with observability. Forwards QueryOptions.
//
// The output slice is pre-sized using the same `min(max(k,0), 1024)`
// clamp as [Table.KNNSlice] so the decorator doesn't silently re-pay
// the slice-growth overhead the bare Table path avoids.
func (o *Observable) KNNSlice(ctx context.Context, query []float32, k int, opts ...QueryOption) ([]Neighbor, error) {
	capHint := min(max(k, 0), 1024)
	out := make([]Neighbor, 0, capHint)
	for m, err := range o.KNN(ctx, query, k, opts...) {
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

func (o *Observable) log(ctx context.Context, msg string, err error, attrs ...slog.Attr) {
	obs.Log(ctx, o.cfg.logger, msg, err, attrs...)
}

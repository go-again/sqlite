package fts

import (
	"context"
	"iter"
	"log/slog"
	"time"

	"github.com/go-again/sqlite/internal/obs"
)

// Observable wraps an Index with optional logging and metrics hooks. Callers
// who want OpenTelemetry trace spans should layer them via Recorder.OnSearch
// — the hook receives the resolved MATCH expression and the elapsed time so
// any tracer/metric backend can be plugged in without this package depending
// on a specific observability library.
//
// Observable is a thin wrapper: every method delegates to the underlying
// Index after recording. It exposes the same Index[K, V] surface so existing
// callers can drop it in unchanged.
type Observable[K, V SQLType] struct {
	inner *Index[K, V]
	cfg   obsConfig
}

type obsConfig struct {
	logger   *slog.Logger
	recorder Recorder
}

// Recorder is the metrics/tracing hook surface. Each method is called after
// the corresponding Index operation has returned; the err argument is the
// operation's result. Implementations should be cheap — heavy work inside a
// Recorder method will block the calling goroutine.
type Recorder interface {
	OnInsert(ctx context.Context, table string, n int, d time.Duration, err error)
	OnDelete(ctx context.Context, table string, n int, d time.Duration, err error)
	OnSearch(ctx context.Context, table, match string, d time.Duration, err error)
}

// Option configures Wrap.
type Option func(*obsConfig)

// WithLogger attaches an slog.Logger that receives Info-level events for
// every successful operation and Error-level events for failures. The logger
// is invoked synchronously.
func WithLogger(l *slog.Logger) Option {
	return func(c *obsConfig) { c.logger = l }
}

// WithRecorder attaches a Recorder for metrics/tracing.
func WithRecorder(r Recorder) Option {
	return func(c *obsConfig) { c.recorder = r }
}

// Wrap returns an Observable view of idx that fires the supplied hooks on
// every operation. Passing no Option makes Wrap a no-op pass-through, useful
// for tests that toggle observability via a build-time flag.
func Wrap[K, V SQLType](idx *Index[K, V], opts ...Option) *Observable[K, V] {
	o := &Observable[K, V]{inner: idx}
	for _, opt := range opts {
		opt(&o.cfg)
	}
	return o
}

// Inner returns the underlying Index. Useful for accessing methods not
// surfaced through Observable (e.g. Optimize, Merge, Drop) without losing
// access to the wrapped instance.
func (o *Observable[K, V]) Inner() *Index[K, V] { return o.inner }

// Name returns the underlying table name.
func (o *Observable[K, V]) Name() string { return o.inner.Name() }

// Insert records the operation and delegates.
func (o *Observable[K, V]) Insert(ctx context.Context, items ...Attr[K, V]) error {
	start := time.Now()
	err := o.inner.Insert(ctx, items...)
	dur := time.Since(start)
	if o.cfg.logger != nil {
		o.log(ctx, "fts.insert", err, slog.String("table", o.inner.Name()),
			slog.Int("count", len(items)), slog.Duration("dur", dur))
	}
	if o.cfg.recorder != nil {
		o.cfg.recorder.OnInsert(ctx, o.inner.Name(), len(items), dur, err)
	}
	return err
}

// Delete records the operation and delegates.
func (o *Observable[K, V]) Delete(ctx context.Context, keys ...K) error {
	start := time.Now()
	err := o.inner.Delete(ctx, keys...)
	dur := time.Since(start)
	if o.cfg.logger != nil {
		o.log(ctx, "fts.delete", err, slog.String("table", o.inner.Name()),
			slog.Int("count", len(keys)), slog.Duration("dur", dur))
	}
	if o.cfg.recorder != nil {
		o.cfg.recorder.OnDelete(ctx, o.inner.Name(), len(keys), dur, err)
	}
	return err
}

// Search wraps the underlying iter.Seq2 so the recorder/logger fire exactly
// once per Search invocation (after the iterator is fully drained or cleaned
// up by an early break). The recorded match string is FTS5's MATCH RHS.
func (o *Observable[K, V]) Search(ctx context.Context, q Query, opts ...SearchOption) iter.Seq2[Hit[K, V], error] {
	start := time.Now()
	matchStr := ""
	if q != nil {
		matchStr = q.Build()
	}
	inner := o.inner.Search(ctx, q, opts...)
	return func(yield func(Hit[K, V], error) bool) {
		var firstErr error
		defer func() {
			dur := time.Since(start)
			if o.cfg.logger != nil {
				o.log(ctx, "fts.search", firstErr,
					slog.String("table", o.inner.Name()),
					slog.String("match", matchStr),
					slog.Duration("dur", dur))
			}
			if o.cfg.recorder != nil {
				o.cfg.recorder.OnSearch(ctx, o.inner.Name(), matchStr, dur, firstErr)
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

// SearchSlice mirrors Index.SearchSlice with observability.
func (o *Observable[K, V]) SearchSlice(ctx context.Context, q Query, opts ...SearchOption) ([]Hit[K, V], error) {
	var out []Hit[K, V]
	for m, err := range o.Search(ctx, q, opts...) {
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// log dispatches to the configured slog.Logger at the right level.
func (o *Observable[K, V]) log(ctx context.Context, msg string, err error, attrs ...slog.Attr) {
	obs.Log(ctx, o.cfg.logger, msg, err, attrs...)
}

package vec_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-again/sqlite/vec"
)

// stubRecorder counts observed events. Used to assert the Recorder hook
// fires once per operation, with the expected arguments.
type stubRecorder struct {
	inserts atomic.Int32
	deletes atomic.Int32
	knns    atomic.Int32
	lastDim atomic.Int32
	lastK   atomic.Int32
	lastErr atomic.Value // error
}

func (s *stubRecorder) OnInsert(_ context.Context, _ string, n, dim int, _ time.Duration, err error) {
	s.inserts.Add(int32(n))
	s.lastDim.Store(int32(dim))
	if err != nil {
		s.lastErr.Store(err)
	}
}
func (s *stubRecorder) OnDelete(_ context.Context, _ string, n int, _ time.Duration, err error) {
	s.deletes.Add(int32(n))
	if err != nil {
		s.lastErr.Store(err)
	}
}
func (s *stubRecorder) OnKNN(_ context.Context, _ string, dim, k int, _ time.Duration, err error) {
	s.knns.Add(1)
	s.lastDim.Store(int32(dim))
	s.lastK.Store(int32(k))
	if err != nil {
		s.lastErr.Store(err)
	}
}

// TestObservable_RecorderAndLogger asserts that wrapping a Table in
// vec.Wrap fires both the Recorder hooks and an slog.Logger for every op.
func TestObservable_RecorderAndLogger(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	tbl, err := vec.Create(ctx, db, "docs", 8, vec.Options{})
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	rec := &stubRecorder{}
	obs := vec.Wrap(tbl, vec.WithLogger(logger), vec.WithRecorder(rec))

	if err := obs.BatchInsert(ctx, fixture); err != nil {
		t.Fatal(err)
	}
	if got := rec.inserts.Load(); got != int32(len(fixture)) {
		t.Errorf("BatchInsert recorded %d items, want %d", got, len(fixture))
	}
	if got := rec.lastDim.Load(); got != 8 {
		t.Errorf("BatchInsert dim=%d, want 8", got)
	}

	if _, err := obs.KNNSlice(ctx, fixtureQuery, 2); err != nil {
		t.Fatal(err)
	}
	if got := rec.knns.Load(); got != 1 {
		t.Errorf("KNN recorded %d times, want 1", got)
	}
	if got := rec.lastK.Load(); got != 2 {
		t.Errorf("KNN last k=%d, want 2", got)
	}

	if err := obs.Insert(ctx, 99, fixture[0].Embedding); err != nil {
		t.Fatal(err)
	}
	if got := rec.inserts.Load(); got != int32(len(fixture)+1) {
		t.Errorf("single Insert did not bump count: now %d, want %d", got, len(fixture)+1)
	}

	if err := obs.Delete(ctx, 99); err != nil {
		t.Fatal(err)
	}
	if got := rec.deletes.Load(); got != 1 {
		t.Errorf("Delete recorded %d times, want 1", got)
	}

	out := buf.String()
	for _, want := range []string{"vec.batch_insert", "vec.knn", "vec.insert", "vec.delete"} {
		if !strings.Contains(out, want) {
			t.Errorf("log output missing %q; got:\n%s", want, out)
		}
	}
}

// TestObservable_NoOpWithoutOptions confirms Wrap with no Options is a
// pass-through — no hooks fire and operations behave identically.
func TestObservable_NoOpWithoutOptions(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	tbl, err := vec.Create(ctx, db, "docs", 8, vec.Options{})
	if err != nil {
		t.Fatal(err)
	}
	obs := vec.Wrap(tbl)

	if err := obs.BatchInsert(ctx, fixture); err != nil {
		t.Fatal(err)
	}
	matches, err := obs.KNNSlice(ctx, fixtureQuery, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 || matches[0].Rowid != 2 {
		t.Errorf("matches=%+v, want top rowid=2", matches)
	}
}

// TestObservable_KNNErrorRecorded confirms the Recorder receives the error
// when KNN fails (e.g. dim mismatch).
func TestObservable_KNNErrorRecorded(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	tbl, err := vec.Create(ctx, db, "docs", 8, vec.Options{})
	if err != nil {
		t.Fatal(err)
	}
	rec := &stubRecorder{}
	obs := vec.Wrap(tbl, vec.WithRecorder(rec))

	_, err = obs.KNNSlice(ctx, []float32{1, 2, 3}, 1) // dim mismatch
	if err == nil {
		t.Fatal("expected error from dim mismatch")
	}
	if rec.lastErr.Load() == nil {
		t.Errorf("recorder did not receive the error")
	}
}

// TestObservable_KNNBreakStillFires asserts the Recorder fires exactly once
// even when the consumer breaks out of the iter.Seq2 early — the deferred
// hook in KNN should run on cleanup.
func TestObservable_KNNBreakStillFires(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	tbl, err := vec.Create(ctx, db, "docs", 8, vec.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := tbl.BatchInsert(ctx, fixture); err != nil {
		t.Fatal(err)
	}
	rec := &stubRecorder{}
	obs := vec.Wrap(tbl, vec.WithRecorder(rec))

	for _, err := range obs.KNN(ctx, fixtureQuery, 4) {
		if err != nil {
			t.Fatal(err)
		}
		break
	}
	if got := rec.knns.Load(); got != 1 {
		t.Errorf("KNN recorded %d times after early break, want 1", got)
	}
}

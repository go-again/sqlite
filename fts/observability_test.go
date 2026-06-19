package fts_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gosqlite.org/fts"
)

// stubRecorder counts observed events. Used to assert the Recorder hook
// fires once per operation, with the expected arguments.
type stubRecorder struct {
	inserts  atomic.Int32
	deletes  atomic.Int32
	searches atomic.Int32
	lastSQL  atomic.Value // string
	lastErr  atomic.Value // error
}

func (s *stubRecorder) OnInsert(_ context.Context, _ string, _ int, _ time.Duration, err error) {
	s.inserts.Add(1)
	if err != nil {
		s.lastErr.Store(err)
	}
}
func (s *stubRecorder) OnDelete(_ context.Context, _ string, _ int, _ time.Duration, err error) {
	s.deletes.Add(1)
	if err != nil {
		s.lastErr.Store(err)
	}
}
func (s *stubRecorder) OnSearch(_ context.Context, _ string, match string, _ time.Duration, err error) {
	s.searches.Add(1)
	s.lastSQL.Store(match)
	if err != nil {
		s.lastErr.Store(err)
	}
}

// TestObservable_RecorderAndLogger asserts that wrapping an Index in
// fts.Wrap fires both the Recorder hooks and an slog.Logger exactly once per
// operation.
func TestObservable_RecorderAndLogger(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	inner := newIdx(t, db, fts.Options{})

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	rec := &stubRecorder{}

	idx := fts.Wrap(inner, fts.WithLogger(logger), fts.WithRecorder(rec))

	if err := idx.Insert(ctx, fixtureCorpus...); err != nil {
		t.Fatal(err)
	}
	if got := rec.inserts.Load(); got != 1 {
		t.Errorf("Insert recorded %d times, want 1", got)
	}

	if _, err := idx.SearchSlice(ctx, fts.Term("fox")); err != nil {
		t.Fatal(err)
	}
	if got := rec.searches.Load(); got != 1 {
		t.Errorf("Search recorded %d times, want 1", got)
	}
	if last, ok := rec.lastSQL.Load().(string); !ok || !strings.Contains(last, `"fox"`) {
		t.Errorf("recorded match=%q, want to contain quoted fox", last)
	}

	if err := idx.Delete(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if got := rec.deletes.Load(); got != 1 {
		t.Errorf("Delete recorded %d times, want 1", got)
	}

	// Logger received all three operations.
	out := buf.String()
	for _, want := range []string{"fts.insert", "fts.search", "fts.delete"} {
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
	inner := newIdx(t, db, fts.Options{})
	idx := fts.Wrap(inner)

	if err := idx.Insert(ctx, fts.Attr[int64, string]{Key: 1, Value: "hello"}); err != nil {
		t.Fatal(err)
	}
	matches, err := idx.SearchSlice(ctx, fts.Term("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Key != 1 {
		t.Errorf("matches=%+v, want [{Key:1}]", matches)
	}
}

// TestObservable_SearchErrorRecorded confirms the Recorder receives the
// error when Search fails (e.g. nil query).
func TestObservable_SearchErrorRecorded(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	inner := newIdx(t, db, fts.Options{})
	rec := &stubRecorder{}
	idx := fts.Wrap(inner, fts.WithRecorder(rec))

	_, err := idx.SearchSlice(ctx, nil)
	if err == nil {
		t.Fatal("expected error from nil query")
	}
	if rec.lastErr.Load() == nil {
		t.Errorf("recorder did not receive the error")
	}
}

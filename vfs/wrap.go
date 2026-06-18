package vfs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"time"
)

// Recorder receives one event per wrapped VFS / File operation. It is
// the instrumentation surface for [Wrap]: per-op latency, byte counts,
// and the Go error each operation returned. The shape parallels
// vfs/crypto's Recorder but operates at the public Go-interface level,
// so it carries an `error` (not a SQLite result code) and the file name
// rather than a file-kind byte.
//
// Methods fire once per operation on the goroutine driving the owning
// connection — keep them cheap; expensive work here stalls the SQLite
// engine thread that triggered the I/O. A read that comes up short past
// end-of-file reports io.EOF, which is SQLite's normal "file is shorter
// than the requested span" signal, not a fault.
//
// Only the four operations that matter for an I/O dashboard are
// surfaced (open latency, read/write bytes+latency, fsync commit-tax);
// every other method forwards to the base VFS unobserved.
type Recorder interface {
	OnOpen(name string, flags OpenFlags, dur time.Duration, err error)
	OnRead(name string, off int64, n int, dur time.Duration, err error)
	OnWrite(name string, off int64, n int, dur time.Duration, err error)
	OnSync(name string, dur time.Duration, err error)
}

// Wrap decorates base so every Open/Read/Write/Sync is timed and
// reported to rec — the building block for latency/throughput/error
// dashboards over any VFS, including ones registered via [Register] or
// the built-in in-memory backends. A nil rec returns base unchanged, so
// instrumentation can be toggled without a branch at every call site:
//
//	impl := myVFS()
//	if *trace {
//		impl = vfs.Wrap(impl, vfs.NewSlogRecorder(logger))
//	}
//	vfs.Register("app", impl)
//
// Wrap observes; it does not alter behaviour. A backend that wants to
// inject faults or shape latency implements [VFS] / [File] directly.
func Wrap(base VFS, rec Recorder) VFS {
	if rec == nil {
		return base
	}
	return &wrappedVFS{base: base, rec: rec}
}

type wrappedVFS struct {
	base VFS
	rec  Recorder
}

func (w *wrappedVFS) Open(name string, flags OpenFlags) (File, OpenFlags, error) {
	start := time.Now()
	f, granted, err := w.base.Open(name, flags)
	w.rec.OnOpen(name, flags, time.Since(start), err)
	if err != nil {
		return nil, granted, err
	}
	return &wrappedFile{base: f, name: name, rec: w.rec}, granted, nil
}

// Delete / Access / FullPathname forward verbatim — they are not on the
// per-page I/O hot path the Recorder targets.
func (w *wrappedVFS) Delete(name string, syncDir bool) error { return w.base.Delete(name, syncDir) }
func (w *wrappedVFS) Access(name string, op AccessOp) (bool, error) {
	return w.base.Access(name, op)
}
func (w *wrappedVFS) FullPathname(name string) (string, error) { return w.base.FullPathname(name) }

type wrappedFile struct {
	base File
	name string
	rec  Recorder
}

func (f *wrappedFile) ReadAt(p []byte, off int64) (int, error) {
	start := time.Now()
	n, err := f.base.ReadAt(p, off)
	f.rec.OnRead(f.name, off, n, time.Since(start), err)
	return n, err
}

func (f *wrappedFile) WriteAt(p []byte, off int64) (int, error) {
	start := time.Now()
	n, err := f.base.WriteAt(p, off)
	f.rec.OnWrite(f.name, off, n, time.Since(start), err)
	return n, err
}

func (f *wrappedFile) Sync(flags SyncFlags) error {
	start := time.Now()
	err := f.base.Sync(flags)
	f.rec.OnSync(f.name, time.Since(start), err)
	return err
}

func (f *wrappedFile) Truncate(size int64) error          { return f.base.Truncate(size) }
func (f *wrappedFile) Size() (int64, error)               { return f.base.Size() }
func (f *wrappedFile) Lock(level LockLevel) error         { return f.base.Lock(level) }
func (f *wrappedFile) Unlock(level LockLevel) error       { return f.base.Unlock(level) }
func (f *wrappedFile) CheckReservedLock() (bool, error)   { return f.base.CheckReservedLock() }
func (f *wrappedFile) SectorSize() int                    { return f.base.SectorSize() }
func (f *wrappedFile) DeviceCharacteristics() DeviceFlags { return f.base.DeviceCharacteristics() }
func (f *wrappedFile) Close() error                       { return f.base.Close() }

// FileControl forwards to the base file's capability when present, so
// wrapping never silently drops it; a base without FileControl yields
// the same ErrNotFound the dispatcher would have produced anyway.
func (f *wrappedFile) FileControl(op int, arg uintptr) error {
	if fc, ok := f.base.(FileControl); ok {
		return fc.FileControl(op, arg)
	}
	return ErrNotFound
}

// slogRecorder is the built-in [Recorder] that logs every observed op.
// Successful ops (and the expected short-read past EOF) log at Debug;
// any other error logs at Warn. Mirrors vfs/crypto's NewSlogRecorder
// and vec / fts's WithLogger.
type slogRecorder struct{ l *slog.Logger }

// NewSlogRecorder wraps a *slog.Logger as a [Recorder]. A nil logger
// uses slog.Default().
func NewSlogRecorder(l *slog.Logger) Recorder {
	if l == nil {
		l = slog.Default()
	}
	return &slogRecorder{l: l}
}

func (r *slogRecorder) OnOpen(name string, flags OpenFlags, dur time.Duration, err error) {
	r.emit("vfs.open", name, 0, 0, dur, err, slog.Int("flags", int(flags)))
}
func (r *slogRecorder) OnRead(name string, off int64, n int, dur time.Duration, err error) {
	r.emit("vfs.read", name, off, n, dur, err)
}
func (r *slogRecorder) OnWrite(name string, off int64, n int, dur time.Duration, err error) {
	r.emit("vfs.write", name, off, n, dur, err)
}
func (r *slogRecorder) OnSync(name string, dur time.Duration, err error) {
	r.emit("vfs.sync", name, 0, 0, dur, err)
}

func (r *slogRecorder) emit(msg, name string, off int64, n int, dur time.Duration, err error, extra ...slog.Attr) {
	level := slog.LevelDebug
	// io.EOF is SQLite's expected short-read signal (fires on every
	// fresh DB open); Warn-level there would flood normal startup logs.
	if err != nil && !errors.Is(err, io.EOF) {
		level = slog.LevelWarn
	}
	ctx := context.Background()
	if !r.l.Enabled(ctx, level) {
		return // skip attr allocation when the logger won't emit
	}
	attrs := append([]slog.Attr{
		slog.String("file", name),
		slog.Int64("off", off),
		slog.Int("bytes", n),
		slog.Duration("dur", dur),
	}, extra...)
	if err != nil {
		attrs = append(attrs, slog.String("err", err.Error()))
	}
	r.l.LogAttrs(ctx, level, msg, attrs...)
}

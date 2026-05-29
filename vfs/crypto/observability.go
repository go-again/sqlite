package crypto

import (
	"context"
	"log/slog"
	"time"

	sqlite3 "modernc.org/sqlite/lib"
)

// Recorder is the metrics/tracing hook surface for vfs/crypto. The
// shape parallels vec.Recorder and fts.Recorder but with VFS-native
// fields (file kind, byte offset, byte amount) so per-package detail
// is preserved without a generic super-interface.
//
// Each method fires once per io-method trampoline invocation (not
// once per encrypted page); for a multi-page read that's one OnRead
// reporting the full span. Implementations should be cheap —
// expensive work inside a Recorder method blocks the SQLite engine
// thread that triggered the read or write.
//
// Pass a Recorder via [Options.Recorder]. Nil means no recording.
//
// Two intentional shape differences from vec.Recorder / fts.Recorder:
//   - No ctx argument: VFS trampolines fire from transpiled C with no
//     Go-side ctx in scope to thread through.
//   - rc int32 instead of err error: SQLite returns result codes, not
//     Go errors; surfacing the raw rc lets consumers split by code
//     (e.g. SQLITE_IOERR_SHORT_READ vs SQLITE_IOERR_READ) without a
//     string-match dance.
type Recorder interface {
	// OnRead reports a completed read. kind is the file-kind tag (see
	// fileKindMainDB etc.); off is the byte offset in the file; amt
	// is the byte count requested; rc is the SQLite result code.
	OnRead(kind byte, off, amt int64, dur time.Duration, rc int32)
	// OnWrite reports a completed write with the same field shape.
	OnWrite(kind byte, off, amt int64, dur time.Duration, rc int32)
	// OnSync reports a completed xSync. fsync latency is often the
	// dominant per-commit cost on real filesystems, so commit-tax
	// dashboards key off this metric specifically.
	OnSync(kind byte, dur time.Duration, rc int32)
}

// FileKindName returns a stable human-readable name for the file-kind
// byte stored on per-file state and surfaced through Recorder. Useful
// when emitting metrics or logs without keeping the enum-to-string
// table in caller code.
func FileKindName(kind byte) string {
	switch kind {
	case fileKindMainDB:
		return "main_db"
	case fileKindMainJournal:
		return "main_journal"
	case fileKindWAL:
		return "wal"
	case fileKindTempDB:
		return "temp_db"
	case fileKindTempJournal:
		return "temp_journal"
	case fileKindSubJournal:
		return "sub_journal"
	default:
		return "unencrypted"
	}
}

// slogRecorder is the built-in [Recorder] that emits one event per
// successful op and one per failure. Use [NewSlogRecorder] when you
// already have a *slog.Logger and want the parallel of vec / fts's
// WithLogger.
type slogRecorder struct {
	l *slog.Logger
}

// NewSlogRecorder wraps a *slog.Logger as a [Recorder] that logs
// every read and write. Successful ops are Debug-level; non-OK
// SQLite result codes are Warn-level.
func NewSlogRecorder(l *slog.Logger) Recorder {
	if l == nil {
		l = slog.Default()
	}
	return &slogRecorder{l: l}
}

func (r *slogRecorder) OnRead(kind byte, off, amt int64, dur time.Duration, rc int32) {
	r.emit("vfs.crypto.read", kind, off, amt, dur, rc)
}

func (r *slogRecorder) OnWrite(kind byte, off, amt int64, dur time.Duration, rc int32) {
	r.emit("vfs.crypto.write", kind, off, amt, dur, rc)
}

func (r *slogRecorder) OnSync(kind byte, dur time.Duration, rc int32) {
	// xSync has no off/amt; surface them as zero so the structured
	// record shape stays uniform.
	r.emit("vfs.crypto.sync", kind, 0, 0, dur, rc)
}

func (r *slogRecorder) emit(msg string, kind byte, off, amt int64, dur time.Duration, rc int32) {
	level := slog.LevelDebug
	switch rc {
	case sqlite3.SQLITE_OK, sqlite3.SQLITE_IOERR_SHORT_READ:
		// SHORT_READ is SQLite's expected signal that the file is
		// shorter than the requested span — fires on every fresh DB
		// open. Warn-level here would flood normal startup logs.
	default:
		level = slog.LevelWarn
	}
	ctx := context.Background()
	if !r.l.Enabled(ctx, level) {
		return // skip attr allocation when the logger won't emit
	}
	r.l.LogAttrs(ctx, level, msg,
		slog.String("kind", FileKindName(kind)),
		slog.Int64("off", off),
		slog.Int64("amt", amt),
		slog.Duration("dur", dur),
		slog.Int("rc", int(rc)),
	)
}

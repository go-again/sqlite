// Package obs holds the slog level-dispatch shared by the vec and fts
// observability wrappers. The Recorder interfaces and Observable types
// deliberately stay per-package — their recorded fields differ (vec
// carries vector dimension / k, fts carries the MATCH string), so a
// generic super-interface would erase the per-package detail. Only the
// logger.LogAttrs level-dispatch, which is byte-identical between the
// two packages, lives here so the "Info on success, Error on failure,
// attach err attr" convention has a single source of truth.
package obs

import (
	"context"
	"log/slog"
)

// Log emits msg to logger at Info level, or at Error level when err is
// non-nil (appending an "err" attribute). It is a no-op when logger is
// nil, so callers may pass their optional logger unconditionally.
func Log(ctx context.Context, logger *slog.Logger, msg string, err error, attrs ...slog.Attr) {
	if logger == nil {
		return
	}
	if err != nil {
		logger.LogAttrs(ctx, slog.LevelError, msg, append(attrs, slog.String("err", err.Error()))...)
		return
	}
	logger.LogAttrs(ctx, slog.LevelInfo, msg, attrs...)
}

---
title: Observability
description: Wrap decorators for vec/fts/vfs with per-op recorders and slog, plus the low-level driver hooks.
sidebar:
  order: 17
---

# Observability

The typed sub-packages ship a `Wrap(...)` decorator that reports per-operation metrics to a `Recorder` you provide (bring your own metrics/tracing library). The `Recorder` interface differs per package because the recorded fields differ — vec records dimension and k; fts records the FTS5 MATCH expression; the VFS recorders record file kind, byte offset, and amount.

```go
idx := fts.Wrap(rawIndex,
	fts.WithLogger(slog.Default()),
	fts.WithRecorder(myMetricsAdapter))
```

- **vec / fts** — `vec.Wrap` / `fts.Wrap` with `WithLogger` + `WithRecorder`.
- **VFS** — `vfs.Wrap(base, recorder)` for any VFS (per-op latency / bytes / errors); `vfs.NewSlogRecorder` is a ready-made `log/slog` recorder. See [Custom VFS](custom-vfs.md).
- **vfs/crypto** — `Options.Recorder = crypto.NewSlogRecorder(slog.Default())` for per-IO encryption observability ([Encryption](encryption.md)).

The core driver exposes lower-level hooks for the same purpose: `(*Conn).SetTrace`, `RegisterUpdateHook`, `RegisterCommitHook`, `RegisterRollbackHook`, `RegisterPreUpdateHook`, `RegisterAuthorizer` ([Hooks](hooks.md)). Prepared-statement and runtime telemetry: `(*Conn).StmtCacheStats` / `Status` and `(*Stmt).Status`.

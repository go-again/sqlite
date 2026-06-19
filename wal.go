package sqlite // import "gosqlite.org"

import (
	"fmt"
	"sync"
	"unsafe"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"
)

// This file exposes WAL-mode control as typed (*Conn) methods: programmatic
// checkpointing (with the frame counts the PRAGMA can't return), the
// auto-checkpoint threshold, and a post-commit WAL hook (which has no PRAGMA
// equivalent). All require the database to be in WAL journal mode.

// CheckpointMode selects how aggressively [Conn.WALCheckpoint] runs.
type CheckpointMode int32

const (
	// CheckpointPassive checkpoints as many frames as possible without waiting
	// for readers or writers; never blocks.
	CheckpointPassive CheckpointMode = sqlite3.SQLITE_CHECKPOINT_PASSIVE
	// CheckpointFull waits for writers, then checkpoints all frames.
	CheckpointFull CheckpointMode = sqlite3.SQLITE_CHECKPOINT_FULL
	// CheckpointRestart is like Full, then waits for readers so the next write
	// restarts the WAL from the beginning.
	CheckpointRestart CheckpointMode = sqlite3.SQLITE_CHECKPOINT_RESTART
	// CheckpointTruncate is like Restart, then truncates the WAL file to zero.
	CheckpointTruncate CheckpointMode = sqlite3.SQLITE_CHECKPOINT_TRUNCATE
)

// WALCheckpoint runs a checkpoint on the WAL of the given schema (pass "" for
// all attached databases). It returns the size of the WAL log in frames and
// the number of frames checkpointed back into the database — values the
// `PRAGMA wal_checkpoint` form cannot surface per-schema. Requires WAL mode.
//
// https://sqlite.org/c3ref/wal_checkpoint_v2.html
func (c *Conn) WALCheckpoint(schema string, mode CheckpointMode) (logFrames, checkpointed int, err error) {
	var zDb uintptr
	if schema != "" {
		z, e := libc.CString(schema)
		if e != nil {
			return 0, 0, e
		}
		defer libc.Xfree(c.tls, z)
		zDb = z
	}
	bp := c.tls.Alloc(8)
	defer c.tls.Free(8)
	rc := sqlite3.Xsqlite3_wal_checkpoint_v2(c.tls, c.db, zDb, int32(mode), bp, bp+4)
	if rc != sqlite3.SQLITE_OK {
		return 0, 0, fmt.Errorf("sqlite: WALCheckpoint: %w", c.errstr(rc))
	}
	return int(*(*int32)(unsafe.Pointer(bp))), int(*(*int32)(unsafe.Pointer(bp + 4))), nil
}

// WALAutoCheckpoint sets the WAL auto-checkpoint threshold: SQLite runs a
// PASSIVE checkpoint after a commit that pushes the WAL past frames frames.
// Pass 0 to disable automatic checkpointing. Default is 1000.
//
// https://sqlite.org/c3ref/wal_autocheckpoint.html
func (c *Conn) WALAutoCheckpoint(frames int) error {
	if rc := sqlite3.Xsqlite3_wal_autocheckpoint(c.tls, c.db, int32(frames)); rc != sqlite3.SQLITE_OK {
		return fmt.Errorf("sqlite: WALAutoCheckpoint: %w", c.errstr(rc))
	}
	return nil
}

// WALHook is called after each commit in WAL mode, with the schema name and
// the total number of frames now in the WAL. Returning a non-nil error makes
// the commit report an error. A registered hook replaces SQLite's built-in
// auto-checkpoint hook, so a hook that wants checkpointing must call
// [Conn.WALCheckpoint] itself.
type WALHook func(schema string, walFrames int) error

var xWALHooks = struct {
	mu sync.RWMutex
	m  map[uintptr]WALHook
}{m: make(map[uintptr]WALHook)}

// RegisterWALHook installs hook as the post-commit WAL callback. Passing nil
// removes it and restores the default auto-checkpoint behavior.
//
// Per-connection registration.
func (c *Conn) RegisterWALHook(hook WALHook) {
	if hook == nil {
		xWALHooks.mu.Lock()
		delete(xWALHooks.m, c.db)
		xWALHooks.mu.Unlock()
		sqlite3.Xsqlite3_wal_hook(c.tls, c.db, 0, 0)
		return
	}
	xWALHooks.mu.Lock()
	xWALHooks.m[c.db] = hook
	xWALHooks.mu.Unlock()
	sqlite3.Xsqlite3_wal_hook(c.tls, c.db, cFuncPointer(walHookTrampoline), c.db)
}

// walHookTrampoline matches sqlite3_wal_hook's callback:
// int (*)(void *pArg, sqlite3 *db, const char *zDb, int nFrame).
func walHookTrampoline(tls *libc.TLS, pArg uintptr, _ uintptr, zDb uintptr, nFrame int32) int32 {
	xWALHooks.mu.RLock()
	hook := xWALHooks.m[pArg]
	xWALHooks.mu.RUnlock()
	if hook == nil {
		return sqlite3.SQLITE_OK
	}
	if err := hook(libc.GoString(zDb), int(nFrame)); err != nil {
		return int32(sqlite3.SQLITE_ERROR)
	}
	return sqlite3.SQLITE_OK
}

var _ func(*libc.TLS, uintptr, uintptr, uintptr, int32) int32 = walHookTrampoline

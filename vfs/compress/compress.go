package compress

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	sqlite "gosqlite.org"
)

// Open opens a database whose on-disk file at cfg.Path is stored compressed.
// It inflates the file into a private, transient working copy, opens that copy
// as a normal database, and — wired through cfg.VFSCloser — recompresses it
// back over cfg.Path when the returned handle is closed. A single
// defer db.Close() therefore both drains the pool and rewrites the compressed
// file, just like a plain [gosqlite.org.Open].
//
//	db, err := compress.Open(sqlite.Config{Path: "app.db.az"}, compress.Options{})
//	if err != nil { ... }
//	defer db.Close()
//
// The durable artifact is the compressed snapshot written at Close, so
// durability is per-session, not per-transaction: a crash while the database
// is open leaves cfg.Path at its previous Close (no corruption, but in-session
// changes are lost). The working copy is the full uncompressed database and
// lives in plaintext under the OS temp dir (or Options.TempDir) for the
// lifetime of the handle — so this is not a substitute for at-rest encryption.
// See the package documentation for the model and its limits.
//
// cfg.Path is required and must be on disk; an in-memory cfg (Path
// [gosqlite.org.InMemory] or Mode [gosqlite.org.ModeMemory]) is rejected, and
// cfg.VFS must be empty.
func Open(cfg sqlite.Config, opts Options) (*sqlite.DB, error) {
	if cfg.VFS != "" {
		return nil, errors.New("compress: Config.VFS must be empty")
	}
	if cfg.Path == sqlite.InMemory || cfg.Mode == sqlite.ModeMemory {
		return nil, errors.New("compress: a compressed database requires an on-disk path (refusing :memory: / mode=memory)")
	}
	if cfg.Path == "" {
		return nil, errors.New("compress: Config.Path is required")
	}
	// Fail fast if the destination directory is missing: otherwise the session
	// would open fine (the working copy lives elsewhere) and only fail at Close
	// when the recompress can't write there — losing the session's data with an
	// error many callers ignore.
	destDir := filepath.Dir(cfg.Path)
	if fi, err := os.Stat(destDir); err != nil {
		return nil, fmt.Errorf("compress: destination directory %q is not accessible: %w", destDir, err)
	} else if !fi.IsDir() {
		return nil, fmt.Errorf("compress: destination parent %q is not a directory", destDir)
	}

	dest := cfg.Path
	workDir, err := os.MkdirTemp(opts.TempDir, "gosqlite-compress-")
	if err != nil {
		return nil, fmt.Errorf("compress: create working dir: %w", err)
	}
	workPath := filepath.Join(workDir, "data.db")

	if err := inflate(dest, workPath); err != nil {
		os.RemoveAll(workDir)
		return nil, err
	}

	rc := &recompressor{
		dest:     dest,
		workDir:  workDir,
		workPath: workPath,
		level:    opts.Level,
		readOnly: cfg.Mode == sqlite.ModeReadOnly,
	}

	cfg.Path = workPath
	cfg.VFSCloser = rc
	db, err := sqlite.Open(cfg)
	if err != nil {
		// sqlite.Open closes cfg.VFSCloser on its error paths; rc is still
		// unarmed, so that only removes the working dir and leaves dest
		// untouched. Close again defensively (idempotent) in case it didn't.
		_ = rc.Close()
		return nil, err
	}
	// Arm only after a successful open, so the recompress never fires for a
	// failed session and overwrites dest with a partial/empty database.
	rc.arm()
	return db, nil
}

// inflate prepares the working copy at workPath from the file at dest:
//   - missing or empty dest      → leave workPath absent (SQLite creates a fresh DB)
//   - raw SQLite database at dest → copy it (adopted; becomes compressed on Close)
//   - otherwise                  → treat dest as a compressed frame and decompress it
func inflate(dest, workPath string) error {
	f, err := os.Open(dest)
	if errors.Is(err, os.ErrNotExist) {
		return nil // fresh database
	}
	if err != nil {
		return fmt.Errorf("compress: open %q: %w", dest, err)
	}
	defer f.Close()

	hdr := make([]byte, len(sqliteMagic))
	n, err := io.ReadFull(f, hdr)
	if n == 0 {
		return nil // empty file → fresh database
	}
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("compress: read %q: %w", dest, err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("compress: seek %q: %w", dest, err)
	}

	out, err := os.Create(workPath)
	if err != nil {
		return fmt.Errorf("compress: create working copy: %w", err)
	}
	defer out.Close()

	if looksLikeSQLite(hdr[:n]) {
		if _, err := io.Copy(out, f); err != nil {
			return fmt.Errorf("compress: copy %q: %w", dest, err)
		}
	} else {
		dn, err := decompressStream(out, f)
		if err != nil {
			return fmt.Errorf("compress: %q is neither a compressed nor a SQLite database: %w", dest, err)
		}
		if dn == 0 {
			// A non-empty source that decompresses to nothing is not a valid
			// compressed database — a 1–3 byte file looks like a cleanly empty
			// stream to the codec. Reject it rather than adopt an empty database
			// and overwrite dest on Close.
			return fmt.Errorf("compress: %q is too short to be a compressed or SQLite database", dest)
		}
	}
	return out.Sync()
}

// recompressor is the cfg.VFSCloser that rewrites the compressed file when the
// database handle is closed. It is closed by DB.Close after the pool drains
// (and by Open on its error paths).
type recompressor struct {
	dest     string
	workDir  string
	workPath string
	level    Compression
	readOnly bool

	mu     sync.Mutex
	armed  bool
	closed bool
}

func (rc *recompressor) arm() {
	rc.mu.Lock()
	rc.armed = true
	rc.mu.Unlock()
}

// Close recompresses the working copy back over dest (when armed and writable)
// and always removes the working directory. It is idempotent.
func (rc *recompressor) Close() error {
	rc.mu.Lock()
	if rc.closed {
		rc.mu.Unlock()
		return nil
	}
	rc.closed = true
	armed := rc.armed
	rc.mu.Unlock()

	defer os.RemoveAll(rc.workDir)

	if !armed || rc.readOnly {
		return nil
	}
	if _, err := os.Stat(rc.workPath); errors.Is(err, os.ErrNotExist) {
		return nil // session never wrote anything; leave dest as-is
	}
	if err := consolidate(rc.workPath); err != nil {
		return fmt.Errorf("compress: consolidate working copy: %w", err)
	}
	if err := packFile(rc.dest, rc.workPath, rc.level); err != nil {
		return fmt.Errorf("compress: recompress to %q: %w", rc.dest, err)
	}
	return nil
}

// consolidate folds any WAL frames back into the main database file so the
// single working file is self-contained before it is compressed. It is a
// no-op outside WAL mode.
func consolidate(path string) error {
	db, err := sqlite.Open(sqlite.Config{Path: path})
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return err
}

// Pack compresses the SQLite database at src into a compressed file at dst,
// written atomically. The database is not opened, so it must not be in use.
func Pack(dst, src string, level Compression) error {
	return packFile(dst, src, level)
}

// Unpack decompresses a compressed file at src into a plain SQLite database at
// dst, written atomically.
func Unpack(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	return atomicWrite(dst, func(w io.Writer) error {
		_, err := decompressStream(w, in)
		return err
	})
}

// packFile compresses src into dst at the given level, atomically.
func packFile(dst, src string, level Compression) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	return atomicWrite(dst, func(w io.Writer) error {
		return compressStream(w, in, level)
	})
}

// atomicWrite calls write to produce the contents of dest into a temp file in
// dest's directory, fsyncs it, and renames it over dest — so dest is never a
// partially written file even if the process crashes mid-write.
func atomicWrite(dest string, write func(io.Writer) error) error {
	dir := filepath.Dir(dest)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(dest)+".compress-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename below succeeds

	if err := write(tmp); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Preserve an existing file's permission bits (os.CreateTemp makes 0600; a
	// brand-new destination keeps that secure default).
	if fi, err := os.Stat(dest); err == nil {
		_ = os.Chmod(tmpName, fi.Mode().Perm())
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return err
	}
	// Best-effort: fsync the parent directory so the rename itself is durable on
	// power loss (POSIX). A no-op/harmless where a directory can't be synced.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

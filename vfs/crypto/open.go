package crypto

import (
	"errors"

	sqlite "gosqlite.org"
)

// Open opens an encrypted database described by cfg, encrypting the on-disk
// file with opts. It registers a crypto VFS for opts, routes cfg through that
// VFS, and wires the VFS teardown into the returned database's Close — so a
// single defer db.Close() releases both the connection pool and the VFS, just
// like a plain [gosqlite.org.Open].
//
//	db, err := crypto.Open(sqlite.Config{Path: "app.db", Pragmas: sqlite.RecommendedPragmas()},
//		crypto.Options{Key: key}) // 32-byte Adiantum key (default cipher)
//	if err != nil { ... }
//	defer db.Close()
//
// Encryption requires an on-disk database, so an in-memory cfg (Path
// [gosqlite.org.InMemory] or Mode [gosqlite.org.ModeMemory]) is rejected.
// cfg.VFS must be empty — Open sets it to the crypto VFS it registers.
func Open(cfg sqlite.Config, opts Options) (*sqlite.DB, error) {
	if cfg.VFS != "" {
		return nil, errors.New("crypto: Config.VFS must be empty (Open sets it to the crypto VFS)")
	}
	if cfg.Path == sqlite.InMemory || cfg.Mode == sqlite.ModeMemory {
		return nil, errors.New("crypto: encryption requires an on-disk path (refusing :memory: / mode=memory)")
	}
	name, fs, err := New(opts)
	if err != nil {
		return nil, err
	}
	// sqlite.Open routes the DSN through this VFS by name and, because we set
	// VFSCloser, runs fs.Close() (unregister) after the pool drains on Close —
	// and also on its own error paths, so fs is released if Open fails.
	cfg.VFS = name
	cfg.VFSCloser = fs
	return sqlite.Open(cfg)
}

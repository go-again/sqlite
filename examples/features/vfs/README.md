# VFS — virtual file systems

Back a SQLite database with storage you control, in pure Go.

| example | what it shows |
|---|---|
| [`custom`](custom/) | implement the public `vfs.VFS` / `vfs.File` interface and `vfs.Register` a writable in-memory VFS — full CRUD + transactions, plus `vfs.Wrap` per-op instrumentation |
| [`crypto`](crypto/) | encryption-at-rest (`vfs/crypto`) — Adiantum or AES-XTS-256, transparent page-level encryption with a slog recorder |
| [`cksm`](cksm/) | page checksums (`vfs/cksm`) — corrupt one byte and observe `SQLITE_IOERR_DATA` on read |
| [`mvcc`](mvcc/) | in-memory MVCC VFS — snapshot-isolated reads; shared (`file:/name`) vs private (`file:name`) databases |
| [`memdb`](memdb/) | plain in-memory VFS — direct per-page store, writes visible to readers immediately |
| [`embed`](embed/) | bundle a `.db` inside `embed.FS` and open it read-only via `vfs.New(fs.FS)` |

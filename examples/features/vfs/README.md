# VFS — virtual file systems

Back a SQLite database with storage you control, in pure Go.

| example | what it shows |
|---|---|
| [`custom`](custom/) | implement the public `vfs.VFS` / `vfs.File` interface and `vfs.Register` a writable in-memory VFS — full CRUD + transactions, plus `vfs.Wrap` per-op instrumentation |
| [`cksm`](cksm/) | page checksums (`vfs/cksm`) — corrupt one byte and observe `SQLITE_IOERR_DATA` on read |
| [`mvcc`](mvcc/) | in-memory MVCC VFS — snapshot-isolated reads; shared (`file:/name`) vs private (`file:name`) databases |
| [`memdb`](memdb/) | plain in-memory VFS — direct per-page store, writes visible to readers immediately |
| [`embed`](embed/) | bundle a `.db` inside `embed.FS` and open it read-only via `vfs.New(fs.FS)` |
| [`crypto`](crypto/) | at-rest encryption (`vfs/crypto`, a separate module) — `crypto.Open` with Adiantum; the example reopens the raw file to prove the plaintext isn't on disk |
| `vault` | the comprehensive container (`vfs/vault`) where compression and encryption are independent options — see [`vfs/vault/example`](../../../vfs/vault/example/main.go) (plain → compress → encrypt → multi-recipient → authenticated → anti-replay) and [`vault-blobstore`](../../vault-blobstore/) (a blobstore over a multi-recipient, tamper-evident container) |

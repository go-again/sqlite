# Features

Capability reference — one runnable example per feature, for both migrators and newcomers. These default to the modern `"sqlite"` driver and typed APIs; where an example uses raw SQL it's because the feature is connection-level (a hook, a changeset, a vtab), not because it's legacy.

| group | what's inside |
|---|---|
| [`search/`](search/) | sqlite-vec vector search (`vec.Table`, KNN, keyed), FTS5 full-text (`fts.Index`, tokenizers, vocab), and hybrid rank fusion |
| [`vfs/`](vfs/) | virtual file systems — a user-implemented one, encryption-at-rest, page checksums, in-memory (MVCC + plain), and `fs.FS`/`embed.FS`-backed |
| [`extensions/`](extensions/) | the loadable `ext/` catalog — scalar UDFs, aggregates, collations, table-valued functions, vtab readers, and specialised stores |
| [`advanced/`](advanced/) | hooks, sessions/changesets, backup + serialize, the bounded page cache, window functions, busy handlers, and other connection-level controls |

New to the library? Start in [`../getting-started/`](../getting-started/). Coming from another package? See [`../migrating/`](../migrating/). Using the gorm dialector? Its examples live in the separate `gosqlite.org/gorm` module — see [`../../gorm/examples/`](../../gorm/examples/).

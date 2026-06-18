# gorm — combined with the rest of the stack

gorm beyond the basics: vector and full-text sidecars, encryption, a read-only VFS, and `ext/` functions. For the plain gorm starting point see [`../../getting-started/gorm`](../../getting-started/gorm/); for the glebarez migration see [`../../migrating/from-glebarez`](../../migrating/from-glebarez/).

| example | what it shows |
|---|---|
| [`vec-tagged`](vec-tagged/) | tag-driven sqlite-vec sidecar — `vec:"dim=N;metric=…"` on a model, `vecgorm.Plugin()`, `vecgorm.KNN[T]`; works with int *and* string primary keys |
| [`fts-tagged`](fts-tagged/) | tag-driven FTS5 sidecar — `fts5:"…"` on a model, `ftsgorm.Plugin()`, `ftsgorm.Search[T]` |
| [`vec`](vec/) | gorm + vec the explicit (no-plugin) way, side by side |
| [`fts`](fts/) | gorm + FTS5 external-content the explicit way |
| [`crypto`](crypto/) | gorm on an encrypted database + vec/fts sidecars + hybrid ranking + argon2id key derivation |
| [`vfs`](vfs/) | gorm reading from a `fstest.MapFS` via `vfs/` |
| [`ext-scalars`](ext-scalars/) | drive `ext/` scalar / aggregate / collation functions through plain gorm `Where`/`Order`/`Select` |
| [`ext-vtabs`](ext-vtabs/) | query `ext/` virtual tables (series, csv, …) through gorm |

---
name: gorm
description: Use when using gorm with gosqlite — the dialector (drop-in for glebarez/go-gorm-sqlite) and the tag-driven vec/FTS5 sidecars that maintain vector and full-text indexes from a struct tag.
---

# gorm with gosqlite

The `gosqlite.org/gorm` package is a `gorm.Dialector` — a drop-in for `glebarez/sqlite` and `go-gorm/sqlite`.

```go
import (
	"gorm.io/gorm"
	sqlite "gosqlite.org/gorm" // package is `sqlite` — drop-in for glebarez/go-gorm
)

db, _ := gorm.Open(sqlite.Open("file:my.db?_pragma=foreign_keys(1)"), &gorm.Config{})
```

The package name is `sqlite`, so existing glebarez / go-gorm call sites compile unchanged: `sqlite.Open(dsn)` and `sqlite.New(Config{...})` both exist for either upstream import path.

For the modern typed config you also need the root `gosqlite.org` (also `package sqlite`), so alias the dialector `sqlitegorm` to avoid the name clash — this is the convention everywhere the two are imported together:

```go
import (
	sqlite "gosqlite.org"
	sqlitegorm "gosqlite.org/gorm"
)

db, _ := sqlitegorm.OpenConfig(sqlite.Config{Path: "my.db", Pragmas: sqlite.RecommendedPragmas()})
```

## Tag-driven vec & FTS5 sidecars

Tag a field, register the plugin, migrate — gorm Create/Update/Delete then maintains the sidecar automatically.

```go
import (
	vecgorm "gosqlite.org/vec/gorm"
	ftsgorm "gosqlite.org/fts/gorm"
	"gosqlite.org/fts"
)

type Document struct {
	ID        uint   `gorm:"primaryKey"`
	Body      string `fts5:"tokenize=porter+unicode61"`
	Embedding vecgorm.Embedding `vec:"dim=384;metric=cosine"`
}

db.Use(vecgorm.Plugin())
db.Use(ftsgorm.Plugin())
vecgorm.Migrate(db, &Document{})
ftsgorm.Migrate(db, &Document{})

db.Create(&Document{Body: "world", Embedding: vec})
near, _ := vecgorm.KNN[Document](ctx, db, queryVec, 5)
hits, _ := ftsgorm.Search[Document](ctx, db, fts.Term("world"))
```

## Rules

- The embedding field MUST be `vecgorm.Embedding` (a `[]float32` alias), OR a raw `[]float32` with a `gorm:"-"` tag alongside the `vec:"…"` tag — otherwise gorm's schema parser errors before any plugin hook runs.
- Works with int AND string primary keys.
- Soft-deleted rows (`gorm.DeletedAt`) are excluded from KNN/Search automatically.
- **Do NOT call `vecgorm.KNN` inside `db.Transaction(...)`** — it needs its own statement context.
- `db.Migrator().DropTable(&Document{})` cascades into the sidecar automatically.

Full reference: [`docs/guides/gorm.md`](../../docs/guides/gorm.md). The underlying typed APIs: the `vector-search` and `full-text-search` skills.

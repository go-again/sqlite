---
title: gorm integration
description: The gorm dialector (drop-in for glebarez and go-gorm/sqlite) plus tag-driven sqlite-vec and FTS5 sidecars.
sidebar:
  order: 4
---

# gorm

The `github.com/go-again/sqlite/gorm` sub-package is a `gorm.Dialector` — a drop-in for both `glebarez/sqlite` and the official `go-gorm/sqlite`.

```go
import (
	"gorm.io/gorm"
	sqlite "github.com/go-again/sqlite/gorm"
)

db, _ := gorm.Open(sqlite.Open("file:my.db?_pragma=foreign_keys(1)"), &gorm.Config{})
```

`sqlite.Open(dsn)` and `sqlite.New(sqlite.Config{...})` are both provided so either import path can be swapped in. For the modern typed entry use `sqlitegorm.OpenConfig(sqlite.Config{...})` ([Configuration](configuration.md)). Coverage matrix: [`dev/coverage/gorm.md`](../../dev/coverage/gorm.md).

## Tag-driven vec & FTS5 sidecars

`vec/gorm` and `fts/gorm` bridge gorm models to the vector / full-text sidecars. Tag a field, register the plugin, and gorm `Create`/`Update`/`Delete` maintains the sidecar automatically. Typed `KNN[T]` / `Search[T]` helpers return matching gorm models in ranking order, distance / rank attached.

```go
import (
	_ "github.com/go-again/sqlite"
	sqlitegorm "github.com/go-again/sqlite/gorm"
	"github.com/go-again/sqlite/fts"
	ftsgorm "github.com/go-again/sqlite/fts/gorm"
	vecgorm "github.com/go-again/sqlite/vec/gorm"
)

type Document struct {
	ID        uint   `gorm:"primaryKey"`
	Title     string `fts5:"tokenize=porter+unicode61"`
	Body      string `fts5:"tokenize=porter+unicode61"`
	Embedding vecgorm.Embedding `vec:"dim=384;metric=cosine"`
}

db, _ := gorm.Open(sqlitegorm.Open("app.db"), &gorm.Config{})
db.Use(vecgorm.Plugin())
db.Use(ftsgorm.Plugin())

vecgorm.Migrate(db, &Document{}) // creates documents + documents_vec
ftsgorm.Migrate(db, &Document{}) // creates documents_fts + triggers

db.Create(&Document{Title: "Hello", Body: "world", Embedding: vec})

near, _ := vecgorm.KNN[Document](ctx, db, queryVec, 5)             // semantic
hits, _ := ftsgorm.Search[Document](ctx, db, fts.Term("world"))   // BM25 lexical
```

What the bridges do:

- Auto-migrate sidecar tables alongside `db.AutoMigrate`.
- Sync-on-write callbacks (vec) or triggers (FTS5), including `CreateInBatches` (one transaction per batch).
- Soft-delete awareness: models using `gorm.DeletedAt` get a metadata column on the sidecar; KNN/Search excludes them automatically — there is no opt-in to include them via the typed helpers.
- Typed helpers return `[]Hit[T]` ordered by ranking — no manual IN-clause + re-sort dance.
- `db.Migrator().DropTable(&Model{})` cascades into the sidecar via the dialector's `DropTableHook` — no manual cleanup.
- FTS5 mode is per-tag: `external` (default, triggers-driven), `external=false` (in-table FTS5), or `contentless=true` (index only).
- Works with int **and** string primary keys.

The embedding field type is `vecgorm.Embedding` (a `[]float32` alias implementing gorm's `GormDataType`); `[]float32` with `gorm:"-"` also works.

Runnable: [`examples/features/gorm/vec-tagged/`](../../examples/features/gorm/vec-tagged/main.go) and [`examples/features/gorm/fts-tagged/`](../../examples/features/gorm/fts-tagged/main.go). Combining `ext/` functions with gorm: [`examples/features/gorm/`](../../examples/features/gorm/).

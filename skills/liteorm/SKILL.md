---
name: liteorm
description: Use when the task wants an ORM or declarative models on top of gosqlite — LiteORM adds first-class native vector / full-text / hybrid (RRF) search and encryption on the gosqlite SQLite driver. Also when porting gorm code to LiteORM, or when one app needs the same data layer across SQLite and Postgres/MySQL/SQL Server.
---

# LiteORM — declarative models on gosqlite

[LiteORM](https://liteorm.org) is a CGo-free Go data library: two front-ends over one shared core, across four backends (SQLite, Postgres, MySQL, SQL Server). **gosqlite is its SQLite backend, and the integration is first-class:**

- LiteORM's SQLite **vector / full-text / hybrid search** is built directly on gosqlite's `vec` / `fts` / `fusion` packages, exposed as declarative model indexes with typed, ranked results.
- **Encryption at rest** rides on gosqlite's `vfs/crypto` (`sqlite.OpenEncrypted`).
- `sqlite.OpenConfig` takes the **same `gosqlite.Config`** the bare driver uses, and `sqlite.Conn(sess)` hands you the underlying **`*gosqlite.DB`** — so any gosqlite feature (sessions, backup, the `ext/` catalog, raw `vec.Table` / `fts.Index`) is reachable from inside an ORM transaction.

If you want declarative models *and* gosqlite's advanced SQLite features together, LiteORM is the path. (The gosqlite [`gorm`](../gorm/SKILL.md) dialector is the choice only when you specifically need `gorm.io/gorm`; it deliberately offers no vector/FTS integration.)

This skill is enough to write real LiteORM code. LiteORM ships its **own** comprehensive skills and docs — treat [liteorm.org](https://liteorm.org) and [pkg.go.dev/liteorm.org](https://pkg.go.dev/liteorm.org) as canonical (see [Going deeper](#going-deeper)).

## Install & open

```go
import (
	liteorm "liteorm.org"               // DB, Session, error sentinels
	"liteorm.org/dialect/sqlite"        // the gosqlite-backed SQLite backend
)

db, err := sqlite.Open("app.db")        // *liteorm.DB
defer db.Close()
```

`go get liteorm.org`. Other SQLite openers:

```go
db, _ := sqlite.OpenEncrypted("secret.db", key)          // page-level encryption (gosqlite vfs/crypto)
db, _ := sqlite.OpenConfig(gosqlite.Config{              // the SAME typed Config the bare driver uses
	Path: "app.db", Pragmas: gosqlite.RecommendedPragmas(),
})
```

The same code runs on `postgres.Open(ctx, dsn)` / `mysql.Open` / `mssql.Open` — only the backend import changes. Search and encryption are SQLite-only (gosqlite-backed).

## Drop to gosqlite anytime

`sqlite.Conn(sess)` returns the backing `*gosqlite.DB`, so LiteORM never boxes you out of the driver:

```go
g, ok := sqlite.Conn(db)        // *gosqlite.DB, ok=false on a non-SQLite session
if ok {
	tbl, _ := vec.Open(g.DB, "doc_vec", 384, vec.Options{Metric: vec.Cosine}) // raw gosqlite typed API
	// …also sessions/changesets, Backup, Serialize, custom VFS — all reachable here.
}
```

## Two front-ends (pick per task, not per project)

| Front-end | Import | Choose it when |
|---|---|---|
| `query` | `liteorm.org/query` | explicit typed SQL — `Select[T]().Where/.Filter/.All`, a CRUD `Repo` (`Insert`/`Get`/`Update`/`Delete`). |
| `orm` | `liteorm.org/orm` | declarative models — struct tags, `AutoMigrate`, search indexes, hooks, soft delete, `Repo.Create`. |

Both take one `liteorm.Session` (satisfied by `*liteorm.DB` and by a transaction), so you can mix them on the same connection or tx.

```go
type User struct {
	ID    int64
	Name  string
	Email string
}
func (User) TableName() string { return "users" } // omit ⇒ snake_case, singular

// declarative orm front-end:
repo := orm.NewRepo[User](db)
u := User{Name: "Ada", Email: "ada@x.io"}
_ = repo.Create(ctx, &u)          // PK read back into u
got, _ := repo.Get(ctx, u.ID)     // liteorm.ErrNoRows if missing

// explicit query front-end (same db / tx):
rows, _ := query.Select[User](db).Where("email = ?", "ada@x.io").All(ctx)
```

## Vector / full-text / hybrid search (the headline)

SQLite-only, capability-gated (a helper on a non-SQLite session returns `search.ErrUnsupportedBackend`). Each index is a sidecar table keyed by the model's primary key, built on gosqlite's `vec` / `fts` / `fusion`.

**Declarative (recommended)** — declare indexes on the model; `orm.AutoMigrate` creates the sidecars + keeps them in sync, so ordinary `Create`/`Update`/`Delete` need no index calls:

```go
import (
	"liteorm.org/orm"
	"liteorm.org/dialect/sqlite/search"
)

type Article struct {
	ID        int64
	Title     string
	Body      string
	Embedding []float32 `orm:"-"` // sidecar-only, not a base column
}

func (Article) SearchIndexes() []orm.SearchIndex {
	return []orm.SearchIndex{
		orm.FullText("Title", "Body"),
		orm.Vector("Embedding", 384).WithMetric(orm.Cosine), // Cosine is the orm.Vector default
	}
}

orm.AutoMigrate[Article](ctx, db)   // articles + articles_fts + articles_vec (+sync)
repo := orm.NewRepo[Article](db)
repo.Create(ctx, &Article{Title: "…", Body: "…", Embedding: vec}) // sidecars sync automatically

s := search.For[Article](db)                                   // *Searcher[Article]
near, _  := s.Vector(ctx, queryVec, 5)                         // []search.Hit[Article]; Score = distance (smaller nearer)
hits, _  := s.FullText(ctx, search.Term("rocket"), 5)          // Score = BM25 rank
fused, _ := s.Hybrid(ctx, queryVec, search.Term("rocket"), 5)  // Score = RRF (larger better)
for _, h := range near { use(h.Model, h.Score) }
```

Tags are the shorthand for the common case: `vec:"dim=384;metric=cosine"` on the embedding, `fts:"tokenize=porter unicode61"` on a text field (`fts5:` is an alias). Use the `SearchIndexes()` method for multi-column full-text, `.WithWeights(...)`, `.WithTokenizer(...)`, `.WithPrefix(...)`. More than one index of a kind → disambiguate with `search.For[Article](db).Field("Title")`.

**Sync mode** (`.WithSync`): the default `orm.SyncAuto` picks per kind — **triggers** for full-text (the text already lives on the base table, so every write, ORM or raw, stays in sync) and **hooks** for vectors (the embedding isn't duplicated on the base table; synced from the ORM write path only). Force one with `orm.SyncTriggers` (a trigger-synced vector stores the embedding as a base column) or `orm.SyncHooks`.

**Low-level building blocks** — drive a sidecar by hand when there is no model:

```go
v, _ := search.NewVector(ctx, db, "doc_vecs", 5, search.Cosine) // OpenVector to attach existing
_ = v.Add(ctx, id, emb)                                         // id int64, emb []float32; re-Add replaces
keys, _ := v.Search(ctx, queryEmb, 3)                           // []int64, nearest first
scored, _ := v.SearchScored(ctx, queryEmb, 3)                   // []search.Scored{Key, Score=distance}

f, _ := search.NewFullText(ctx, db, "doc_fts")
_ = f.Add(ctx, id, title+" "+body)
keys, _ = f.Search(ctx, search.And(search.Term("software"), search.Term("flight")), 5)

fused, _ := search.Hybrid(ctx, v, f, queryEmb, search.Term("software"), 4) // []search.Scored, Score=RRF
docs, _  := search.Fetch[Doc](ctx, db, keys)                    // or FetchScored from []Scored; preserves order
```

Query builders (re-exported, no gosqlite import needed): `Term`, `Phrase`, `Prefix`, `And`, `Or`, `Not`, `Near`, `Column`, `Raw`. Metrics: `orm.Cosine` (the `orm.Vector` default), `orm.L2`, `orm.L1`, `orm.Hamming` (or `search.Cosine`/`L2`/… in the low-level layer). RRF tuning: `search.WithK(60)`, `search.WithWeights(wVec, wText)`.

## REGEXP and other gosqlite SQL functions

gosqlite registers scalar functions globally, so the `ext/` catalog works through LiteORM with no glue. Blank-import the `/auto` package, then either write the operator inline or use the typed `sqlite.WhereRegex` helper:

```go
import _ "gosqlite.org/ext/regexp/auto"

// inline operator:
rows, _ := query.Select[User](db).Where("email REGEXP ?", `^.*@example\.com$`).All(ctx)

// typed helper — returns (fragment, args). For a LEFT-ANCHORED pattern it
// prepends a GLOB prefix so an index on the column range-scans and the RE2
// match runs only on the survivors (an unanchored pattern is a plain REGEXP):
frag, args := sqlite.WhereRegex("title", `^Intro to .* with Go$`)
docs, _ := query.Select[Doc](db).Where(frag, args...).All(ctx)
```

## Errors are normalized (errors.Is across all backends)

```go
errors.Is(err, liteorm.ErrNoRows)          // also sql.ErrNoRows
errors.Is(err, liteorm.ErrUniqueViolation)
errors.Is(err, liteorm.ErrForeignKey)
errors.Is(err, liteorm.ErrNotNull)
errors.Is(err, liteorm.ErrCheck)
```

## Transactions

```go
tx, _ := db.Begin(ctx)        // *BoundTx, also a liteorm.Session
defer tx.Rollback(ctx)        // no-op after Commit
_ = orm.NewRepo[User](tx).Create(ctx, &u)
_ = tx.Commit(ctx)
```

Nested `tx.Begin(ctx)` opens a savepoint; its `Rollback` undoes just that savepoint.

## Pitfalls

- **Sync mode matters for raw writes.** Trigger-synced indexes (default for full-text) stay current on *every* write, including raw `query` writes. Hook-synced indexes (default for an `orm:"-"` vector) only fire through the orm `Repo`; a raw `query` insert won't be indexed.
- **Score direction differs.** `.Vector` / `SearchScored` return distance (smaller is nearer); `.Hybrid` returns an RRF score (larger is better). Don't compare across them.
- **Full-text needs an `int64` primary key** (FTS5 is rowid-keyed); a string-PK model errors at migrate. Vector search supports both `int64` and string PKs.

## Going deeper

LiteORM ships its own skills (load these from the LiteORM repo / `liteorm.org`): `using-liteorm`, `sqlite-search`, `orm-models`, `query-builder`, `migrations`, `encryption`, `porting-from-gorm`, `pitfalls`. API reference: [pkg.go.dev/liteorm.org](https://pkg.go.dev/liteorm.org), [.../dialect/sqlite](https://pkg.go.dev/liteorm.org/dialect/sqlite), and [.../dialect/sqlite/search](https://pkg.go.dev/liteorm.org/dialect/sqlite/search). Runnable example in this repo: [`examples/liteorm/`](../../examples/liteorm/).

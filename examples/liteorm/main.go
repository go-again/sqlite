// Command liteorm is a tour of LiteORM — an ORM with native vector, full-text,
// and hybrid search built on the gosqlite driver — and the first-class
// integration between them: declarative models whose search runs on gosqlite's
// vec / fts / fusion packages, plus the escape hatch back to the raw driver.
//
// LiteORM is a separate module (liteorm.org); this example builds against the
// local .liteorm reference via the replace directives in go.mod (see README.md).
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	gosqlite "gosqlite.org"
	_ "gosqlite.org/ext/regexp/auto" // registers REGEXP globally — usable straight through liteorm

	"liteorm.org/dialect/sqlite"
	"liteorm.org/dialect/sqlite/search"
	"liteorm.org/orm"
	"liteorm.org/query"
)

// Article is a declarative model. It declares a full-text index over its text
// columns and a vector index over a sidecar-only embedding; AutoMigrate creates
// the base table plus the FTS5 and vec0 sidecars and the triggers/hooks that
// keep them current, so ordinary Repo.Create needs no manual index calls.
type Article struct {
	ID        int64
	Title     string
	Body      string
	Embedding []float32 `orm:"-"` // sidecar-only; not a base-table column
}

func (Article) TableName() string { return "articles" }

func (Article) SearchIndexes() []orm.SearchIndex {
	return []orm.SearchIndex{
		orm.FullText("Title", "Body"),
		orm.Vector("Embedding", 5).WithMetric(orm.Cosine),
	}
}

// A toy 5-dimensional "topic" embedding over [animals, space, cooking, tech, music].
// Real systems use an embedding model; the shape is the same — an int64 key and a []float32.
var corpus = []Article{
	{Title: "Foxes of the Northern Woods", Body: "Tracking red foxes across the boreal forest.", Embedding: []float32{1, 0, 0, 0, 0}},
	{Title: "Apollo and the Race to the Moon", Body: "The Apollo program landed astronauts on the moon.", Embedding: []float32{0, 1, 0, 0, 0}},
	{Title: "Sourdough: A Baker's Guide", Body: "Wild yeast, hydration, and a crackling crust.", Embedding: []float32{0, 0, 1, 0, 0}},
	{Title: "Rockets, Engines, and Orbital Mechanics", Body: "How rocket nozzles and orbital insertion work.", Embedding: []float32{0, 0.7, 0, 0.7, 0}},
	{Title: "The Software Behind Spaceflight", Body: "The flight software that keeps a spacecraft on course.", Embedding: []float32{0, 0.6, 0, 0.8, 0}},
}

var spaceQuery = []float32{0, 1, 0, 0, 0}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "liteorm-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	// (1) Open the SQLite backend with the SAME gosqlite.Config the bare driver
	// uses — one typed Config across database/sql, the gorm dialector, and liteorm.
	db, err := sqlite.OpenConfig(gosqlite.Config{
		Path:    filepath.Join(dir, "library.db"),
		Pragmas: gosqlite.RecommendedPragmas(),
	})
	if err != nil {
		return err
	}
	defer db.Close()

	// (2) Declare → migrate → write. AutoMigrate provisions the FTS5 + vec0
	// sidecars; Create writes the row AND keeps both sidecars in sync.
	if err := orm.AutoMigrate[Article](ctx, db); err != nil {
		return err
	}
	repo := orm.NewRepo[Article](db)
	for i := range corpus {
		if err := repo.Create(ctx, &corpus[i]); err != nil {
			return err
		}
	}

	// (3) Search — vector / full-text / hybrid — returns ranked models.
	section("Vector: nearest to the 'space' topic")
	near, err := search.For[Article](db).Vector(ctx, spaceQuery, 3)
	if err != nil {
		return err
	}
	for _, h := range near {
		fmt.Printf("  %.4f  %s\n", h.Score, h.Model.Title)
	}

	section("Full-text: 'software' AND 'flight'")
	hits, err := search.For[Article](db).FullText(ctx, search.And(search.Term("software"), search.Term("flight")), 5)
	if err != nil {
		return err
	}
	for _, h := range hits {
		fmt.Printf("  #%d  %s\n", h.Model.ID, h.Model.Title)
	}

	section("Hybrid (RRF): vector 'space' ⊕ text 'software'")
	fused, err := search.For[Article](db).Hybrid(ctx, spaceQuery, search.Term("software"), 4)
	if err != nil {
		return err
	}
	for _, h := range fused {
		fmt.Printf("  %.4f  %s\n", h.Score, h.Model.Title)
	}
	fmt.Println("  → 'The Software Behind Spaceflight' tops the hybrid: strong in BOTH modalities.")

	// (4) gosqlite's ext/ SQL functions register globally, so REGEXP works
	// straight through liteorm. sqlite.WhereRegex builds the predicate and, for a
	// left-anchored pattern, prepends a GLOB prefix so an index on the column
	// range-scans and the RE2 match runs only on the survivors.
	section("REGEXP via sqlite.WhereRegex through the query builder")
	frag, args := sqlite.WhereRegex("title", `^The Software`)
	matched, err := query.Select[Article](db).Where(frag, args...).All(ctx)
	if err != nil {
		return err
	}
	for _, a := range matched {
		fmt.Printf("  #%d  %s\n", a.ID, a.Title)
	}

	// (5) Escape hatch: drop to the underlying *gosqlite.DB for any driver
	// feature liteorm doesn't surface (sessions, backup, raw typed vec/fts, …).
	if g, ok := sqlite.Conn(db); ok {
		var n int
		if err := g.DB.QueryRowContext(ctx, `SELECT count(*) FROM articles`).Scan(&n); err != nil {
			return err
		}
		section(fmt.Sprintf("Escape hatch: the raw *gosqlite.DB sees %d articles", n))
	}

	fmt.Println()
	return nil
}

func section(s string) { fmt.Printf("\n── %s ──\n", s) }

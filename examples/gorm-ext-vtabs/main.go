// gorm-ext-vtabs: pair gorm with the virtual-table flavors of ext/.
// Each demo registers a vtab on the same gorm-managed DB and drives
// it through plain gorm calls — Exec for DDL, Raw / Scan / Where for
// reads. The blank-import auto packages wire each module onto every
// pooled connection via Driver.ConnectHook.
//
// Covered: ext/array (bind Go slice via sqlite.Pointer), ext/lines
// (text-stream as rows), ext/csv (CSV file as a SQL table),
// ext/statement (parametrized view), ext/closure (graph walk),
// ext/bloom (persistent Bloom filter), ext/spellfix1 (fuzzy lookup).
//
// Note about pooling: vtab-backed schema objects (CREATE VIRTUAL
// TABLE) survive on the conn that issued them. For predictable
// :memory: + multi-conn behavior we pin the pool to one conn and use
// db.Exec / db.Raw — the standard gorm idiom for everything below.
//
// Run with:
//
//	just example gorm-ext-vtabs
package main

import (
	"fmt"
	"log"
	"strings"
	"testing/fstest"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/csv"
	sqlitegorm "github.com/go-again/sqlite/gorm"

	_ "github.com/go-again/sqlite/ext/array/auto"
	_ "github.com/go-again/sqlite/ext/bloom/auto"
	_ "github.com/go-again/sqlite/ext/closure/auto"
	_ "github.com/go-again/sqlite/ext/lines/auto"
	_ "github.com/go-again/sqlite/ext/spellfix1/auto"
	_ "github.com/go-again/sqlite/ext/statement/auto"
)

// User is a simple model used as the bind / join target across demos.
type User struct {
	ID    uint `gorm:"primaryKey"`
	Name  string
	Email string
}

// OrgEdge feeds the closure example. parent_id=NULL marks a root.
type OrgEdge struct {
	ID       int64 `gorm:"primaryKey"`
	Name     string
	ParentID *int64 // nullable so the root has no manager
}

func main() {
	// csv needs an fs.FS bound at the time the module registers, which
	// the auto-package can't do — install a connect-hook that registers
	// csv (with our fsys) on every conn the gorm pool opens. Must
	// happen before sqlitegorm.Open is called.
	csvFS := fstest.MapFS{
		"sales.csv": &fstest.MapFile{Data: []byte("region,qty\nEU,10\nUS,25\nEU,7\n")},
	}
	sqlite.DefaultDriver().RegisterConnectionHook(func(c sqlite.ExecQuerierContext, _ string) error {
		return csv.RegisterFS(c.(*sqlite.Conn), csvFS)
	})

	db, err := gorm.Open(sqlitegorm.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		log.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		log.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1) // vtabs are per-conn; pin the pool

	if err := db.AutoMigrate(&User{}, &OrgEdge{}); err != nil {
		log.Fatal(err)
	}
	if err := db.Create(&[]User{
		{Name: "alice", Email: "alice@example.com"},
		{Name: "bob", Email: "bob@example.com"},
		{Name: "carol", Email: "carol@example.com"},
		{Name: "dave", Email: "dave@example.com"},
	}).Error; err != nil {
		log.Fatal(err)
	}

	demoArray(db)
	demoLines(db)
	demoCSV(db)
	demoStatement(db)
	demoClosure(db)
	demoBloom(db)
	demoSpellfix1(db)
}

// --- ext/array ---
// Bind a Go slice as a SQL table-valued function. The transparent
// sqlite.Pointer(slice) form means gorm's standard `?` placeholder
// works without reaching for db.Raw + custom args.
func demoArray(db *gorm.DB) {
	ids := []int64{1, 3}
	var users []User
	if err := db.Where(
		`id IN (SELECT value FROM array(?))`,
		sqlite.Pointer(ids)).Find(&users).Error; err != nil {
		log.Fatal(err)
	}
	names := make([]string, len(users))
	for i, u := range users {
		names[i] = u.Name
	}
	fmt.Printf("[array]     id IN array(%v) → %s\n", ids, strings.Join(names, ", "))
}

// --- ext/lines ---
// Vtab with the text body inlined in the DDL via data=. Yields one row
// per line — handy for SELECTing matches out of a log-shaped string.
func demoLines(db *gorm.DB) {
	if err := db.Exec(
		`CREATE VIRTUAL TABLE temp.log USING lines(data='INFO  boot
ERROR migrate
WARN  cache cold
ERROR write')`).Error; err != nil {
		log.Fatal(err)
	}
	defer db.Exec(`DROP TABLE temp.log`)
	var hits []struct {
		Lineno int
		Line   string
	}
	if err := db.Raw(
		`SELECT lineno, line FROM temp.log WHERE line LIKE 'ERROR%' ORDER BY lineno`).
		Scan(&hits).Error; err != nil {
		log.Fatal(err)
	}
	fmt.Printf("[lines]     %d ERROR lines out of 4: line %d=%q\n",
		len(hits), hits[0].Lineno, hits[0].Line)
}

// --- ext/csv ---
// csv was bound to a sandboxed fstest.MapFS via the connect-hook
// installed in main. Plain gorm Exec / Scan calls drive the vtab.
func demoCSV(db *gorm.DB) {
	if err := db.Exec(`CREATE VIRTUAL TABLE sales USING csv(
		filename='sales.csv', header=on,
		schema='CREATE TABLE x(region TEXT, qty INTEGER)')`).Error; err != nil {
		log.Fatal(err)
	}
	defer db.Exec(`DROP TABLE sales`)
	var totals []struct {
		Region string
		Qty    int64
	}
	if err := db.Raw(
		`SELECT region, SUM(qty) AS qty FROM sales GROUP BY region ORDER BY region`).
		Scan(&totals).Error; err != nil {
		log.Fatal(err)
	}
	fmt.Printf("[csv]       sandboxed sales.csv → %v\n", totals)
}

// --- ext/statement ---
// Parametrized view. The bound parameter shows up as a HIDDEN column
// "?1"; we constrain it in WHERE to drive the underlying SELECT.
func demoStatement(db *gorm.DB) {
	if err := db.Exec(
		`CREATE VIRTUAL TABLE recent USING statement('SELECT id, name FROM users WHERE id >= ?')`).
		Error; err != nil {
		log.Fatal(err)
	}
	defer db.Exec(`DROP TABLE recent`)
	var users []User
	if err := db.Raw(`SELECT id, name FROM recent WHERE "?1" = ?`, 2).
		Scan(&users).Error; err != nil {
		log.Fatal(err)
	}
	names := make([]string, len(users))
	for i, u := range users {
		names[i] = u.Name
	}
	fmt.Printf("[statement] id>=2 → %s\n", strings.Join(names, ", "))
}

// --- ext/closure ---
// transitive_closure builds an org chart out of OrgEdge rows and walks
// it from a root, with optional depth bounds.
func demoClosure(db *gorm.DB) {
	none := func(p *int64) *int64 { return p }
	mk := func(id int64) *int64 { return &id }
	if err := db.Create(&[]OrgEdge{
		{ID: 1, Name: "ceo", ParentID: none(nil)},
		{ID: 2, Name: "vp_eng", ParentID: mk(1)},
		{ID: 3, Name: "vp_sales", ParentID: mk(1)},
		{ID: 4, Name: "manager_a", ParentID: mk(2)},
		{ID: 5, Name: "manager_b", ParentID: mk(2)},
		{ID: 6, Name: "ic_x", ParentID: mk(4)},
		{ID: 7, Name: "ic_y", ParentID: mk(4)},
	}).Error; err != nil {
		log.Fatal(err)
	}
	if err := db.Exec(
		`CREATE VIRTUAL TABLE temp.tc USING transitive_closure(
			tablename=org_edges, idcolumn=id, parentcolumn=parent_id)`).Error; err != nil {
		log.Fatal(err)
	}
	defer db.Exec(`DROP TABLE temp.tc`)

	var rows []struct {
		ID    int64
		Depth int
	}
	if err := db.Raw(
		`SELECT id, depth FROM temp.tc WHERE root = ? AND depth <= ? ORDER BY id`,
		2, 2).Scan(&rows).Error; err != nil {
		log.Fatal(err)
	}
	fmt.Printf("[closure]   descendants of vp_eng depth<=2: %d nodes (incl. self)\n",
		len(rows))
}

// --- ext/bloom ---
// Persistent Bloom filter. Insert via the HIDDEN word column, test via
// SELECT present FROM filter WHERE word=?. False positives are
// expected; false negatives never occur.
func demoBloom(db *gorm.DB) {
	if err := db.Exec(
		`CREATE VIRTUAL TABLE recent_logins USING bloom(size=1000, p=0.01)`).
		Error; err != nil {
		log.Fatal(err)
	}
	defer db.Exec(`DROP TABLE recent_logins`)
	for _, u := range []string{"alice@example.com", "bob@example.com"} {
		if err := db.Exec(`INSERT INTO recent_logins(word) VALUES (?)`, u).Error; err != nil {
			log.Fatal(err)
		}
	}
	check := func(addr string) bool {
		var present int
		_ = db.Raw(`SELECT present FROM recent_logins WHERE word = ?`, addr).
			Scan(&present).Error
		return present == 1
	}
	fmt.Printf("[bloom]     alice@…=%v, mallory@…=%v\n",
		check("alice@example.com"), check("mallory@example.com"))
}

// --- ext/spellfix1 ---
// Fuzzy text vtab. Insert a vocabulary, then MATCH a misspelling to
// retrieve the nearest neighbours with edit distance + Soundex
// prefilter.
func demoSpellfix1(db *gorm.DB) {
	if err := db.Exec(
		`CREATE VIRTUAL TABLE dict USING spellfix1`).Error; err != nil {
		log.Fatal(err)
	}
	defer db.Exec(`DROP TABLE dict`)
	for _, w := range []string{"sqlite", "selenium", "scylla", "spanner"} {
		if err := db.Exec(`INSERT INTO dict(word) VALUES (?)`, w).Error; err != nil {
			log.Fatal(err)
		}
	}
	var hits []struct {
		Word     string
		Distance int
	}
	if err := db.Raw(
		`SELECT word, distance FROM dict WHERE word MATCH ? LIMIT 3`, "sqlie").
		Scan(&hits).Error; err != nil {
		log.Fatal(err)
	}
	suggestions := make([]string, len(hits))
	for i, h := range hits {
		suggestions[i] = fmt.Sprintf("%s(d=%d)", h.Word, h.Distance)
	}
	fmt.Printf("[spellfix1] 'sqlie' → %s\n", strings.Join(suggestions, ", "))
}

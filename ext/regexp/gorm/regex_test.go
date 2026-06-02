package regexpgorm_test

import (
	"path/filepath"
	"strings"
	"testing"

	"gorm.io/gorm/clause"

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/regexp"
	regexpgorm "github.com/go-again/sqlite/ext/regexp/gorm"
	sqlitegorm "github.com/go-again/sqlite/gorm"
)

type doc struct {
	ID    uint   `gorm:"primaryKey"`
	Title string `gorm:"not null"`
}

func setup(t *testing.T) *sqlitegorm.DB {
	t.Helper()
	// Install a ConnectHook so REGEXP is wired on every new gorm conn,
	// avoiding the conn-starvation deadlock that comes from pinning a
	// *sql.Conn for setup while gorm tries to grab another.
	d := sqlite.DefaultDriver()
	prev := d.ConnectHook
	d.ConnectHook = func(c *sqlite.Conn) error {
		if prev != nil {
			if err := prev(c); err != nil {
				return err
			}
		}
		return regexp.Register(c)
	}
	t.Cleanup(func() { d.ConnectHook = prev })

	dir := t.TempDir()
	db, err := sqlitegorm.OpenConfig(sqlite.Config{
		Path: filepath.Join(dir, "regex.db"),
	})
	if err != nil {
		t.Fatalf("OpenConfig: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := db.AutoMigrate(&doc{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	for _, title := range []string{
		"Intro to Go",
		"Intro to Go for Java devs",
		"Intro to Rust",
		"Advanced Go patterns",
		"a totally unrelated post",
	} {
		if err := db.Create(&doc{Title: title}).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestWhereRegex_Anchored(t *testing.T) {
	db := setup(t)
	var got []doc
	if err := db.Where(regexpgorm.WhereRegex("title", `^Intro to .* for .+`)).Find(&got).Error; err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Title != "Intro to Go for Java devs" {
		t.Errorf("got %v, want one match: 'Intro to Go for Java devs'", got)
	}
}

func TestWhereRegex_Unanchored(t *testing.T) {
	// Pattern starts with `.*` — no usable GLOB prefix. Falls back to
	// plain REGEXP; should still match correctly.
	db := setup(t)
	var got []doc
	if err := db.Where(regexpgorm.WhereRegex("title", `Java devs$`)).Find(&got).Error; err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Title != "Intro to Go for Java devs" {
		t.Errorf("got %v, want unanchored match", got)
	}
}

func TestWhereRegex_EmitsGlobAndRegexp(t *testing.T) {
	// Confirm the generated clause carries both GLOB and REGEXP fragments
	// for an anchored pattern.
	expr := regexpgorm.WhereRegex("title", `^Intro to Go`)
	e, ok := expr.(clause.Expr)
	if !ok {
		t.Fatalf("expr type = %T, want clause.Expr", expr)
	}
	if !strings.Contains(e.SQL, "GLOB") || !strings.Contains(e.SQL, "REGEXP") {
		t.Errorf("SQL=%q, want both GLOB and REGEXP fragments", e.SQL)
	}
	if len(e.Vars) != 2 {
		t.Errorf("Vars=%v, want [prefix, pattern]", e.Vars)
	}
	prefix, ok := e.Vars[0].(string)
	if !ok || !strings.HasPrefix(prefix, "Intro to Go") {
		t.Errorf("prefix=%q, want it to start with 'Intro to Go'", prefix)
	}
}

func TestWhereRegex_EmitsRegexpOnlyForUnanchored(t *testing.T) {
	expr := regexpgorm.WhereRegex("title", `something`)
	e, ok := expr.(clause.Expr)
	if !ok {
		t.Fatalf("expr type = %T, want clause.Expr", expr)
	}
	if strings.Contains(e.SQL, "GLOB") {
		t.Errorf("SQL=%q, should not contain GLOB for unanchored pattern", e.SQL)
	}
	if !strings.Contains(e.SQL, "REGEXP") {
		t.Errorf("SQL=%q, should contain REGEXP fallback", e.SQL)
	}
}

func TestWhereRegex_ExplainHitsIndex(t *testing.T) {
	// Pin that the SQLite planner picks up the GLOB clause as a usable
	// index hint. Create a regular (non-rowid) index on title so the
	// GLOB range can use it.
	db := setup(t)
	if err := db.Exec(`CREATE INDEX idx_title ON docs(title)`).Error; err != nil {
		t.Fatal(err)
	}
	type explain struct {
		Detail string
	}
	var rows []explain
	if err := db.Raw(`EXPLAIN QUERY PLAN
		SELECT * FROM docs WHERE title GLOB 'Intro to *' AND title REGEXP '^Intro to .*'`).
		Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	joined := ""
	for _, r := range rows {
		joined += r.Detail + "\n"
	}
	if !strings.Contains(joined, "idx_title") {
		t.Errorf("EXPLAIN QUERY PLAN did not mention idx_title:\n%s", joined)
	}
}

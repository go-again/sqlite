package xormcompat

// Compatibility gate: drives xorm.io/xorm through its public surface with
// gosqlite.org as the registered SQLite driver. Proves "gosqlite.org is a
// drop-in xorm SQLite driver" with no xorm-specific code — xorm maps the
// "sqlite3" / "sqlite" / "libsql" names to its driver-agnostic SQLite
// dialect, and gosqlite.org registers under "sqlite" and "sqlite3".

import (
	"testing"
	"time"

	_ "gosqlite.org" // registers the "sqlite" and "sqlite3" drivers
	"xorm.io/xorm"
)

type xormUser struct {
	Id      int64  `xorm:"pk autoincr"`
	Name    string `xorm:"varchar(64) notnull unique"`
	Age     int
	Active  bool
	Balance float64
	Blob    []byte
	Created time.Time `xorm:"created"`
}

// newEngine opens an isolated shared-cache in-memory engine per test, with
// foreign_keys forced on through our DSN flag (verified in TestXorm_DSNFlags).
func newEngine(t *testing.T) *xorm.Engine {
	t.Helper()
	eng, err := xorm.NewEngine("sqlite3", "file:"+t.Name()+"?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	if err := eng.Sync2(new(xormUser)); err != nil {
		t.Fatalf("Sync2: %v", err)
	}
	return eng
}

func TestXorm_CRUD(t *testing.T) {
	eng := newEngine(t)

	if _, err := eng.Insert(&xormUser{Name: "alice", Age: 30, Active: true, Balance: 12.5}); err != nil {
		t.Fatalf("Insert alice: %v", err)
	}
	if _, err := eng.Insert(&xormUser{Name: "bob", Age: 25}); err != nil {
		t.Fatalf("Insert bob: %v", err)
	}

	var got xormUser
	has, err := eng.Where("name = ?", "alice").Get(&got)
	if err != nil || !has {
		t.Fatalf("Get alice: has=%v err=%v", has, err)
	}
	if got.Age != 30 || !got.Active || got.Balance != 12.5 {
		t.Fatalf("Get alice roundtrip mismatch: %+v", got)
	}

	got.Age = 31
	if _, err := eng.ID(got.Id).Cols("age").Update(&got); err != nil {
		t.Fatalf("Update: %v", err)
	}
	var after xormUser
	if _, err := eng.ID(got.Id).Get(&after); err != nil {
		t.Fatalf("re-Get: %v", err)
	}
	if after.Age != 31 {
		t.Fatalf("Update not persisted: age=%d want 31", after.Age)
	}

	var all []xormUser
	if err := eng.OrderBy("age").Find(&all); err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(all) != 2 || all[0].Name != "bob" {
		t.Fatalf("Find order/len wrong: %+v", all)
	}

	n, err := eng.Count(new(xormUser))
	if err != nil || n != 2 {
		t.Fatalf("Count: n=%d err=%v", n, err)
	}

	if _, err := eng.ID(got.Id).Delete(new(xormUser)); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if n, _ := eng.Count(new(xormUser)); n != 1 {
		t.Fatalf("Count after delete: %d want 1", n)
	}
}

func TestXorm_Types(t *testing.T) {
	eng := newEngine(t)

	in := &xormUser{Name: "typed", Age: -7, Active: true, Balance: 3.14159, Blob: []byte{0, 1, 2, 255}}
	if _, err := eng.Insert(in); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	var out xormUser
	if _, err := eng.ID(in.Id).Get(&out); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if out.Age != -7 || !out.Active || out.Balance != 3.14159 || string(out.Blob) != string(in.Blob) {
		t.Fatalf("type roundtrip mismatch: %+v", out)
	}
	// `created` time column populated and scannable back as time.Time.
	if out.Created.IsZero() {
		t.Fatalf("created time not populated")
	}
}

func TestXorm_Transaction(t *testing.T) {
	eng := newEngine(t)

	// Rolled-back session leaves no row.
	s1 := eng.NewSession()
	if err := s1.Begin(); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := s1.Insert(&xormUser{Name: "ghost"}); err != nil {
		t.Fatalf("tx insert: %v", err)
	}
	if err := s1.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	s1.Close()

	// Committed session persists.
	s2 := eng.NewSession()
	if err := s2.Begin(); err != nil {
		t.Fatalf("Begin2: %v", err)
	}
	if _, err := s2.Insert(&xormUser{Name: "real"}); err != nil {
		t.Fatalf("tx insert2: %v", err)
	}
	if err := s2.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	s2.Close()

	if n, _ := eng.Count(new(xormUser)); n != 1 {
		t.Fatalf("post-tx count=%d want 1 (rollback must drop ghost)", n)
	}
}

func TestXorm_Introspection(t *testing.T) {
	eng := newEngine(t)

	tables, err := eng.DBMetas()
	if err != nil {
		t.Fatalf("DBMetas: %v", err)
	}
	found := false
	for _, tbl := range tables {
		if tbl.Name == "xorm_user" {
			found = true
			if len(tbl.Columns()) != 7 {
				t.Fatalf("xorm_user columns=%d want 7", len(tbl.Columns()))
			}
		}
	}
	if !found {
		t.Fatalf("DBMetas did not report the xorm_user table; got %d tables", len(tables))
	}
}

func TestXorm_DSNFlags(t *testing.T) {
	eng := newEngine(t)

	// Our `_pragma=foreign_keys(1)` DSN flag must reach the driver.
	rows, err := eng.QueryString("PRAGMA foreign_keys")
	if err != nil {
		t.Fatalf("PRAGMA query: %v", err)
	}
	if len(rows) != 1 || rows[0]["foreign_keys"] != "1" {
		t.Fatalf("foreign_keys pragma not applied via DSN: %v", rows)
	}

	// And it's enforced: a FK violation is rejected.
	if _, err := eng.Exec("CREATE TABLE parent(id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if _, err := eng.Exec("CREATE TABLE child(id INTEGER PRIMARY KEY, pid INTEGER REFERENCES parent(id))"); err != nil {
		t.Fatalf("create child: %v", err)
	}
	if _, err := eng.Exec("INSERT INTO child(id, pid) VALUES (1, 999)"); err == nil {
		t.Fatalf("FK violation accepted; foreign_keys not enforced")
	}
}

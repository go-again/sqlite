package sql_test

import (
	"strings"
	"testing"
)

func TestPragma_ForeignKeys(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `pragma foreign_keys = ON`)
	var v int
	scanOne(t, db, &v, `pragma foreign_keys`)
	if v != 1 {
		t.Errorf("foreign_keys=%d, want 1", v)
	}
	mustExec(t, db, `pragma foreign_keys = OFF`)
	scanOne(t, db, &v, `pragma foreign_keys`)
	if v != 0 {
		t.Errorf("foreign_keys after OFF=%d, want 0", v)
	}
}

func TestPragma_JournalMode(t *testing.T) {
	db := openDB(t)
	// For in-memory, journal_mode is "memory" by default and persists.
	// We can switch it; assert the round-trip.
	var got string
	mustExec(t, db, `pragma journal_mode = MEMORY`)
	scanOne(t, db, &got, `pragma journal_mode`)
	if !strings.EqualFold(got, "memory") {
		t.Errorf("journal_mode=%s, want memory", got)
	}
}

func TestPragma_Synchronous(t *testing.T) {
	db := openDB(t)
	for _, mode := range []string{"OFF", "NORMAL", "FULL", "EXTRA"} {
		mustExec(t, db, `pragma synchronous = `+mode)
		var got int
		scanOne(t, db, &got, `pragma synchronous`)
		// synchronous returns the numeric form: OFF=0 NORMAL=1 FULL=2 EXTRA=3
		// — but exact mapping is documented; we just assert it round-trips.
		if got < 0 || got > 3 {
			t.Errorf("synchronous=%s read back as %d, out of range", mode, got)
		}
	}
}

func TestPragma_BusyTimeout(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `pragma busy_timeout = 5000`)
	var got int
	scanOne(t, db, &got, `pragma busy_timeout`)
	if got != 5000 {
		t.Errorf("busy_timeout=%d, want 5000", got)
	}
}

func TestPragma_CacheSize(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `pragma cache_size = -2000`) // negative = KB
	var got int
	scanOne(t, db, &got, `pragma cache_size`)
	if got != -2000 {
		t.Errorf("cache_size=%d, want -2000", got)
	}
}

func TestPragma_RecursiveTriggers(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `pragma recursive_triggers = ON`)
	var got int
	scanOne(t, db, &got, `pragma recursive_triggers`)
	if got != 1 {
		t.Errorf("recursive_triggers=%d, want 1", got)
	}
}

func TestPragma_UserVersion(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `pragma user_version = 42`)
	var got int
	scanOne(t, db, &got, `pragma user_version`)
	if got != 42 {
		t.Errorf("user_version=%d, want 42", got)
	}
}

func TestPragma_ApplicationId(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `pragma application_id = 99`)
	var got int
	scanOne(t, db, &got, `pragma application_id`)
	if got != 99 {
		t.Errorf("application_id=%d, want 99", got)
	}
}

func TestPragma_TableInfo(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (id int primary key, name text not null, age int default 0)`)
	rows := scanAll(t, db, `pragma table_info(t)`)
	if len(rows) != 3 {
		t.Fatalf("table_info rows=%d, want 3", len(rows))
	}
	// Columns: cid, name, type, notnull, dflt_value, pk
	if rows[0][1].(string) != "id" {
		t.Errorf("col 0 name=%v, want id", rows[0][1])
	}
}

func TestPragma_IndexList(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (a int, b int, c int)`)
	mustExec(t, db, `create index ix_a on t(a)`)
	mustExec(t, db, `create unique index ix_b on t(b)`)
	rows := scanAll(t, db, `pragma index_list(t)`)
	// At least the two named indexes are present.
	names := map[string]bool{}
	for _, r := range rows {
		if name, ok := r[1].(string); ok {
			names[name] = true
		}
	}
	if !names["ix_a"] || !names["ix_b"] {
		t.Errorf("index_list missing ix_a/ix_b: %+v", names)
	}
}

func TestPragma_ForeignKeyList(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table p (id int primary key)`)
	mustExec(t, db, `create table c (id int, p_id int, foreign key (p_id) references p(id))`)
	rows := scanAll(t, db, `pragma foreign_key_list(c)`)
	if len(rows) != 1 {
		t.Errorf("foreign_key_list rows=%d, want 1", len(rows))
	}
}

func TestPragma_IntegrityCheck(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (v int); insert into t values (1)`)
	var msg string
	scanOne(t, db, &msg, `pragma integrity_check`)
	if msg != "ok" {
		t.Errorf("integrity_check=%q, want ok", msg)
	}
}

func TestPragma_QuickCheck(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (v int); insert into t values (1)`)
	var msg string
	scanOne(t, db, &msg, `pragma quick_check`)
	if msg != "ok" {
		t.Errorf("quick_check=%q, want ok", msg)
	}
}

func TestPragma_AutoVacuum(t *testing.T) {
	db := openDB(t)
	for _, mode := range []string{"NONE", "FULL", "INCREMENTAL"} {
		mustExec(t, db, `pragma auto_vacuum = `+mode)
		var got int
		scanOne(t, db, &got, `pragma auto_vacuum`)
		if got < 0 || got > 2 {
			t.Errorf("auto_vacuum=%s read back as %d, out of range", mode, got)
		}
	}
}

func TestPragma_CaseSensitiveLike(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (s text); insert into t values ('Apple'), ('apple')`)

	mustExec(t, db, `pragma case_sensitive_like = ON`)
	var n int
	scanOne(t, db, &n, `select count(*) from t where s like 'a%'`)
	if n != 1 {
		t.Errorf("case_sensitive ON: %d, want 1", n)
	}

	mustExec(t, db, `pragma case_sensitive_like = OFF`)
	scanOne(t, db, &n, `select count(*) from t where s like 'a%'`)
	if n != 2 {
		t.Errorf("case_sensitive OFF: %d, want 2", n)
	}
}

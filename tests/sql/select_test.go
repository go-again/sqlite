package sql_test

import (
	"database/sql"
	"reflect"
	"testing"
)

// seedNumbers populates a small `n` table with rowid + value pairs.
// Reused by the SELECT tests so each one stays focused on the clause
// under test rather than fixture boilerplate.
func seedNumbers(t *testing.T, db *sql.DB) {
	t.Helper()
	mustExec(t, db, `create table n (id integer primary key, v integer, label text)`)
	mustExec(t, db, `insert into n(id, v, label) values
		(1, 10, 'a'),
		(2, 20, 'b'),
		(3, 20, 'a'),
		(4, 30, 'c'),
		(5, NULL, 'a')`)
}

func TestSelect_WhereComparisons(t *testing.T) {
	db := openDB(t)
	seedNumbers(t, db)
	cases := []struct {
		name string
		sql  string
		want []int64
	}{
		{"eq", `select id from n where v = 20`, []int64{2, 3}},
		{"ne", `select id from n where v != 20`, []int64{1, 4}},
		{"lt", `select id from n where v < 20`, []int64{1}},
		{"gt", `select id from n where v > 20`, []int64{4}},
		{"between", `select id from n where v between 15 and 25`, []int64{2, 3}},
		{"in", `select id from n where v in (10, 30)`, []int64{1, 4}},
		{"notin", `select id from n where v not in (20)`, []int64{1, 4}},
		{"isnull", `select id from n where v is null`, []int64{5}},
		{"isnotnull", `select id from n where v is not null`, []int64{1, 2, 3, 4}},
		{"like", `select id from n where label like 'a%'`, []int64{1, 3, 5}},
		{"glob", `select id from n where label glob 'a*'`, []int64{1, 3, 5}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rows := scanAll(t, db, c.sql+` order by id`)
			got := make([]int64, len(rows))
			for i, r := range rows {
				got[i] = r[0].(int64)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestSelect_OrderBy(t *testing.T) {
	db := openDB(t)
	seedNumbers(t, db)

	// Single column, ASC default + explicit DESC.
	asc := scanAll(t, db, `select id from n where v is not null order by v`)
	if asc[0][0].(int64) != 1 || asc[len(asc)-1][0].(int64) != 4 {
		t.Errorf("ASC: head=%v tail=%v", asc[0], asc[len(asc)-1])
	}
	desc := scanAll(t, db, `select id from n where v is not null order by v desc`)
	if desc[0][0].(int64) != 4 || desc[len(desc)-1][0].(int64) != 1 {
		t.Errorf("DESC: head=%v tail=%v", desc[0], desc[len(desc)-1])
	}

	// Multi-column: by v ASC, then label DESC tie-breaks rowid 2 (b) before 3 (a).
	multi := scanAll(t, db, `select id from n where v = 20 order by v asc, label desc`)
	if len(multi) != 2 || multi[0][0].(int64) != 2 || multi[1][0].(int64) != 3 {
		t.Errorf("multi-col: %+v, want [2, 3]", multi)
	}
}

func TestSelect_GroupByHaving(t *testing.T) {
	db := openDB(t)
	seedNumbers(t, db)

	// label → count(*). 'a' appears 3 times, 'b' once, 'c' once.
	rows := scanAll(t, db, `select label, count(*) from n group by label order by label`)
	want := [][]any{
		{"a", int64(3)},
		{"b", int64(1)},
		{"c", int64(1)},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("group by: %+v, want %+v", rows, want)
	}

	// HAVING filters post-aggregation: only 'a' has > 1.
	having := scanAll(t, db, `select label from n group by label having count(*) > 1`)
	if len(having) != 1 || having[0][0].(string) != "a" {
		t.Errorf("having: %+v, want [a]", having)
	}
}

func TestSelect_Distinct(t *testing.T) {
	db := openDB(t)
	seedNumbers(t, db)
	rows := scanAll(t, db, `select distinct v from n where v is not null order by v`)
	want := [][]any{{int64(10)}, {int64(20)}, {int64(30)}}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("distinct: %+v, want %+v", rows, want)
	}
}

func TestSelect_SetOperators(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table a (v int); insert into a values (1), (2), (3)`)
	mustExec(t, db, `create table b (v int); insert into b values (2), (3), (4)`)

	cases := []struct {
		name string
		sql  string
		want []int64
	}{
		{"union", `select v from a union select v from b order by v`, []int64{1, 2, 3, 4}},
		{"unionall", `select v from a union all select v from b order by v`, []int64{1, 2, 2, 3, 3, 4}},
		{"intersect", `select v from a intersect select v from b order by v`, []int64{2, 3}},
		{"except", `select v from a except select v from b order by v`, []int64{1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rows := scanAll(t, db, c.sql)
			got := make([]int64, len(rows))
			for i, r := range rows {
				got[i] = r[0].(int64)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestSelect_LimitOffset(t *testing.T) {
	db := openDB(t)
	seedNumbers(t, db)

	// LIMIT N
	rows := scanAll(t, db, `select id from n order by id limit 2`)
	if len(rows) != 2 || rows[0][0].(int64) != 1 || rows[1][0].(int64) != 2 {
		t.Errorf("LIMIT 2: %+v, want [1 2]", rows)
	}

	// LIMIT N OFFSET M
	rows = scanAll(t, db, `select id from n order by id limit 2 offset 2`)
	if len(rows) != 2 || rows[0][0].(int64) != 3 || rows[1][0].(int64) != 4 {
		t.Errorf("LIMIT 2 OFFSET 2: %+v, want [3 4]", rows)
	}

	// LIMIT M, N — alternate SQLite syntax meaning OFFSET M LIMIT N.
	rows = scanAll(t, db, `select id from n order by id limit 1, 2`)
	if len(rows) != 2 || rows[0][0].(int64) != 2 || rows[1][0].(int64) != 3 {
		t.Errorf("LIMIT 1, 2: %+v, want [2 3]", rows)
	}
}

func TestSelect_ColumnAliases(t *testing.T) {
	db := openDB(t)
	seedNumbers(t, db)
	rows, err := db.Query(`select v as value, label as name from n where id = 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	if cols[0] != "value" || cols[1] != "name" {
		t.Errorf("aliases: %v, want [value name]", cols)
	}
}

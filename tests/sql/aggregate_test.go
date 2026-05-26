package sql_test

import (
	"database/sql"
	"math"
	"testing"
)

func seedSales(t *testing.T, db *sql.DB) {
	t.Helper()
	mustExec(t, db, `create table sales (id int, region text, amount int);
		insert into sales values
			(1, 'east', 100),
			(2, 'east', 200),
			(3, 'west', 150),
			(4, 'west', NULL),
			(5, NULL,   50)`)
}

func TestAgg_CountVariants(t *testing.T) {
	db := openDB(t)
	seedSales(t, db)

	var star, col, distinct int64
	scanOne(t, db, &star, `select count(*) from sales`)
	scanOne(t, db, &col, `select count(amount) from sales`)
	scanOne(t, db, &distinct, `select count(distinct region) from sales`)
	// count(*) = 5, count(amount) excludes NULL → 4, count(distinct region) = 2 (east, west; NULL excluded)
	if star != 5 || col != 4 || distinct != 2 {
		t.Errorf("count(*)=%d count(amount)=%d count(distinct region)=%d, want 5, 4, 2", star, col, distinct)
	}
}

func TestAgg_SumAndTotal(t *testing.T) {
	db := openDB(t)
	seedSales(t, db)

	var sumVal sql.NullInt64
	scanOne(t, db, &sumVal, `select sum(amount) from sales`)
	if !sumVal.Valid || sumVal.Int64 != 500 {
		t.Errorf("sum=%+v, want 500", sumVal)
	}

	// On an empty rowset, sum() returns NULL but total() returns 0.
	mustExec(t, db, `create table empty (v int)`)
	var sumEmpty sql.NullInt64
	scanOne(t, db, &sumEmpty, `select sum(v) from empty`)
	if sumEmpty.Valid {
		t.Errorf("sum of empty=%+v, want NULL", sumEmpty)
	}
	var totalEmpty float64
	scanOne(t, db, &totalEmpty, `select total(v) from empty`)
	if totalEmpty != 0 {
		t.Errorf("total of empty=%f, want 0", totalEmpty)
	}
}

func TestAgg_Avg(t *testing.T) {
	db := openDB(t)
	seedSales(t, db)
	var avg float64
	scanOne(t, db, &avg, `select avg(amount) from sales`)
	// (100+200+150+50)/4 = 125 (NULL excluded)
	if math.Abs(avg-125) > 1e-9 {
		t.Errorf("avg=%f, want 125", avg)
	}
}

func TestAgg_MinMax(t *testing.T) {
	db := openDB(t)
	seedSales(t, db)
	var min, max int64
	scanOne(t, db, &min, `select min(amount) from sales`)
	scanOne(t, db, &max, `select max(amount) from sales`)
	if min != 50 || max != 200 {
		t.Errorf("min=%d max=%d, want 50, 200", min, max)
	}
}

func TestAgg_GroupConcat(t *testing.T) {
	db := openDB(t)
	seedSales(t, db)
	// Default separator is comma.
	var gc string
	scanOne(t, db, &gc, `select group_concat(region) from sales where region is not null order by id`)
	// Order is implementation-defined, but the set of substrings should be present.
	if len(gc) < 5 {
		t.Errorf("group_concat too short: %q", gc)
	}
	// Custom separator.
	var withSep string
	scanOne(t, db, &withSep, `select group_concat(region, ' | ') from sales where region is not null`)
	if withSep == "" {
		t.Errorf("group_concat with sep empty")
	}
}

// TestAgg_FilterClause exercises the SQL:2003 FILTER (WHERE ...) clause
// on aggregates — supported in SQLite 3.30+.
func TestAgg_FilterClause(t *testing.T) {
	db := openDB(t)
	seedSales(t, db)
	var sumEast, sumWest int64
	scanOne(t, db, &sumEast, `select sum(amount) filter (where region = 'east') from sales`)
	scanOne(t, db, &sumWest, `select sum(amount) filter (where region = 'west') from sales`)
	if sumEast != 300 || sumWest != 150 {
		t.Errorf("FILTER: east=%d west=%d, want 300, 150", sumEast, sumWest)
	}
}

func TestAgg_DistinctInAgg(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (v int); insert into t values (1), (1), (2), (3), (3)`)

	var sumAll, sumDistinct int64
	scanOne(t, db, &sumAll, `select sum(v) from t`)
	scanOne(t, db, &sumDistinct, `select sum(distinct v) from t`)
	if sumAll != 10 || sumDistinct != 6 {
		t.Errorf("sum=%d sum(distinct)=%d, want 10, 6", sumAll, sumDistinct)
	}
}

// TestAgg_GroupByMulti exercises grouping by two columns.
func TestAgg_GroupByMulti(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table sale (region text, product text, qty int);
		insert into sale values
			('east', 'a', 10),
			('east', 'a', 20),
			('east', 'b', 5),
			('west', 'a', 30)`)
	rows := scanAll(t, db, `
		select region, product, sum(qty) from sale
		group by region, product
		order by region, product`)
	want := [][]any{
		{"east", "a", int64(30)},
		{"east", "b", int64(5)},
		{"west", "a", int64(30)},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows=%d, want %d", len(rows), len(want))
	}
	for i, r := range rows {
		if r[0] != want[i][0] || r[1] != want[i][1] || r[2] != want[i][2] {
			t.Errorf("row %d=%+v, want %+v", i, r, want[i])
		}
	}
}

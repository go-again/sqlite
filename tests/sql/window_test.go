package sql_test

import (
	"database/sql"
	"reflect"
	"testing"
)

func seedScores(t *testing.T, db *sql.DB) {
	t.Helper()
	mustExec(t, db, `create table scores (id int, team text, score int);
		insert into scores values
			(1, 'A', 30),
			(2, 'A', 10),
			(3, 'A', 20),
			(4, 'B', 25),
			(5, 'B', 25),
			(6, 'B', 5)`)
}

// TestWindow_RowNumber checks ROW_NUMBER over PARTITION BY team ordered by score desc.
func TestWindow_RowNumber(t *testing.T) {
	db := openDB(t)
	seedScores(t, db)
	rows := scanAll(t, db, `
		select id, row_number() over (partition by team order by score desc) as rn
		from scores order by id`)
	// A: id 1 score 30 (rn 1), id 3 score 20 (rn 2), id 2 score 10 (rn 3)
	// B: ids 4&5 score 25 (rn 1&2 deterministically by rowid), id 6 score 5 (rn 3)
	want := []int64{1, 3, 2, 1, 2, 3}
	for i, r := range rows {
		if r[1].(int64) != want[i] {
			t.Errorf("row %d (id=%v) rn=%v, want %d", i, r[0], r[1], want[i])
		}
	}
}

func TestWindow_RankAndDenseRank(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (id int, v int);
		insert into t values (1, 10), (2, 20), (3, 20), (4, 30)`)

	rows := scanAll(t, db, `
		select id,
		       rank() over (order by v) as r,
		       dense_rank() over (order by v) as dr
		from t order by id`)
	want := [][2]int64{{1, 1}, {2, 2}, {2, 2}, {4, 3}}
	for i, r := range rows {
		if r[1].(int64) != want[i][0] || r[2].(int64) != want[i][1] {
			t.Errorf("id=%v rank=%v dense=%v, want %d, %d", r[0], r[1], r[2], want[i][0], want[i][1])
		}
	}
}

func TestWindow_Ntile(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (id int);
		insert into t values (1), (2), (3), (4), (5), (6), (7), (8)`)

	rows := scanAll(t, db, `
		select id, ntile(4) over (order by id) as bucket from t order by id`)
	want := []int64{1, 1, 2, 2, 3, 3, 4, 4}
	for i, r := range rows {
		if r[1].(int64) != want[i] {
			t.Errorf("id=%v bucket=%v, want %d", r[0], r[1], want[i])
		}
	}
}

func TestWindow_LagLead(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (id int, v int);
		insert into t values (1, 10), (2, 20), (3, 30)`)

	rows := scanAll(t, db, `
		select id,
		       lag(v, 1, -1) over (order by id) as lag1,
		       lead(v, 1, -1) over (order by id) as lead1
		from t order by id`)
	wantLag := []int64{-1, 10, 20}
	wantLead := []int64{20, 30, -1}
	for i, r := range rows {
		if r[1].(int64) != wantLag[i] {
			t.Errorf("id=%v lag=%v, want %d", r[0], r[1], wantLag[i])
		}
		if r[2].(int64) != wantLead[i] {
			t.Errorf("id=%v lead=%v, want %d", r[0], r[2], wantLead[i])
		}
	}
}

func TestWindow_FirstLastNthValue(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (id int, v int);
		insert into t values (1, 100), (2, 200), (3, 300)`)
	// Use UNBOUNDED FOLLOWING frame so LAST_VALUE sees the last row.
	rows := scanAll(t, db, `
		select id,
		       first_value(v) over w as fv,
		       last_value(v)  over w as lv,
		       nth_value(v, 2) over w as nv
		from t
		window w as (order by id rows between unbounded preceding and unbounded following)
		order by id`)
	wantFV := []int64{100, 100, 100}
	wantLV := []int64{300, 300, 300}
	wantNV := []int64{200, 200, 200}
	for i, r := range rows {
		if r[1].(int64) != wantFV[i] || r[2].(int64) != wantLV[i] || r[3].(int64) != wantNV[i] {
			t.Errorf("id=%v fv=%v lv=%v nv=%v, want %d, %d, %d",
				r[0], r[1], r[2], r[3], wantFV[i], wantLV[i], wantNV[i])
		}
	}
}

func TestWindow_AggOverWindow(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (id int, v int);
		insert into t values (1, 10), (2, 20), (3, 30), (4, 40)`)
	// Running sum: each row's value plus all preceding.
	rows := scanAll(t, db, `
		select id, sum(v) over (order by id rows between unbounded preceding and current row) as running
		from t order by id`)
	want := []int64{10, 30, 60, 100}
	for i, r := range rows {
		if r[1].(int64) != want[i] {
			t.Errorf("id=%v running=%v, want %d", r[0], r[1], want[i])
		}
	}
}

func TestWindow_RowsFrameVariants(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (id int, v int);
		insert into t values (1, 10), (2, 20), (3, 30), (4, 40), (5, 50)`)

	// ROWS BETWEEN 1 PRECEDING AND 1 FOLLOWING — windowed average over 3 rows.
	rows := scanAll(t, db, `
		select id, avg(v) over (order by id rows between 1 preceding and 1 following) as a
		from t order by id`)
	// id=1: avg(10,20)=15  (no preceding)
	// id=2: avg(10,20,30)=20
	// id=3: avg(20,30,40)=30
	// id=4: avg(30,40,50)=40
	// id=5: avg(40,50)=45 (no following)
	want := []float64{15, 20, 30, 40, 45}
	for i, r := range rows {
		if r[1].(float64) != want[i] {
			t.Errorf("id=%v avg=%v, want %f", r[0], r[1], want[i])
		}
	}
}

func TestWindow_RangeFrame(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (id int, v int);
		insert into t values (1, 10), (2, 10), (3, 20), (4, 30)`)
	// RANGE includes all rows with peer ORDER BY values.
	rows := scanAll(t, db, `
		select id, sum(id) over (order by v range between current row and current row) as s
		from t order by id`)
	// v=10 → rows 1 and 2 are peers, sum(id)=1+2=3 for both
	// v=20 → only row 3, sum=3
	// v=30 → only row 4, sum=4
	want := []int64{3, 3, 3, 4}
	for i, r := range rows {
		if r[1].(int64) != want[i] {
			t.Errorf("id=%v sum=%v, want %d", r[0], r[1], want[i])
		}
	}
}

func TestWindow_NamedAndPartitioned(t *testing.T) {
	db := openDB(t)
	seedScores(t, db)
	// Named window referenced by multiple aggregates.
	rows := scanAll(t, db, `
		select id,
		       sum(score) over w as team_sum,
		       avg(score) over w as team_avg
		from scores
		window w as (partition by team)
		order by id`)
	if len(rows) != 6 {
		t.Fatalf("rows=%d, want 6", len(rows))
	}
	// Team A sum = 30+10+20 = 60, avg = 20
	// Team B sum = 25+25+5 = 55, avg ≈ 18.333
	for _, r := range rows {
		id := r[0].(int64)
		sum := r[1].(int64)
		switch {
		case id <= 3 && sum != 60:
			t.Errorf("team A id=%d sum=%d, want 60", id, sum)
		case id >= 4 && sum != 55:
			t.Errorf("team B id=%d sum=%d, want 55", id, sum)
		}
	}
}

func TestWindow_ExcludeClause(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (id int, v int);
		insert into t values (1, 10), (2, 20), (3, 30)`)
	// EXCLUDE CURRENT ROW: sum excludes the current row's own value.
	rows := scanAll(t, db, `
		select id, sum(v) over (
			order by id rows between unbounded preceding and unbounded following
			exclude current row
		) as s from t order by id`)
	want := []int64{50, 40, 30} // 10+20+30 minus the current row
	for i, r := range rows {
		if r[1].(int64) != want[i] {
			t.Errorf("id=%v exclude-current sum=%v, want %d", r[0], r[1], want[i])
		}
	}
}

// TestWindow_FilterClause asserts FILTER (WHERE ...) works inside an OVER.
func TestWindow_FilterClause(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (id int, v int, kind text);
		insert into t values
			(1, 10, 'a'),
			(2, 20, 'b'),
			(3, 30, 'a'),
			(4, 40, 'b')`)
	rows := scanAll(t, db, `
		select id, sum(v) filter (where kind = 'a') over (order by id) as cum_a
		from t order by id`)
	// cum_a: kind='a' rows summed cumulatively → 10, 10, 40, 40
	want := []any{int64(10), int64(10), int64(40), int64(40)}
	for i, r := range rows {
		if !reflect.DeepEqual(r[1], want[i]) {
			t.Errorf("id=%v cum_a=%v, want %v", r[0], r[1], want[i])
		}
	}
}

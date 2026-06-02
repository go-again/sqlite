package sql_test

import (
	"reflect"
	"testing"
)

func TestCTE_Simple(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (v int); insert into t values (1), (2), (3)`)

	rows := scanAll(t, db, `
		with doubled as (select v * 2 as d from t)
		select d from doubled order by d`)
	want := [][]any{{int64(2)}, {int64(4)}, {int64(6)}}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("simple CTE: %+v, want %+v", rows, want)
	}
}

func TestCTE_Chained(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (v int); insert into t values (1), (2), (3)`)

	rows := scanAll(t, db, `
		with
		  doubled as (select v * 2 as d from t),
		  squared as (select d * d as sq from doubled)
		select sq from squared order by sq`)
	want := [][]any{{int64(4)}, {int64(16)}, {int64(36)}}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("chained CTE: %+v, want %+v", rows, want)
	}
}

// TestCTE_Recursive_Fibonacci computes the first 8 Fibonacci numbers via
// the textbook WITH RECURSIVE pattern.
func TestCTE_Recursive_Fibonacci(t *testing.T) {
	db := openDB(t)
	rows := scanAll(t, db, `
		with recursive fib(i, a, b) as (
			values (1, 0, 1)
			union all
			select i + 1, b, a + b from fib where i < 8
		)
		select a from fib order by i`)
	want := [][]any{
		{int64(0)},
		{int64(1)},
		{int64(1)},
		{int64(2)},
		{int64(3)},
		{int64(5)},
		{int64(8)},
		{int64(13)},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("recursive CTE: %+v, want %+v", rows, want)
	}
}

// TestCTE_Recursive_TreeWalk walks a self-referential parent/child table.
func TestCTE_Recursive_TreeWalk(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table tree (id int, parent int, label text);
		insert into tree values
			(1, NULL, 'root'),
			(2, 1, 'a'),
			(3, 1, 'b'),
			(4, 2, 'a1'),
			(5, 2, 'a2')`)

	rows := scanAll(t, db, `
		with recursive descendants(id, label, depth) as (
			select id, label, 0 from tree where parent is null
			union all
			select t.id, t.label, d.depth + 1
			from tree t join descendants d on t.parent = d.id
		)
		select label, depth from descendants order by depth, label`)
	want := [][]any{
		{"root", int64(0)},
		{"a", int64(1)},
		{"b", int64(1)},
		{"a1", int64(2)},
		{"a2", int64(2)},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("tree walk: %+v, want %+v", rows, want)
	}
}

func TestCTE_InInsert(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table src (v int); insert into src values (1), (2), (3)`)
	mustExec(t, db, `create table dst (v int)`)
	mustExec(t, db, `
		with filtered as (select v from src where v > 1)
		insert into dst select v from filtered`)
	rows := scanAll(t, db, `select v from dst order by v`)
	want := [][]any{{int64(2)}, {int64(3)}}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("CTE in INSERT: %+v, want %+v", rows, want)
	}
}

// TestCTE_MaterializationHint asserts the MATERIALIZED / NOT MATERIALIZED
// hints parse and execute against the bundled SQLite. Result is the same
// either way; the hint affects the planner, not the semantics.
func TestCTE_MaterializationHint(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (v int); insert into t values (1), (2), (3)`)

	for _, hint := range []string{"materialized", "not materialized"} {
		t.Run(hint, func(t *testing.T) {
			rows := scanAll(t, db, `
				with d as `+hint+` (select v * 2 as v2 from t)
				select v2 from d order by v2`)
			want := [][]any{{int64(2)}, {int64(4)}, {int64(6)}}
			if !reflect.DeepEqual(rows, want) {
				t.Errorf("%s CTE: %+v, want %+v", hint, rows, want)
			}
		})
	}
}

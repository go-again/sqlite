// Copyright 2026 The go-again/sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be found
// in the LICENSE file.

package vec_test

import (
	"database/sql"
	"math"
	"testing"

	_ "github.com/go-again/sqlite"
	_ "github.com/go-again/sqlite/vec"
)

// TestRaw_SqliteVecSample mirrors modernc.org/sqlite/vec_test.go's known
// fixture: a 4-vector dataset queried via the MATCH operator. The expected
// rowids and distances are reproduced verbatim from upstream so a future
// modernc bump that changes these values would be flagged here too.
//
// Source:
// https://github.com/asg017/sqlite-vec?tab=readme-ov-file#sample-usage
func TestRaw_SqliteVecSample(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`
create virtual table vec_examples using vec0(
  sample_embedding float[8]
);

insert into vec_examples(rowid, sample_embedding)
  values
    (1, '[-0.200, 0.250, 0.341, -0.211, 0.645, 0.935, -0.316, -0.924]'),
    (2, '[0.443, -0.501, 0.355, -0.771, 0.707, -0.708, -0.185, 0.362]'),
    (3, '[0.716, -0.927, 0.134, 0.052, -0.669, 0.793, -0.634, -0.162]'),
    (4, '[-0.710, 0.330, 0.656, 0.041, -0.990, 0.726, 0.385, -0.958]');
`); err != nil {
		t.Fatalf("setup: %v", err)
	}

	rows, err := db.Query(`
select rowid, distance from vec_examples
where sample_embedding match '[0.890, 0.544, 0.825, 0.961, 0.358, 0.0196, 0.521, 0.175]'
order by distance limit 2;
`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	wantRowids := []int64{2, 1}
	wantDist := []float64{2.38687372207642, 2.38978505134583}
	i := 0
	for rows.Next() {
		var rowid int64
		var distance float64
		if err := rows.Scan(&rowid, &distance); err != nil {
			t.Fatal(err)
		}
		if i >= len(wantRowids) {
			t.Fatalf("more rows than expected, got rowid=%d distance=%f", rowid, distance)
		}
		if rowid != wantRowids[i] {
			t.Errorf("[%d] rowid=%d, want %d", i, rowid, wantRowids[i])
		}
		if math.Abs(distance-wantDist[i]) > 1e-6 {
			t.Errorf("[%d] distance=%f, want %f", i, distance, wantDist[i])
		}
		i++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if i != len(wantRowids) {
		t.Errorf("got %d rows, want %d", i, len(wantRowids))
	}
}

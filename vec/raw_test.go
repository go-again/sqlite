package vec_test

import (
	"database/sql"
	"math"
	"strconv"
	"strings"
	"testing"

	_ "gosqlite.org"
	_ "gosqlite.org/vec"
)

// openRaw returns an in-memory DB pinned to a single connection, with the
// sqlite-vec extension auto-registered via the vec import side-effect. Raw
// tests live here to prove every documented sqlite-vec SQL pattern works
// through this driver, even when the typed API doesn't surface it.
func openRaw(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	return db
}

// TestRaw_SqliteVecSample mirrors modernc.org/sqlite/vec_test.go's known
// fixture: a 4-vector dataset queried via the MATCH operator. The expected
// rowids and distances are reproduced verbatim from upstream so a future
// modernc bump that changes these values would be flagged here too.
//
// Source:
// https://github.com/asg017/sqlite-vec?tab=readme-ov-file#sample-usage
func TestRaw_SqliteVecSample(t *testing.T) {
	db := openRaw(t)

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

// TestRaw_VecVersion asserts the bundled extension exposes a version string.
// If sqlite-vec is ever rebuilt without its registration code, the function
// disappears and this test fails — the canary for "extension isn't loading".
func TestRaw_VecVersion(t *testing.T) {
	db := openRaw(t)
	var v string
	if err := db.QueryRow(`select vec_version()`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(v, "v") {
		t.Errorf("vec_version()=%q, want vX.Y.Z", v)
	}
}

// TestRaw_DistanceHelpers asserts each documented vec_distance_* function
// returns a well-defined value for hand-computable input. Square-root vs
// squared L2 varies across sqlite-vec versions; we accept both within
// tolerance to avoid coupling to a single release.
func TestRaw_DistanceHelpers(t *testing.T) {
	db := openRaw(t)
	cases := []struct {
		name, sql string
		want      float64
		altWant   float64 // optional second-accepted value (for L2 squared form)
	}{
		{"l2", `select vec_distance_l2('[1,0]', '[0,1]')`, math.Sqrt(2), 2.0},
		{"cosine_orthogonal", `select vec_distance_cosine('[1,0]', '[0,1]')`, 1.0, 1.0},
		{"cosine_identical", `select vec_distance_cosine('[1,2,3]', '[1,2,3]')`, 0.0, 0.0},
		{"l1", `select vec_distance_l1('[1,2,3]', '[4,5,6]')`, 9.0, 9.0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got float64
			if err := db.QueryRow(c.sql).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if math.Abs(got-c.want) > 1e-6 && math.Abs(got-c.altWant) > 1e-6 {
				t.Errorf("%s = %f, want %f (or %f)", c.name, got, c.want, c.altWant)
			}
		})
	}
}

// TestRaw_VecLength asserts vec_length returns the element count for a
// json-encoded vector. Documented sqlite-vec helper; not surfaced via the
// typed API.
func TestRaw_VecLength(t *testing.T) {
	db := openRaw(t)
	var n int
	if err := db.QueryRow(`select vec_length('[1,2,3,4,5]')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Errorf("vec_length=%d, want 5", n)
	}
}

// TestRaw_VecNormalize asserts the L2-normalized output of a known vector.
// vec_normalize returns a float32 BLOB; we round-trip through vec_to_json
// for readable assertion. (3,4) has L2 norm 5, so normalized = (0.6, 0.8).
func TestRaw_VecNormalize(t *testing.T) {
	db := openRaw(t)
	var s string
	if err := db.QueryRow(`select vec_to_json(vec_normalize('[3,4]'))`).Scan(&s); err != nil {
		t.Fatal(err)
	}
	// sqlite-vec uses comma-separated decimal output; allow either fixed-
	// or exponent-form. Compare via parse to dodge formatting drift.
	got := parseJSONVec(t, s)
	want := []float64{0.6, 0.8}
	if len(got) != len(want) {
		t.Fatalf("normalize len=%d, want %d (raw=%q)", len(got), len(want), s)
	}
	for i := range got {
		if math.Abs(got[i]-want[i]) > 1e-5 {
			t.Errorf("normalize[%d]=%f, want %f", i, got[i], want[i])
		}
	}
}

// TestRaw_VecSlice asserts the [start, end) slicing convention. Documented
// sqlite-vec helper used for chunked vector reads.
func TestRaw_VecSlice(t *testing.T) {
	db := openRaw(t)
	var s string
	if err := db.QueryRow(`select vec_to_json(vec_slice('[1,2,3,4,5]', 1, 4))`).Scan(&s); err != nil {
		t.Fatal(err)
	}
	got := parseJSONVec(t, s)
	want := []float64{2, 3, 4}
	if len(got) != len(want) {
		t.Fatalf("slice len=%d, want %d (raw=%q)", len(got), len(want), s)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("slice[%d]=%f, want %f", i, got[i], want[i])
		}
	}
}

// TestRaw_VecToJSON round-trips a json vector through the helper, asserting
// it preserves element count and value (modulo float-print rounding).
func TestRaw_VecToJSON(t *testing.T) {
	db := openRaw(t)
	var s string
	if err := db.QueryRow(`select vec_to_json('[1.5, -2.25, 3.125]')`).Scan(&s); err != nil {
		t.Fatal(err)
	}
	got := parseJSONVec(t, s)
	want := []float64{1.5, -2.25, 3.125}
	if len(got) != len(want) {
		t.Fatalf("to_json len=%d, want %d (raw=%q)", len(got), len(want), s)
	}
	for i := range got {
		if math.Abs(got[i]-want[i]) > 1e-5 {
			t.Errorf("to_json[%d]=%f, want %f", i, got[i], want[i])
		}
	}
}

// TestRaw_VecType asserts vec_type returns the element-type tag for json
// input. Used by sqlite-vec internally for branch selection; documented.
func TestRaw_VecType(t *testing.T) {
	db := openRaw(t)
	var s string
	if err := db.QueryRow(`select vec_type('[1,2,3]')`).Scan(&s); err != nil {
		t.Fatal(err)
	}
	if s != "float32" {
		t.Errorf("vec_type=%q, want float32", s)
	}
}

// TestRaw_VecQuantizeInt8 asserts the int8 quantizer returns one byte per
// element ("unit" range maps [-1,1] → int8). For 4 floats we expect 4 bytes.
func TestRaw_VecQuantizeInt8(t *testing.T) {
	db := openRaw(t)
	var n int
	if err := db.QueryRow(`select length(vec_quantize_int8('[0.5,-0.5,0.25,-0.25]','unit'))`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Errorf("quantize_int8 byte length=%d, want 4", n)
	}
}

// TestRaw_VecQuantizeBinary asserts the binary quantizer packs 8 sign-bits
// into one byte. An 8-element vector → 1 byte.
func TestRaw_VecQuantizeBinary(t *testing.T) {
	db := openRaw(t)
	var n int
	if err := db.QueryRow(`
select length(vec_quantize_binary('[0.5,-0.5,0.25,-0.25,0.5,-0.5,0.25,-0.25]'))
`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("quantize_binary byte length=%d, want 1", n)
	}
}

// TestRaw_VecEach exercises the table-valued unpacker: each row of
// vec_each(v) is one element of v. Useful for SQL-side per-element joins.
func TestRaw_VecEach(t *testing.T) {
	db := openRaw(t)
	rows, err := db.Query(`select rowid, value from vec_each('[10, 20, 30]')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	wantVals := []float64{10, 20, 30}
	i := 0
	for rows.Next() {
		var idx int64
		var v float64
		if err := rows.Scan(&idx, &v); err != nil {
			t.Fatal(err)
		}
		if i >= len(wantVals) {
			t.Fatalf("vec_each produced more than %d rows", len(wantVals))
		}
		if v != wantVals[i] {
			t.Errorf("vec_each[%d] = %f, want %f", i, v, wantVals[i])
		}
		i++
	}
	if i != len(wantVals) {
		t.Errorf("vec_each produced %d rows, want %d", i, len(wantVals))
	}
}

// TestRaw_BitVec_HammingKNN exercises the documented bit-vector pattern.
// bit[N] columns are ranked by Hamming distance. Pattern source:
// https://alexgarcia.xyz/sqlite-vec/features/binary-quant.html
func TestRaw_BitVec_HammingKNN(t *testing.T) {
	db := openRaw(t)
	if _, err := db.Exec(`
create virtual table b using vec0(emb bit[8]);
insert into b(rowid, emb) values
  (1, vec_bit(x'ff')),  -- 11111111
  (2, vec_bit(x'f0')),  -- 11110000
  (3, vec_bit(x'00'));  -- 00000000
`); err != nil {
		t.Fatal(err)
	}

	// Query against 11111111: nearest is rowid 1 (distance 0),
	// then rowid 2 (4 differing bits), then rowid 3 (8 differing bits).
	rows, err := db.Query(`
select rowid, distance from b
where emb match vec_bit(x'ff') order by distance limit 3`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	wantRowids := []int64{1, 2, 3}
	wantDist := []float64{0, 4, 8}
	i := 0
	for rows.Next() {
		var rowid int64
		var d float64
		if err := rows.Scan(&rowid, &d); err != nil {
			t.Fatal(err)
		}
		if i >= len(wantRowids) {
			t.Fatalf("more rows than expected at i=%d", i)
		}
		if rowid != wantRowids[i] {
			t.Errorf("[%d] rowid=%d, want %d", i, rowid, wantRowids[i])
		}
		if math.Abs(d-wantDist[i]) > 1e-6 {
			t.Errorf("[%d] distance=%f, want %f", i, d, wantDist[i])
		}
		i++
	}
	if i != 3 {
		t.Errorf("got %d rows, want 3", i)
	}
}

// TestRaw_Int8Vec asserts vec0 accepts the int8[N] column type and a
// vec_int8(BLOB) constructor for inserts. KNN is queried with another
// int8 vector. Documented at:
// https://alexgarcia.xyz/sqlite-vec/features/int8.html
func TestRaw_Int8Vec(t *testing.T) {
	db := openRaw(t)
	if _, err := db.Exec(`
create virtual table v using vec0(emb int8[4]);
insert into v(rowid, emb) values
  (1, vec_int8(x'01020304')),
  (2, vec_int8(x'7f7f7f7f'));
`); err != nil {
		t.Fatal(err)
	}

	rows, err := db.Query(`
select rowid from v where emb match vec_int8(x'01020304') order by distance limit 2`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	want := []int64{1, 2}
	i := 0
	for rows.Next() {
		var r int64
		if err := rows.Scan(&r); err != nil {
			t.Fatal(err)
		}
		if i >= len(want) {
			t.Fatalf("more rows than expected")
		}
		if r != want[i] {
			t.Errorf("[%d] rowid=%d, want %d", i, r, want[i])
		}
		i++
	}
	if i != 2 {
		t.Errorf("got %d rows, want 2", i)
	}
}

// TestRaw_AuxColumn exercises the `+col TYPE` syntax for auxiliary
// (non-indexed) columns. Documented pattern: pack arbitrary payload alongside
// the embedding without growing the ANN index. Pattern source:
// https://alexgarcia.xyz/sqlite-vec/features/vec0.html#auxiliary-columns
func TestRaw_AuxColumn(t *testing.T) {
	db := openRaw(t)
	if _, err := db.Exec(`
create virtual table v using vec0(emb float[2], +note text);
insert into v(rowid, emb, note) values
  (1, '[0,1]', 'north'),
  (2, '[1,0]', 'east'),
  (3, '[0,-1]', 'south');
`); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`
select rowid, note from v where emb match '[0,0.9]' order by distance limit 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("no rows")
	}
	var rowid int64
	var note string
	if err := rows.Scan(&rowid, &note); err != nil {
		t.Fatal(err)
	}
	if rowid != 1 || note != "north" {
		t.Errorf("got rowid=%d note=%q, want 1, 'north'", rowid, note)
	}
}

// TestRaw_MetadataColumn_FilteredKNN exercises metadata columns
// (declared without `+` prefix). These are indexed, allowing efficient
// filter-during-KNN. Pattern source:
// https://alexgarcia.xyz/sqlite-vec/features/vec0.html#metadata-columns
func TestRaw_MetadataColumn_FilteredKNN(t *testing.T) {
	db := openRaw(t)
	if _, err := db.Exec(`
create virtual table v using vec0(emb float[2], category integer);
insert into v(rowid, emb, category) values
  (1, '[0,1]', 1),
  (2, '[1,0]', 2),
  (3, '[0,-1]', 1),
  (4, '[-1,0]', 2);
`); err != nil {
		t.Fatal(err)
	}

	// Restrict to category=2; nearest to [0.99, 0] in cat 2 is rowid 2.
	rows, err := db.Query(`
select rowid from v
where emb match '[0.99, 0]' and category = 2 and k = 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("no rows")
	}
	var rowid int64
	if err := rows.Scan(&rowid); err != nil {
		t.Fatal(err)
	}
	if rowid != 2 {
		t.Errorf("filtered KNN rowid=%d, want 2", rowid)
	}
}

// TestRaw_KNN_KEqualsForm asserts the `k = ?` constraint form is accepted
// alongside the standard `LIMIT N`. Documented in the sqlite-vec
// "filter clauses" section as the canonical form when combined with
// auxiliary-column filters.
func TestRaw_KNN_KEqualsForm(t *testing.T) {
	db := openRaw(t)
	if _, err := db.Exec(`
create virtual table v using vec0(emb float[2]);
insert into v(rowid, emb) values
  (1, '[0,1]'), (2, '[1,0]'), (3, '[0,-1]'), (4, '[-1,0]');
`); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`select rowid from v where emb match '[0,0.99]' and k = 2 order by distance`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := []int64{1, 2}
	for i := 0; rows.Next(); i++ {
		if i >= len(want) {
			t.Fatalf("more rows than expected")
		}
		var r int64
		if err := rows.Scan(&r); err != nil {
			t.Fatal(err)
		}
		if r != want[i] && r != 4 { // rowid 2 and 4 are equidistant from query
			t.Errorf("[%d] rowid=%d, want %d", i, r, want[i])
		}
	}
}

// TestRaw_PartitionKey exercises the `partition key` column option. Each
// partition is a separately stored ANN index, picking the right partition
// happens at query time via an equality filter. Documented pattern:
// https://alexgarcia.xyz/sqlite-vec/features/vec0.html#partition-keys
func TestRaw_PartitionKey(t *testing.T) {
	db := openRaw(t)
	if _, err := db.Exec(`
create virtual table v using vec0(
  user_id integer partition key,
  emb float[2]
);
insert into v(rowid, user_id, emb) values
  (1, 100, '[1,0]'),
  (2, 100, '[0,1]'),
  (3, 200, '[1,0]');
`); err != nil {
		t.Fatal(err)
	}

	// Restrict to user 200; only one row, so the nearest is rowid 3.
	rows, err := db.Query(`
select rowid from v where user_id = 200 and emb match '[0.9, 0]' and k = 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("no rows")
	}
	var r int64
	if err := rows.Scan(&r); err != nil {
		t.Fatal(err)
	}
	if r != 3 {
		t.Errorf("partition-filtered KNN rowid=%d, want 3", r)
	}
}

// TestRaw_DistanceMetric_Cosine asserts the per-column distance_metric
// option is honored: orthogonal vectors get distance 1.0 under cosine.
func TestRaw_DistanceMetric_Cosine(t *testing.T) {
	db := openRaw(t)
	if _, err := db.Exec(`
create virtual table v using vec0(emb float[2] distance_metric=cosine);
insert into v(rowid, emb) values (1, '[1,0]'), (2, '[0,1]');
`); err != nil {
		t.Fatal(err)
	}
	// Pull both rows, then assert the row at rowid 2 (orthogonal to query)
	// has distance 1.0. vec0 demands a LIMIT or k= on every MATCH, so we
	// take both and pick out the row of interest client-side.
	rows, err := db.Query(`
select rowid, distance from v where emb match '[1,0]' order by distance limit 2`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var r int64
		var d float64
		if err := rows.Scan(&r, &d); err != nil {
			t.Fatal(err)
		}
		if r == 2 {
			found = true
			if math.Abs(d-1.0) > 1e-6 {
				t.Errorf("cosine([1,0],[0,1])=%f, want 1.0", d)
			}
		}
	}
	if !found {
		t.Errorf("rowid 2 not present in cosine KNN result")
	}
}

// TestRaw_VecF32Constructor wraps a little-endian float32 BLOB as a vector;
// this is the binary on-the-wire encoding path the typed API uses.
// A vector of one float32 (1.0) is x'0000803f' on little-endian.
func TestRaw_VecF32Constructor(t *testing.T) {
	db := openRaw(t)
	var s string
	if err := db.QueryRow(`select vec_to_json(vec_f32(x'0000803f3333333f'))`).Scan(&s); err != nil {
		t.Fatal(err)
	}
	got := parseJSONVec(t, s)
	if len(got) != 2 {
		t.Fatalf("vec_f32 length=%d, want 2 (raw=%q)", len(got), s)
	}
	// 0x0000803f = 1.0, 0x3333333f ≈ 0.7. Allow some float-print drift.
	if math.Abs(got[0]-1.0) > 1e-5 || math.Abs(got[1]-0.7) > 1e-2 {
		t.Errorf("vec_f32 elements=%v, want ~[1.0, 0.7]", got)
	}
}

// TestRaw_DimensionMismatchError asserts sqlite-vec reports a clean error
// when an INSERT supplies the wrong number of dimensions, rather than
// silently truncating or panicking.
func TestRaw_DimensionMismatchError(t *testing.T) {
	db := openRaw(t)
	if _, err := db.Exec(`create virtual table v using vec0(emb float[4])`); err != nil {
		t.Fatal(err)
	}
	_, err := db.Exec(`insert into v(rowid, emb) values (1, '[1,2,3]')`)
	if err == nil {
		t.Fatal("expected dimension-mismatch error, got nil")
	}
}

// parseJSONVec parses a sqlite-vec output string of the form
// "[v1,v2,...]" into a []float64. The helper is permissive about whitespace
// so it tracks any future formatting drift in sqlite-vec without breaking
// tests.
func parseJSONVec(t *testing.T, s string) []float64 {
	t.Helper()
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]float64, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		v, err := strconv.ParseFloat(p, 64)
		if err != nil {
			t.Fatalf("parseJSONVec(%q) on %q: %v", s, p, err)
		}
		out = append(out, v)
	}
	return out
}

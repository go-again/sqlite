package stats

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"

	sqlite "gosqlite.org"
)

// Register installs every statistics aggregate / window function plus
// the two scalar helpers (cbrt, cot) on c.
//
// All window aggregates support both classic GROUP BY aggregation AND
// SQLite window-function frames (ORDER BY … RANGE / ROWS BETWEEN), with
// streaming Inverse for the sliding-window path.
func Register(c *sqlite.Conn) error {
	// Aggregate constructors return a fresh WindowAccumulator per query.
	// SQLite calls Step on row inclusion, Inverse on row eviction, and
	// Value on each output row.
	reg := func(name string, nArg int, ctor func() sqlite.WindowAccumulator) error {
		return c.RegisterWindowFunction(name, nArg, ctor, true)
	}
	scalar := func(name string, fn any) error {
		return c.RegisterFunc(name, fn, true)
	}
	return errors.Join(
		reg("var_pop", 1, func() sqlite.WindowAccumulator { return &variance{kind: varPop} }),
		reg("var_samp", 1, func() sqlite.WindowAccumulator { return &variance{kind: varSamp} }),
		reg("stddev_pop", 1, func() sqlite.WindowAccumulator { return &variance{kind: stddevPop} }),
		reg("stddev_samp", 1, func() sqlite.WindowAccumulator { return &variance{kind: stddevSamp} }),
		reg("skewness_pop", 1, func() sqlite.WindowAccumulator { return &momentFn{kind: skewnessPop} }),
		reg("skewness_samp", 1, func() sqlite.WindowAccumulator { return &momentFn{kind: skewnessSamp} }),
		reg("kurtosis_pop", 1, func() sqlite.WindowAccumulator { return &momentFn{kind: kurtosisPop} }),
		reg("kurtosis_samp", 1, func() sqlite.WindowAccumulator { return &momentFn{kind: kurtosisSamp} }),
		reg("covar_pop", 2, func() sqlite.WindowAccumulator { return &covariance{kind: varPop} }),
		reg("covar_samp", 2, func() sqlite.WindowAccumulator { return &covariance{kind: varSamp} }),
		reg("corr", 2, func() sqlite.WindowAccumulator { return &covariance{kind: corr} }),
		reg("regr_r2", 2, func() sqlite.WindowAccumulator { return &covariance{kind: regrR2} }),
		reg("regr_sxx", 2, func() sqlite.WindowAccumulator { return &covariance{kind: regrSxx} }),
		reg("regr_syy", 2, func() sqlite.WindowAccumulator { return &covariance{kind: regrSyy} }),
		reg("regr_sxy", 2, func() sqlite.WindowAccumulator { return &covariance{kind: regrSxy} }),
		reg("regr_avgx", 2, func() sqlite.WindowAccumulator { return &covariance{kind: regrAvgX} }),
		reg("regr_avgy", 2, func() sqlite.WindowAccumulator { return &covariance{kind: regrAvgY} }),
		reg("regr_slope", 2, func() sqlite.WindowAccumulator { return &covariance{kind: regrSlope} }),
		reg("regr_intercept", 2, func() sqlite.WindowAccumulator { return &covariance{kind: regrIntercept} }),
		reg("regr_count", 2, func() sqlite.WindowAccumulator { return &covariance{kind: regrCount} }),
		reg("regr_json", 2, func() sqlite.WindowAccumulator { return &covariance{kind: regrJSON} }),
		reg("median", 1, func() sqlite.WindowAccumulator { return &percentile{kind: median} }),
		reg("percentile", 2, func() sqlite.WindowAccumulator { return &percentile{kind: percentile100} }),
		reg("percentile_cont", 2, func() sqlite.WindowAccumulator { return &percentile{kind: percentileCont} }),
		reg("percentile_disc", 2, func() sqlite.WindowAccumulator { return &percentile{kind: percentileDisc} }),
		reg("every", 1, func() sqlite.WindowAccumulator { return &boolean{kind: every} }),
		reg("some", 1, func() sqlite.WindowAccumulator { return &boolean{kind: some} }),
		reg("mode", 1, func() sqlite.WindowAccumulator { return &mode{} }),
		scalar("cbrt", math.Cbrt),
		scalar("cot", cot),
	)
}

func cot(f float64) float64 {
	if f == 0 {
		return math.NaN()
	}
	return 1 / math.Tan(f)
}

// Kind enum shared across the aggregate families.
const (
	varPop = iota
	varSamp
	stddevPop
	stddevSamp
	skewnessPop
	skewnessSamp
	kurtosisPop
	kurtosisSamp
	corr
	regrR2
	regrSxx
	regrSyy
	regrSxy
	regrAvgX
	regrAvgY
	regrSlope
	regrIntercept
	regrCount
	regrJSON

	median
	percentile100
	percentileCont
	percentileDisc

	every
	some
)

// special encodes the "this many samples is too few" rule per ncruces.
// null=true means the function returns SQL NULL; zero=true overrides
// to 0.0 (matches PostgreSQL semantics for var_pop / stddev_pop and
// the like-corrected regr_* family on a single sample).
func special(kind int, n int64) (null, zero bool) {
	switch kind {
	case varPop, stddevPop, regrSxx, regrSyy, regrSxy:
		return n <= 0, n == 1
	case regrAvgX, regrAvgY:
		return n <= 0, false
	case kurtosisSamp:
		return n <= 3, false
	case skewnessSamp:
		return n <= 2, false
	case skewnessPop:
		return n <= 1, n == 2
	default:
		return n <= 1, false
	}
}

// toFloat coerces a driver.Value into a float64 + ok bit. NULL maps to
// (0, false) — callers should skip. Non-numeric scalars (e.g. text)
// also yield ok=false; the function silently skips, matching ncruces.
func toFloat(v driver.Value) (float64, bool) {
	switch x := v.(type) {
	case nil:
		return 0, false
	case int64:
		return float64(x), true
	case float64:
		return x, true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case []byte:
		f, err := strconv.ParseFloat(string(x), 64)
		if err != nil {
			return 0, false
		}
		return f, true
	case string:
		f, err := strconv.ParseFloat(x, 64)
		if err != nil {
			return 0, false
		}
		return f, true
	}
	return 0, false
}

// variance accumulator: var_pop / var_samp / stddev_pop / stddev_samp.
type variance struct {
	welford
	kind int
}

func (fn *variance) Step(_ *sqlite.FunctionContext, args []driver.Value) error {
	if f, ok := toFloat(args[0]); ok {
		fn.enqueue(f)
	}
	return nil
}

func (fn *variance) Inverse(_ *sqlite.FunctionContext, args []driver.Value) error {
	if f, ok := toFloat(args[0]); ok {
		fn.dequeue(f)
	}
	return nil
}

func (fn *variance) Value(*sqlite.FunctionContext) (driver.Value, error) {
	null, zero := special(fn.kind, fn.n)
	if zero {
		return float64(0), nil
	}
	if null {
		return nil, nil
	}
	switch fn.kind {
	case varPop:
		return fn.varPop(), nil
	case varSamp:
		return fn.varSamp(), nil
	case stddevPop:
		return fn.stddevPop(), nil
	case stddevSamp:
		return fn.stddevSamp(), nil
	}
	return nil, nil
}

// covariance accumulator: covar_*, corr, regr_*.
type covariance struct {
	welford2
	kind int
}

func (fn *covariance) Step(_ *sqlite.FunctionContext, args []driver.Value) error {
	y, oy := toFloat(args[0])
	x, ox := toFloat(args[1])
	if oy && ox {
		fn.enqueue(y, x)
	}
	return nil
}

func (fn *covariance) Inverse(_ *sqlite.FunctionContext, args []driver.Value) error {
	y, oy := toFloat(args[0])
	x, ox := toFloat(args[1])
	if oy && ox {
		fn.dequeue(y, x)
	}
	return nil
}

// jsonSubtype is SQLite's JSON1 subtype tag ('J', 0x4A). Tagging a TEXT result
// with it lets json_extract / -> / ->> treat the value as already-parsed JSON,
// skipping a re-parse.
const jsonSubtype = 74

func (fn *covariance) Value(ctx *sqlite.FunctionContext) (driver.Value, error) {
	if fn.kind == regrCount {
		return fn.regrCount(), nil
	}
	null, zero := special(fn.kind, fn.n)
	if zero {
		return float64(0), nil
	}
	if null {
		return nil, nil
	}
	switch fn.kind {
	case varPop:
		return fn.covarPop(), nil
	case varSamp:
		return fn.covarSamp(), nil
	case corr:
		return fn.correlation(), nil
	case regrR2:
		return fn.regrR2(), nil
	case regrSxx:
		return fn.regrSxx(), nil
	case regrSyy:
		return fn.regrSyy(), nil
	case regrSxy:
		return fn.regrSxy(), nil
	case regrAvgX:
		return fn.regrAvgX(), nil
	case regrAvgY:
		return fn.regrAvgY(), nil
	case regrSlope:
		return fn.regrSlope(), nil
	case regrIntercept:
		return fn.regrIntercept(), nil
	case regrJSON:
		if ctx != nil {
			ctx.ResultSubtype(jsonSubtype)
		}
		return fn.regrJSONString(), nil
	}
	return nil, nil
}

func (w welford2) regrJSONString() string {
	var b strings.Builder
	b.WriteString(`{"count":`)
	b.WriteString(strconv.FormatInt(w.regrCount(), 10))
	b.WriteString(`,"avgy":`)
	appendFloat(&b, w.regrAvgY())
	b.WriteString(`,"avgx":`)
	appendFloat(&b, w.regrAvgX())
	b.WriteString(`,"syy":`)
	appendFloat(&b, w.regrSyy())
	b.WriteString(`,"sxx":`)
	appendFloat(&b, w.regrSxx())
	b.WriteString(`,"sxy":`)
	appendFloat(&b, w.regrSxy())
	b.WriteString(`,"slope":`)
	appendFloat(&b, w.regrSlope())
	b.WriteString(`,"intercept":`)
	appendFloat(&b, w.regrIntercept())
	b.WriteString(`,"r2":`)
	appendFloat(&b, w.regrR2())
	b.WriteByte('}')
	return b.String()
}

func appendFloat(b *strings.Builder, f float64) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		b.WriteString(`null`)
		return
	}
	b.WriteString(strconv.FormatFloat(f, 'g', -1, 64))
}

// momentFn accumulator: skewness_* / kurtosis_*.
type momentFn struct {
	moments
	kind int
}

func (fn *momentFn) Step(_ *sqlite.FunctionContext, args []driver.Value) error {
	if f, ok := toFloat(args[0]); ok {
		fn.enqueue(f)
	}
	return nil
}

func (fn *momentFn) Inverse(_ *sqlite.FunctionContext, args []driver.Value) error {
	if f, ok := toFloat(args[0]); ok {
		fn.dequeue(f)
	}
	return nil
}

func (fn *momentFn) Value(*sqlite.FunctionContext) (driver.Value, error) {
	null, zero := special(fn.kind, fn.n)
	if zero {
		return float64(0), nil
	}
	if null {
		return nil, nil
	}
	switch fn.kind {
	case skewnessPop:
		return fn.skewnessPop(), nil
	case skewnessSamp:
		return fn.skewnessSamp(), nil
	case kurtosisPop:
		return fn.kurtosisPop(), nil
	case kurtosisSamp:
		return fn.kurtosisSamp(), nil
	}
	return nil, nil
}

// boolean accumulator: every / some.
type boolean struct {
	count int
	total int
	kind  int
}

func (b *boolean) Step(_ *sqlite.FunctionContext, args []driver.Value) error {
	if args[0] == nil {
		return nil
	}
	b.total++
	if asBool(args[0]) {
		b.count++
	}
	return nil
}

func (b *boolean) Inverse(_ *sqlite.FunctionContext, args []driver.Value) error {
	if args[0] == nil {
		return nil
	}
	b.total--
	if asBool(args[0]) {
		b.count--
	}
	return nil
}

func (b *boolean) Value(*sqlite.FunctionContext) (driver.Value, error) {
	if b.total == 0 {
		return nil, nil
	}
	if b.kind == every {
		return b.count == b.total, nil
	}
	return b.count > 0, nil
}

func asBool(v driver.Value) bool {
	switch x := v.(type) {
	case bool:
		return x
	case int64:
		return x != 0
	case float64:
		return x != 0
	case []byte:
		return len(x) > 0 && x[0] != '0'
	case string:
		return len(x) > 0 && x[0] != '0'
	}
	return false
}

// percentile accumulator: median / percentile / percentile_cont / percentile_disc.
//
// For non-windowed aggregation we buffer all values and sort in Value.
// For windowed sliding aggregation we use the same buffer + Inverse
// scan-and-remove; that's O(N) per Inverse, fine for moderate window
// sizes. The window-function planner only emits Inverse when it sees a
// sliding frame; whole-frame and GROUP BY skip it.
type percentile struct {
	nums   []float64
	posArg []byte // raw text of the second arg (only for non-median)
	kind   int
}

func (q *percentile) Step(_ *sqlite.FunctionContext, args []driver.Value) error {
	if f, ok := toFloat(args[0]); ok {
		q.nums = append(q.nums, f)
	}
	if q.kind != median {
		// The second arg is a per-query constant by spec. Capture
		// whenever we see a non-NULL value — refresh-on-every-row also
		// works because the value is constant, but skipping NULLs keeps
		// `posArg` populated when the first row's position is NULL.
		if b := takeBytes(args[1]); b != nil {
			q.posArg = b
		}
	}
	return nil
}

func (q *percentile) Inverse(_ *sqlite.FunctionContext, args []driver.Value) error {
	if f, ok := toFloat(args[0]); ok {
		if i := slices.Index(q.nums, f); i >= 0 {
			l := len(q.nums) - 1
			q.nums[i] = q.nums[l]
			q.nums = q.nums[:l]
		}
	}
	return nil
}

func (q *percentile) Value(*sqlite.FunctionContext) (driver.Value, error) {
	if len(q.nums) == 0 {
		return nil, nil
	}
	if q.kind == median {
		v, _ := q.at(0.5)
		return v, nil
	}
	// percentile / percentile_cont / percentile_disc accept either a
	// single number or a JSON array of numbers; the latter returns a
	// JSON string.
	if len(q.posArg) > 0 {
		var multi []float64
		if err := json.Unmarshal(q.posArg, &multi); err == nil {
			for i := range multi {
				v, err := q.at(multi[i])
				if err != nil {
					return nil, err
				}
				multi[i] = v
			}
			out, _ := json.Marshal(multi)
			return string(out), nil
		}
	}
	var pos float64
	if err := json.Unmarshal(q.posArg, &pos); err != nil {
		// Best-effort: try ParseFloat on the raw bytes.
		f, perr := strconv.ParseFloat(string(q.posArg), 64)
		if perr != nil {
			return nil, errors.New("percentile: position must be a number or JSON array of numbers")
		}
		pos = f
	}
	v, err := q.at(pos)
	if err != nil {
		return nil, err
	}
	return v, nil
}

func (q *percentile) at(pos float64) (float64, error) {
	if q.kind == percentile100 {
		pos = pos / 100
	}
	if pos < 0 || pos > 1 {
		return 0, errors.New("percentile: position out of range [0, 1]")
	}
	// Sort once per Value call. For the typical analytics use case
	// (N rows, single percentile call) that's O(N log N), no different
	// from a manual SELECT … ORDER BY + LIMIT. Sliding-window callers
	// pay this on every output row, which is the documented trade-off.
	sorted := slices.Clone(q.nums)
	sort.Float64s(sorted)
	idx, frac := math.Modf(pos * float64(len(sorted)-1))
	m0 := sorted[int(idx)]
	if frac == 0 || q.kind == percentileDisc {
		return m0, nil
	}
	m1 := sorted[int(idx)+1]
	return m0 + frac*(m1-m0), nil
}

func takeBytes(v driver.Value) []byte {
	switch x := v.(type) {
	case nil:
		return nil
	case []byte:
		out := make([]byte, len(x))
		copy(out, x)
		return out
	case string:
		return []byte(x)
	case int64:
		return []byte(strconv.FormatInt(x, 10))
	case float64:
		return []byte(strconv.FormatFloat(x, 'g', -1, 64))
	}
	return nil
}

// mode accumulator: most frequent value, deterministic tiebreak.
//
// Tracks per-type counters because SQL values can collide between
// INTEGER 1 and FLOAT 1.0 and TEXT "1" — keeping them apart preserves
// the type of the eventual mode. Mixed INTEGER + FLOAT in the same
// column promotes to FLOAT (matches ncruces / SQLite-bundled semantics).
type mode struct {
	ints  map[int64]uint
	reals map[float64]uint
	texts map[string]uint
	blobs map[string]uint
}

func (m *mode) Step(_ *sqlite.FunctionContext, args []driver.Value) error {
	switch x := args[0].(type) {
	case nil:
		return nil
	case int64:
		if m.reals != nil {
			m.bumpReal(float64(x))
			return nil
		}
		if m.ints == nil {
			m.ints = make(map[int64]uint)
		}
		m.ints[x]++
	case float64:
		m.bumpReal(x)
	case bool:
		if m.ints == nil {
			m.ints = make(map[int64]uint)
		}
		if x {
			m.ints[1]++
		} else {
			m.ints[0]++
		}
	case string:
		if m.texts == nil {
			m.texts = make(map[string]uint)
		}
		m.texts[x]++
	case []byte:
		if m.blobs == nil {
			m.blobs = make(map[string]uint)
		}
		m.blobs[string(x)]++
	}
	return nil
}

func (m *mode) bumpReal(f float64) {
	if m.reals == nil {
		m.reals = make(map[float64]uint)
		for k, v := range m.ints {
			m.reals[float64(k)] += v
		}
		m.ints = nil
	}
	m.reals[f]++
}

func (m *mode) Inverse(_ *sqlite.FunctionContext, args []driver.Value) error {
	switch x := args[0].(type) {
	case nil:
		return nil
	case int64:
		if m.reals != nil {
			decReal(m.reals, float64(x))
			return nil
		}
		decInt(m.ints, x)
	case float64:
		decReal(m.reals, x)
	case bool:
		if x {
			decInt(m.ints, 1)
		} else {
			decInt(m.ints, 0)
		}
	case string:
		decStr(m.texts, x)
	case []byte:
		decStr(m.blobs, string(x))
	}
	return nil
}

func decInt(c map[int64]uint, k int64) {
	if c == nil {
		return
	}
	if c[k] <= 1 {
		delete(c, k)
		return
	}
	c[k]--
}

func decReal(c map[float64]uint, k float64) {
	if c == nil {
		return
	}
	if c[k] <= 1 {
		delete(c, k)
		return
	}
	c[k]--
}

func decStr(c map[string]uint, k string) {
	if c == nil {
		return
	}
	if c[k] <= 1 {
		delete(c, k)
		return
	}
	c[k]--
}

func (m *mode) Value(*sqlite.FunctionContext) (driver.Value, error) {
	var (
		bestKind = -1
		bestN    uint
		i64      int64
		f64      float64
		str      string
		isBlob   bool
	)
	const (
		kInt = iota
		kFloat
		kText
		kBlob
	)
	for k, v := range m.ints {
		if v > bestN || (v == bestN && bestKind == kInt && k < i64) {
			bestKind, bestN, i64 = kInt, v, k
		}
	}
	for k, v := range m.reals {
		if v > bestN || (v == bestN && bestKind == kFloat && k < f64) {
			bestKind, bestN, f64 = kFloat, v, k
		}
	}
	for k, v := range m.texts {
		if v > bestN || (v == bestN && bestKind == kText && k < str) {
			bestKind, bestN, str, isBlob = kText, v, k, false
		}
	}
	for k, v := range m.blobs {
		if v > bestN || (v == bestN && bestKind == kBlob && k < str) {
			bestKind, bestN, str, isBlob = kBlob, v, k, true
		}
	}
	if bestN == 0 {
		return nil, nil
	}
	switch bestKind {
	case kInt:
		return i64, nil
	case kFloat:
		return f64, nil
	case kText:
		if isBlob {
			return []byte(str), nil
		}
		return str, nil
	case kBlob:
		return []byte(str), nil
	}
	return nil, nil
}

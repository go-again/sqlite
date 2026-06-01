package stats

import "math"

// Kahan summation reduces floating-point error accumulation in the
// running sums that back Welford's algorithm. The (hi, lo) pair carries
// the running compensation; add / sub thread the lost low-order bits
// back in on every step. Same algorithm as nalgeon/sqlean and ncruces;
// see https://en.wikipedia.org/wiki/Kahan_summation_algorithm.
type kahan struct{ hi, lo float64 }

func (k *kahan) add(x float64) {
	y := k.lo + x
	t := k.hi + y
	k.lo = y - (t - k.hi)
	k.hi = t
}

func (k *kahan) sub(x float64) {
	y := k.lo - x
	t := k.hi + y
	k.lo = y - (t - k.hi)
	k.hi = t
}

// welford tracks the streaming mean and variance of a single series
// using Welford's online algorithm + Kahan compensation. Supports
// dequeue (window-frame eviction) — the trailing-edge update mirrors
// the leading-edge math with a sign flip.
//
// https://en.wikipedia.org/wiki/Algorithms_for_calculating_variance#Welford's_online_algorithm
type welford struct {
	m1, m2 kahan
	n      int64
}

func (w welford) varPop() float64    { return w.m2.hi / float64(w.n) }
func (w welford) varSamp() float64   { return w.m2.hi / float64(w.n-1) } // Bessel's correction.
func (w welford) stddevPop() float64 { return math.Sqrt(w.varPop()) }
func (w welford) stddevSamp() float64 {
	return math.Sqrt(w.varSamp())
}

func (w *welford) enqueue(x float64) {
	n := w.n + 1
	w.n = n
	d1 := x - w.m1.hi - w.m1.lo
	w.m1.add(d1 / float64(n))
	d2 := x - w.m1.hi - w.m1.lo
	w.m2.add(d1 * d2)
}

func (w *welford) dequeue(x float64) {
	n := w.n - 1
	if n <= 0 {
		*w = welford{}
		return
	}
	w.n = n
	d1 := x - w.m1.hi - w.m1.lo
	w.m1.sub(d1 / float64(n))
	d2 := x - w.m1.hi - w.m1.lo
	w.m2.sub(d1 * d2)
}

// welford2 is the two-variable extension supporting covariance,
// correlation, and the regr_* family. Identical idea to welford —
// streaming centered sums of y, x, and their product, with Kahan
// compensation on each.
type welford2 struct {
	m1y, m2y kahan
	m1x, m2x kahan
	cov      kahan
	n        int64
}

func (w welford2) covarPop() float64  { return w.cov.hi / float64(w.n) }
func (w welford2) covarSamp() float64 { return w.cov.hi / float64(w.n-1) }
func (w welford2) correlation() float64 {
	return w.cov.hi / math.Sqrt(w.m2y.hi*w.m2x.hi)
}
func (w welford2) regrAvgY() float64  { return w.m1y.hi }
func (w welford2) regrAvgX() float64  { return w.m1x.hi }
func (w welford2) regrSyy() float64   { return w.m2y.hi }
func (w welford2) regrSxx() float64   { return w.m2x.hi }
func (w welford2) regrSxy() float64   { return w.cov.hi }
func (w welford2) regrCount() int64   { return w.n }
func (w welford2) regrSlope() float64 { return w.cov.hi / w.m2x.hi }
func (w welford2) regrR2() float64    { return w.cov.hi * w.cov.hi / (w.m2y.hi * w.m2x.hi) }
func (w welford2) regrIntercept() float64 {
	slope := -w.regrSlope()
	hi := math.FMA(slope, w.m1x.hi, w.m1y.hi)
	lo := math.FMA(slope, w.m1x.lo, w.m1y.lo)
	return hi + lo
}

func (w *welford2) enqueue(y, x float64) {
	n := w.n + 1
	w.n = n
	d1y := y - w.m1y.hi - w.m1y.lo
	d1x := x - w.m1x.hi - w.m1x.lo
	w.m1y.add(d1y / float64(n))
	w.m1x.add(d1x / float64(n))
	d2y := y - w.m1y.hi - w.m1y.lo
	d2x := x - w.m1x.hi - w.m1x.lo
	w.m2y.add(d1y * d2y)
	w.m2x.add(d1x * d2x)
	w.cov.add(d1y * d2x)
}

func (w *welford2) dequeue(y, x float64) {
	n := w.n - 1
	if n <= 0 {
		*w = welford2{}
		return
	}
	w.n = n
	d1y := y - w.m1y.hi - w.m1y.lo
	d1x := x - w.m1x.hi - w.m1x.lo
	w.m1y.sub(d1y / float64(n))
	w.m1x.sub(d1x / float64(n))
	d2y := y - w.m1y.hi - w.m1y.lo
	d2x := x - w.m1x.hi - w.m1x.lo
	w.m2y.sub(d1y * d2y)
	w.m2x.sub(d1x * d2x)
	w.cov.sub(d1y * d2x)
}

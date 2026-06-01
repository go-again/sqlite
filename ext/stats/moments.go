package stats

import "math"

// moments extends Welford's algorithm to the third and fourth central
// moments, backing skewness and kurtosis. Terriberry's online algorithm
// with Kahan compensation; same algebra ncruces and the bundled SQLite
// extension use.
//
// https://en.wikipedia.org/wiki/Algorithms_for_calculating_variance#Higher-order_statistics
type moments struct {
	m1, m2, m3, m4 kahan
	n              int64
}

func (m moments) skewnessPop() float64 {
	m2 := m.m2.hi
	if div := m2 * m2 * m2; div != 0 {
		return m.m3.hi * math.Sqrt(float64(m.n)/div)
	}
	return math.NaN()
}

func (m moments) skewnessSamp() float64 {
	n := m.n
	// https://mathworks.com/help/stats/skewness.html#f1132178
	return m.skewnessPop() * math.Sqrt(float64(n*(n-1))) / float64(n-2)
}

func (m moments) rawKurtosisPop() float64 {
	m2 := m.m2.hi
	if div := m2 * m2; div != 0 {
		return m.m4.hi * float64(m.n) / div
	}
	return math.NaN()
}

func (m moments) kurtosisPop() float64 { return m.rawKurtosisPop() - 3 }

func (m moments) kurtosisSamp() float64 {
	n := m.n
	// https://mathworks.com/help/stats/kurtosis.html#f4975293
	k := math.FMA(m.rawKurtosisPop(), float64(n+1), float64(3-3*n))
	return k * float64(n-1) / float64((n-2)*(n-3))
}

func (m *moments) enqueue(x float64) {
	n := m.n + 1
	m.n = n
	d1 := x - m.m1.hi - m.m1.lo
	dn := d1 / float64(n)
	d2 := dn * dn
	t1 := d1 * dn * float64(n-1)
	m.m4.add(t1*d2*float64(n*n-3*n+3) + 6*d2*m.m2.hi - 4*dn*m.m3.hi)
	m.m3.add(t1*dn*float64(n-2) - 3*dn*m.m2.hi)
	m.m2.add(t1)
	m.m1.add(dn)
}

func (m *moments) dequeue(x float64) {
	n := m.n - 1
	if n <= 0 {
		*m = moments{}
		return
	}
	m.n = n
	d1 := x - m.m1.hi - m.m1.lo
	dn := d1 / float64(n)
	d2 := dn * dn
	t1 := d1 * dn * float64(n+1)
	m.m4.sub(t1*d2*float64(n*n+3*n+3) - 6*d2*m.m2.hi - 4*dn*m.m3.hi)
	m.m3.sub(t1*dn*float64(n+2) - 3*dn*m.m2.hi)
	m.m2.sub(t1)
	m.m1.sub(dn)
}

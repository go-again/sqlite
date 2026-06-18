package fuzzy_test

import "testing"

func TestFuzzy_Caverphone(t *testing.T) {
	ctx, db := openDB(t)

	// Exact codes and the phonetic-equivalence property (the whole point
	// of a phonetic code: spellings that sound alike collapse together).
	if got := scanStr(t, ctx, db, `SELECT caverphone('Thompson')`); got != "TMPSN11111" {
		t.Errorf("caverphone('Thompson') = %q, want TMPSN11111", got)
	}
	if got := scanStr(t, ctx, db, `SELECT caverphone('')`); got != "1111111111" {
		t.Errorf("caverphone('') = %q, want 1111111111", got)
	}

	eq := []struct{ a, b string }{
		{"Thompson", "Tompson"},
		{"Catherine", "Katherine"},
	}
	for _, c := range eq {
		ca := scanStr(t, ctx, db, `SELECT caverphone(?)`, c.a)
		cb := scanStr(t, ctx, db, `SELECT caverphone(?)`, c.b)
		if ca != cb {
			t.Errorf("caverphone(%q)=%q != caverphone(%q)=%q", c.a, ca, c.b, cb)
		}
		if len(ca) != 10 {
			t.Errorf("caverphone(%q) length %d, want 10", c.a, len(ca))
		}
	}
}

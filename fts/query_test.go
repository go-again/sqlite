package fts

import "testing"

// These tests live in the internal `fts` package (not fts_test) so they can
// call the unexported `build()` method on every Query constructor. The
// behavior-driven tests in fts_test.go assert that searches return the
// right rows; the string assertions here lock down the exact FTS5 syntax
// each builder emits, so a future refactor of the builder layer that
// happens to round-trip the easy fixtures but breaks adversarial inputs
// (FTS5 operator keywords, embedded quotes, etc.) is caught.

func TestBuild_Term(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"fox", `"fox"`},
		// FTS5 operator keywords must be quoted as ordinary tokens.
		{"AND", `"AND"`},
		{"NEAR", `"NEAR"`},
		// Embedded double-quotes are doubled, per FTS5's escape rule.
		{`a "b" c`, `"a ""b"" c"`},
		// Empty term is still a valid quoted string.
		{"", `""`},
	}
	for _, tc := range cases {
		if got := Term(tc.in).build(); got != tc.want {
			t.Errorf("Term(%q).build() = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBuild_Phrase(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"brown", "dog"}, `"brown dog"`},
		// FTS5 phrase syntax wraps all tokens in one set of quotes — the
		// previous bug was emitting two phrases (AND-ed).
		{[]string{"a", "b", "c"}, `"a b c"`},
		// Quotes inside a phrase token are doubled, just like Term.
		{[]string{`he said "hi"`}, `"he said ""hi"""`},
		// Empty token list yields an empty quoted string.
		{nil, `""`},
	}
	for _, tc := range cases {
		if got := Phrase(tc.in...).build(); got != tc.want {
			t.Errorf("Phrase(%v).build() = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBuild_Prefix(t *testing.T) {
	if got := Prefix("ru").build(); got != "ru*" {
		t.Errorf("Prefix(ru).build() = %q, want %q", got, "ru*")
	}
	if got := Prefix("").build(); got != "*" {
		t.Errorf("Prefix(empty).build() = %q, want %q", got, "*")
	}
}

func TestBuild_And(t *testing.T) {
	q := And(Term("a"), Term("b"))
	want := `("a") AND ("b")`
	if got := q.build(); got != want {
		t.Errorf("And.build() = %q, want %q", got, want)
	}
	// Three-way AND keeps left-to-right grouping (And is associative;
	// flat join is the natural emit).
	want3 := `("a") AND ("b") AND ("c")`
	if got := And(Term("a"), Term("b"), Term("c")).build(); got != want3 {
		t.Errorf("And(3).build() = %q, want %q", got, want3)
	}
}

func TestBuild_Or(t *testing.T) {
	q := Or(Term("a"), Term("b"))
	want := `("a") OR ("b")`
	if got := q.build(); got != want {
		t.Errorf("Or.build() = %q, want %q", got, want)
	}
}

func TestBuild_Not_Single(t *testing.T) {
	// NOT with no negatives degenerates to just the positive — FTS5
	// requires at least one term in the result set.
	q := Not(Term("a"))
	if got := q.build(); got != `"a"` {
		t.Errorf("Not(a).build() = %q, want %q", got, `"a"`)
	}
}

func TestBuild_Not_MultipleNegatives(t *testing.T) {
	q := Not(Term("a"), Term("b"), Term("c"))
	want := `("a") NOT (("b") OR ("c"))`
	if got := q.build(); got != want {
		t.Errorf("Not(a; b, c).build() = %q, want %q", got, want)
	}
}

func TestBuild_Near(t *testing.T) {
	cases := []struct {
		dist int
		in   []string
		want string
	}{
		{0, []string{"foo", "bar"}, `NEAR("foo" "bar")`},
		{5, []string{"foo", "bar"}, `NEAR("foo" "bar", 5)`},
		{3, []string{"x", "y", "z"}, `NEAR("x" "y" "z", 3)`},
	}
	for _, tc := range cases {
		if got := Near(tc.dist, tc.in...).build(); got != tc.want {
			t.Errorf("Near(%d, %v).build() = %q, want %q", tc.dist, tc.in, got, tc.want)
		}
	}
}

func TestBuild_Column(t *testing.T) {
	q := Column("title", Term("fox"))
	want := `title: ("fox")`
	if got := q.build(); got != want {
		t.Errorf("Column.build() = %q, want %q", got, want)
	}
	// Composing — Column wrapping And.
	q2 := Column("body", And(Term("fox"), Term("dog")))
	want2 := `body: (("fox") AND ("dog"))`
	if got := q2.build(); got != want2 {
		t.Errorf("Column+And.build() = %q, want %q", got, want2)
	}
}

func TestBuild_Raw(t *testing.T) {
	// Raw passes through verbatim — bound to be useful when something
	// the builder doesn't cover comes up.
	if got := Raw("foo OR (bar NEAR baz)").build(); got != "foo OR (bar NEAR baz)" {
		t.Errorf("Raw didn't pass through verbatim")
	}
}

// Package fuzzy adds approximate-string-matching SQL scalar functions —
// edit distances, the Jaro / Jaro-Winkler similarity, and the Soundex phonetic
// code:
//
//	levenshtein(a, b)          -- edit distance (insert/delete/substitute)
//	damerau_levenshtein(a, b)  -- + adjacent transposition (OSA variant)
//	hamming(a, b)              -- positions that differ; errors if lengths differ
//	jaro(a, b)                 -- Jaro similarity in [0, 1]
//	jaro_winkler(a, b)         -- Jaro with a common-prefix bonus
//	soundex(s)                 -- 4-character American Soundex code
//	caverphone(s)              -- 10-character Caverphone 2.0 phonetic code
//
// Distances/similarities are rune-aware (Unicode-correct); Soundex and
// Caverphone operate on ASCII letters as those algorithms are defined. The
// cousin to ext/spellfix1 (a
// vtab-based fuzzy vocabulary): these are stateless scalars over two strings.
// Ported in spirit from the sqlean `fuzzy` module.
//
// # Usage
//
//	import (
//	    sqlite "github.com/go-again/sqlite"
//	    "github.com/go-again/sqlite/ext/fuzzy"
//	)
//
//	fuzzy.Register(conn) // or blank-import ".../ext/fuzzy/auto" for pool-wide
//	db.QueryRow(`SELECT levenshtein('kitten', 'sitting')`) // 3
package fuzzy

import (
	"errors"
	"fmt"

	sqlite "github.com/go-again/sqlite"
)

// Register installs the fuzzy SQL functions on c. All are deterministic.
func Register(c *sqlite.Conn) error {
	return errors.Join(
		c.RegisterFunc("levenshtein", levenshtein, true),
		c.RegisterFunc("damerau_levenshtein", damerauLevenshtein, true),
		c.RegisterFunc("hamming", hamming, true),
		c.RegisterFunc("jaro", jaro, true),
		c.RegisterFunc("jaro_winkler", jaroWinkler, true),
		c.RegisterFunc("soundex", soundex, true),
		c.RegisterFunc("caverphone", caverphone, true),
	)
}

// levenshtein is the classic edit distance (insertions, deletions,
// substitutions) over runes, computed with two rolling rows.
func levenshtein(a, b string) int64 {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 {
		return int64(len(rb))
	}
	if len(rb) == 0 {
		return int64(len(ra))
	}
	prev := make([]int, len(rb)+1)
	curr := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		curr[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			curr[j] = min(min(prev[j]+1, curr[j-1]+1), prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return int64(prev[len(rb)])
}

// damerauLevenshtein is Levenshtein plus adjacent transposition (the
// optimal-string-alignment variant — substrings are not edited more than once).
func damerauLevenshtein(a, b string) int64 {
	ra, rb := []rune(a), []rune(b)
	la, lb := len(ra), len(rb)
	if la == 0 {
		return int64(lb)
	}
	if lb == 0 {
		return int64(la)
	}
	d := make([][]int, la+1)
	for i := range d {
		d[i] = make([]int, lb+1)
		d[i][0] = i
	}
	for j := 0; j <= lb; j++ {
		d[0][j] = j
	}
	for i := 1; i <= la; i++ {
		for j := 1; j <= lb; j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			d[i][j] = min(min(d[i-1][j]+1, d[i][j-1]+1), d[i-1][j-1]+cost)
			if i > 1 && j > 1 && ra[i-1] == rb[j-2] && ra[i-2] == rb[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return int64(d[la][lb])
}

// hamming counts the positions at which two equal-length strings differ; it
// errors when the rune lengths differ.
func hamming(a, b string) (int64, error) {
	ra, rb := []rune(a), []rune(b)
	if len(ra) != len(rb) {
		return 0, fmt.Errorf("hamming: strings must be equal length (%d != %d)", len(ra), len(rb))
	}
	var dist int64
	for i := range ra {
		if ra[i] != rb[i] {
			dist++
		}
	}
	return dist, nil
}

// jaro returns the Jaro similarity of a and b in [0, 1] (1 == identical).
func jaro(a, b string) float64 {
	ra, rb := []rune(a), []rune(b)
	la, lb := len(ra), len(rb)
	if la == 0 && lb == 0 {
		return 1
	}
	if la == 0 || lb == 0 {
		return 0
	}
	matchDist := max(max(la, lb)/2-1, 0)
	aMatch := make([]bool, la)
	bMatch := make([]bool, lb)
	matches := 0
	for i := range ra {
		start := max(0, i-matchDist)
		end := min(lb, i+matchDist+1)
		for k := start; k < end; k++ {
			if bMatch[k] || ra[i] != rb[k] {
				continue
			}
			aMatch[i] = true
			bMatch[k] = true
			matches++
			break
		}
	}
	if matches == 0 {
		return 0
	}
	transpositions := 0
	k := 0
	for i := range ra {
		if !aMatch[i] {
			continue
		}
		for !bMatch[k] {
			k++
		}
		if ra[i] != rb[k] {
			transpositions++
		}
		k++
	}
	transpositions /= 2
	m := float64(matches)
	return (m/float64(la) + m/float64(lb) + (m-float64(transpositions))/m) / 3
}

// jaroWinkler boosts the Jaro similarity by a bonus for a shared prefix (up to
// 4 runes), favoring strings that match from the start.
func jaroWinkler(a, b string) float64 {
	j := jaro(a, b)
	if j == 0 {
		return 0
	}
	ra, rb := []rune(a), []rune(b)
	prefix := 0
	for prefix < 4 && prefix < len(ra) && prefix < len(rb) && ra[prefix] == rb[prefix] {
		prefix++
	}
	return j + float64(prefix)*0.1*(1-j)
}

// soundex returns the 4-character American Soundex code of s (a letter followed
// by three digits), or "" when s has no ASCII letters.
func soundex(s string) string {
	letters := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		if c >= 'A' && c <= 'Z' {
			letters = append(letters, c)
		}
	}
	if len(letters) == 0 {
		return ""
	}
	code := []byte{letters[0]}
	last := soundexDigit(letters[0])
	for i := 1; i < len(letters) && len(code) < 4; i++ {
		switch letters[i] {
		case 'H', 'W':
			// Ignored entirely: do not break a run of same-coded consonants.
		case 'A', 'E', 'I', 'O', 'U', 'Y':
			last = '0' // a vowel lets an identical consonant code repeat
		default:
			if d := soundexDigit(letters[i]); d != '0' && d != last {
				code = append(code, d)
				last = d
			} else {
				last = d
			}
		}
	}
	for len(code) < 4 {
		code = append(code, '0')
	}
	return string(code)
}

func soundexDigit(c byte) byte {
	switch c {
	case 'B', 'F', 'P', 'V':
		return '1'
	case 'C', 'G', 'J', 'K', 'Q', 'S', 'X', 'Z':
		return '2'
	case 'D', 'T':
		return '3'
	case 'L':
		return '4'
	case 'M', 'N':
		return '5'
	case 'R':
		return '6'
	}
	return '0' // vowels, H, W, Y
}

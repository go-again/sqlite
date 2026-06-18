package fuzzy

import (
	"regexp"
	"strings"
)

// Caverphone 2.0 (Caversham Project, 2004): a phonetic code tuned for
// matching names, especially anglicised ones. The algorithm is a fixed,
// ordered pipeline of replacements that ends in a 10-character code
// padded with '1's — two names that sound alike collapse to the same
// code. Reference: https://caversham.otago.ac.nz/files/working/ctp150804.pdf

var (
	reRunS = regexp.MustCompile(`s+`)
	reRunT = regexp.MustCompile(`t+`)
	reRunP = regexp.MustCompile(`p+`)
	reRunK = regexp.MustCompile(`k+`)
	reRunF = regexp.MustCompile(`f+`)
	reRunM = regexp.MustCompile(`m+`)
	reRunN = regexp.MustCompile(`n+`)
)

func caverphone(s string) string {
	// 1–2: lowercase, keep only a–z.
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' {
			b.WriteRune(r)
		}
	}
	w := b.String()
	if w == "" {
		return "1111111111"
	}

	// 3: start sequences.
	for _, p := range [][2]string{
		{"cough", "cou2f"}, {"rough", "rou2f"}, {"tough", "tou2f"},
		{"enough", "enou2f"}, {"trough", "trou2f"},
	} {
		if strings.HasPrefix(w, p[0]) {
			w = p[1] + w[len(p[0]):]
			break
		}
	}
	if strings.HasPrefix(w, "gn") {
		w = "2n" + w[2:]
	}
	// 4: end "mb".
	if strings.HasSuffix(w, "mb") {
		w = w[:len(w)-2] + "m2"
	}

	// 5: the main consonant table (order matters).
	for _, p := range [][2]string{
		{"cq", "2q"}, {"ci", "si"}, {"ce", "se"}, {"cy", "sy"},
		{"tch", "2ch"}, {"c", "k"}, {"q", "k"}, {"x", "k"}, {"v", "f"},
		{"dg", "2g"}, {"tio", "sio"}, {"tia", "sia"}, {"d", "t"},
		{"ph", "fh"}, {"b", "p"}, {"sh", "s2"}, {"z", "s"},
	} {
		w = strings.ReplaceAll(w, p[0], p[1])
	}

	// 6: vowels — initial → A, others → 3.
	w = replaceInitialVowel(w)
	w = replaceVowels(w)

	// 7: j / y handling.
	w = strings.ReplaceAll(w, "j", "y")
	switch {
	case strings.HasPrefix(w, "y3"):
		w = "Y3" + w[2:]
	case strings.HasPrefix(w, "y"):
		w = "A" + w[1:]
	}
	w = strings.ReplaceAll(w, "y", "3")

	// 8: gh / g.
	w = strings.ReplaceAll(w, "3gh3", "3kh3")
	w = strings.ReplaceAll(w, "gh", "22")
	w = strings.ReplaceAll(w, "g", "k")

	// 9: collapse consonant runs to a single uppercase letter.
	w = reRunS.ReplaceAllString(w, "S")
	w = reRunT.ReplaceAllString(w, "T")
	w = reRunP.ReplaceAllString(w, "P")
	w = reRunK.ReplaceAllString(w, "K")
	w = reRunF.ReplaceAllString(w, "F")
	w = reRunM.ReplaceAllString(w, "M")
	w = reRunN.ReplaceAllString(w, "N")

	// 10–13: w / h / r / l, with initial and final special cases.
	w = strings.ReplaceAll(w, "w3", "W3")
	w = strings.ReplaceAll(w, "wh3", "Wh3")
	w = trimSuffixReplace(w, "w", "3")
	w = strings.ReplaceAll(w, "w", "2")
	if strings.HasPrefix(w, "h") {
		w = "A" + w[1:]
	}
	w = strings.ReplaceAll(w, "h", "2")
	w = strings.ReplaceAll(w, "r3", "R3")
	w = trimSuffixReplace(w, "r", "3")
	w = strings.ReplaceAll(w, "r", "2")
	w = strings.ReplaceAll(w, "l3", "L3")
	w = trimSuffixReplace(w, "l", "3")
	w = strings.ReplaceAll(w, "l", "2")

	// 14: drop 2; final 3 → A; drop 3.
	w = strings.ReplaceAll(w, "2", "")
	if strings.HasSuffix(w, "3") {
		w = w[:len(w)-1] + "A"
	}
	w = strings.ReplaceAll(w, "3", "")

	// 15: pad and clip to 10.
	w += "1111111111"
	return w[:10]
}

func replaceInitialVowel(w string) string {
	if w == "" {
		return w
	}
	switch w[0] {
	case 'a', 'e', 'i', 'o', 'u':
		return "A" + w[1:]
	}
	return w
}

func replaceVowels(w string) string {
	return strings.NewReplacer("a", "3", "e", "3", "i", "3", "o", "3", "u", "3").Replace(w)
}

// trimSuffixReplace replaces a trailing single-char old with new (the
// "if it ends with X" rule in the Caverphone spec).
func trimSuffixReplace(w, old, new string) string {
	if strings.HasSuffix(w, old) {
		return w[:len(w)-len(old)] + new
	}
	return w
}

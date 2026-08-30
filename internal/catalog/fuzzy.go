package catalog

import "strings"

// Fuzzy matching, reached with the ~ prefix on a term: ~presly, artist:~presly.
//
// It is opt-in rather than automatic because the plain search is a filter and
// people rely on it being one — a script that pipes `find -format path` into a
// playlist wants the tracks it asked for, not the tracks that were nearly it.
// Paying for the fuzzy pass only when asked also keeps the common case a
// linear substring scan, which is what makes 100,000 tracks feel instant.
//
// A score is three attempts in descending order of confidence, and the first
// one that lands wins:
//
//	substring    "presley" inside "elvis presley"        0.75 – 1.00
//	subsequence  "elvpres" spread across it, in order    0.45 – 0.74
//	typo         "presly", within a bounded edit distance
//
// The bands do not overlap, so a literal hit always outranks a spread-out one
// and a spread-out one always outranks a misspelling, whatever the bonuses
// inside a band add up to. That ordering is the point of the score: the caller
// sorts by it.
const (
	// fuzzyFloor is the score below which a match is not worth reporting.
	// Without it a three-letter pattern subsequences into half the library.
	fuzzyFloor = 0.45

	// fuzzyCeil caps the weaker two tiers so they stay below the substring
	// band's floor of 0.75.
	fuzzyCeil = 0.74

	// minTypoLen is the shortest pattern a typo allowance applies to. Below
	// it, one edit is most of the word and everything looks like everything.
	minTypoLen = 4

	// maxTypoLen bounds the edit-distance table so it lives on the stack.
	// Longer patterns simply do not get the typo tier.
	maxTypoLen = 64
)

// fuzzyScore rates how well pattern matches text. Both must already be folded.
// It returns 0 for no match and 1 for an exact one.
//
// The anchors mean the same thing they do in an exact term: ^ pins the match
// to the start of the field, $ to the end, and both together demand the whole
// field.
func fuzzyScore(text, pattern string, aStart, aEnd bool) float64 {
	if pattern == "" {
		if text == "" {
			return 1
		}
		return 0
	}
	if text == "" {
		return 0
	}

	if s := scoreSubstring(text, pattern, aStart, aEnd); s > 0 {
		return s
	}

	// A multi-word pattern is scored word by word, because the whole phrase
	// having failed above says nothing about the words in it: "elvis presly"
	// should find "Elvis Presley" on the strength of one clean word and one
	// near miss. The average is used rather than the best, so that a query
	// with one word right and one word wrong ranks below one with both right.
	if i := strings.IndexByte(pattern, ' '); i >= 0 {
		words := strings.Fields(pattern)
		if len(words) > 1 {
			total := 0.0
			for n, w := range words {
				// The anchors belong to the ends of the phrase, so they apply
				// to the first and last word only.
				s := fuzzyScore(text, w, aStart && n == 0, aEnd && n == len(words)-1)
				if s == 0 {
					return 0
				}
				total += s
			}
			return min(total/float64(len(words)), fuzzyCeil)
		}
	}

	if s := scoreSubsequence(text, pattern, aStart, aEnd); s > 0 {
		return s
	}
	return scoreTypo(text, pattern, aStart, aEnd)
}

// scoreSubstring rates a literal hit. An exact whole-field match scores 1;
// everything else is graded on how much of the field the pattern accounts for
// and whether it starts at a word.
func scoreSubstring(text, pattern string, aStart, aEnd bool) float64 {
	idx := 0
	switch {
	case aStart && aEnd:
		if text != pattern {
			return 0
		}
	case aStart:
		if !strings.HasPrefix(text, pattern) {
			return 0
		}
	case aEnd:
		if !strings.HasSuffix(text, pattern) {
			return 0
		}
		idx = len(text) - len(pattern)
	default:
		if idx = strings.Index(text, pattern); idx < 0 {
			return 0
		}
	}

	s := 0.75 + 0.15*float64(len(pattern))/float64(len(text))
	switch {
	case idx == 0:
		s += 0.10
	case isBoundary(text[idx-1]):
		s += 0.05
	}
	return min(s, 1)
}

// scoreSubsequence rates the pattern's characters appearing in order but not
// together. The greedy leftmost match is used rather than the best possible
// one: finding the optimal alignment costs a DP table per field per track, and
// the leftmost match orders results the same way in all but contrived cases.
//
// It compares bytes rather than runes. Folded text is overwhelmingly ASCII,
// and the worst a split multi-byte character can do is nudge a score.
func scoreSubsequence(text, pattern string, aStart, aEnd bool) float64 {
	n, m := len(text), len(pattern)
	if m > n {
		return 0
	}

	runs, bounds, j, prev := 0, 0, 0, -2
	for i := 0; i < n && j < m; i++ {
		if text[i] != pattern[j] {
			continue
		}
		if i != prev+1 {
			// The leftmost match takes the first occurrence of the pattern's
			// first character, so if that is not the start of the field, no
			// anchored match exists at all.
			if j == 0 && aStart && i != 0 {
				return 0
			}
			runs++
			if i == 0 || isBoundary(text[i-1]) {
				bounds++
			}
		}
		prev, j = i, j+1
	}
	if j < m {
		return 0
	}
	if aEnd && prev != n-1 {
		return 0
	}

	// Three things make a spread-out match convincing: few gaps, runs that
	// begin at word starts, and not being lost in a very long field.
	contig := 1.0
	if m > 1 {
		contig = float64(m-runs) / float64(m-1)
	}
	s := 0.35 + 0.25*contig + 0.10*float64(bounds)/float64(runs) +
		0.05*float64(m)/float64(n)
	if s < fuzzyFloor {
		return 0
	}
	return min(s, fuzzyCeil)
}

// scoreTypo rates a misspelling by bounded edit distance.
//
// The comparison is against each word of the field as well as the whole of it,
// because a typo in one word of a long title is still one typo — measured
// against the whole title it would be a dozen edits and would never be found.
func scoreTypo(text, pattern string, aStart, aEnd bool) float64 {
	m := len(pattern)
	if m < minTypoLen || m > maxTypoLen {
		return 0
	}
	max := 1
	switch {
	case m >= 10:
		max = 3
	case m >= 7:
		max = 2
	}

	// One scratch table for every candidate this field offers, because
	// declaring it per candidate would cost more zeroing than the comparisons
	// it holds. See typoRows.
	var rows typoRows

	best, found := max+1, false
	try := func(candidate string) {
		if d, ok := editDistance(candidate, pattern, max, &rows); ok && d < best {
			best, found = d, true
		}
	}

	switch {
	case aStart && aEnd:
		try(text) // the whole field, or nothing
	case aStart:
		// Anchored to the start, the candidates are the field's leading runs
		// of about the pattern's length.
		for l := m - max; l <= m+max; l++ {
			if l > 0 && l <= len(text) {
				try(text[:l])
			}
		}
	case aEnd:
		for l := m - max; l <= m+max; l++ {
			if l > 0 && l <= len(text) {
				try(text[len(text)-l:])
			}
		}
	default:
		try(text)
		// The words are walked in place rather than split out: this runs for
		// every field of every track, and a slice per field would be the
		// largest single cost in the search.
		for start, i := -1, 0; i <= len(text); i++ {
			if i == len(text) || isBoundary(text[i]) {
				if start >= 0 {
					try(text[start:i])
					start = -1
				}
				continue
			}
			if start < 0 {
				start = i
			}
		}
	}
	if !found {
		return 0
	}

	s := 0.20 + 0.50*(1-float64(best)/float64(m))
	if s < fuzzyFloor {
		return 0
	}
	return min(s, fuzzyCeil)
}

// isBoundary reports whether a byte separates words. NUL is included because
// it separates fields inside the search blob, and the punctuation because
// "presley/elvis" holds two words however it is spelled.
func isBoundary(c byte) bool {
	switch c {
	case ' ', '\t', 0, '-', '_', '/', '\\', '.', ',', '(', ')', '[', ']', '&':
		return true
	}
	return false
}

// typoRows is the scratch space for editDistance: three rows of its dynamic
// programming table.
//
// It is uint8 because the bound admits no distance longer than the pattern,
// and it is passed in rather than declared inside editDistance because Go
// zeroes it on declaration — scoring one field against a dozen words should
// pay for that once, not a dozen times. It is the difference between a fuzzy
// search of a hundred thousand tracks taking tens of milliseconds and taking
// hundreds.
type typoRows [3][maxTypoLen + 1]uint8

// editDistance is Damerau-Levenshtein — insert, delete, substitute and the
// transposition of two adjacent characters — abandoned as soon as every
// alignment in flight has already cost more than max.
//
// Transposition is in because "Presely" for "Presley" is the single commonest
// typing mistake there is, and plain Levenshtein charges two edits for it.
func editDistance(a, b string, max int, rows *typoRows) (int, bool) {
	la, lb := len(a), len(b)
	if lb > maxTypoLen || la-lb > max || lb-la > max {
		return 0, false
	}
	if a == b {
		return 0, true
	}

	prev2, prev, cur := rows[0][:lb+1], rows[1][:lb+1], rows[2][:lb+1]
	for j := 0; j <= lb; j++ {
		prev[j] = uint8(j)
	}

	for i := 1; i <= la; i++ {
		cur[0] = uint8(i)
		rowMin := i
		for j := 1; j <= lb; j++ {
			cost := uint8(1)
			if a[i-1] == b[j-1] {
				cost = 0
			}
			v := min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				v = min(v, prev2[j-2]+1)
			}
			cur[j] = v
			rowMin = min(rowMin, int(v))
		}
		if rowMin > max {
			return 0, false
		}
		prev2, prev, cur = prev, cur, prev2
	}

	d := int(prev[lb])
	return d, d <= max
}

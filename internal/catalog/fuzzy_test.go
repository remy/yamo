package catalog

import "testing"

func score(text, pattern string) float64 {
	return fuzzyScore(Fold(text), Fold(pattern), false, false)
}

func TestFuzzyScoreTiers(t *testing.T) {
	// The bands must not overlap, or the score stops being an ordering.
	exact := score("Elvis Presley", "elvis presley")
	substr := score("Elvis Presley", "presley")
	subseq := score("Elvis Presley", "elvpres")
	typo := score("Elvis Presley", "presly")

	if exact != 1 {
		t.Errorf("an exact match scored %.3f, want 1", exact)
	}
	if !(exact > substr && substr >= 0.75) {
		t.Errorf("substring %.3f is not in its band below exact %.3f", substr, exact)
	}
	if !(substr > subseq && subseq >= fuzzyFloor && subseq <= fuzzyCeil) {
		t.Errorf("subsequence %.3f is not in its band below substring %.3f", subseq, substr)
	}
	if !(typo >= fuzzyFloor && typo <= fuzzyCeil) {
		t.Errorf("typo scored %.3f, want it inside [%.2f, %.2f]", typo, fuzzyFloor, fuzzyCeil)
	}
}

func TestFuzzyScoreMatches(t *testing.T) {
	hits := []struct{ text, pattern string }{
		{"Elvis Presley", "presly"},       // a dropped letter
		{"Elvis Presley", "prelsey"},      // two letters swapped
		{"Elvis Presley", "elvis presly"}, // one word right, one wrong
		{"Radiohead", "radiohed"},
		{"Björk", "bjork"}, // folding still applies underneath
		{"Elvis Costello", "elvcos"},
		{"The Dark Side of the Moon", "drak side"},
	}
	for _, h := range hits {
		if s := score(h.text, h.pattern); s == 0 {
			t.Errorf("%q did not match %q", h.pattern, h.text)
		}
	}

	misses := []struct{ text, pattern string }{
		{"Elvis Presley", "bjork"},
		{"Elvis Costello", "presly"},
		{"The Dark Side of the Moon", "abc"}, // scattered letters are not a match
		{"Elvis Presley", "elvis kraftwerk"}, // every word of a phrase must land
		{"Kind of Blue", "bul"},              // too short for a typo allowance
	}
	for _, m := range misses {
		if s := score(m.text, m.pattern); s > 0 {
			t.Errorf("%q matched %q with %.3f, want no match", m.pattern, m.text, s)
		}
	}
}

func TestFuzzyScoreRanking(t *testing.T) {
	// The closer of two candidates must win, whichever tier each lands in.
	if a, b := score("Elvis Presley", "presley"), score("Elvis Presley", "presly"); a <= b {
		t.Errorf("a clean match scored %.3f, no better than a typo at %.3f", a, b)
	}
	// A short field is a better home for a pattern than a long one.
	if a, b := score("Elvis", "elvis"), score("Elvis Presley Live", "elvis"); a <= b {
		t.Errorf("exact %.3f did not beat buried %.3f", a, b)
	}
	// Two typos are worse than one.
	if a, b := score("Radiohead", "radiohed"), score("Radiohead", "radiohd"); a <= b {
		t.Errorf("one typo %.3f did not beat two %.3f", a, b)
	}
}

func TestFuzzyScoreAnchors(t *testing.T) {
	cases := []struct {
		text, pattern string
		aStart, aEnd  bool
		want          bool
	}{
		{"Elvis Presley", "elvis", true, false, true},
		{"Elvis Presley", "presley", true, false, false},
		{"Elvis Presley", "presley", false, true, true},
		{"Elvis Presley", "elvis", false, true, false},
		{"Elvis Presley", "elvis presley", true, true, true},
		{"Elvis Presley", "elvis", true, true, false},
		// The anchors bind the fuzzy tiers too.
		{"Elvis Presley", "elvsi", true, false, true},
		{"Elvis Presley", "presly", true, false, false},
		{"Elvis Presley", "presly", false, true, true},
	}
	for _, c := range cases {
		got := fuzzyScore(Fold(c.text), Fold(c.pattern), c.aStart, c.aEnd) > 0
		if got != c.want {
			t.Errorf("fuzzyScore(%q, %q, ^%v $%v) matched = %v, want %v",
				c.text, c.pattern, c.aStart, c.aEnd, got, c.want)
		}
	}
}

func TestEditDistance(t *testing.T) {
	cases := []struct {
		a, b string
		max  int
		want int
		ok   bool
	}{
		{"presley", "presley", 2, 0, true},
		{"presley", "presly", 2, 1, true},   // a deletion
		{"presley", "presleyy", 2, 1, true}, // an insertion
		{"presley", "presiey", 2, 1, true},  // a substitution
		{"presley", "prelsey", 2, 1, true},  // a transposition, which is one edit
		{"presley", "bjork", 2, 0, false},   // beyond the bound
		{"presley", "", 2, 0, false},
	}
	var rows typoRows
	for _, c := range cases {
		got, ok := editDistance(c.a, c.b, c.max, &rows)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("editDistance(%q, %q, %d) = %d, %v; want %d, %v",
				c.a, c.b, c.max, got, ok, c.want, c.ok)
		}
	}
}

func TestSearchFuzzy(t *testing.T) {
	c := makeCatalog(600)
	ix := c.Index()

	count := func(q string) int { return len(ix.Search(ParseQuery(q))) }

	if n := count("artist:presly"); n != 0 {
		t.Errorf("an exact term matched %d misspellings, want 0", n)
	}
	if n := count("artist:~presly"); n != 100 {
		t.Errorf("artist:~presly matched %d, want the 100 Elvis tracks", n)
	}
	// Folding and fuzziness compose: a misspelt, unaccented album still lands.
	if n := count("album:~homogenik"); n != 100 {
		t.Errorf("album:~homogenik matched %d, want 100", n)
	}
	// A bare fuzzy term searches the display fields, not the path.
	if n := count("~presly"); n != 100 {
		t.Errorf("~presly matched %d, want 100", n)
	}
	// Fuzzy terms combine with exact ones, and with negation.
	if n := count("artist:~presly year:>2000"); n == 0 || n >= 100 {
		t.Errorf("artist:~presly year:>2000 matched %d, want some but not all", n)
	}
	if n := count("-artist:~presly"); n != 500 {
		t.Errorf("-artist:~presly matched %d, want 500", n)
	}

	// The scores must rank a literal hit above a misspelt one.
	literal := ix.SearchScored(ParseQuery("artist:~presley"))
	near := ix.SearchScored(ParseQuery("artist:~presly"))
	if len(literal) == 0 || len(near) == 0 {
		t.Fatal("a fuzzy search returned nothing to score")
	}
	if literal[0].Score <= near[0].Score {
		t.Errorf("a literal hit scored %.3f, no better than a typo at %.3f",
			literal[0].Score, near[0].Score)
	}
	// A whole field matched exactly is the only thing that scores 1.
	whole := ix.SearchScored(ParseQuery(`artist:"~elvis presley"`))
	if len(whole) == 0 || whole[0].Score != 1 {
		t.Errorf("a whole-field match scored %v, want 1", whole)
	}

	// An exact query scores 1 throughout, so nothing downstream has to know
	// whether a score is meaningful except by asking the query.
	if ParseQuery("artist:elvis").Fuzzy() {
		t.Error("an exact query reported itself as fuzzy")
	}
	if !ParseQuery("artist:~elvis").Fuzzy() {
		t.Error("a fuzzy query did not report itself as fuzzy")
	}
	// A negated fuzzy term ranks nothing, so it does not make a query fuzzy.
	if ParseQuery("-artist:~elvis").Fuzzy() {
		t.Error("a negated fuzzy term made the query fuzzy")
	}
}

func TestSearchAnchors(t *testing.T) {
	c := makeCatalog(600)
	ix := c.Index()

	count := func(q string) int { return len(ix.Search(ParseQuery(q))) }

	if n := count("artist:^elvis"); n != 100 {
		t.Errorf("artist:^elvis matched %d, want 100", n)
	}
	if n := count("artist:^presley"); n != 0 {
		t.Errorf("artist:^presley matched %d, want 0", n)
	}
	if n := count("artist:presley$"); n != 100 {
		t.Errorf("artist:presley$ matched %d, want 100", n)
	}
	if n := count("artist:elvis$"); n != 0 {
		t.Errorf("artist:elvis$ matched %d, want 0", n)
	}
	if n := count(`artist:"^elvis presley$"`); n != 100 {
		t.Errorf("an exact whole-field match found %d, want 100", n)
	}
	if n := count(`artist:"^elvis$"`); n != 0 {
		t.Errorf("a partial whole-field match found %d, want 0", n)
	}
	// An unqualified anchor asks whether some field begins with the value,
	// not whether the concatenation of them all does.
	if n := count("^homogenic"); n != 100 {
		t.Errorf("^homogenic matched %d, want the 100 tracks on that album", n)
	}
	if n := count("^resley"); n != 0 {
		t.Errorf("^resley matched %d, want 0", n)
	}
}

func TestParseMarkers(t *testing.T) {
	// A marker with nothing to apply to is the character itself, so the forms
	// that were valid before the markers existed still mean what they did.
	if q := ParseQuery("album:"); len(q.terms) != 1 || q.terms[0].value != "" || q.terms[0].anchorStart {
		t.Errorf("album: parsed as %+v", q.terms)
	}
	if q := ParseQuery("album:^"); len(q.terms) != 1 || q.terms[0].value != "^" {
		t.Errorf("album:^ parsed as %+v", q.terms)
	}
	if q := ParseQuery("artist:~presly"); len(q.terms) != 1 ||
		!q.terms[0].fuzzy || q.terms[0].value != "presly" {
		t.Errorf("artist:~presly parsed as %+v", q.terms)
	}
	if q := ParseQuery("artist:~^presly$"); len(q.terms) != 1 || !q.terms[0].fuzzy ||
		!q.terms[0].anchorStart || !q.terms[0].anchorEnd || q.terms[0].value != "presly" {
		t.Errorf("artist:~^presly$ parsed as %+v", q.terms)
	}
	// Numeric fields have nothing to loosen or tighten, so the markers come
	// off and the comparison stays exact.
	if q := ParseQuery("year:~1977"); len(q.terms) != 1 || q.terms[0].fuzzy ||
		q.terms[0].op != cmpEq || q.terms[0].lo != 1977 {
		t.Errorf("year:~1977 parsed as %+v", q.terms)
	}
}

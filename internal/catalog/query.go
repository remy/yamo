package catalog

import "strings"

// fieldAny is the sentinel for a bare term, which matches any text field.
const fieldAny = numFields

// cmpOp is the comparison used by a numeric term.
type cmpOp uint8

const (
	cmpNone cmpOp = iota
	cmpEq
	cmpLT
	cmpLTE
	cmpGT
	cmpGTE
	cmpRange
)

// term is one clause of a query. All terms in a query must match.
type term struct {
	field  Field
	negate bool
	value  string // folded, for text matching
	op     cmpOp
	lo, hi int32
}

// Query is a parsed search expression.
//
// The syntax is deliberately small and unquoted-friendly, because it is typed
// live into a search bar:
//
//	elvis                 any text field contains "elvis"
//	artist:elvis          the artist field contains "elvis"
//	artist:"elvis presley" quoted values may contain spaces
//	year:1977             exact year
//	year:>1980            comparison; <, <=, >, >= all work
//	year:1970-1979        inclusive range
//	-genre:christmas      negation: exclude matches
//	album:                the field is empty (useful for finding gaps)
//
// Terms are ANDed. An empty query matches everything.
type Query struct {
	Raw   string
	terms []term
}

// Empty reports whether the query imposes no constraints.
func (q *Query) Empty() bool { return len(q.terms) == 0 }

// ParseQuery compiles a query string. It never fails: anything unparseable is
// treated as literal text, so a half-typed query still does something useful.
func ParseQuery(s string) *Query {
	q := &Query{Raw: s}
	for _, tok := range tokenise(s) {
		if t, ok := compileTerm(tok); ok {
			q.terms = append(q.terms, t)
		}
	}
	return q
}

// token is one whitespace-delimited chunk, with quoting resolved.
type token struct {
	text   string
	quoted bool // a quoted value is never re-interpreted as field:value
}

// tokenise splits on whitespace, honouring double quotes. A quote may open
// after a field prefix, so artist:"elvis presley" stays one token.
func tokenise(s string) []token {
	var out []token
	var cur strings.Builder
	inQuote := false
	sawQuote := false

	flush := func() {
		if cur.Len() > 0 {
			out = append(out, token{text: cur.String(), quoted: sawQuote})
			cur.Reset()
		}
		sawQuote = false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			inQuote = !inQuote
			sawQuote = true
		case !inQuote && (c == ' ' || c == '\t'):
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return out
}

// compileTerm turns one token into a matcher clause.
func compileTerm(tok token) (term, bool) {
	t := term{field: fieldAny}
	s := tok.text
	if strings.HasPrefix(s, "-") && len(s) > 1 {
		t.negate = true
		s = s[1:]
	}

	// A field prefix only counts when the name resolves; "AC:DC" and times
	// like "3:04" should stay literal text.
	if i := strings.IndexByte(s, ':'); i > 0 && !tok.quoted {
		if f, ok := LookupField(s[:i]); ok {
			t.field = f
			s = s[i+1:]
		}
	} else if i > 0 && tok.quoted {
		// The quote came after the colon: artist:"elvis presley".
		if f, ok := LookupField(s[:i]); ok {
			t.field = f
			s = s[i+1:]
		}
	}

	if t.field == FieldYear || t.field == FieldTrackNo || t.field == FieldDisc ||
		t.field == FieldCompilation {
		if op, lo, hi, ok := parseNumeric(s); ok {
			t.op, t.lo, t.hi = op, lo, hi
			return t, true
		}
	}

	t.value = Fold(s)
	// A bare "-" or an empty unqualified term constrains nothing.
	if t.value == "" && t.field == fieldAny {
		return t, false
	}
	return t, true
}

// parseNumeric compiles the comparison forms accepted for numeric fields.
func parseNumeric(s string) (op cmpOp, lo, hi int32, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return cmpEq, 0, 0, true // year: finds tracks with no year
	}
	switch {
	case strings.HasPrefix(s, ">="):
		return cmpGTE, atoi32(s[2:]), 0, isNumeric(s[2:])
	case strings.HasPrefix(s, "<="):
		return cmpLTE, atoi32(s[2:]), 0, isNumeric(s[2:])
	case strings.HasPrefix(s, ">"):
		return cmpGT, atoi32(s[1:]), 0, isNumeric(s[1:])
	case strings.HasPrefix(s, "<"):
		return cmpLT, atoi32(s[1:]), 0, isNumeric(s[1:])
	}
	// A hyphen after the first character is a range, not a negative number.
	if i := strings.IndexByte(s[1:], '-'); i >= 0 {
		a, b := s[:i+1], s[i+2:]
		if isNumeric(a) && isNumeric(b) {
			return cmpRange, atoi32(a), atoi32(b), true
		}
	}
	if isNumeric(s) {
		return cmpEq, atoi32(s), 0, true
	}
	return cmpNone, 0, 0, false
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// matchNumeric evaluates a compiled numeric clause.
func (t *term) matchNumeric(v int32) bool {
	switch t.op {
	case cmpEq:
		return v == t.lo
	case cmpLT:
		return v < t.lo
	case cmpLTE:
		return v <= t.lo
	case cmpGT:
		return v > t.lo
	case cmpGTE:
		return v >= t.lo
	case cmpRange:
		return v >= t.lo && v <= t.hi
	}
	return false
}

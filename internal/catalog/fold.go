package catalog

import (
	"strings"
	"unicode"
)

// Fold normalises a string for searching: lower case, with Latin diacritics
// stripped so "Bjork" matches "Björk" and "Beyonce" matches "Beyoncé".
//
// The ASCII fast path returns the input unchanged when nothing needs doing,
// which is the case for most of a typical library and avoids an allocation.
func Fold(s string) string {
	clean := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= utf8Start || (c >= 'A' && c <= 'Z') {
			clean = false
			break
		}
	}
	if clean {
		return s
	}

	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r < utf8Start {
			if r >= 'A' && r <= 'Z' {
				r += 'a' - 'A'
			}
			b.WriteRune(r)
			continue
		}
		r = unicode.ToLower(r)
		if repl, ok := latinFold[r]; ok {
			b.WriteString(repl)
			continue
		}
		if unicode.Is(unicode.Mn, r) {
			continue // a combining mark left over from decomposed input
		}
		b.WriteRune(r)
	}
	return b.String()
}

const utf8Start = 0x80

// latinFold maps lower-cased accented Latin letters to their base forms. It
// covers Latin-1 Supplement and Latin Extended-A, which between them account
// for essentially every accented character in Western music metadata.
var latinFold = map[rune]string{
	'à': "a", 'á': "a", 'â': "a", 'ã': "a", 'ä': "a", 'å': "a", 'ā': "a",
	'ă': "a", 'ą': "a", 'æ': "ae",
	'ç': "c", 'ć': "c", 'ĉ': "c", 'ċ': "c", 'č': "c",
	'ď': "d", 'đ': "d", 'ð': "d",
	'è': "e", 'é': "e", 'ê': "e", 'ë': "e", 'ē': "e", 'ĕ': "e", 'ė': "e",
	'ę': "e", 'ě': "e",
	'ĝ': "g", 'ğ': "g", 'ġ': "g", 'ģ': "g",
	'ĥ': "h", 'ħ': "h",
	'ì': "i", 'í': "i", 'î': "i", 'ï': "i", 'ĩ': "i", 'ī': "i", 'ĭ': "i",
	'į': "i", 'ı': "i", 'ĳ': "ij",
	'ĵ': "j", 'ķ': "k",
	'ĺ': "l", 'ļ': "l", 'ľ': "l", 'ŀ': "l", 'ł': "l",
	'ñ': "n", 'ń': "n", 'ņ': "n", 'ň': "n", 'ŉ': "n", 'ŋ': "n",
	'ò': "o", 'ó': "o", 'ô': "o", 'õ': "o", 'ö': "o", 'ø': "o", 'ō': "o",
	'ŏ': "o", 'ő': "o", 'œ': "oe",
	'ŕ': "r", 'ŗ': "r", 'ř': "r",
	'ś': "s", 'ŝ': "s", 'ş': "s", 'š': "s", 'ſ': "s", 'ß': "ss",
	'ţ': "t", 'ť': "t", 'ŧ': "t", 'þ': "th",
	'ù': "u", 'ú': "u", 'û': "u", 'ü': "u", 'ũ': "u", 'ū': "u", 'ŭ': "u",
	'ů': "u", 'ű': "u", 'ų': "u",
	'ŵ': "w",
	'ý': "y", 'ÿ': "y", 'ŷ': "y",
	'ź': "z", 'ż': "z", 'ž': "z",
}

package tags

import (
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Text encodings as used by the ID3v2 encoding byte.
const (
	encISO8859 = 0
	encUTF16   = 1 // with BOM
	encUTF16BE = 2 // without BOM
	encUTF8    = 3
)

// decodeText converts a raw ID3 text payload to a Go string. It trims trailing
// NULs, which many taggers append and some append several of.
func decodeText(enc byte, b []byte) string {
	switch enc {
	case encUTF8:
		return trimNulUTF8(string(b))
	case encUTF16:
		return decodeUTF16BOM(b)
	case encUTF16BE:
		return decodeUTF16(b, true)
	default:
		return decodeLatin1(b)
	}
}

func trimNulUTF8(s string) string {
	s = strings.TrimRight(s, "\x00")
	if !utf8.ValidString(s) {
		// Salvage what we can rather than dropping the field entirely.
		return strings.ToValidUTF8(s, "")
	}
	return s
}

// decodeLatin1 maps ISO-8859-1 bytes to runes. Every byte is a valid code
// point, so this cannot fail.
func decodeLatin1(b []byte) string {
	b = trimTrailingNul(b)
	ascii := true
	for _, c := range b {
		if c >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return string(b)
	}
	var sb strings.Builder
	sb.Grow(len(b) * 2)
	for _, c := range b {
		sb.WriteRune(rune(c))
	}
	return sb.String()
}

func decodeUTF16BOM(b []byte) string {
	if len(b) < 2 {
		return ""
	}
	switch {
	case b[0] == 0xFF && b[1] == 0xFE:
		return decodeUTF16(b[2:], false)
	case b[0] == 0xFE && b[1] == 0xFF:
		return decodeUTF16(b[2:], true)
	}
	// No BOM despite the encoding byte claiming one. Assume little endian,
	// which is what the taggers that get this wrong tend to emit.
	return decodeUTF16(b, false)
}

func decodeUTF16(b []byte, bigEndian bool) string {
	if len(b) < 2 {
		return ""
	}
	n := len(b) &^ 1 // ignore a stray trailing byte
	u := make([]uint16, 0, n/2)
	for i := 0; i < n; i += 2 {
		var v uint16
		if bigEndian {
			v = uint16(b[i])<<8 | uint16(b[i+1])
		} else {
			v = uint16(b[i+1])<<8 | uint16(b[i])
		}
		if v == 0 {
			break // NUL terminates
		}
		u = append(u, v)
	}
	return string(utf16.Decode(u))
}

func trimTrailingNul(b []byte) []byte {
	for len(b) > 0 && b[len(b)-1] == 0 {
		b = b[:len(b)-1]
	}
	return b
}

// parseIntPrefix reads a leading base-10 integer, ignoring anything after it.
// Tag values are routinely "3/12", "2005-06-01" or "  7 ", and a strict parse
// would discard all of those.
func parseIntPrefix(s string) int32 {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	neg := false
	if i < len(s) && (s[i] == '-' || s[i] == '+') {
		neg = s[i] == '-'
		i++
	}
	start := i
	var n int64
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		n = n*10 + int64(s[i]-'0')
		if n > 1<<31-1 {
			return 0 // nonsense, not a real tag value
		}
		i++
	}
	if i == start {
		return 0
	}
	if neg {
		return int32(-n)
	}
	return int32(n)
}

// parsePair splits the "n/total" form used by track and disc numbers.
func parsePair(s string) (num, total int32) {
	if i := strings.IndexAny(s, "/-"); i >= 0 {
		return parseIntPrefix(s[:i]), parseIntPrefix(s[i+1:])
	}
	return parseIntPrefix(s), 0
}

// parseYear extracts a four-digit year from any of the date shapes that appear
// in the wild: "1977", "1977-08-16", "16/08/1977", "1977-08-16T12:00:00Z".
func parseYear(s string) int32 {
	s = strings.TrimSpace(s)
	// Scan for the first run of exactly four digits that reads like a year.
	for i := 0; i+4 <= len(s); i++ {
		if !isDigit(s[i]) {
			continue
		}
		if i > 0 && isDigit(s[i-1]) {
			continue
		}
		if !isDigit(s[i+1]) || !isDigit(s[i+2]) || !isDigit(s[i+3]) {
			continue
		}
		if i+4 < len(s) && isDigit(s[i+4]) {
			continue
		}
		y, _ := strconv.Atoi(s[i : i+4])
		if y >= 1000 && y <= 3000 {
			return int32(y)
		}
	}
	return 0
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// id3v1Genres is the numeric genre table. ID3v2.3 tags frequently store a
// genre as "(17)" or "(17)Hard Rock" rather than plain text.
var id3v1Genres = [...]string{
	"Blues", "Classic Rock", "Country", "Dance", "Disco", "Funk", "Grunge",
	"Hip-Hop", "Jazz", "Metal", "New Age", "Oldies", "Other", "Pop", "R&B",
	"Rap", "Reggae", "Rock", "Techno", "Industrial", "Alternative", "Ska",
	"Death Metal", "Pranks", "Soundtrack", "Euro-Techno", "Ambient",
	"Trip-Hop", "Vocal", "Jazz+Funk", "Fusion", "Trance", "Classical",
	"Instrumental", "Acid", "House", "Game", "Sound Clip", "Gospel", "Noise",
	"AlternRock", "Bass", "Soul", "Punk", "Space", "Meditative",
	"Instrumental Pop", "Instrumental Rock", "Ethnic", "Gothic", "Darkwave",
	"Techno-Industrial", "Electronic", "Pop-Folk", "Eurodance", "Dream",
	"Southern Rock", "Comedy", "Cult", "Gangsta", "Top 40", "Christian Rap",
	"Pop/Funk", "Jungle", "Native American", "Cabaret", "New Wave",
	"Psychadelic", "Rave", "Showtunes", "Trailer", "Lo-Fi", "Tribal",
	"Acid Punk", "Acid Jazz", "Polka", "Retro", "Musical", "Rock & Roll",
	"Hard Rock", "Folk", "Folk-Rock", "National Folk", "Swing", "Fast Fusion",
	"Bebob", "Latin", "Revival", "Celtic", "Bluegrass", "Avantgarde",
	"Gothic Rock", "Progressive Rock", "Psychedelic Rock", "Symphonic Rock",
	"Slow Rock", "Big Band", "Chorus", "Easy Listening", "Acoustic", "Humour",
	"Speech", "Chanson", "Opera", "Chamber Music", "Sonata", "Symphony",
	"Booty Bass", "Primus", "Porn Groove", "Satire", "Slow Jam", "Club",
	"Tango", "Samba", "Folklore", "Ballad", "Power Ballad", "Rhythmic Soul",
	"Freestyle", "Duet", "Punk Rock", "Drum Solo", "A capella", "Euro-House",
	"Dance Hall", "Goa", "Drum & Bass", "Club-House", "Hardcore", "Terror",
	"Indie", "BritPop", "Negerpunk", "Polsk Punk", "Beat", "Christian Gangsta",
	"Heavy Metal", "Black Metal", "Crossover", "Contemporary Christian",
	"Christian Rock", "Merengue", "Salsa", "Trash Metal", "Anime", "Jpop",
	"Synthpop",
}

func genreByID(n int) string {
	if n >= 0 && n < len(id3v1Genres) {
		return id3v1Genres[n]
	}
	return ""
}

// isTrueFlag reads the boolean a tag uses for a yes/no field. Taggers disagree
// about how to spell it: "1", "true" and "yes" all turn up, and iTunes writes a
// raw byte that arrives here as "\x01".
func isTrueFlag(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "0", "false", "no", "\x00":
		return false
	}
	return true
}

// normaliseGenre resolves the "(17)", "(17)Hard Rock" and bare "17" forms that
// ID3v2 genre frames use, falling back to the literal text.
func normaliseGenre(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if s[0] == '(' {
		if end := strings.IndexByte(s, ')'); end > 1 {
			inner := s[1:end]
			rest := strings.TrimSpace(s[end+1:])
			if rest != "" {
				return rest // "(17)Hard Rock" -> the text wins
			}
			if n, err := strconv.Atoi(inner); err == nil {
				if g := genreByID(n); g != "" {
					return g
				}
			}
			if inner == "RX" {
				return "Remix"
			}
			if inner == "CR" {
				return "Cover"
			}
			return inner
		}
	}
	// A bare number is a genre reference too.
	if n, err := strconv.Atoi(s); err == nil {
		if g := genreByID(n); g != "" {
			return g
		}
	}
	return s
}

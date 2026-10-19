package tags

import "strings"

// MusicBrainz identifiers are the one piece of metadata that names an artist
// unambiguously. "Genesis" is three different bands; a MusicBrainz artist id is
// one. They are read here so that something outside the file — an artist photo,
// in the first instance — can be looked up without guessing from a name.
//
// Every container spells the two keys differently, and Picard, which writes
// most of them, is not the only writer:
//
//	ID3   TXXX:MusicBrainz Artist Id         TXXX:MusicBrainz Album Artist Id
//	      TXXX:MUSICBRAINZ_ARTISTID (ffmpeg, carrying a Vorbis key across)
//	Vorbis MUSICBRAINZ_ARTISTID              MUSICBRAINZ_ALBUMARTISTID
//	MP4   ----:com.apple.iTunes:MusicBrainz Artist Id (and Album Artist Id)
//	ASF   MusicBrainz/Artist Id              MusicBrainz/Album Artist Id
//
// Reducing a key to its letters, upper-cased, makes all of those the same two
// strings, which is what musicBrainzKey compares against.

const (
	mbArtistKey      = "MUSICBRAINZARTISTID"
	mbAlbumArtistKey = "MUSICBRAINZALBUMARTISTID"
)

// musicBrainzKey reduces a description or field name to its letters,
// upper-cased, so the spellings above compare equal.
func musicBrainzKey(desc string) string {
	var b strings.Builder
	b.Grow(len(desc))
	for i := 0; i < len(desc); i++ {
		c := desc[i]
		switch {
		case c >= 'a' && c <= 'z':
			b.WriteByte(c - 'a' + 'A')
		case c >= 'A' && c <= 'Z':
			b.WriteByte(c)
		}
	}
	return b.String()
}

// applyMusicBrainz stores the ids in vals if desc names one of the two
// MusicBrainz artist keys, and reports whether it did. The first writer wins,
// as it does for every other field.
func applyMusicBrainz(md *Metadata, desc string, vals ...string) bool {
	var dst *string
	switch musicBrainzKey(desc) {
	case mbArtistKey:
		dst = &md.MusicBrainzArtistID
	case mbAlbumArtistKey:
		dst = &md.MusicBrainzAlbumArtistID
	default:
		return false
	}
	setIfEmpty(dst, joinMBIDs(vals...))
	return true
}

// joinMBIDs extracts every well-formed MusicBrainz id from vals, lower-cased
// and de-duplicated, joined the way frameText joins a multi-value frame.
//
// A track by two artists carries two ids, and the writers disagree on how to
// say so: ID3v2.4 separates them with NULs, Picard's ID3v2.3 output with a
// slash, Vorbis and MP4 with a second field or data atom. Splitting on all of
// them and keeping only what is shaped like a UUID handles every one, and
// throws away the junk that occasionally sits in these fields — which matters,
// because an id is later put into a URL.
func joinMBIDs(vals ...string) string {
	var out []string
	for _, v := range vals {
		for _, tok := range strings.FieldsFunc(v, isMBIDSeparator) {
			tok = strings.ToLower(tok)
			if isMBID(tok) && !containsStr(out, tok) {
				out = append(out, tok)
			}
		}
	}
	return strings.Join(out, "; ")
}

func isMBIDSeparator(r rune) bool {
	switch r {
	case 0, '/', ';', ',', ' ', '\t', '\n', '\r':
		return true
	}
	return false
}

// isMBID reports whether s is a lower-case UUID: 8-4-4-4-12 hex digits.
func isMBID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return false
			}
		}
	}
	return true
}

// FirstMBID returns the first id in a field holding one or more, or "" if
// there is none. For a collaboration that is the first-credited artist.
func FirstMBID(ids string) string {
	first, _, _ := strings.Cut(ids, ";")
	if first = strings.TrimSpace(first); isMBID(first) {
		return first
	}
	return ""
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Writing them back. The keys are spelled the way Picard spells them, since
// Picard wrote most of the ids that are already out there and a library that
// mixes two spellings of one key is the mess the reader has to untangle.
const (
	mbArtistDesc           = "MusicBrainz Artist Id"
	mbAlbumArtistDesc      = "MusicBrainz Album Artist Id"
	mbArtistVorbisKey      = "MUSICBRAINZ_ARTISTID"
	mbAlbumArtistVorbisKey = "MUSICBRAINZ_ALBUMARTISTID"
)

// NormaliseMBIDs checks a value meant for one of the id fields and returns it
// in the form the catalogue holds: lower-case UUIDs joined with "; ". Empty is
// valid and means "remove the ids".
//
// It is stricter than joinMBIDs on purpose. Reading keeps whatever is
// UUID-shaped and quietly drops the rest, because a file is what it is; a
// write is a request, and a request with a typo in it should be refused
// rather than half-applied — an id with one digit wrong is a different
// artist, or none, and nothing downstream could tell.
func NormaliseMBIDs(v string) (string, bool) {
	var out []string
	for _, tok := range strings.FieldsFunc(v, isMBIDSeparator) {
		tok = strings.ToLower(tok)
		if !isMBID(tok) {
			return "", false
		}
		if !containsStr(out, tok) {
			out = append(out, tok)
		}
	}
	return strings.Join(out, "; "), true
}

// splitMBIDs is the list behind a normalised value, for the containers that
// store one id per field rather than one string.
func splitMBIDs(v string) []string {
	var out []string
	for _, tok := range strings.FieldsFunc(v, isMBIDSeparator) {
		if tok = strings.ToLower(tok); isMBID(tok) {
			out = append(out, tok)
		}
	}
	return out
}

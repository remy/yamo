package tags

import "strings"

const id3v1Len = 128

// parseID3v1 decodes the 128-byte trailer. tail must be the last 128 bytes of
// the file. It is only consulted for fields ID3v2 left empty, since v2 tags win.
func parseID3v1(tail []byte, md *Metadata) bool {
	if len(tail) < id3v1Len {
		return false
	}
	b := tail[len(tail)-id3v1Len:]
	if b[0] != 'T' || b[1] != 'A' || b[2] != 'G' {
		return false
	}
	setIfEmpty(&md.Title, latin1Field(b[3:33]))
	setIfEmpty(&md.Artist, latin1Field(b[33:63]))
	setIfEmpty(&md.Album, latin1Field(b[63:93]))
	if md.Year == 0 {
		md.Year = parseYear(latin1Field(b[93:97]))
	}

	comment := b[97:127]
	// ID3v1.1 steals the last two comment bytes for a track number.
	if comment[28] == 0 && comment[29] != 0 {
		if md.Track == 0 {
			md.Track = int32(comment[29])
		}
		comment = comment[:28]
	}
	if md.Comment == "" {
		md.Comment = latin1Field(comment)
	}
	if md.Genre == "" {
		md.Genre = genreByID(int(b[127]))
	}
	return true
}

// latin1Field trims the NUL and space padding that fixed-width ID3v1 fields use.
func latin1Field(b []byte) string {
	if i := indexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return strings.TrimSpace(decodeLatin1(b))
}

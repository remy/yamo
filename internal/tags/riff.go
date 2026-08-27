package tags

import (
	"encoding/binary"
	"math"
	"os"
	"strings"
)

// riffChunks walks a RIFF or IFF chunk list, calling fn for each chunk. RIFF
// (WAV) sizes are little-endian; IFF (AIFF) sizes are big-endian. Chunks are
// padded to an even length, which is not counted in the size field.
func riffChunks(b []byte, bigEndian bool, fn func(id string, body []byte) bool) {
	for pos := 0; pos+8 <= len(b); {
		id := string(b[pos : pos+4])
		var n int
		if bigEndian {
			n = be32(b[pos+4 : pos+8])
		} else {
			n = int(binary.LittleEndian.Uint32(b[pos+4 : pos+8]))
		}
		if n < 0 {
			return
		}
		end := pos + 8 + n
		if end > len(b) {
			end = len(b) // truncated by the read window; hand over what we have
		}
		if !fn(id, b[pos+8:end]) {
			return
		}
		pos += 8 + n
		if n%2 == 1 {
			pos++ // pad byte
		}
	}
}

// readRIFF parses a WAV file: the fmt chunk for stream properties, the data
// chunk for duration, and LIST/INFO or an embedded ID3v2 chunk for metadata.
func (r *Reader) readRIFF(f *os.File, size int64, head []byte, md *Metadata) error {
	if len(head) < 12 || string(head[0:4]) != "RIFF" {
		return ErrNoTags
	}
	var byteRate uint32
	var dataBytes int64

	riffChunks(head[12:], false, func(id string, body []byte) bool {
		switch id {
		case "fmt ":
			if len(body) >= 16 {
				md.Channels = uint8(binary.LittleEndian.Uint16(body[2:4]))
				md.SampleRate = int32(binary.LittleEndian.Uint32(body[4:8]))
				byteRate = binary.LittleEndian.Uint32(body[8:12])
			}
		case "data":
			dataBytes = int64(len(body))
		case "LIST":
			if len(body) >= 4 && string(body[0:4]) == "INFO" {
				riffChunks(body[4:], false, func(id string, v []byte) bool {
					applyInfoTag(id, latin1Field(v), md)
					return true
				})
			}
		case "id3 ", "ID3 ":
			if t, err := parseID3v2(body); err == nil {
				t.applyTo(md)
			}
		}
		return true
	})

	// The data chunk's declared size is authoritative, but a truncated read
	// window under-reports it; fall back to the file size.
	if dataBytes <= 0 {
		dataBytes = size - 44
	}
	if byteRate > 0 && dataBytes > 0 {
		md.DurationMS = int32(dataBytes * 1000 / int64(byteRate))
		md.Bitrate = int32(byteRate * 8 / 1000)
	}
	return nil
}

// applyInfoTag maps a RIFF INFO chunk id onto a metadata field.
func applyInfoTag(id, val string, md *Metadata) {
	if val == "" {
		return
	}
	switch id {
	case "INAM", "TITL":
		setIfEmpty(&md.Title, val)
	case "IART":
		setIfEmpty(&md.Artist, val)
	case "IPRD", "IALB":
		setIfEmpty(&md.Album, val)
	case "IGNR":
		setIfEmpty(&md.Genre, normaliseGenre(val))
	case "ICMT":
		setIfEmpty(&md.Comment, val)
	case "IMUS", "IENG":
		setIfEmpty(&md.Composer, val)
	case "ICRD", "IYER":
		if md.Year == 0 {
			md.Year = parseYear(val)
		}
	case "ITRK", "IPRT":
		if md.Track == 0 {
			md.Track, md.TrackTotal = parsePair(val)
		}
	}
}

// readAIFF parses an AIFF/AIFC file: COMM for stream properties and frame
// count, the text chunks for basic metadata, and an ID3 chunk when present.
func (r *Reader) readAIFF(f *os.File, size int64, head []byte, md *Metadata) error {
	if len(head) < 12 || string(head[0:4]) != "FORM" {
		return ErrNoTags
	}
	riffChunks(head[12:], true, func(id string, body []byte) bool {
		switch id {
		case "COMM":
			if len(body) >= 18 {
				md.Channels = uint8(binary.BigEndian.Uint16(body[0:2]))
				frames := int64(binary.BigEndian.Uint32(body[2:6]))
				rate := extended80ToFloat(body[8:18])
				if rate > 0 {
					md.SampleRate = int32(rate)
					md.DurationMS = int32(float64(frames) / rate * 1000)
				}
			}
		case "NAME":
			setIfEmpty(&md.Title, latin1Field(body))
		case "AUTH":
			setIfEmpty(&md.Artist, latin1Field(body))
		case "ANNO":
			setIfEmpty(&md.Comment, latin1Field(body))
		case "ID3 ", "id3 ":
			if t, err := parseID3v2(body); err == nil {
				t.applyTo(md)
			}
		}
		return true
	})
	if md.DurationMS > 0 {
		md.Bitrate = int32(size * 8 / int64(md.DurationMS))
	}
	return nil
}

// extended80ToFloat decodes the 80-bit IEEE 754 extended precision float that
// AIFF uses for its sample rate.
func extended80ToFloat(b []byte) float64 {
	if len(b) < 10 {
		return 0
	}
	sign := 1.0
	if b[0]&0x80 != 0 {
		sign = -1
	}
	exp := int(binary.BigEndian.Uint16(b[0:2]) & 0x7FFF)
	mant := binary.BigEndian.Uint64(b[2:10])
	if exp == 0 && mant == 0 {
		return 0
	}
	if exp == 0x7FFF {
		return 0 // infinity or NaN: not a usable sample rate
	}
	return sign * float64(mant) * math.Pow(2, float64(exp-16383-63))
}

// utf16LEString decodes a NUL-terminated UTF-16LE string, as used throughout ASF.
func utf16LEString(b []byte) string {
	return strings.TrimSpace(decodeUTF16(b, false))
}

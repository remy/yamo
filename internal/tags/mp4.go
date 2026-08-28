package tags

import (
	"encoding/binary"
	"errors"
	"io"
	"strings"
)

// maxMoovSize caps how much of a moov atom is read into memory. Real moov
// atoms are tens of kilobytes; anything past this is either cover art of
// unusual size or a corrupt length field, and neither is worth the allocation.
const maxMoovSize = 64 << 20

var errNoMoov = errors.New("tags: no moov atom")

// atomHeader is a parsed atom header: its total size (header included) and the
// number of header bytes consumed.
type atomHeader struct {
	size    int64
	typ     string
	hdrLen  int64
	toEnd   bool // size 0: atom runs to end of file
	invalid bool
}

func parseAtomHeader(b []byte) atomHeader {
	if len(b) < 8 {
		return atomHeader{invalid: true}
	}
	size := int64(binary.BigEndian.Uint32(b[0:4]))
	typ := string(b[4:8])
	switch {
	case size == 1:
		if len(b) < 16 {
			return atomHeader{invalid: true}
		}
		return atomHeader{size: int64(binary.BigEndian.Uint64(b[8:16])), typ: typ, hdrLen: 16}
	case size == 0:
		return atomHeader{typ: typ, hdrLen: 8, toEnd: true}
	case size < 8:
		return atomHeader{invalid: true}
	}
	return atomHeader{size: size, typ: typ, hdrLen: 8}
}

// findMoov scans the top-level atom chain for moov and returns its byte range.
// The chain is walked by header alone, so a moov at the end of a multi-gigabyte
// file costs a handful of 16-byte reads to reach.
func findMoov(r io.ReaderAt, fileSize int64) (off, size int64, err error) {
	var hdr [16]byte
	for pos := int64(0); pos < fileSize; {
		n, rerr := r.ReadAt(hdr[:], pos)
		if n < 8 {
			if rerr != nil {
				return 0, 0, rerr
			}
			return 0, 0, errNoMoov
		}
		a := parseAtomHeader(hdr[:n])
		if a.invalid {
			return 0, 0, errNoMoov
		}
		if a.toEnd {
			a.size = fileSize - pos
		}
		if a.typ == "moov" {
			return pos, a.size, nil
		}
		if a.size <= 0 {
			return 0, 0, errNoMoov
		}
		pos += a.size
	}
	return 0, 0, errNoMoov
}

// walkAtoms calls fn for each child atom in b. Returning false stops the walk.
func walkAtoms(b []byte, fn func(typ string, body []byte) bool) {
	for pos := 0; pos+8 <= len(b); {
		a := parseAtomHeader(b[pos:])
		if a.invalid {
			return
		}
		size := a.size
		if a.toEnd {
			size = int64(len(b) - pos)
		}
		if size < a.hdrLen || pos+int(size) > len(b) {
			return
		}
		if !fn(a.typ, b[pos+int(a.hdrLen):pos+int(size)]) {
			return
		}
		pos += int(size)
	}
}

// parseMP4Moov extracts metadata from a moov atom body.
func parseMP4Moov(moov []byte, md *Metadata) {
	walkAtoms(moov, func(typ string, body []byte) bool {
		switch typ {
		case "mvhd":
			parseMVHD(body, md)
		case "udta":
			walkAtoms(body, func(typ string, body []byte) bool {
				if typ == "meta" {
					// meta is a full atom: skip its version and flags word.
					if len(body) >= 4 {
						walkAtoms(body[4:], func(typ string, body []byte) bool {
							if typ == "ilst" {
								parseILST(body, md)
							}
							return true
						})
					}
				}
				return true
			})
		case "trak":
			parseTrakForAudio(body, md)
		}
		return true
	})
}

// parseMVHD reads the movie header for the overall duration.
func parseMVHD(b []byte, md *Metadata) {
	if len(b) < 4 {
		return
	}
	version := b[0]
	var timescale, duration uint64
	switch version {
	case 0:
		if len(b) < 20 {
			return
		}
		timescale = uint64(binary.BigEndian.Uint32(b[12:16]))
		duration = uint64(binary.BigEndian.Uint32(b[16:20]))
	case 1:
		if len(b) < 32 {
			return
		}
		timescale = uint64(binary.BigEndian.Uint32(b[20:24]))
		duration = binary.BigEndian.Uint64(b[24:32])
	default:
		return
	}
	if timescale > 0 && duration > 0 {
		md.DurationMS = int32(duration * 1000 / timescale)
	}
}

// parseTrakForAudio digs out sample rate and channel count from the first
// audio sample description (trak > mdia > minf > stbl > stsd).
func parseTrakForAudio(trak []byte, md *Metadata) {
	walkAtoms(trak, func(typ string, mdia []byte) bool {
		if typ != "mdia" {
			return true
		}
		walkAtoms(mdia, func(typ string, minf []byte) bool {
			if typ != "minf" {
				return true
			}
			walkAtoms(minf, func(typ string, stbl []byte) bool {
				if typ != "stbl" {
					return true
				}
				walkAtoms(stbl, func(typ string, stsd []byte) bool {
					if typ == "stsd" && len(stsd) > 8 {
						parseSTSD(stsd[8:], md) // skip version/flags and entry count
					}
					return true
				})
				return true
			})
			return true
		})
		return true
	})
}

// parseSTSD reads an audio sample entry: 6 reserved bytes, data reference
// index, then version, revision, vendor, channel count and sample size,
// followed by a 16.16 fixed-point sample rate.
func parseSTSD(b []byte, md *Metadata) {
	walkAtoms(b, func(typ string, body []byte) bool {
		if len(body) < 28 {
			return true
		}
		channels := binary.BigEndian.Uint16(body[16:18])
		rate := binary.BigEndian.Uint32(body[24:28]) >> 16
		if rate > 0 && md.SampleRate == 0 {
			md.SampleRate = int32(rate)
			md.Channels = uint8(channels)
			return false
		}
		return true
	})
}

// iTunes metadata atom names. The 0xA9 prefix is the "©" byte.
const (
	atomTitle       = "\xa9nam"
	atomArtist      = "\xa9ART"
	atomAlbumArtist = "aART"
	atomAlbum       = "\xa9alb"
	atomGenreText   = "\xa9gen"
	atomGenreID     = "gnre"
	atomCompilation = "cpil"
	atomDate        = "\xa9day"
	atomComposer    = "\xa9wrt"
	atomComment     = "\xa9cmt"
	atomTrack       = "trkn"
	atomDisc        = "disk"
	atomCover       = "covr"
)

// parseILST decodes the iTunes metadata item list.
func parseILST(ilst []byte, md *Metadata) {
	walkAtoms(ilst, func(typ string, body []byte) bool {
		switch typ {
		case atomCover:
			md.HasArt = true
			return true
		case atomTrack:
			if md.Track == 0 {
				md.Track, md.TrackTotal = mp4NumberPair(body)
			}
			return true
		case atomDisc:
			if md.Disc == 0 {
				md.Disc, md.DiscTotal = mp4NumberPair(body)
			}
			return true
		}

		val := mp4DataString(body)
		if val == "" {
			return true
		}
		switch typ {
		case atomTitle:
			setIfEmpty(&md.Title, val)
		case atomArtist:
			setIfEmpty(&md.Artist, val)
		case atomAlbumArtist:
			setIfEmpty(&md.AlbumArtist, val)
		case atomAlbum:
			setIfEmpty(&md.Album, val)
		case atomGenreText:
			setIfEmpty(&md.Genre, normaliseGenre(val))
		case atomGenreID:
			if md.Genre == "" {
				// gnre stores a 1-based ID3v1 genre index as a big-endian u16.
				if n := mp4DataBytes(body); len(n) >= 2 {
					md.Genre = genreByID(int(binary.BigEndian.Uint16(n)) - 1)
				}
			}
		case atomComposer:
			setIfEmpty(&md.Composer, val)
		case atomComment:
			setIfEmpty(&md.Comment, val)
		case atomDate:
			if md.Year == 0 {
				md.Year = parseYear(val)
			}
		case atomCompilation:
			// cpil is a single byte rather than text, so the decoded string is
			// no use: read the payload.
			if b := mp4DataBytes(body); len(b) > 0 {
				md.Compilation = b[0] != 0
			}
		}
		return true
	})
}

// mp4DataBytes returns the payload of an item's nested data atom.
func mp4DataBytes(item []byte) []byte {
	var out []byte
	walkAtoms(item, func(typ string, body []byte) bool {
		if typ == "data" && len(body) >= 8 {
			out = body[8:] // 4 bytes type/flags, 4 bytes locale
			return false
		}
		return true
	})
	return out
}

// mp4DataString decodes an item's data atom as text, honouring the well-known
// type codes rather than assuming UTF-8.
func mp4DataString(item []byte) string {
	var out string
	walkAtoms(item, func(typ string, body []byte) bool {
		if typ != "data" || len(body) < 8 {
			return true
		}
		dataType := binary.BigEndian.Uint32(body[0:4]) & 0x00FFFFFF
		payload := body[8:]
		switch dataType {
		case 1: // UTF-8
			out = strings.TrimSpace(trimNulUTF8(string(payload)))
		case 2: // UTF-16BE
			out = strings.TrimSpace(decodeUTF16(payload, true))
		case 21, 22: // signed / unsigned big-endian integer
			out = mp4IntString(payload)
		default:
			out = strings.TrimSpace(trimNulUTF8(string(payload)))
		}
		return false
	})
	return out
}

func mp4IntString(b []byte) string {
	var v int64
	for _, c := range b {
		v = v<<8 | int64(c)
	}
	if len(b) == 0 {
		return ""
	}
	return itoa(v)
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [24]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// mp4NumberPair decodes trkn/disk, which store the number and total as
// big-endian u16 values at offsets 2 and 4 of the data payload.
func mp4NumberPair(item []byte) (num, total int32) {
	b := mp4DataBytes(item)
	if len(b) >= 4 {
		num = int32(binary.BigEndian.Uint16(b[2:4]))
	}
	if len(b) >= 6 {
		total = int32(binary.BigEndian.Uint16(b[4:6]))
	}
	return num, total
}

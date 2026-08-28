package tags

import (
	"bytes"
	"errors"
	"strings"
)

// ID3v2 tag header flags.
const (
	id3Unsync    = 0x80
	id3ExtHeader = 0x40
	id3Footer    = 0x10
)

// id3Frame is one decoded frame. Payload excludes the frame header and has
// already had per-frame unsynchronisation and data-length indicators removed,
// so it is directly usable and directly re-emittable.
type id3Frame struct {
	id      string
	flags   uint16
	payload []byte
}

// id3Tag is a parsed ID3v2 container. Frames are kept in file order so a
// rewrite can preserve every frame this package does not understand.
type id3Tag struct {
	major   byte
	rev     byte
	frames  []id3Frame
	tagSize int // total on-disk bytes, including the 10-byte header and any footer
}

var errNotID3 = errors.New("tags: not an ID3v2 tag")

// synchsafe decodes the 7-bits-per-byte integer ID3v2 uses for sizes.
func synchsafe(b []byte) int {
	return int(b[0]&0x7F)<<21 | int(b[1]&0x7F)<<14 | int(b[2]&0x7F)<<7 | int(b[3]&0x7F)
}

func be32(b []byte) int {
	return int(b[0])<<24 | int(b[1])<<16 | int(b[2])<<8 | int(b[3])
}

func be24(b []byte) int {
	return int(b[0])<<16 | int(b[1])<<8 | int(b[2])
}

// id3v2Size returns the total on-disk size of the ID3v2 tag starting at head,
// or 0 if head does not begin with one. Only the 10-byte header is needed, so
// the scanner can use this to decide how much more of the file to read.
func id3v2Size(head []byte) int {
	if len(head) < 10 || head[0] != 'I' || head[1] != 'D' || head[2] != '3' {
		return 0
	}
	if head[3] == 0xFF || head[4] == 0xFF {
		return 0 // invalid version bytes; not a real tag
	}
	size := 10 + synchsafe(head[6:10])
	if head[5]&id3Footer != 0 {
		size += 10
	}
	return size
}

// deunsync reverses ID3v2 unsynchronisation: every 0xFF 0x00 pair becomes a
// lone 0xFF. Returns the input untouched when no pair is present, which is the
// overwhelmingly common case.
func deunsync(b []byte) []byte {
	idx := bytes.Index(b, []byte{0xFF, 0x00})
	if idx < 0 {
		return b
	}
	out := make([]byte, 0, len(b))
	out = append(out, b[:idx]...)
	for i := idx; i < len(b); i++ {
		out = append(out, b[i])
		if b[i] == 0xFF && i+1 < len(b) && b[i+1] == 0x00 {
			i++ // drop the inserted zero
		}
	}
	return out
}

// parseID3v2 decodes the tag at the start of buf. buf must contain at least the
// whole tag as reported by id3v2Size.
func parseID3v2(buf []byte) (*id3Tag, error) {
	total := id3v2Size(buf)
	if total == 0 {
		return nil, errNotID3
	}
	if total > len(buf) {
		return nil, errors.New("tags: truncated ID3v2 tag")
	}
	major, rev, flags := buf[3], buf[4], buf[5]
	if major < 2 || major > 4 {
		return nil, ErrUnsupported
	}

	declared := synchsafe(buf[6:10])
	body := buf[10 : 10+declared]

	// v2.2 and v2.3 unsynchronise the entire tag body; v2.4 does it per frame.
	if flags&id3Unsync != 0 && major < 4 {
		body = deunsync(body)
	}
	body = skipExtendedHeader(body, major, flags)

	t := &id3Tag{major: major, rev: rev, tagSize: total}
	t.frames = parseFrames(body, major)
	return t, nil
}

func skipExtendedHeader(body []byte, major, flags byte) []byte {
	if flags&id3ExtHeader == 0 || len(body) < 6 {
		return body
	}
	var n int
	if major == 4 {
		n = synchsafe(body[0:4]) // v2.4: size includes the size field itself
	} else {
		n = 4 + be32(body[0:4]) // v2.3: size excludes it
	}
	if n <= 0 || n > len(body) {
		return body[:0]
	}
	return body[n:]
}

// parseFrames walks the frame list. It stops at padding (a zero frame ID) or
// at the first structurally impossible frame rather than trying to resynchronise,
// because a bad size almost always means the rest of the tag is junk too.
func parseFrames(body []byte, major byte) []id3Frame {
	idLen, sizeLen, flagLen := 4, 4, 2
	if major == 2 {
		idLen, sizeLen, flagLen = 3, 3, 0
	}
	hdrLen := idLen + sizeLen + flagLen

	frames := make([]id3Frame, 0, 16)
	for pos := 0; pos+hdrLen <= len(body); {
		id := body[pos : pos+idLen]
		if id[0] == 0 {
			break // padding
		}
		if !validFrameID(id) {
			break
		}
		sz := body[pos+idLen : pos+idLen+sizeLen]
		var size int
		switch {
		case major == 2:
			size = be24(sz)
		case major == 4:
			size = synchsafe(sz)
			// Some encoders write plain big-endian sizes in v2.4 tags. If the
			// synchsafe reading does not land on a valid frame but the
			// big-endian one does, trust the big-endian value.
			if b := be32(sz); b != size && plausibleNextFrame(body, pos+hdrLen+b, hdrLen, idLen) &&
				!plausibleNextFrame(body, pos+hdrLen+size, hdrLen, idLen) {
				size = b
			}
		default:
			size = be32(sz)
		}
		if size < 0 || pos+hdrLen+size > len(body) {
			break
		}
		var flags uint16
		if flagLen == 2 {
			flags = uint16(body[pos+idLen+sizeLen])<<8 | uint16(body[pos+idLen+sizeLen+1])
		}
		payload := body[pos+hdrLen : pos+hdrLen+size]
		pos += hdrLen + size

		payload, ok := normaliseFramePayload(payload, flags, major)
		if !ok {
			continue // compressed or encrypted; skip but keep walking
		}
		frames = append(frames, id3Frame{id: string(id), flags: flags, payload: payload})
	}
	return frames
}

// normaliseFramePayload strips the v2.4 data-length indicator and per-frame
// unsynchronisation, and rejects frames we cannot decode.
func normaliseFramePayload(payload []byte, flags uint16, major byte) ([]byte, bool) {
	if major == 4 {
		if flags&0x000C != 0 { // compressed or encrypted
			return nil, false
		}
		if flags&0x0001 != 0 { // data length indicator
			if len(payload) < 4 {
				return nil, false
			}
			payload = payload[4:]
		}
		if flags&0x0002 != 0 { // frame-level unsynchronisation
			payload = deunsync(payload)
		}
	} else if major == 3 {
		if flags&0x00C0 != 0 { // compressed or encrypted
			return nil, false
		}
	}
	return payload, true
}

// plausibleNextFrame reports whether offset looks like the start of another
// frame (or clean end-of-tag). Used only to disambiguate v2.4 size encodings.
func plausibleNextFrame(body []byte, off, hdrLen, idLen int) bool {
	if off == len(body) {
		return true
	}
	if off < 0 || off+hdrLen > len(body) {
		return false
	}
	return body[off] == 0 || validFrameID(body[off:off+idLen])
}

func validFrameID(id []byte) bool {
	for _, c := range id {
		if !(c >= 'A' && c <= 'Z') && !(c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// applyID3v2 folds a parsed tag into md. Frames appearing more than once keep
// the first value, matching how players resolve duplicates.
func (t *id3Tag) applyTo(md *Metadata) {
	for i := range t.frames {
		f := &t.frames[i]
		switch f.id {
		case "TIT2", "TT2":
			setIfEmpty(&md.Title, frameText(f.payload))
		case "TPE1", "TP1":
			setIfEmpty(&md.Artist, frameText(f.payload))
		case "TPE2", "TP2":
			setIfEmpty(&md.AlbumArtist, frameText(f.payload))
		case "TALB", "TAL":
			setIfEmpty(&md.Album, frameText(f.payload))
		case "TCON", "TCO":
			setIfEmpty(&md.Genre, normaliseGenre(frameText(f.payload)))
		case "TCOM", "TCM":
			setIfEmpty(&md.Composer, frameText(f.payload))
		case "TRCK", "TRK":
			if md.Track == 0 {
				md.Track, md.TrackTotal = parsePair(frameText(f.payload))
			}
		case "TPOS", "TPA":
			if md.Disc == 0 {
				md.Disc, md.DiscTotal = parsePair(frameText(f.payload))
			}
		case "TDRC", "TDRL", "TYER", "TYE", "TDAT":
			if md.Year == 0 {
				md.Year = parseYear(frameText(f.payload))
			}
		case "COMM", "COM":
			// A comment frame's description is what separates a listener's
			// comment from an application's private data. iTunes stores
			// gapless playback information and volume normalisation in this
			// very frame, and taking the first one regardless puts a line of
			// hex where the comment should be.
			if md.Comment == "" && tagForID3Frame(f.id, f.payload) == TagComment {
				md.Comment = commentText(f.payload)
			}
		case "APIC", "PIC":
			md.HasArt = true
		case "TXXX", "TXX":
			// A user-defined frame is only as good as its description, and
			// taggers put real metadata here routinely: ffmpeg writes both the
			// comment and the compilation flag as TXXX rather than in the
			// frames the specification provides.
			desc, val := userText(f.payload)
			if val == "" {
				break
			}
			t, _ := tagForDescription(desc)
			switch t {
			case TagAlbumArtist:
				setIfEmpty(&md.AlbumArtist, val)
			case TagComment:
				setIfEmpty(&md.Comment, val)
			case TagComposer:
				setIfEmpty(&md.Composer, val)
			case TagGenre:
				setIfEmpty(&md.Genre, normaliseGenre(val))
			case TagDate:
				if md.Year == 0 {
					md.Year = parseYear(val)
				}
			}
		}
	}
}

func setIfEmpty(dst *string, v string) {
	if *dst == "" && v != "" {
		*dst = v
	}
}

// frameText decodes a text frame payload: one encoding byte then the string.
// ID3v2.4 allows NUL-separated multiple values, which are joined.
func frameText(p []byte) string {
	if len(p) < 1 {
		return ""
	}
	enc, body := p[0], p[1:]
	if enc == encUTF16 || enc == encUTF16BE {
		return strings.TrimSpace(decodeText(enc, body))
	}
	// Split on NUL for multi-value frames, dropping the trailing padding NULs.
	parts := bytes.Split(body, []byte{0})
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if s := strings.TrimSpace(decodeText(enc, part)); s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, "; ")
}

// commentText decodes COMM: encoding, 3-byte language, NUL-terminated short
// description, then the comment itself.
func commentText(p []byte) string {
	if len(p) < 5 {
		return ""
	}
	enc, body := p[0], p[4:]
	_, rest, ok := splitTerminated(body, enc)
	if !ok {
		return ""
	}
	return strings.TrimSpace(decodeText(enc, rest))
}

// userText decodes TXXX: encoding, NUL-terminated description, then the value.
func userText(p []byte) (desc, value string) {
	if len(p) < 2 {
		return "", ""
	}
	enc := p[0]
	d, rest, ok := splitTerminated(p[1:], enc)
	if !ok {
		return "", ""
	}
	return strings.TrimSpace(decodeText(enc, d)), strings.TrimSpace(decodeText(enc, rest))
}

// splitTerminated splits at the NUL terminator for the given encoding, which is
// one byte for Latin-1/UTF-8 and two for the UTF-16 variants.
func splitTerminated(b []byte, enc byte) (first, rest []byte, ok bool) {
	if enc == encUTF16 || enc == encUTF16BE {
		for i := 0; i+1 < len(b); i += 2 {
			if b[i] == 0 && b[i+1] == 0 {
				return b[:i], b[i+2:], true
			}
		}
		return nil, nil, false
	}
	i := bytes.IndexByte(b, 0)
	if i < 0 {
		return nil, nil, false
	}
	return b[:i], b[i+1:], true
}

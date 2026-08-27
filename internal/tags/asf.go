package tags

import (
	"encoding/binary"
	"os"
	"strings"
)

// ASF (WMA) object GUIDs, in the mixed-endian byte order they appear on disk.
var (
	guidHeader         = [16]byte{0x30, 0x26, 0xB2, 0x75, 0x8E, 0x66, 0xCF, 0x11, 0xA6, 0xD9, 0x00, 0xAA, 0x00, 0x62, 0xCE, 0x6C}
	guidFileProps      = [16]byte{0xA1, 0xDC, 0xAB, 0x8C, 0x47, 0xA9, 0xCF, 0x11, 0x8E, 0xE4, 0x00, 0xC0, 0x0C, 0x20, 0x53, 0x65}
	guidContentDesc    = [16]byte{0x33, 0x26, 0xB2, 0x75, 0x8E, 0x66, 0xCF, 0x11, 0xA6, 0xD9, 0x00, 0xAA, 0x00, 0x62, 0xCE, 0x6C}
	guidExtContentDesc = [16]byte{0x40, 0xA4, 0xD0, 0xD2, 0x07, 0xE3, 0xD2, 0x11, 0x97, 0xF0, 0x00, 0xA0, 0xC9, 0x5E, 0xA8, 0x50}
	guidStreamProps    = [16]byte{0x91, 0x07, 0xDC, 0xB7, 0xB7, 0xA9, 0xCF, 0x11, 0x8E, 0xE6, 0x00, 0xC0, 0x0C, 0x20, 0x53, 0x65}
	guidAudioMedia     = [16]byte{0x40, 0x9E, 0x69, 0xF8, 0x4D, 0x5B, 0xCF, 0x11, 0xA8, 0xFD, 0x00, 0x80, 0x5F, 0x5C, 0x44, 0x2B}
)

func guidEq(b []byte, g [16]byte) bool {
	return len(b) >= 16 && string(b[:16]) == string(g[:])
}

// readASF parses a Windows Media (ASF) header. Every metadata object lives
// inside the header object at the start of the file, so one read covers it.
func (r *Reader) readASF(f *os.File, size int64, head []byte, md *Metadata) error {
	if len(head) < 30 || !guidEq(head, guidHeader) {
		return ErrNoTags
	}
	headerSize := int64(binary.LittleEndian.Uint64(head[16:24]))
	if headerSize > int64(len(head)) && headerSize <= maxHeadSize {
		if grown, err := r.readHead(f, size, int(headerSize)); err == nil && len(grown) > len(head) {
			head = grown
		}
	}
	end := int(headerSize)
	if end > len(head) || end <= 0 {
		end = len(head)
	}

	// Sub-objects begin after the 24-byte header plus a 4-byte object count
	// and two reserved bytes.
	var preroll, playDuration uint64
	for pos := 30; pos+24 <= end; {
		objSize := int(binary.LittleEndian.Uint64(head[pos+16 : pos+24]))
		if objSize < 24 || pos+objSize > end {
			break
		}
		body := head[pos+24 : pos+objSize]
		switch {
		case guidEq(head[pos:], guidContentDesc):
			parseASFContentDesc(body, md)
		case guidEq(head[pos:], guidExtContentDesc):
			parseASFExtContentDesc(body, md)
		case guidEq(head[pos:], guidFileProps):
			if len(body) >= 64 {
				playDuration = binary.LittleEndian.Uint64(body[40:48])
				preroll = binary.LittleEndian.Uint64(body[56:64])
			}
			if len(body) >= 80 {
				md.Bitrate = int32(binary.LittleEndian.Uint32(body[76:80]) / 1000)
			}
		case guidEq(head[pos:], guidStreamProps):
			parseASFStreamProps(body, md)
		}
		pos += objSize
	}

	// Play duration is in 100ns units and includes the preroll buffer.
	if ms := playDuration / 10000; ms > preroll {
		md.DurationMS = int32(ms - preroll)
	}
	return nil
}

// parseASFContentDesc reads the fixed five-field description object.
func parseASFContentDesc(b []byte, md *Metadata) {
	if len(b) < 10 {
		return
	}
	lens := [5]int{}
	for i := 0; i < 5; i++ {
		lens[i] = int(binary.LittleEndian.Uint16(b[i*2 : i*2+2]))
	}
	p := 10
	field := func(n int) string {
		if n <= 0 || p+n > len(b) {
			p = len(b) + 1
			return ""
		}
		s := utf16LEString(b[p : p+n])
		p += n
		return s
	}
	setIfEmpty(&md.Title, field(lens[0]))
	setIfEmpty(&md.Artist, field(lens[1]))
	field(lens[2]) // copyright, not modelled
	setIfEmpty(&md.Comment, field(lens[3]))
}

// parseASFExtContentDesc reads the name/value descriptor list, which is where
// album, genre, track number and year actually live.
func parseASFExtContentDesc(b []byte, md *Metadata) {
	if len(b) < 2 {
		return
	}
	count := int(binary.LittleEndian.Uint16(b[0:2]))
	p := 2
	for i := 0; i < count; i++ {
		if p+2 > len(b) {
			return
		}
		nameLen := int(binary.LittleEndian.Uint16(b[p : p+2]))
		p += 2
		if p+nameLen+4 > len(b) {
			return
		}
		name := utf16LEString(b[p : p+nameLen])
		p += nameLen
		valType := binary.LittleEndian.Uint16(b[p : p+2])
		valLen := int(binary.LittleEndian.Uint16(b[p+2 : p+4]))
		p += 4
		if p+valLen > len(b) {
			return
		}
		raw := b[p : p+valLen]
		p += valLen

		val := asfValue(valType, raw)
		switch strings.ToUpper(name) {
		case "WM/ALBUMTITLE":
			setIfEmpty(&md.Album, val)
		case "WM/ALBUMARTIST":
			setIfEmpty(&md.AlbumArtist, val)
		case "WM/GENRE":
			setIfEmpty(&md.Genre, normaliseGenre(val))
		case "WM/COMPOSER":
			setIfEmpty(&md.Composer, val)
		case "WM/TRACKNUMBER", "WM/TRACK":
			if md.Track == 0 {
				md.Track, md.TrackTotal = parsePair(val)
			}
		case "WM/PARTOFSET":
			if md.Disc == 0 {
				md.Disc, md.DiscTotal = parsePair(val)
			}
		case "WM/YEAR", "WM/ORIGINALRELEASEYEAR":
			if md.Year == 0 {
				md.Year = parseYear(val)
			}
		case "WM/PICTURE":
			md.HasArt = true
		}
	}
}

// asfValue renders a descriptor value according to its type code.
func asfValue(typ uint16, raw []byte) string {
	switch typ {
	case 0: // UTF-16LE string
		return utf16LEString(raw)
	case 2: // BOOL stored as a 32-bit word
		if len(raw) >= 4 && binary.LittleEndian.Uint32(raw) != 0 {
			return "1"
		}
		return "0"
	case 3: // DWORD
		if len(raw) >= 4 {
			return itoa(int64(binary.LittleEndian.Uint32(raw)))
		}
	case 4: // QWORD
		if len(raw) >= 8 {
			return itoa(int64(binary.LittleEndian.Uint64(raw)))
		}
	case 5: // WORD
		if len(raw) >= 2 {
			return itoa(int64(binary.LittleEndian.Uint16(raw)))
		}
	}
	return ""
}

// parseASFStreamProps pulls sample rate and channel count out of the audio
// stream's WAVEFORMATEX block.
func parseASFStreamProps(b []byte, md *Metadata) {
	if len(b) < 54 || !guidEq(b, guidAudioMedia) {
		return
	}
	// 16 stream type + 16 error correction + 8 time offset + 4 + 4 + 2 + 4.
	spec := b[54:]
	if len(spec) < 16 {
		return
	}
	md.Channels = uint8(binary.LittleEndian.Uint16(spec[2:4]))
	md.SampleRate = int32(binary.LittleEndian.Uint32(spec[4:8]))
}

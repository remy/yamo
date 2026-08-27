package tags

import (
	"bytes"
	"encoding/binary"
)

// oggPage is a decoded Ogg page header plus its payload.
type oggPage struct {
	headerType byte
	granule    int64
	serial     uint32
	segments   []byte
	payload    []byte
	total      int // bytes the whole page occupies
}

// parseOggPage decodes the page starting at b[0]. ok is false when b does not
// start with a page or does not contain all of it.
func parseOggPage(b []byte) (p oggPage, ok bool) {
	if len(b) < 27 || string(b[0:4]) != "OggS" || b[4] != 0 {
		return p, false
	}
	nseg := int(b[26])
	if len(b) < 27+nseg {
		return p, false
	}
	segs := b[27 : 27+nseg]
	dataLen := 0
	for _, s := range segs {
		dataLen += int(s)
	}
	total := 27 + nseg + dataLen
	if len(b) < total {
		return p, false
	}
	return oggPage{
		headerType: b[5],
		granule:    int64(binary.LittleEndian.Uint64(b[6:14])),
		serial:     binary.LittleEndian.Uint32(b[14:18]),
		segments:   segs,
		payload:    b[27+nseg : total],
		total:      total,
	}, true
}

// oggPackets reassembles the first maxPackets logical packets of the first
// bitstream in buf. Packets continued across page boundaries are joined.
func oggPackets(buf []byte, maxPackets int) [][]byte {
	var packets [][]byte
	var cur []byte
	var serial uint32
	haveSerial := false

	for pos := 0; pos < len(buf); {
		p, ok := parseOggPage(buf[pos:])
		if !ok {
			break
		}
		pos += p.total
		if !haveSerial {
			serial, haveSerial = p.serial, true
		} else if p.serial != serial {
			continue // a different multiplexed stream
		}

		off := 0
		for _, seg := range p.segments {
			n := int(seg)
			cur = append(cur, p.payload[off:off+n]...)
			off += n
			if n < 255 { // a short segment terminates the packet
				packets = append(packets, cur)
				cur = nil
				if len(packets) >= maxPackets {
					return packets
				}
			}
		}
	}
	return packets
}

// oggLastGranule finds the granule position of the final page in tail, which
// is the stream's total sample count. tail is the last chunk of the file.
func oggLastGranule(tail []byte) (int64, bool) {
	// Scan backwards for the last capture pattern that parses as a page.
	for i := len(tail) - 27; i >= 0; i-- {
		if tail[i] != 'O' || tail[i+1] != 'g' || tail[i+2] != 'g' || tail[i+3] != 'S' {
			continue
		}
		if p, ok := parseOggPage(tail[i:]); ok {
			return p.granule, true
		}
		// A page truncated by the read window still has a usable granule.
		if tail[i+4] == 0 {
			return int64(binary.LittleEndian.Uint64(tail[i+6 : i+14])), true
		}
	}
	return 0, false
}

var (
	magicVorbisIdent   = []byte{0x01, 'v', 'o', 'r', 'b', 'i', 's'}
	magicVorbisComment = []byte{0x03, 'v', 'o', 'r', 'b', 'i', 's'}
	magicOpusHead      = []byte("OpusHead")
	magicOpusTags      = []byte("OpusTags")
)

// parseOgg decodes the identification and comment headers from the start of an
// Ogg stream, covering both Vorbis and Opus payloads.
func parseOgg(head []byte, md *Metadata) (*vorbisComment, bool) {
	packets := oggPackets(head, 3)
	var vc *vorbisComment
	found := false

	for _, pkt := range packets {
		switch {
		case bytes.HasPrefix(pkt, magicVorbisIdent):
			// version u32, channels u8, sample rate u32, then bitrate hints.
			if len(pkt) >= 16 {
				md.Format = FormatOggVorbis
				md.Channels = pkt[11]
				md.SampleRate = int32(binary.LittleEndian.Uint32(pkt[12:16]))
			}
			if len(pkt) >= 24 {
				if nominal := int32(binary.LittleEndian.Uint32(pkt[20:24])); nominal > 0 {
					md.Bitrate = nominal / 1000
				}
			}
		case bytes.HasPrefix(pkt, magicOpusHead):
			// magic(8), version(1), channels(1), preskip(2), input rate(4).
			if len(pkt) >= 19 {
				md.Format = FormatOpus
				md.Channels = pkt[9]
				// Opus always decodes at 48kHz regardless of the input rate.
				md.SampleRate = 48000
			}
		case bytes.HasPrefix(pkt, magicVorbisComment):
			if c, ok := parseVorbisComment(pkt[7:]); ok {
				vc, found = c, true
			}
		case bytes.HasPrefix(pkt, magicOpusTags):
			if c, ok := parseVorbisComment(pkt[8:]); ok {
				vc, found = c, true
			}
		}
	}
	return vc, found
}

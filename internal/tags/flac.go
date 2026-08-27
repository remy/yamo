package tags

import "encoding/binary"

// FLAC metadata block types.
const (
	flacStreamInfo    = 0
	flacVorbisComment = 4
	flacPicture       = 6
)

// flacBlock is one metadata block, excluding its 4-byte header.
type flacBlock struct {
	typ  byte
	last bool
	body []byte
	// off is the block's byte offset in the file, including its header. The
	// writer uses this to rewrite the metadata region in place when it can.
	off int
}

// flacBlocks walks the metadata block chain at the start of buf. ok is false
// when buf does not start with the fLaC marker; truncated is true when the
// chain runs past the end of buf, meaning the caller must read more.
func flacBlocks(buf []byte) (blocks []flacBlock, audioStart int, truncated, ok bool) {
	if len(buf) < 4 || string(buf[0:4]) != "fLaC" {
		return nil, 0, false, false
	}
	p := 4
	for {
		if p+4 > len(buf) {
			return blocks, 0, true, true
		}
		last := buf[p]&0x80 != 0
		typ := buf[p] & 0x7F
		n := be24(buf[p+1 : p+4])
		if p+4+n > len(buf) {
			return blocks, 0, true, true
		}
		blocks = append(blocks, flacBlock{typ: typ, last: last, body: buf[p+4 : p+4+n], off: p})
		p += 4 + n
		if last {
			return blocks, p, false, true
		}
		if len(blocks) > 1024 {
			return blocks, p, false, true // malformed chain; stop rather than spin
		}
	}
}

// parseFLACStreamInfo reads the 34-byte STREAMINFO block, which carries the
// exact sample count and therefore an exact duration.
func parseFLACStreamInfo(b []byte, md *Metadata) {
	if len(b) < 18 {
		return
	}
	// Bits 80..99 sample rate, 100..102 channels-1, 103..107 bits-1,
	// 108..143 total samples. Read the 8 bytes at offset 10 as one word.
	v := binary.BigEndian.Uint64(b[10:18])
	sampleRate := uint32(v >> 44)
	channels := uint8((v>>41)&0x07) + 1
	totalSamples := v & 0xFFFFFFFFF // low 36 bits

	if sampleRate == 0 {
		return
	}
	md.SampleRate = int32(sampleRate)
	md.Channels = channels
	if totalSamples > 0 {
		md.DurationMS = int32(totalSamples * 1000 / uint64(sampleRate))
	}
}

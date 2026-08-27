package tags

import "bytes"

// mpegHeader is a decoded MPEG audio frame header. Only the fields needed to
// derive duration and bitrate are kept.
type mpegHeader struct {
	version    int // 1, 2 or 25 (MPEG 2.5)
	layer      int // 1, 2 or 3
	bitrate    int // kbps, 0 when the frame declares "free" format
	sampleRate int
	channels   int
	frameLen   int
	sideInfo   int // bytes of side information before any Xing header
}

// Bitrate tables, indexed [version is MPEG1][layer][index].
var bitrateV1 = [4][15]int{
	1: {0, 32, 64, 96, 128, 160, 192, 224, 256, 288, 320, 352, 384, 416, 448}, // Layer I
	2: {0, 32, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 384},    // Layer II
	3: {0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320},     // Layer III
}

var bitrateV2 = [4][15]int{
	1: {0, 32, 48, 56, 64, 80, 96, 112, 128, 144, 160, 176, 192, 224, 256}, // Layer I
	2: {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160},      // Layer II & III
	3: {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160},
}

var sampleRates = map[int][3]int{
	1:  {44100, 48000, 32000},
	2:  {22050, 24000, 16000},
	25: {11025, 12000, 8000},
}

// parseMPEGHeader decodes the 4-byte frame header at b[0:4].
func parseMPEGHeader(b []byte) (mpegHeader, bool) {
	var h mpegHeader
	if len(b) < 4 || b[0] != 0xFF || b[1]&0xE0 != 0xE0 {
		return h, false
	}
	switch (b[1] >> 3) & 0x03 {
	case 0:
		h.version = 25
	case 2:
		h.version = 2
	case 3:
		h.version = 1
	default:
		return h, false // reserved
	}
	switch (b[1] >> 1) & 0x03 {
	case 1:
		h.layer = 3
	case 2:
		h.layer = 2
	case 3:
		h.layer = 1
	default:
		return h, false // reserved
	}

	brIdx := int(b[2] >> 4)
	srIdx := int((b[2] >> 2) & 0x03)
	if brIdx == 15 || srIdx == 3 {
		return h, false
	}
	if h.version == 1 {
		h.bitrate = bitrateV1[h.layer][brIdx]
	} else {
		h.bitrate = bitrateV2[h.layer][brIdx]
	}
	h.sampleRate = sampleRates[h.version][srIdx]
	if h.sampleRate == 0 {
		return h, false
	}

	padding := int((b[2] >> 1) & 0x01)
	mono := (b[3]>>6)&0x03 == 3
	h.channels = 2
	if mono {
		h.channels = 1
	}

	// Frame length in bytes. Layer I counts 4-byte slots.
	spf := h.samplesPerFrame()
	if h.layer == 1 {
		h.frameLen = (12*h.bitrate*1000/h.sampleRate + padding) * 4
	} else {
		h.frameLen = spf*h.bitrate*1000/8/h.sampleRate + padding
	}

	// Side info size, which is where a Xing/Info header would begin.
	switch {
	case h.version == 1 && mono:
		h.sideInfo = 17
	case h.version == 1:
		h.sideInfo = 32
	case mono:
		h.sideInfo = 9
	default:
		h.sideInfo = 17
	}
	return h, true
}

func (h mpegHeader) samplesPerFrame() int {
	switch {
	case h.layer == 1:
		return 384
	case h.layer == 2:
		return 1152
	case h.version == 1:
		return 1152
	default:
		return 576 // Layer III in MPEG 2 / 2.5
	}
}

// findFrameSync locates the first plausible MPEG frame header in b. A single
// 0xFF pair occurs constantly in binary data, so a candidate is only accepted
// when the frame it declares is followed by another valid header.
func findFrameSync(b []byte) int {
	for i := 0; i+4 <= len(b); i++ {
		if b[i] != 0xFF {
			continue
		}
		h, ok := parseMPEGHeader(b[i:])
		if !ok || h.frameLen <= 4 {
			continue
		}
		next := i + h.frameLen
		if next+4 > len(b) {
			return i // cannot confirm, but nothing contradicts it either
		}
		if _, ok := parseMPEGHeader(b[next:]); ok {
			return i
		}
	}
	return -1
}

// mp3Properties derives duration, bitrate, sample rate and channel count from
// the audio stream. audio must start at or before the first frame; audioBytes
// is the size of the stream excluding any surrounding tags.
//
// A Xing/Info or VBRI header gives an exact frame count for VBR files; without
// one the file is assumed CBR and duration comes from the declared bitrate.
func mp3Properties(audio []byte, audioBytes int64, md *Metadata) {
	off := findFrameSync(audio)
	if off < 0 {
		return
	}
	h, ok := parseMPEGHeader(audio[off:])
	if !ok {
		return
	}
	md.SampleRate = int32(h.sampleRate)
	md.Channels = uint8(h.channels)
	md.Bitrate = int32(h.bitrate)

	spf := h.samplesPerFrame()
	frame := audio[off:]

	if frames, streamBytes, ok := parseVBRHeader(frame, h); ok && frames > 0 {
		ms := float64(frames) * float64(spf) / float64(h.sampleRate) * 1000
		md.DurationMS = int32(ms)
		if streamBytes > 0 && ms > 0 {
			md.Bitrate = int32(float64(streamBytes) * 8 / ms) // kbps
		}
		return
	}

	if h.bitrate > 0 && audioBytes > 0 {
		md.DurationMS = int32(audioBytes * 8 / int64(h.bitrate)) // bytes*8/kbps == ms
	}
}

// parseVBRHeader looks for the Xing, Info or VBRI header inside the first
// audio frame and returns the total frame count and stream byte count.
func parseVBRHeader(frame []byte, h mpegHeader) (frames, bytesTotal int, ok bool) {
	// Xing/Info sits immediately after the 4-byte header plus side info.
	if start := 4 + h.sideInfo; start+12 <= len(frame) {
		tagID := frame[start : start+4]
		if bytes.Equal(tagID, []byte("Xing")) || bytes.Equal(tagID, []byte("Info")) {
			flags := be32(frame[start+4 : start+8])
			p := start + 8
			if flags&0x01 != 0 && p+4 <= len(frame) {
				frames = be32(frame[p : p+4])
				p += 4
			}
			if flags&0x02 != 0 && p+4 <= len(frame) {
				bytesTotal = be32(frame[p : p+4])
			}
			return frames, bytesTotal, frames > 0
		}
	}
	// VBRI (Fraunhofer) always sits 32 bytes after the frame header.
	if 36+26 <= len(frame) && bytes.Equal(frame[36:40], []byte("VBRI")) {
		bytesTotal = be32(frame[46:50])
		frames = be32(frame[50:54])
		return frames, bytesTotal, frames > 0
	}
	return 0, 0, false
}

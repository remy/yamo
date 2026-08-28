package tags

import (
	"io"
	"os"
)

// headSize is how much of a file the reader pulls in its first read. It is
// chosen to cover the metadata region of the overwhelming majority of files in
// a single syscall; the reader grows it only when a tag says it must.
const headSize = 64 << 10

// maxHeadSize bounds tag-driven growth. A metadata region larger than this is
// pathological and reading it would cost more than the tag is worth.
const maxHeadSize = 16 << 20

// Reader extracts metadata from audio files. It owns reusable buffers, so a
// single Reader can process an entire library without per-file allocation.
// A Reader is not safe for concurrent use; give each worker its own.
type Reader struct {
	head []byte
	tail [id3v1Len]byte
}

// NewReader returns a Reader with its buffers pre-allocated.
func NewReader() *Reader {
	return &Reader{head: make([]byte, headSize)}
}

// ReadFile opens path and extracts its metadata.
func (r *Reader) ReadFile(path string) (Metadata, error) {
	f, err := os.Open(path)
	if err != nil {
		return Metadata{}, err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return Metadata{}, err
	}
	return r.ReadOpen(f, fi.Size(), FormatForPath(path))
}

// ReadOpen extracts metadata from an already-open file. format is a hint from
// the file extension; the sniffed content wins when the two disagree.
func (r *Reader) ReadOpen(f *os.File, size int64, format Format) (Metadata, error) {
	md := Metadata{Format: format}
	if size <= 0 {
		return md, ErrNoTags
	}

	head, err := r.readHead(f, size, headSize)
	if err != nil {
		return md, err
	}
	if len(head) == 0 {
		return md, ErrNoTags
	}

	// Content sniffing overrides the extension, which is wrong often enough
	// to matter in a library assembled over decades.
	if sniffed := sniff(head); sniffed != FormatUnknown {
		format = sniffed
		md.Format = sniffed
	}

	switch format {
	case FormatMP3:
		err = r.readMP3(f, size, head, &md)
	case FormatFLAC:
		err = r.readFLAC(f, size, head, &md)
	case FormatMP4:
		err = r.readMP4(f, size, &md)
	case FormatOggVorbis, FormatOpus:
		err = r.readOgg(f, size, head, &md)
	case FormatWAV:
		err = r.readRIFF(f, size, head, &md)
	case FormatAIFF:
		err = r.readAIFF(f, size, head, &md)
	case FormatWMA:
		err = r.readASF(f, size, head, &md)
	default:
		return md, ErrUnsupported
	}
	return md, err
}

// readHead reads up to n bytes from the start of the file into r.head, growing
// the buffer when n exceeds its capacity.
func (r *Reader) readHead(f *os.File, size int64, n int) ([]byte, error) {
	if int64(n) > size {
		n = int(size)
	}
	if n > maxHeadSize {
		n = maxHeadSize
	}
	if cap(r.head) < n {
		r.head = make([]byte, n)
	}
	buf := r.head[:n]
	got, err := f.ReadAt(buf, 0)
	if got > 0 {
		return buf[:got], nil
	}
	if err == io.EOF {
		return nil, nil
	}
	return nil, err
}

// sniff identifies a container from its leading bytes.
//
// Every container below announces itself at offset 0, so anything that starts
// with an ID3v2 tag or an MPEG frame is an MPEG stream whatever its extension
// claims — and files misnamed .m4a do turn up in real libraries.
func sniff(head []byte) Format {
	switch {
	case len(head) >= 4 && string(head[0:4]) == "fLaC":
		return FormatFLAC
	case len(head) >= 4 && string(head[0:4]) == "OggS":
		return FormatUnknown // Vorbis vs Opus is decided by the first packet
	case len(head) >= 12 && string(head[0:4]) == "RIFF" && string(head[8:12]) == "WAVE":
		return FormatWAV
	case len(head) >= 12 && string(head[0:4]) == "FORM" &&
		(string(head[8:12]) == "AIFF" || string(head[8:12]) == "AIFC"):
		return FormatAIFF
	case len(head) >= 12 && string(head[4:8]) == "ftyp":
		return FormatMP4
	case len(head) >= 16 && head[0] == 0x30 && head[1] == 0x26 && head[2] == 0xB2 && head[3] == 0x75:
		return FormatWMA
	case id3v2Size(head) > 0:
		return FormatMP3
	}
	// A bare frame header, for a stripped file with no tag at all. The full
	// header is decoded rather than matching the sync word alone, since eleven
	// set bits are not rare in arbitrary binary.
	if _, ok := parseMPEGHeader(head); ok {
		return FormatMP3
	}
	return FormatUnknown
}

// readMP3 parses the ID3v2 tag, falls back to ID3v1, then reads the first
// audio frame for stream properties.
func (r *Reader) readMP3(f *os.File, size int64, head []byte, md *Metadata) error {
	tagSize := id3v2Size(head)

	// Grow the buffer when the tag is larger than the initial read, which
	// happens whenever cover art is embedded up front.
	if tagSize > len(head) {
		var err error
		head, err = r.readHead(f, size, tagSize)
		if err != nil {
			return err
		}
		if len(head) < tagSize {
			tagSize = 0 // truncated file; fall through to ID3v1
		}
	}

	if tagSize > 0 {
		if t, err := parseID3v2(head); err == nil {
			t.applyTo(md)
		}
	}

	// ID3v1 is only worth a syscall when ID3v2 left the basics unset.
	v1Size := int64(0)
	if md.Title == "" || md.Artist == "" || md.Album == "" {
		if r.readTail(f, size) {
			if parseID3v1(r.tail[:], md) {
				v1Size = id3v1Len
			}
		}
	} else if size > id3v1Len {
		// Still need to know whether a trailer exists so the CBR duration
		// estimate does not count it as audio.
		if r.readTail(f, size) && r.tail[0] == 'T' && r.tail[1] == 'A' && r.tail[2] == 'G' {
			v1Size = id3v1Len
		}
	}

	r.mp3Stream(f, size, head, int64(tagSize), v1Size, md)
	return nil
}

// mp3Stream locates the audio and derives duration, bitrate and sample rate.
func (r *Reader) mp3Stream(f *os.File, size int64, head []byte, tagSize, v1Size int64, md *Metadata) {
	audioBytes := size - tagSize - v1Size
	if audioBytes <= 0 {
		return
	}
	// The first frame usually sits inside the buffer we already have.
	if int64(len(head)) > tagSize {
		window := head[tagSize:]
		if len(window) >= 4 {
			mp3Properties(window, audioBytes, md)
			if md.SampleRate != 0 {
				return
			}
		}
	}
	// Otherwise fetch a small window at the start of the audio.
	var buf [8 << 10]byte
	n := int64(len(buf))
	if n > audioBytes {
		n = audioBytes
	}
	got, err := f.ReadAt(buf[:n], tagSize)
	if got > 4 && (err == nil || err == io.EOF) {
		mp3Properties(buf[:got], audioBytes, md)
	}
}

// readTail loads the final 128 bytes of the file into r.tail.
func (r *Reader) readTail(f *os.File, size int64) bool {
	if size < id3v1Len {
		return false
	}
	n, err := f.ReadAt(r.tail[:], size-id3v1Len)
	return n == id3v1Len && (err == nil || err == io.EOF)
}

// readFLAC walks the metadata block chain, growing the read window if the
// chain extends past what has been read so far.
func (r *Reader) readFLAC(f *os.File, size int64, head []byte, md *Metadata) error {
	for attempt := 0; ; attempt++ {
		blocks, _, truncated, ok := flacBlocks(head)
		if !ok {
			return ErrNoTags
		}
		haveComment := false
		for _, b := range blocks {
			switch b.typ {
			case flacStreamInfo:
				parseFLACStreamInfo(b.body, md)
			case flacVorbisComment:
				if vc, ok := parseVorbisComment(b.body); ok {
					vc.applyTo(md)
					haveComment = true
				}
			case flacPicture:
				md.HasArt = true
			}
		}
		// Stop once the chain is complete, or once the comment block has been
		// seen and only trailing blocks (usually a large PICTURE) remain.
		if !truncated || haveComment || attempt >= 3 || int64(len(head)) >= size {
			break
		}
		grown, err := r.readHead(f, size, len(head)*8)
		if err != nil || len(grown) <= len(head) {
			break
		}
		head = grown
	}
	if md.SampleRate > 0 && md.DurationMS > 0 {
		// FLAC has no bitrate field; derive it from the file size.
		md.Bitrate = int32(size * 8 / int64(md.DurationMS))
	}
	return nil
}

// readMP4 locates the moov atom, which may sit at either end of the file, and
// reads only that.
func (r *Reader) readMP4(f *os.File, size int64, md *Metadata) error {
	off, moovSize, err := findMoov(f, size)
	if err != nil {
		return ErrNoTags
	}
	if moovSize <= 8 || moovSize > maxMoovSize {
		return ErrNoTags
	}
	buf := make([]byte, moovSize-8)
	if _, err := f.ReadAt(buf, off+8); err != nil && err != io.EOF {
		return err
	}
	parseMP4Moov(buf, md)
	if md.DurationMS > 0 {
		md.Bitrate = int32(size * 8 / int64(md.DurationMS))
	}
	return nil
}

// readOgg decodes the identification and comment headers, then reads the final
// page for the granule position that gives the exact duration.
func (r *Reader) readOgg(f *os.File, size int64, head []byte, md *Metadata) error {
	vc, ok := parseOgg(head, md)
	if !ok && int64(len(head)) < size {
		// The comment header spilled past the first read; widen once.
		if grown, err := r.readHead(f, size, 512<<10); err == nil && len(grown) > len(head) {
			vc, ok = parseOgg(grown, md)
		}
	}
	if ok && vc != nil {
		vc.applyTo(md)
	}

	if md.SampleRate > 0 {
		var tail [16 << 10]byte
		n := int64(len(tail))
		if n > size {
			n = size
		}
		if got, err := f.ReadAt(tail[:n], size-n); got > 27 && (err == nil || err == io.EOF) {
			if g, found := oggLastGranule(tail[:got]); found && g > 0 {
				md.DurationMS = int32(g * 1000 / int64(md.SampleRate))
			}
		}
	}
	if md.DurationMS > 0 && md.Bitrate == 0 {
		md.Bitrate = int32(size * 8 / int64(md.DurationMS))
	}
	return nil
}

package tags

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// Ogg's CRC is a plain CRC-32 with polynomial 0x04c11db7: no input or output
// reflection, no initial value and no final inversion. It is not the CRC-32
// in the standard library, so the table is built here.
var oggCRCTable = func() [256]uint32 {
	var t [256]uint32
	for i := range t {
		r := uint32(i) << 24
		for j := 0; j < 8; j++ {
			if r&0x80000000 != 0 {
				r = r<<1 ^ 0x04c11db7
			} else {
				r <<= 1
			}
		}
		t[i] = r
	}
	return t
}()

func oggCRC(b []byte) uint32 {
	var crc uint32
	for _, c := range b {
		crc = crc<<8 ^ oggCRCTable[byte(crc>>24)^c]
	}
	return crc
}

// writeOgg applies an edit to an Ogg Vorbis or Opus file.
//
// Unlike the other containers there is no in-place path. Ogg pages carry a
// checksum and a sequence number, and the comment packet sits inside the
// header pages, so changing its length re-paginates the stream and every later
// page needs renumbering. The whole file is therefore rewritten.
func writeOgg(path string, e *Edit) error {
	return updateOgg(path, func(vc *vorbisComment) bool {
		var cur Metadata
		vc.applyTo(&cur)
		applyEditToVorbis(vc, e, &cur)
		if e.Artwork != nil {
			setVorbisPictures(vc, *e.Artwork)
		}
		return true
	})
}

// oggMutator rewrites an Ogg stream's comment header. Returning false leaves
// the file untouched.
type oggMutator func(vc *vorbisComment) bool

// updateOgg rewrites an Ogg Vorbis or Opus file's comment header.
func updateOgg(path string, mutate oggMutator) error {
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close()

	fi, err := src.Stat()
	if err != nil {
		return err
	}

	br := bufio.NewReaderSize(src, 1<<20)
	pr := &pageReader{r: br}

	// Read the header pages, which hold every packet before the audio.
	first, err := pr.next()
	if err != nil {
		return err
	}
	if first.headerType&0x02 == 0 {
		return fmt.Errorf("%w: Ogg stream does not start with a BOS page", ErrMalformed)
	}
	serial := first.serial

	wantPackets, err := headerPacketCount(first.payload, first.segments)
	if err != nil {
		return err
	}

	packets, headerPages, err := collectHeaderPackets(pr, first, serial, wantPackets)
	if err != nil {
		return err
	}

	// Rewrite the comment packet, which is always the second one.
	rewritten, changed, err := rewriteCommentPacket(packets[1], mutate)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	packets[1] = rewritten

	// Re-paginate: the identification packet must sit alone on the BOS page,
	// and the remaining header packets follow on their own pages.
	var out bytes.Buffer
	out.Grow(int(fi.Size()) + 4096)
	pages := paginate(&out, packets[:1], serial, 0, true, 0)
	pages += paginate(&out, packets[1:], serial, uint32(pages), false, 0)

	// Copy the audio pages, shifting sequence numbers by however many pages
	// the header grew or shrank and recomputing each checksum.
	delta := int32(pages) - int32(headerPages)
	for {
		p, err := pr.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if p.serial != serial {
			return fmt.Errorf("%w: multiplexed Ogg streams are not supported", ErrUnsupported)
		}
		raw := p.raw
		seq := binary.LittleEndian.Uint32(raw[18:22])
		binary.LittleEndian.PutUint32(raw[18:22], uint32(int32(seq)+delta))
		stampCRC(raw)
		out.Write(raw)
	}

	return replaceWholeFile(path, src, fi, out.Bytes())
}

// pageReader reads whole Ogg pages from a stream.
type pageReader struct {
	r   *bufio.Reader
	hdr [27]byte
}

// rawPage is one page plus its original bytes, ready to be re-emitted.
type rawPage struct {
	headerType byte
	serial     uint32
	segments   []byte
	payload    []byte
	raw        []byte
}

func (pr *pageReader) next() (*rawPage, error) {
	if _, err := io.ReadFull(pr.r, pr.hdr[:]); err != nil {
		if err == io.ErrUnexpectedEOF {
			return nil, io.EOF
		}
		return nil, err
	}
	if string(pr.hdr[0:4]) != "OggS" || pr.hdr[4] != 0 {
		return nil, fmt.Errorf("%w: corrupt Ogg page", ErrMalformed)
	}
	nseg := int(pr.hdr[26])
	raw := make([]byte, 27+nseg)
	copy(raw, pr.hdr[:])
	if _, err := io.ReadFull(pr.r, raw[27:]); err != nil {
		return nil, err
	}
	dataLen := 0
	for _, s := range raw[27:] {
		dataLen += int(s)
	}
	raw = append(raw, make([]byte, dataLen)...)
	if _, err := io.ReadFull(pr.r, raw[27+nseg:]); err != nil {
		return nil, err
	}
	return &rawPage{
		headerType: raw[5],
		serial:     binary.LittleEndian.Uint32(raw[14:18]),
		segments:   raw[27 : 27+nseg],
		payload:    raw[27+nseg:],
		raw:        raw,
	}, nil
}

// headerPacketCount reports how many header packets the codec uses: Vorbis has
// identification, comment and setup; Opus has identification and tags.
func headerPacketCount(payload, segments []byte) (int, error) {
	switch {
	case bytes.HasPrefix(payload, magicVorbisIdent):
		return 3, nil
	case bytes.HasPrefix(payload, magicOpusHead):
		return 2, nil
	}
	return 0, fmt.Errorf("%w: unrecognised Ogg codec", ErrUnsupported)
}

// collectHeaderPackets reads pages until the header packets are complete.
func collectHeaderPackets(pr *pageReader, first *rawPage, serial uint32, want int) (packets [][]byte, pages int, err error) {
	page := first
	var cur []byte
	for {
		if page.serial != serial {
			return nil, 0, fmt.Errorf("%w: multiplexed Ogg streams are not supported", ErrUnsupported)
		}
		pages++
		off := 0
		for _, seg := range page.segments {
			n := int(seg)
			cur = append(cur, page.payload[off:off+n]...)
			off += n
			if n < 255 {
				packets = append(packets, cur)
				cur = nil
			}
		}
		if len(packets) >= want {
			if off != len(page.payload) || cur != nil {
				return nil, 0, fmt.Errorf("%w: audio data shares a page with the headers", ErrMalformed)
			}
			return packets[:want], pages, nil
		}
		page, err = pr.next()
		if err != nil {
			return nil, 0, fmt.Errorf("%w: Ogg headers are truncated", ErrMalformed)
		}
	}
}

// rewriteCommentPacket rebuilds the comment header with the mutation applied,
// keeping the codec's magic prefix and framing.
func rewriteCommentPacket(pkt []byte, mutate oggMutator) ([]byte, bool, error) {
	var prefix []byte
	var body []byte
	framing := false

	switch {
	case bytes.HasPrefix(pkt, magicVorbisComment):
		prefix, body, framing = pkt[:7], pkt[7:], true
	case bytes.HasPrefix(pkt, magicOpusTags):
		prefix, body = pkt[:8], pkt[8:]
	default:
		return nil, false, fmt.Errorf("%w: second Ogg packet is not a comment header", ErrMalformed)
	}

	vc, ok := parseVorbisComment(body)
	if !ok {
		vc = &vorbisComment{vendor: "tagmgr"}
	}
	if !mutate(vc) {
		return nil, false, nil
	}

	encoded := encodeVorbisComment(vc)
	out := make([]byte, 0, len(prefix)+len(encoded)+1)
	out = append(out, prefix...)
	out = append(out, encoded...)
	if framing {
		out = append(out, 1) // Vorbis comment headers end with a framing bit
	}
	return out, true, nil
}

// paginate writes packets as Ogg pages and returns how many it emitted.
func paginate(w *bytes.Buffer, packets [][]byte, serial, startSeq uint32, bos bool, granule int64) int {
	// Build the full lacing table first. A packet whose length is an exact
	// multiple of 255 needs a trailing zero lace so its end is unambiguous.
	var laces []byte
	var data []byte
	for _, p := range packets {
		n := len(p)
		for n >= 255 {
			laces = append(laces, 255)
			n -= 255
		}
		laces = append(laces, byte(n))
		data = append(data, p...)
	}

	pages := 0
	dataOff := 0
	continued := false
	for i := 0; i < len(laces); {
		n := len(laces) - i
		if n > 255 {
			n = 255
		}
		chunk := laces[i : i+n]
		segBytes := 0
		for _, l := range chunk {
			segBytes += int(l)
		}

		var flags byte
		if continued {
			flags |= 0x01
		}
		if bos && pages == 0 {
			flags |= 0x02
		}

		page := make([]byte, 0, 27+n+segBytes)
		page = append(page, "OggS"...)
		page = append(page, 0, flags)
		page = binary.LittleEndian.AppendUint64(page, uint64(granule))
		page = binary.LittleEndian.AppendUint32(page, serial)
		page = binary.LittleEndian.AppendUint32(page, startSeq+uint32(pages))
		page = binary.LittleEndian.AppendUint32(page, 0) // checksum placeholder
		page = append(page, byte(n))
		page = append(page, chunk...)
		page = append(page, data[dataOff:dataOff+segBytes]...)
		stampCRC(page)
		w.Write(page)

		dataOff += segBytes
		i += n
		pages++
		continued = chunk[len(chunk)-1] == 255
	}
	return pages
}

// stampCRC computes a page's checksum with the checksum field zeroed and
// writes it back into that field, which is how the format defines it.
func stampCRC(page []byte) {
	page[22], page[23], page[24], page[25] = 0, 0, 0, 0
	binary.LittleEndian.PutUint32(page[22:26], oggCRC(page))
}

// replaceWholeFile writes data over path via a temporary file and a rename.
func replaceWholeFile(path string, src *os.File, fi os.FileInfo, data []byte) error {
	tmp, err := os.CreateTemp(dirOf(path), ".tagmgr-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName)
	}()

	if err := tmp.Chmod(fi.Mode().Perm()); err != nil && !os.IsPermission(err) {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	src.Close()
	return os.Rename(tmpName, path)
}

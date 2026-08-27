package tags

import (
	"errors"
	"io"
	"os"
)

const flacPadding = 1 // PADDING block type

// newFlacPadding is the slack left after a rewritten FLAC metadata region, so
// later edits can be applied in place rather than rewriting the audio.
const newFlacPadding = 4096

// writeFLAC applies an edit to a FLAC file's Vorbis comment block.
func writeFLAC(path string, e *Edit) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return err
	}
	size := fi.Size()

	region, blocks, audioStart, err := readFLACMetadata(f, size)
	if err != nil {
		return err
	}
	_ = region

	// Locate or synthesise the comment block.
	vc := &vorbisComment{vendor: "tagmgr"}
	for _, b := range blocks {
		if b.typ == flacVorbisComment {
			if parsed, ok := parseVorbisComment(b.body); ok {
				vc = parsed
			}
			break
		}
	}
	var cur Metadata
	vc.applyTo(&cur)
	applyEditToVorbis(vc, e, &cur)
	commentBody := encodeVorbisComment(vc)

	// Rebuild the block list: existing blocks in order, the comment block
	// replaced, and padding dropped so it can be recalculated.
	type outBlock struct {
		typ  byte
		body []byte
	}
	out := make([]outBlock, 0, len(blocks)+1)
	wroteComment := false
	for _, b := range blocks {
		switch b.typ {
		case flacPadding:
			continue
		case flacVorbisComment:
			out = append(out, outBlock{typ: flacVorbisComment, body: commentBody})
			wroteComment = true
		default:
			out = append(out, outBlock{typ: b.typ, body: b.body})
		}
	}
	if !wroteComment {
		// STREAMINFO must stay first, so insert immediately after it.
		at := 0
		if len(out) > 0 && out[0].typ == flacStreamInfo {
			at = 1
		}
		out = append(out, outBlock{})
		copy(out[at+1:], out[at:])
		out[at] = outBlock{typ: flacVorbisComment, body: commentBody}
	}

	content := 0
	for _, b := range out {
		content += 4 + len(b.body)
	}

	// Decide the padding size. Keeping the metadata region byte-identical in
	// length means the audio never moves and the write is a single pwrite.
	oldContent := int(audioStart) - 4
	padding := -1
	if slack := oldContent - content; slack == 0 || slack >= 4 {
		padding = slack
	}
	inPlace := padding >= 0
	if !inPlace {
		padding = newFlacPadding
	}

	buf := make([]byte, 0, 4+content+padding)
	buf = append(buf, "fLaC"...)
	for i, b := range out {
		last := i == len(out)-1 && padding == 0
		buf = appendFLACBlock(buf, b.typ, b.body, last)
	}
	if padding > 0 {
		buf = appendFLACBlock(buf, flacPadding, make([]byte, padding-4), true)
	}

	if inPlace {
		if _, err := f.WriteAt(buf, 0); err != nil {
			return err
		}
		return f.Sync()
	}
	return rewriteWithNewHeader(path, f, size, audioStart, buf)
}

// appendFLACBlock writes a metadata block header and body.
func appendFLACBlock(dst []byte, typ byte, body []byte, last bool) []byte {
	h := typ & 0x7F
	if last {
		h |= 0x80
	}
	n := len(body)
	return append(append(dst, h, byte(n>>16), byte(n>>8), byte(n)), body...)
}

// readFLACMetadata reads the complete metadata region, growing the read until
// the block chain terminates.
func readFLACMetadata(f *os.File, size int64) (region []byte, blocks []flacBlock, audioStart int64, err error) {
	n := int64(64 << 10)
	for {
		if n > size {
			n = size
		}
		buf := make([]byte, n)
		if _, err := f.ReadAt(buf, 0); err != nil && err != io.EOF {
			return nil, nil, 0, err
		}
		bs, start, truncated, ok := flacBlocks(buf)
		if !ok {
			return nil, nil, 0, errors.New("tags: not a FLAC file")
		}
		if !truncated {
			return buf[:start], bs, int64(start), nil
		}
		if n >= size {
			return nil, nil, 0, errors.New("tags: truncated FLAC metadata")
		}
		if n > maxHeadSize {
			return nil, nil, 0, errors.New("tags: FLAC metadata too large")
		}
		n *= 4
	}
}

// applyEditToVorbis folds an edit into a comment block. Vorbis stores a number
// and its total as separate fields rather than as "n/total", so the aliases
// that mean the same thing are cleared to avoid two disagreeing values.
func applyEditToVorbis(vc *vorbisComment, e *Edit, cur *Metadata) {
	setStr := func(key string, v *string) {
		if v != nil {
			vc.set(key, *v)
		}
	}
	setStr("TITLE", e.Title)
	setStr("ARTIST", e.Artist)
	setStr("ALBUM", e.Album)
	setStr("GENRE", e.Genre)
	setStr("COMPOSER", e.Composer)

	if e.AlbumArtist != nil {
		vc.set("ALBUMARTIST", *e.AlbumArtist)
		vc.set("ALBUM ARTIST", "")
	}
	if e.Comment != nil {
		vc.set("COMMENT", *e.Comment)
		vc.set("DESCRIPTION", "")
	}
	if e.Year != nil {
		year := ""
		if *e.Year > 0 {
			year = itoa(int64(*e.Year))
		}
		vc.set("DATE", year)
		vc.set("YEAR", "")
	}
	if e.Track != nil {
		vc.set("TRACKNUMBER", numOrEmpty(*e.Track))
	}
	if e.TrackTotal != nil {
		vc.set("TRACKTOTAL", numOrEmpty(*e.TrackTotal))
		vc.set("TOTALTRACKS", "")
	}
	if e.Disc != nil {
		vc.set("DISCNUMBER", numOrEmpty(*e.Disc))
	}
	if e.DiscTotal != nil {
		vc.set("DISCTOTAL", numOrEmpty(*e.DiscTotal))
		vc.set("TOTALDISCS", "")
	}
}

func numOrEmpty(v int32) string {
	if v <= 0 {
		return ""
	}
	return itoa(int64(v))
}

package tags

import (
	"fmt"
	"io"
	"os"
)

const flacPadding = 1 // PADDING block type

// newFlacPadding is the slack left after a rewritten FLAC metadata region, so
// later edits can be applied in place rather than rewriting the audio.
const newFlacPadding = 4096

// flacMutator rewrites a FLAC file's metadata. It receives the parsed Vorbis
// comment and the file's other metadata blocks, and returns the blocks to keep
// alongside it. Returning changed false leaves the file untouched.
type flacMutator func(vc *vorbisComment, other []flacBlock) (keep []flacBlock, changed bool)

// writeFLAC applies an edit to a FLAC file's Vorbis comment block.
func writeFLAC(path string, e *Edit) error {
	return updateFLAC(path, func(vc *vorbisComment, other []flacBlock) ([]flacBlock, bool) {
		var cur Metadata
		vc.applyTo(&cur)
		applyEditToVorbis(vc, e, &cur)

		if e.Artwork != nil {
			// FLAC keeps images in their own metadata blocks. A file
			// converted from Ogg may also carry one base64-encoded in the
			// comment, so clear that too or the old cover would survive.
			vc.set("METADATA_BLOCK_PICTURE", "")
			kept := other[:0]
			for _, b := range other {
				if b.typ != flacPicture {
					kept = append(kept, b)
				}
			}
			for i := range *e.Artwork {
				kept = append(kept, flacBlock{typ: flacPicture, body: encodeFLACPicture(&(*e.Artwork)[i])})
			}
			other = kept
		}
		return other, true
	})
}

// updateFLAC reads a FLAC file's metadata region, hands it to mutate, and
// writes the result back.
//
// Where the rebuilt region is the same length as the old one — or can be made
// so by resizing the padding block the format reserves for exactly this — the
// write is a single pwrite over the head of the file and the audio never
// moves. That matters here more than anywhere else: FLAC files are large, and
// rewriting one to change a text field would be absurd.
func updateFLAC(path string, mutate flacMutator) error {
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

	blocks, audioStart, err := readFLACMetadata(f, size)
	if err != nil {
		return err
	}

	// Split the comment block out from the rest; a file without one gets an
	// empty comment so an edit has somewhere to land.
	vc := &vorbisComment{vendor: "yamo"}
	other := make([]flacBlock, 0, len(blocks))
	for _, b := range blocks {
		switch b.typ {
		case flacVorbisComment:
			if parsed, ok := parseVorbisComment(b.body); ok {
				vc = parsed
			}
		case flacPadding:
			// Dropped here and recalculated after the new size is known.
		default:
			other = append(other, b)
		}
	}

	keep, changed := mutate(vc, other)
	if !changed {
		return nil
	}
	commentBody := encodeVorbisComment(vc)

	// STREAMINFO must come first; the comment block goes immediately after it.
	type outBlock struct {
		typ  byte
		body []byte
	}
	out := make([]outBlock, 0, len(keep)+1)
	for _, b := range keep {
		if b.typ == flacStreamInfo {
			out = append(out, outBlock{typ: b.typ, body: b.body})
		}
	}
	out = append(out, outBlock{typ: flacVorbisComment, body: commentBody})
	for _, b := range keep {
		if b.typ != flacStreamInfo {
			out = append(out, outBlock{typ: b.typ, body: b.body})
		}
	}

	content := 0
	for _, b := range out {
		content += 4 + len(b.body)
	}

	// Keeping the metadata region the same length means the audio stays put.
	// A leftover of one to three bytes cannot hold a padding block header, so
	// only an exact fit or four bytes upward can be absorbed.
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
func readFLACMetadata(f *os.File, size int64) (blocks []flacBlock, audioStart int64, err error) {
	n := int64(64 << 10)
	for {
		if n > size {
			n = size
		}
		buf := make([]byte, n)
		if _, err := f.ReadAt(buf, 0); err != nil && err != io.EOF {
			return nil, 0, err
		}
		bs, start, truncated, ok := flacBlocks(buf)
		if !ok {
			return nil, 0, fmt.Errorf("%w: not a FLAC file", ErrMalformed)
		}
		if !truncated {
			return bs, int64(start), nil
		}
		if n >= size {
			return nil, 0, fmt.Errorf("%w: truncated FLAC metadata", ErrMalformed)
		}
		if n > maxHeadSize {
			return nil, 0, fmt.Errorf("%w: FLAC metadata too large", ErrMalformed)
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
	if e.Compilation != nil {
		if *e.Compilation {
			vc.set("COMPILATION", "1")
		} else {
			vc.set("COMPILATION", "")
		}
	}
	setStr("TITLE", e.Title)
	setStr("ARTIST", e.Artist)
	setStr("ALBUM", e.Album)
	setStr("GENRE", e.Genre)
	setStr("COMPOSER", e.Composer)
	setStr("TITLESORT", e.TitleSort)
	setStr("ARTISTSORT", e.ArtistSort)
	setStr("ALBUMSORT", e.AlbumSort)
	setStr("COMPOSERSORT", e.ComposerSort)

	if e.AlbumArtist != nil {
		vc.set("ALBUMARTIST", *e.AlbumArtist)
		vc.set("ALBUM ARTIST", "")
	}
	if e.AlbumArtistSort != nil {
		vc.set("ALBUMARTISTSORT", *e.AlbumArtistSort)
		vc.set("ALBUM ARTIST SORT", "")
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

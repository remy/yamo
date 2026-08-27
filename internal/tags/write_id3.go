package tags

import (
	"errors"
	"io"
	"os"
	"strings"
	"unicode/utf16"
)

// newTagPadding is the slack left after a freshly written ID3v2 tag. Padding
// is what lets a later edit that grows the tag stay in place instead of
// rewriting the whole file, so it pays for itself the first time a title is
// corrected.
const newTagPadding = 2048

// writeID3v2 applies an edit to the ID3v2 tag of an MP3 (or of any container
// that fronts its audio with one).
func writeID3v2(path string, e *Edit) error {
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

	// Read the existing tag, if any.
	var header [10]byte
	oldTagSize := 0
	var tag *id3Tag
	if n, _ := f.ReadAt(header[:], 0); n == 10 {
		if oldTagSize = id3v2Size(header[:]); oldTagSize > 0 && int64(oldTagSize) <= size {
			buf := make([]byte, oldTagSize)
			if _, err := f.ReadAt(buf, 0); err != nil && err != io.EOF {
				return err
			}
			tag, _ = parseID3v2(buf)
		}
	}
	if tag == nil {
		tag = &id3Tag{major: 3} // new tags default to v2.3 for player compatibility
		oldTagSize = 0
	}
	// Read the current values before any conversion, so number/total pairs can
	// be merged against what the file actually says.
	var cur Metadata
	tag.applyTo(&cur)

	// v2.2 has no writer here, so its frames are translated to v2.3 first.
	upgradeV22Frames(tag)

	applyEditToFrames(tag, e, &cur)
	body := encodeID3Frames(tag)

	// An edit that fits inside the existing tag can be written in place: pad
	// the difference and overwrite the header region. No audio is touched and
	// no temporary file is needed, which matters for multi-gigabyte files.
	if oldTagSize >= len(body)+10 {
		out := make([]byte, oldTagSize)
		copy(out, id3TagHeader(tag.major, oldTagSize-10))
		copy(out[10:], body)
		if _, err := f.WriteAt(out, 0); err != nil {
			return err
		}
		return f.Sync()
	}

	// Otherwise the file has to be rebuilt around a larger tag.
	newSize := len(body) + newTagPadding
	return rewriteWithNewHeader(path, f, size, int64(oldTagSize),
		append(id3TagHeader(tag.major, newSize), padTo(body, newSize)...))
}

// id3TagHeader builds the 10-byte tag header for a body of bodySize bytes.
func id3TagHeader(major byte, bodySize int) []byte {
	h := make([]byte, 10)
	copy(h, "ID3")
	h[3], h[4], h[5] = major, 0, 0
	putSynchsafe(h[6:10], bodySize)
	return h
}

func putSynchsafe(b []byte, v int) {
	b[0] = byte(v>>21) & 0x7F
	b[1] = byte(v>>14) & 0x7F
	b[2] = byte(v>>7) & 0x7F
	b[3] = byte(v) & 0x7F
}

func padTo(b []byte, n int) []byte {
	if len(b) >= n {
		return b
	}
	out := make([]byte, n)
	copy(out, b)
	return out
}

// applyEditToFrames folds an edit into a parsed tag's frame list, preserving
// every frame the edit does not mention.
func applyEditToFrames(tag *id3Tag, e *Edit, cur *Metadata) {
	set := func(id string, v *string) {
		if v != nil {
			setTextFrame(tag, id, *v)
		}
	}
	set("TIT2", e.Title)
	set("TPE1", e.Artist)
	set("TPE2", e.AlbumArtist)
	set("TALB", e.Album)
	set("TCON", e.Genre)
	set("TCOM", e.Composer)

	if e.AlbumArtist != nil {
		// A TXXX:ALBUMARTIST would otherwise shadow the TPE2 just written.
		removeUserTextFrame(tag, "ALBUMARTIST")
		removeUserTextFrame(tag, "ALBUM ARTIST")
	}
	if s, ok := resolvePair(e.Track, e.TrackTotal, cur.Track, cur.TrackTotal); ok {
		setTextFrame(tag, "TRCK", s)
	}
	if s, ok := resolvePair(e.Disc, e.DiscTotal, cur.Disc, cur.DiscTotal); ok {
		setTextFrame(tag, "TPOS", s)
	}
	if e.Year != nil {
		year := ""
		if *e.Year > 0 {
			year = itoa(int64(*e.Year))
		}
		// The year frame differs between versions, so clear both and write the
		// one this tag version defines.
		removeFrames(tag, "TYER", "TDRC", "TDAT", "TRDA")
		if year != "" {
			if tag.major >= 4 {
				setTextFrame(tag, "TDRC", year)
			} else {
				setTextFrame(tag, "TYER", year)
			}
		}
	}
	if e.Comment != nil {
		setCommentFrame(tag, *e.Comment)
	}
}

// setTextFrame replaces the first matching frame and drops any duplicates. An
// empty value removes the frame entirely.
func setTextFrame(tag *id3Tag, id, value string) {
	payload := encodeTextPayload(tag.major, value)
	replaceFrame(tag, id, payload, value == "")
}

// setCommentFrame writes COMM with an empty description and "eng" language,
// which is the slot every player displays.
func setCommentFrame(tag *id3Tag, value string) {
	if value == "" {
		removeFrames(tag, "COMM")
		return
	}
	enc, text := encodeString(tag.major, value)
	payload := make([]byte, 0, len(text)+8)
	payload = append(payload, enc)
	payload = append(payload, "eng"...)
	payload = append(payload, terminator(enc)...) // empty description
	payload = append(payload, text...)
	replaceFrame(tag, "COMM", payload, false)
}

// replaceFrame overwrites the first frame with the given id, removing later
// duplicates. When remove is true the frame is deleted instead.
func replaceFrame(tag *id3Tag, id string, payload []byte, remove bool) {
	out := tag.frames[:0]
	done := false
	for _, f := range tag.frames {
		if f.id != id {
			out = append(out, f)
			continue
		}
		if remove || done {
			continue
		}
		out = append(out, id3Frame{id: id, payload: payload})
		done = true
	}
	if !done && !remove {
		out = append(out, id3Frame{id: id, payload: payload})
	}
	tag.frames = out
}

func removeFrames(tag *id3Tag, ids ...string) {
	out := tag.frames[:0]
	for _, f := range tag.frames {
		drop := false
		for _, id := range ids {
			if f.id == id {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, f)
		}
	}
	tag.frames = out
}

// removeUserTextFrame drops the TXXX frame with the given description.
func removeUserTextFrame(tag *id3Tag, desc string) {
	out := tag.frames[:0]
	for _, f := range tag.frames {
		if f.id == "TXXX" {
			if d, _ := userText(f.payload); strings.EqualFold(d, desc) {
				continue
			}
		}
		out = append(out, f)
	}
	tag.frames = out
}

// encodeTextPayload builds a text frame body: the encoding byte then the text.
func encodeTextPayload(major byte, value string) []byte {
	enc, text := encodeString(major, value)
	out := make([]byte, 0, len(text)+1)
	out = append(out, enc)
	return append(out, text...)
}

// encodeString picks the narrowest encoding the tag version allows. ID3v2.3
// has no UTF-8, so non-Latin-1 text must go out as UTF-16 with a BOM; v2.4
// uses UTF-8 throughout.
func encodeString(major byte, s string) (enc byte, b []byte) {
	if major >= 4 {
		return encUTF8, []byte(s)
	}
	if latin1Encodable(s) {
		return encISO8859, encodeLatin1(s)
	}
	return encUTF16, encodeUTF16LEWithBOM(s)
}

func latin1Encodable(s string) bool {
	for _, r := range s {
		if r > 0xFF {
			return false
		}
	}
	return true
}

func encodeLatin1(s string) []byte {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		out = append(out, byte(r))
	}
	return out
}

func encodeUTF16LEWithBOM(s string) []byte {
	u := utf16.Encode([]rune(s))
	out := make([]byte, 0, len(u)*2+2)
	out = append(out, 0xFF, 0xFE)
	for _, v := range u {
		out = append(out, byte(v), byte(v>>8))
	}
	return out
}

// terminator is the NUL sequence that ends a string in the given encoding.
func terminator(enc byte) []byte {
	if enc == encUTF16 || enc == encUTF16BE {
		return []byte{0, 0}
	}
	return []byte{0}
}

// encodeID3Frames serialises a frame list to the tag body, without padding.
func encodeID3Frames(tag *id3Tag) []byte {
	size := 0
	for _, f := range tag.frames {
		size += 10 + len(f.payload)
	}
	out := make([]byte, 0, size)
	for _, f := range tag.frames {
		if len(f.id) != 4 || len(f.payload) == 0 {
			continue
		}
		out = append(out, f.id...)
		var sz [4]byte
		if tag.major >= 4 {
			putSynchsafe(sz[:], len(f.payload))
		} else {
			sz[0] = byte(len(f.payload) >> 24)
			sz[1] = byte(len(f.payload) >> 16)
			sz[2] = byte(len(f.payload) >> 8)
			sz[3] = byte(len(f.payload))
		}
		out = append(out, sz[:]...)
		out = append(out, 0, 0) // frame flags: none set
		out = append(out, f.payload...)
	}
	return out
}

// rewriteWithNewHeader replaces the first oldHeaderLen bytes of a file with
// newHeader, copying the remainder. The result is written to a temporary file
// in the same directory and renamed into place, so an interrupted write cannot
// leave a half-rewritten track behind.
func rewriteWithNewHeader(path string, src *os.File, size, oldHeaderLen int64, newHeader []byte) error {
	fi, err := src.Stat()
	if err != nil {
		return err
	}
	dir := dirOf(path)
	tmp, err := os.CreateTemp(dir, ".tagmgr-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName) // no-op after a successful rename
	}()

	if err := tmp.Chmod(fi.Mode().Perm()); err != nil && !errors.Is(err, os.ErrPermission) {
		return err
	}
	if _, err := tmp.Write(newHeader); err != nil {
		return err
	}
	if _, err := io.Copy(tmp, io.NewSectionReader(src, oldHeaderLen, size-oldHeaderLen)); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// The source must be closed before the rename on some filesystems, and
	// keeping it open past this point serves no purpose.
	src.Close()
	return os.Rename(tmpName, path)
}

func dirOf(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[:i]
	}
	return "."
}

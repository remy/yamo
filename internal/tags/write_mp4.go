package tags

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// writeMP4 applies an edit to an MP4/M4A file's iTunes metadata.
//
// MP4 is the awkward container: the sample tables inside moov hold absolute
// file offsets into mdat, so resizing moov can invalidate them. Three
// strategies are tried in order of how much of the file they disturb:
//
//  1. Absorb the size change in a neighbouring free atom, so nothing moves.
//  2. If moov sits after the media data, rewrite from moov onward; the offsets
//     point backwards and are unaffected.
//  3. Otherwise rebuild the file and patch every chunk offset by the delta.
func writeMP4(path string, e *Edit) error {
	return updateMP4(path, func(items []mp4Item, cur *Metadata) []mp4Item {
		return applyEditToILST(items, e, cur)
	})
}

// mp4Item is one entry of the iTunes metadata item list.
type mp4Item struct {
	Name string // atom name, or "----" for a freeform item
	Body []byte // the item's contents, excluding its own atom header
}

// mp4Mutator rewrites the item list. cur holds the values already in the file,
// so an edit can merge against them.
type mp4Mutator func(items []mp4Item, cur *Metadata) []mp4Item

// updateMP4 rewrites an MP4 file's metadata item list.
func updateMP4(path string, mutate mp4Mutator) error {
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

	atoms, err := topLevelAtoms(f, size)
	if err != nil {
		return err
	}
	moovIdx := indexOfAtom(atoms, "moov")
	if moovIdx < 0 {
		return fmt.Errorf("%w: no moov atom to write into", ErrMalformed)
	}
	moov := atoms[moovIdx]
	if moov.size > maxMoovSize {
		return fmt.Errorf("%w: moov atom too large to rewrite", ErrMalformed)
	}

	old := make([]byte, moov.size)
	if _, err := f.ReadAt(old, moov.off); err != nil && err != io.EOF {
		return err
	}
	newMoov, err := rebuildMoov(old, mutate)
	if err != nil {
		return err
	}
	delta := int64(len(newMoov)) - moov.size

	if delta == 0 {
		if _, err := f.WriteAt(newMoov, moov.off); err != nil {
			return err
		}
		return f.Sync()
	}

	// Strategy 1: retune an adjacent free atom so the file layout is stable.
	if next := moovIdx + 1; next < len(atoms) && isFreeAtom(atoms[next].typ) {
		if newFree := atoms[next].size - delta; newFree == 0 || newFree >= 8 {
			buf := append(newMoov, freeAtom(newFree)...)
			if _, err := f.WriteAt(buf, moov.off); err != nil {
				return err
			}
			return f.Sync()
		}
	}

	// Strategy 2: moov after the media data means chunk offsets stay valid.
	if mdat := indexOfAtom(atoms, "mdat"); mdat >= 0 && mdat < moovIdx {
		return replaceTail(path, f, size, moov.off, newMoov, atoms, moovIdx)
	}

	// Strategy 3: the media is about to move, so every chunk offset shifts.
	patchChunkOffsets(newMoov, delta)
	return replaceRange(path, f, size, moov.off, moov.size, newMoov)
}

// topAtom records one top-level atom's position.
type topAtom struct {
	typ  string
	off  int64
	size int64
}

func topLevelAtoms(r io.ReaderAt, fileSize int64) ([]topAtom, error) {
	var out []topAtom
	var hdr [16]byte
	for pos := int64(0); pos < fileSize; {
		n, err := r.ReadAt(hdr[:], pos)
		if n < 8 {
			if err != nil && err != io.EOF {
				return nil, err
			}
			break
		}
		a := parseAtomHeader(hdr[:n])
		if a.invalid {
			return nil, fmt.Errorf("%w: malformed MP4 atom", ErrMalformed)
		}
		if a.toEnd {
			a.size = fileSize - pos
		}
		if a.size <= 0 {
			break
		}
		out = append(out, topAtom{typ: a.typ, off: pos, size: a.size})
		pos += a.size
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: not an MP4 file", ErrMalformed)
	}
	return out, nil
}

func indexOfAtom(atoms []topAtom, typ string) int {
	for i, a := range atoms {
		if a.typ == typ {
			return i
		}
	}
	return -1
}

func isFreeAtom(typ string) bool { return typ == "free" || typ == "skip" }

// freeAtom builds a free atom of exactly n bytes, or nothing when n is zero.
func freeAtom(n int64) []byte {
	if n <= 0 {
		return nil
	}
	b := make([]byte, n)
	binary.BigEndian.PutUint32(b[0:4], uint32(n))
	copy(b[4:8], "free")
	return b
}

// atom serialises one atom with a 32-bit size field.
func atom(typ string, body ...[]byte) []byte {
	n := 8
	for _, b := range body {
		n += len(b)
	}
	out := make([]byte, 8, n)
	binary.BigEndian.PutUint32(out[0:4], uint32(n))
	copy(out[4:8], typ)
	for _, b := range body {
		out = append(out, b...)
	}
	return out
}

// rebuildMoov returns a new moov atom with the edit applied to its ilst,
// creating the udta/meta/ilst chain if the file has none.
func rebuildMoov(moov []byte, mutate mp4Mutator) ([]byte, error) {
	if len(moov) < 8 || string(moov[4:8]) != "moov" {
		return nil, fmt.Errorf("%w: expected a moov atom", ErrMalformed)
	}
	body := moov[8:]

	var children [][]byte
	sawUdta := false
	var walkErr error
	walkAtoms(body, func(typ string, b []byte) bool {
		if typ != "udta" {
			children = append(children, atom(typ, b))
			return true
		}
		sawUdta = true
		rebuilt, err := rebuildUdta(b, mutate)
		if err != nil {
			walkErr = err
			return false
		}
		children = append(children, rebuilt)
		return true
	})
	if walkErr != nil {
		return nil, walkErr
	}
	if !sawUdta {
		rebuilt, err := rebuildUdta(nil, mutate)
		if err != nil {
			return nil, err
		}
		children = append(children, rebuilt)
	}
	return atom("moov", children...), nil
}

func rebuildUdta(udta []byte, mutate mp4Mutator) ([]byte, error) {
	var children [][]byte
	sawMeta := false
	walkAtoms(udta, func(typ string, b []byte) bool {
		if typ != "meta" {
			children = append(children, atom(typ, b))
			return true
		}
		sawMeta = true
		children = append(children, rebuildMeta(b, mutate))
		return true
	})
	if !sawMeta {
		children = append(children, rebuildMeta(nil, mutate))
	}
	return atom("udta", children...), nil
}

// mdirHandler is the hdlr atom iTunes expects inside a metadata meta atom.
// Files missing it are ignored by some players, so one is written when the
// chain is created from scratch.
func mdirHandler() []byte {
	body := make([]byte, 4+4+4+4+4+12)
	copy(body[8:12], "mdir")
	copy(body[12:16], "appl")
	return atom("hdlr", body)
}

func rebuildMeta(meta []byte, mutate mp4Mutator) []byte {
	// meta is a full box: four bytes of version and flags precede its children.
	versionFlags := []byte{0, 0, 0, 0}
	var rest []byte
	if len(meta) >= 4 {
		copy(versionFlags, meta[:4])
		rest = meta[4:]
	}

	var children [][]byte
	sawIlst, sawHdlr := false, false
	walkAtoms(rest, func(typ string, b []byte) bool {
		switch typ {
		case "ilst":
			sawIlst = true
			children = append(children, rebuildILST(b, mutate))
		case "hdlr":
			sawHdlr = true
			children = append(children, atom(typ, b))
		default:
			children = append(children, atom(typ, b))
		}
		return true
	})
	if !sawHdlr {
		children = append([][]byte{mdirHandler()}, children...)
	}
	if !sawIlst {
		children = append(children, rebuildILST(nil, mutate))
	}
	return atom("meta", append([][]byte{versionFlags}, children...)...)
}

// rebuildILST parses the item list, hands it to the mutator, and re-serialises
// whatever comes back.
func rebuildILST(ilst []byte, mutate mp4Mutator) []byte {
	var cur Metadata
	var items []mp4Item
	if ilst != nil {
		parseILST(ilst, &cur)
		walkAtoms(ilst, func(typ string, body []byte) bool {
			items = append(items, mp4Item{Name: typ, Body: body})
			return true
		})
	}
	out := mutate(items, &cur)

	encoded := make([][]byte, 0, len(out))
	for _, it := range out {
		encoded = append(encoded, atom(it.Name, it.Body))
	}
	return atom("ilst", encoded...)
}

// applyEditToILST folds an edit into the item list, keeping every item the
// edit does not mention.
func applyEditToILST(items []mp4Item, e *Edit, cur *Metadata) []mp4Item {
	// Replacements keyed by atom name. A key present with a nil value means
	// the item should be dropped.
	repl := map[string][]byte{}
	setText := func(name string, v *string) {
		if v == nil {
			return
		}
		if *v == "" {
			repl[name] = nil
			return
		}
		repl[name] = mp4TextBody(*v)
	}
	setText(atomTitle, e.Title)
	setText(atomArtist, e.Artist)
	setText(atomAlbumArtist, e.AlbumArtist)
	setText(atomAlbum, e.Album)
	setText(atomComposer, e.Composer)
	setText(atomComment, e.Comment)

	if e.Genre != nil {
		setText(atomGenreText, e.Genre)
		repl[atomGenreID] = nil // a numeric genre would shadow the text one
	}
	if e.Year != nil {
		if *e.Year > 0 {
			repl[atomDate] = mp4TextBody(itoa(int64(*e.Year)))
		} else {
			repl[atomDate] = nil
		}
	}
	if e.Track != nil || e.TrackTotal != nil {
		repl[atomTrack] = mp4NumberBody(pick(e.Track, cur.Track), pick(e.TrackTotal, cur.TrackTotal), 8)
	}
	if e.Disc != nil || e.DiscTotal != nil {
		repl[atomDisc] = mp4NumberBody(pick(e.Disc, cur.Disc), pick(e.DiscTotal, cur.DiscTotal), 6)
	}

	out := make([]mp4Item, 0, len(items)+len(repl))
	seen := map[string]bool{}
	for _, it := range items {
		// Artwork is handled separately: unlike every other item there may be
		// several covr atoms, so they are dropped here and re-added below.
		if it.Name == atomCover && e.Artwork != nil {
			continue
		}
		if v, ok := repl[it.Name]; ok {
			if !seen[it.Name] && v != nil {
				out = append(out, mp4Item{Name: it.Name, Body: v})
			}
			seen[it.Name] = true
			continue
		}
		out = append(out, it)
	}
	if e.Artwork != nil {
		for i := range *e.Artwork {
			out = append(out, mp4Item{Name: atomCover, Body: encodeMP4Cover(&(*e.Artwork)[i])})
		}
	}
	for _, name := range ilstOrder {
		if v, ok := repl[name]; ok && !seen[name] && v != nil {
			out = append(out, mp4Item{Name: name, Body: v})
		}
	}
	return out
}

// ilstOrder gives newly added items a deterministic order.
var ilstOrder = []string{
	atomTitle, atomArtist, atomAlbumArtist, atomAlbum, atomGenreText,
	atomDate, atomTrack, atomDisc, atomComposer, atomComment,
}

func pick(v *int32, cur int32) int32 {
	if v != nil {
		return *v
	}
	return cur
}

// mp4TextBody builds the body of a metadata item holding UTF-8 text.
func mp4TextBody(value string) []byte {
	data := make([]byte, 8, 8+len(value))
	binary.BigEndian.PutUint32(data[0:4], 1) // well-known type 1: UTF-8
	data = append(data, value...)
	return atom("data", data)
}

// mp4NumberBody builds the body of a trkn or disk item. Apple writes eight
// bytes for trkn and six for disk, and some players are fussy about it.
func mp4NumberBody(num, total int32, payloadLen int) []byte {
	if num <= 0 && total <= 0 {
		return nil
	}
	data := make([]byte, 8+payloadLen)
	// Type 0 marks the payload as implicit/binary rather than text.
	binary.BigEndian.PutUint16(data[8+2:8+4], uint16(clampU16(num)))
	binary.BigEndian.PutUint16(data[8+4:8+6], uint16(clampU16(total)))
	return atom("data", data)
}

func clampU16(v int32) int32 {
	if v < 0 {
		return 0
	}
	if v > 0xFFFF {
		return 0xFFFF
	}
	return v
}

// patchChunkOffsets adjusts every stco/co64 entry in moov by delta, which is
// required when the metadata resize moves the media data.
func patchChunkOffsets(moov []byte, delta int64) {
	if len(moov) < 8 {
		return
	}
	var visit func(b []byte)
	visit = func(b []byte) {
		walkAtoms(b, func(typ string, body []byte) bool {
			switch typ {
			case "stco":
				patchSTCO(body, delta)
			case "co64":
				patchCO64(body, delta)
			case "moov", "trak", "mdia", "minf", "stbl", "edts":
				visit(body)
			}
			return true
		})
	}
	visit(moov[8:])
}

func patchSTCO(b []byte, delta int64) {
	if len(b) < 8 {
		return
	}
	n := int(binary.BigEndian.Uint32(b[4:8]))
	for i := 0; i < n && 8+i*4+4 <= len(b); i++ {
		p := b[8+i*4 : 8+i*4+4]
		v := int64(binary.BigEndian.Uint32(p)) + delta
		if v < 0 {
			v = 0
		}
		binary.BigEndian.PutUint32(p, uint32(v))
	}
}

func patchCO64(b []byte, delta int64) {
	if len(b) < 8 {
		return
	}
	n := int(binary.BigEndian.Uint32(b[4:8]))
	for i := 0; i < n && 8+i*8+8 <= len(b); i++ {
		p := b[8+i*8 : 8+i*8+8]
		v := int64(binary.BigEndian.Uint64(p)) + delta
		if v < 0 {
			v = 0
		}
		binary.BigEndian.PutUint64(p, uint64(v))
	}
}

// replaceTail rewrites the file from off onward, dropping the old moov and any
// atoms that followed it and re-emitting them after the new moov.
func replaceTail(path string, src *os.File, size, off int64, newMoov []byte, atoms []topAtom, moovIdx int) error {
	var trailing []byte
	for _, a := range atoms[moovIdx+1:] {
		if isFreeAtom(a.typ) {
			continue // stale padding; no reason to carry it forward
		}
		b := make([]byte, a.size)
		if _, err := src.ReadAt(b, a.off); err != nil && err != io.EOF {
			return err
		}
		trailing = append(trailing, b...)
	}
	if err := truncateAndWrite(src, off, append(newMoov, trailing...)); err != nil {
		return err
	}
	return src.Sync()
}

// truncateAndWrite replaces everything from off with data.
func truncateAndWrite(f *os.File, off int64, data []byte) error {
	if _, err := f.WriteAt(data, off); err != nil {
		return err
	}
	return f.Truncate(off + int64(len(data)))
}

// replaceRange rebuilds the file with the bytes at [off, off+oldLen) swapped
// for data, via a temporary file in the same directory.
func replaceRange(path string, src *os.File, size, off, oldLen int64, data []byte) error {
	fi, err := src.Stat()
	if err != nil {
		return err
	}
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
	if _, err := io.Copy(tmp, io.NewSectionReader(src, 0, off)); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	rest := size - (off + oldLen)
	if rest > 0 {
		if _, err := io.Copy(tmp, io.NewSectionReader(src, off+oldLen, rest)); err != nil {
			return err
		}
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

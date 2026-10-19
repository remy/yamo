package tags

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

// Editing a tag by its native key.
//
// Every other write here goes through Edit, in the canonical vocabulary, and
// that is right for the fields a library is organised by. It leaves no way to
// touch anything else: the ReplayGain values a ripper wrote under TXXX, an
// encoder's comment, a MusicBrainz id that has nowhere in the catalogue, a
// freeform atom a tagger left behind. Clean up can only remove all of those at
// once, which is not the same as being able to correct one of them.
//
// The key is the one RawTags reports — RemovedTag.Display — so whatever a
// client was shown is what it sends back: "TXXX:REPLAYGAIN_TRACK_GAIN",
// "COMM:iTunNORM", "----:com.apple.iTunes:iTunSMPB", "ENCODER". Addressing by
// anything narrower than that label (an index into the frame list, say) would
// go stale the moment another write reordered the frames.
//
// A label can name more than one entry: Vorbis comments repeat freely, and two
// COMM frames can share a description in different languages. Setting such a
// key writes its first entry and drops the rest, and removing it removes them
// all. The alternative, editing the entries one by one, needs an identity for
// each that survives a round trip, which none of these formats provides.
//
// Only text can be set. A cover, a PRIV blob or an MP4 integer atom can be
// removed, but there is no honest way to accept a string for one: the listing
// shows them as a size, not as their value.

// ErrNativeKey is returned for a key that is not in the file, or one whose
// value is not text and so cannot be set.
var ErrNativeKey = errors.New("tags: cannot edit that tag")

// NativeEdits maps native keys to their new values. A nil value, or an empty
// one, removes every entry under the key: an empty tag is not a thing anybody
// wants kept, and readers treat it the same as a missing one.
type NativeEdits map[string]*string

// EditNative applies native edits to one file and reports whether it wrote.
//
// Every key must already be present. That is a restriction rather than an
// oversight: creating a tag needs its container chosen for it (TXXX or COMM,
// which language, which freeform namespace), and that is a question nobody is
// asking yet.
func EditNative(path string, edits NativeEdits) (bool, error) {
	if len(edits) == 0 {
		return false, nil
	}
	format, err := detectFormat(path)
	if err != nil {
		return false, err
	}
	switch format {
	case FormatMP3:
		return editNativeID3(path, edits)
	case FormatFLAC:
		return editNativeFLAC(path, edits)
	case FormatOggVorbis, FormatOpus:
		return editNativeOgg(path, edits)
	case FormatMP4:
		return editNativeMP4(path, edits)
	}
	return false, fmt.Errorf("%w: %s files cannot be written by this build", ErrUnsupported, format)
}

// wanted reports what an edit asks of a key: whether to keep an entry at all,
// and the text to write into it.
func (e NativeEdits) wanted(key string) (value string, remove bool) {
	v := e[key]
	if v == nil || strings.TrimSpace(*v) == "" {
		return "", true
	}
	return strings.TrimSpace(*v), false
}

// nativeEditor walks a file's entries in order and decides each one's fate. It
// is shared by every format so that "first entry written, the rest dropped"
// and "every key must exist" are one rule rather than four.
type nativeEditor struct {
	edits   NativeEdits
	seen    map[string]bool
	changed bool
}

func newNativeEditor(edits NativeEdits) *nativeEditor {
	return &nativeEditor{edits: edits, seen: map[string]bool{}}
}

// visit is called for one entry with its key, its current text, and whether
// that text can be replaced. It returns whether to keep the entry and, when
// replace is true, the text to write into it.
func (n *nativeEditor) visit(key, current string, editable bool) (keep, replace bool, value string, err error) {
	if _, ok := n.edits[key]; !ok {
		return true, false, "", nil
	}
	first := !n.seen[key]
	n.seen[key] = true
	value, remove := n.edits.wanted(key)
	if remove || !first {
		n.changed = true
		return false, false, "", nil
	}
	if !editable {
		return false, false, "", fmt.Errorf("%w: %s is not text, so it can only be removed", ErrNativeKey, key)
	}
	if value == current {
		return true, false, "", nil
	}
	n.changed = true
	return true, true, value, nil
}

// finish refuses keys the file never held, so a client that sent a stale
// listing hears about it rather than seeing "saved" and nothing change.
func (n *nativeEditor) finish() error {
	for k := range n.edits {
		if !n.seen[k] {
			return fmt.Errorf("%w: the file has no %s", ErrNativeKey, k)
		}
	}
	return nil
}

// --- ID3 -------------------------------------------------------------------

func editNativeID3(path string, edits NativeEdits) (bool, error) {
	f, size, oldTagSize, tag, err := openID3(path, true)
	if err != nil {
		return false, err
	}
	defer f.Close()
	// The same translation a strip makes, so the keys match the listing: it
	// reports a v2.2 file under v2.3 names, and this writes it out as v2.3.
	upgradeV22Frames(tag)

	ed := newNativeEditor(edits)
	out := tag.frames[:0]
	for _, fr := range tag.frames {
		cur, editable := id3NativeText(fr.id, fr.payload)
		keep, replace, value, err := ed.visit(id3Label(fr.id, fr.payload), cur, editable)
		if err != nil {
			return false, err
		}
		if !keep {
			continue
		}
		if replace {
			fr = id3Frame{id: fr.id, payload: id3NativePayload(tag.major, fr.id, fr.payload, value)}
		}
		out = append(out, fr)
	}
	if err := ed.finish(); err != nil {
		return false, err
	}
	if !ed.changed {
		return false, nil
	}
	tag.frames = out
	return true, flushID3(path, f, size, oldTagSize, tag)
}

// id3NativeText is a frame's whole value, for editing, and whether it is text
// at all. Unlike DescribeFrame it is not clipped, and for the frames that carry
// a description it is the value alone, since the description is in the key.
func id3NativeText(id string, payload []byte) (string, bool) {
	switch {
	case id == "TXXX":
		_, v := userText(payload)
		return v, true
	case id == "COMM":
		return commentText(payload), len(payload) >= 4
	case id == "WXXX":
		return "", false
	case strings.HasPrefix(id, "T"):
		return frameText(payload), true
	case strings.HasPrefix(id, "W"):
		return strings.TrimRight(string(payload), "\x00"), true
	}
	return "", false
}

// id3NativePayload rebuilds a frame around a new value, keeping whatever else
// the old one carried — the description, the comment's language — since those
// are part of what the key names.
func id3NativePayload(major byte, id string, old []byte, value string) []byte {
	switch {
	case id == "TXXX":
		desc, _ := userText(old)
		enc := id3PairEncoding(major, desc, value)
		out := []byte{enc}
		out = append(out, encodeIn(enc, desc)...)
		out = append(out, terminator(enc)...)
		return append(out, encodeIn(enc, value)...)
	case id == "COMM":
		lang := []byte("eng")
		var desc string
		if len(old) >= 4 {
			lang = old[1:4]
			if d, _, ok := splitTerminated(old[4:], old[0]); ok {
				desc = decodeText(old[0], d)
			}
		}
		enc := id3PairEncoding(major, desc, value)
		out := []byte{enc}
		out = append(out, lang...)
		out = append(out, encodeIn(enc, desc)...)
		out = append(out, terminator(enc)...)
		return append(out, encodeIn(enc, value)...)
	case strings.HasPrefix(id, "W"):
		return encodeLatin1(value)
	}
	return encodeTextPayload(major, value)
}

// id3PairEncoding chooses one encoding for a frame holding two strings, a
// description and a value. A frame has a single encoding byte, so encodeString
// cannot be asked about each string separately: a Latin-1 description beside a
// value that needs UTF-16 has to go out as UTF-16 too.
func id3PairEncoding(major byte, a, b string) byte {
	switch {
	case major >= 4:
		return encUTF8
	case latin1Encodable(a) && latin1Encodable(b):
		return encISO8859
	}
	return encUTF16
}

// encodeIn writes a string in an encoding already chosen for the frame.
func encodeIn(enc byte, s string) []byte {
	switch enc {
	case encUTF16:
		return encodeUTF16LEWithBOM(s)
	case encUTF8:
		return []byte(s)
	}
	return encodeLatin1(s)
}

// --- Vorbis comments -------------------------------------------------------

// vorbisPictureField is the base64 cover an Ogg file carries in its comment,
// and which a FLAC converted from one may carry too. Its value is an image, not
// text, whatever the container says.
const vorbisPictureField = "METADATA_BLOCK_PICTURE"

func editVorbisNative(vc *vorbisComment, ed *nativeEditor) error {
	out := vc.fields[:0]
	for _, f := range vc.fields {
		editable := !strings.EqualFold(f.key, vorbisPictureField)
		keep, replace, value, err := ed.visit(f.key, f.value, editable)
		if err != nil {
			return err
		}
		if !keep {
			continue
		}
		if replace {
			f.value = value
		}
		out = append(out, f)
	}
	vc.fields = out
	return nil
}

func editNativeFLAC(path string, edits NativeEdits) (bool, error) {
	ed := newNativeEditor(edits)
	var verr error
	err := updateFLAC(path, func(vc *vorbisComment, other []flacBlock) ([]flacBlock, bool) {
		if verr = editVorbisNative(vc, ed); verr != nil {
			return other, false
		}
		kept := other[:0]
		for _, b := range other {
			if b.typ == flacPicture {
				if keep, _, _, err := ed.visit(flacPictureKey, "", false); err != nil {
					verr = err
					return other, false
				} else if !keep {
					continue
				}
			}
			kept = append(kept, b)
		}
		if verr = ed.finish(); verr != nil {
			return other, false
		}
		return kept, ed.changed
	})
	if verr != nil {
		return false, verr
	}
	return ed.changed, err
}

func editNativeOgg(path string, edits NativeEdits) (bool, error) {
	ed := newNativeEditor(edits)
	var verr error
	err := updateOgg(path, func(vc *vorbisComment) bool {
		if verr = editVorbisNative(vc, ed); verr != nil {
			return false
		}
		if verr = ed.finish(); verr != nil {
			return false
		}
		return ed.changed
	})
	if verr != nil {
		return false, verr
	}
	return ed.changed, err
}

// --- MP4 -------------------------------------------------------------------

func editNativeMP4(path string, edits NativeEdits) (bool, error) {
	// Decided on a read first, because updateMP4 rewrites whatever its mutator
	// returns: asking it to make no change would still cost a write, and a
	// refused key has to be refused before anything touches the file.
	items, err := readMP4Items(path)
	if err != nil {
		return false, err
	}
	ed := newNativeEditor(edits)
	if _, err := editMP4Items(items, ed); err != nil {
		return false, err
	}
	if err := ed.finish(); err != nil {
		return false, err
	}
	if !ed.changed {
		return false, nil
	}
	err = updateMP4(path, func(items []mp4Item, _ *Metadata) []mp4Item {
		out, _ := editMP4Items(items, newNativeEditor(edits))
		return out
	})
	return err == nil, err
}

func editMP4Items(items []mp4Item, ed *nativeEditor) ([]mp4Item, error) {
	out := make([]mp4Item, 0, len(items))
	for _, it := range items {
		cur, editable := mp4NativeText(it.Body)
		keep, replace, value, err := ed.visit(describeMP4Item(it.Name, it.Body), cur, editable)
		if err != nil {
			return nil, err
		}
		if !keep {
			continue
		}
		if replace {
			it = mp4Item{Name: it.Name, Body: mp4ReplaceText(it.Body, value)}
		}
		out = append(out, it)
	}
	return out, nil
}

// mp4NativeText is an item's text, and whether it holds text at all. Only the
// UTF-8 data type counts: an integer atom rendered as digits would come back as
// text and be written as text, which is a different atom to every reader.
func mp4NativeText(body []byte) (string, bool) {
	text, ok := "", false
	walkAtoms(body, func(typ string, b []byte) bool {
		if typ != "data" || len(b) < 8 {
			return true
		}
		if binary.BigEndian.Uint32(b[0:4])&0x00FFFFFF == 1 {
			text, ok = strings.TrimSpace(trimNulUTF8(string(b[8:]))), true
		}
		return false
	})
	return text, ok
}

// mp4ReplaceText swaps an item's data atom for a UTF-8 one holding value, and
// keeps everything else — a freeform item's mean and name atoms are its key.
func mp4ReplaceText(body []byte, value string) []byte {
	var out []byte
	done := false
	walkAtoms(body, func(typ string, b []byte) bool {
		if typ == "data" {
			if !done {
				out = append(out, mp4TextBody(value)...)
				done = true
			}
			return true
		}
		out = append(out, atom(typ, b)...)
		return true
	})
	if !done {
		out = append(out, mp4TextBody(value)...)
	}
	return out
}

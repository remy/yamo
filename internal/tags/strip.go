package tags

import (
	"os"
	"sort"
	"strings"
)

// DefaultKeepTags is the metadata a strip preserves.
//
// The first group identifies the song. The second exists so that a library
// groups and sorts correctly rather than because it describes the recording.
// Everything else — encoder signatures, comments, private blobs, URLs,
// ratings, external identifiers — is removed.
var DefaultKeepTags = []Tag{
	TagTitle, TagArtist, TagAlbum, TagAlbumArtist,
	TagTrack, TagDisc, TagGenre, TagDate,

	TagCompilation, TagComposer, TagArtwork,
	TagTitleSort, TagArtistSort, TagAlbumSort, TagAlbumArtistSort,

	// Everything iTunes wrote. Gapless data is the one that would hurt to lose
	// — it is the only record of where the encoder padding starts, players
	// read it, and nothing can reconstruct it — but the rest is kept as well
	// so that a library ripped and bought through iTunes comes out of a strip
	// still describing itself the way iTunes described it.
	TagGapless, TagSoundCheck, TagITunes,
}

// KeepSet is the set of canonical tags to preserve.
type KeepSet map[Tag]bool

// NewKeepSet builds a keep set from canonical tags.
func NewKeepSet(tags []Tag) KeepSet {
	k := make(KeepSet, len(tags))
	for _, t := range tags {
		k[t] = true
	}
	return k
}

// ParseKeepSet resolves a list of names into a keep set. Names may be
// canonical ("albumartist") or ID3 frame identifiers ("TPE2"), so a list
// written in either vocabulary works.
func ParseKeepSet(names []string) (KeepSet, []string) {
	k := KeepSet{}
	var unknown []string
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if t, ok := LookupTag(n); ok {
			k[t] = true
		} else {
			unknown = append(unknown, n)
		}
	}
	return k, unknown
}

// Sorted lists the members in name order, for display.
func (k KeepSet) Sorted() []Tag {
	out := make([]Tag, 0, len(k))
	for t := range k {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// RemovedTag is one piece of metadata a strip took out, retained in enough
// detail to put it back.
type RemovedTag struct {
	Format  Format `json:"format"`
	Name    string `json:"name"`            // native key, used to restore it
	Label   string `json:"label,omitempty"` // how it reads in a report
	Tag     Tag    `json:"tag"`             // canonical meaning, if it had one
	Meaning string `json:"meaning,omitempty"`
	Sample  string `json:"sample,omitempty"`
	Bytes   int    `json:"bytes"`
	Raw     []byte `json:"raw"`
}

// Display is the key as it should appear in a report.
func (r RemovedTag) Display() string {
	if r.Label != "" {
		return r.Label
	}
	return r.Name
}

// StripReport describes what a strip did, or would do, to one file.
type StripReport struct {
	Path        string
	Format      Format
	Removed     []RemovedTag
	Kept        int
	Upgraded    bool // an ID3v2.2 tag was rewritten as v2.3 while removing frames
	NoTag       bool // the file carries no metadata container at all
	Unsupported bool // this build cannot write the file's format
	Changed     bool // metadata was removed, or would be in a dry run

	// NonCanonical lists kept tags the file does not hold the way this library
	// writes them — under an older name, in a numeric form, or missing one of
	// the frames a write would produce. Readers still find them; they just
	// find them somewhere the next tool along may not look. Rewriting the
	// field puts it right.
	NonCanonical []Tag
}

// noteNonCanonical records a tag once.
func (r *StripReport) noteNonCanonical(t Tag) {
	for _, have := range r.NonCanonical {
		if have == t {
			return
		}
	}
	r.NonCanonical = append(r.NonCanonical, t)
}

// BytesRemoved totals the on-disk cost of what was removed.
func (r *StripReport) BytesRemoved() int {
	n := 0
	for _, t := range r.Removed {
		n += t.Bytes
	}
	return n
}

// StripFile removes every piece of metadata whose meaning is not in keep.
//
// With apply false nothing is written and the report describes what would
// happen, which is the only responsible default for an operation that
// permanently discards data across a whole library.
//
// The keep list is expressed in canonical tags, so the same list applies to
// every format: an album artist is preserved whether the file spells it TPE2,
// ALBUMARTIST or aART.
func StripFile(path string, keep KeepSet, apply bool) (*StripReport, error) {
	format, err := detectFormat(path)
	if err != nil {
		return nil, err
	}
	rep := &StripReport{Path: path, Format: format}

	switch format {
	case FormatMP3:
		err = stripID3(path, keep, apply, rep)
	case FormatFLAC:
		err = stripFLAC(path, keep, apply, rep)
	case FormatOggVorbis, FormatOpus:
		err = stripOgg(path, keep, apply, rep)
	case FormatMP4:
		err = stripMP4(path, keep, apply, rep)
	default:
		rep.Unsupported = true
		return rep, nil
	}
	if err != nil {
		return nil, err
	}
	rep.Changed = len(rep.Removed) > 0
	return rep, nil
}

// stripID3 filters an MP3's ID3v2 frames.
//
// ID3v2.2 tags are rewritten as v2.3, because there is no v2.2 writer here.
// The frames are translated before the keep list is applied, so a v2.2 "TP1"
// is recognised as an artist rather than removed as unknown.
func stripID3(path string, keep KeepSet, apply bool, rep *StripReport) error {
	f, size, oldTagSize, tag, err := openID3(path, apply)
	if err != nil {
		return err
	}
	defer f.Close()

	if oldTagSize == 0 {
		rep.NoTag = true
		return nil
	}
	wasV22 := tag.major < 3
	upgradeV22Frames(tag)

	kept := tag.frames[:0]
	for _, fr := range tag.frames {
		t := tagForID3Frame(fr.id, fr.payload)
		if keep[t] {
			kept = append(kept, fr)
			switch {
			case wasV22:
				// Every frame in a v2.2 tag is under its three-character name.
				rep.noteNonCanonical(t)
			case fr.id == "TXXX" && len(t.NativeKeys(FormatMP3)) > 0:
				// A user-defined frame holding something with a frame of its
				// own. ffmpeg writes the compilation flag as TXXX:TCMP and the
				// comment as TXXX:comment, and a reader that does not know
				// that finds neither.
				rep.noteNonCanonical(t)
			case t == TagGenre:
				if v := frameText(fr.payload); normaliseGenre(v) != v {
					rep.noteNonCanonical(t) // "(19)" rather than "Industrial"
				}
			}
			continue
		}
		rep.Removed = append(rep.Removed, RemovedTag{
			Format: FormatMP3, Name: fr.id, Label: id3Label(fr.id, fr.payload),
			Tag: t, Meaning: tagMeaning(t, frameMeaning(fr.id)),
			Sample: DescribeFrame(fr.id, fr.payload),
			Bytes:  len(fr.payload) + 10, Raw: clone(fr.payload),
		})
	}
	// A date with no TDRL beside it. ID3 separates the recording year from the
	// release date and MP4 does not, so an MP3 carrying only a year frame
	// reports no release date at all while an M4A of the same song reports
	// one; see the date block in write_id3.go.
	if keep[TagDate] && hasFrameID(kept, "TYER", "TDRC") && !hasFrameID(kept, "TDRL") {
		rep.noteNonCanonical(TagDate)
	}

	tag.frames = kept
	rep.Kept = len(kept)

	// Only write when something is actually being removed. A file that already
	// contains nothing but keepers is left alone rather than rewritten, which
	// would put the whole library through a needless round of IO.
	if !apply || len(rep.Removed) == 0 {
		return nil
	}
	if err := flushID3(path, f, size, oldTagSize, tag); err != nil {
		return err
	}
	rep.Upgraded = wasV22
	return nil
}

// hasFrameID reports whether any of the named frames is present.
func hasFrameID(frames []id3Frame, ids ...string) bool {
	for _, f := range frames {
		for _, id := range ids {
			if f.id == id {
				return true
			}
		}
	}
	return false
}

// tagMeaning prefers the canonical description, so a report reads the same
// whichever container the metadata came out of, and falls back to the native
// key's own meaning when nothing recognised it.
func tagMeaning(t Tag, fallback string) string {
	if t != TagUnknown {
		return t.Desc()
	}
	return fallback
}

// id3Label distinguishes the frames whose meaning lives in a description, so a
// report says "COMM:iTunSMPB" rather than listing three unrelated things as
// one COMM total.
func id3Label(id string, payload []byte) string {
	switch id {
	case "TXXX", "TXX":
		if d, _ := userText(payload); d != "" {
			return id + ":" + d
		}
	case "COMM", "COM":
		if d := id3CommentDescription(payload); d != "" {
			return id + ":" + d
		}
	}
	return id
}

// vorbisAliases are the field names this library reads but never writes, so a
// file holding one of them keeps its value somewhere the next tool along may
// not look. It is a list rather than "anything but the first spelling in
// tagSpecs", because several of those later entries are separate fields sharing
// a canonical tag — TRACKTOTAL is not another way of saying TRACKNUMBER.
var vorbisAliases = map[string]bool{
	"PERFORMER": true, "ALBUM ARTIST": true, "ENSEMBLE": true,
	"YEAR": true, "DESCRIPTION": true, "ENCODED-BY": true, "ORGANIZATION": true,
	"UNSYNCEDLYRICS": true, "MIXARTIST": true, "CONTENTGROUP": true,
}

// stripVorbisFields filters a Vorbis comment in place and records what went.
func stripVorbisFields(vc *vorbisComment, keep KeepSet, format Format, rep *StripReport) {
	out := vc.fields[:0]
	for _, f := range vc.fields {
		t := tagForVorbisField(f.key)
		if keep[t] {
			out = append(out, f)
			if vorbisAliases[strings.ToUpper(f.key)] {
				rep.noteNonCanonical(t)
			}
			continue
		}
		rep.Removed = append(rep.Removed, RemovedTag{
			Format: format, Name: f.key, Tag: t, Meaning: t.Desc(),
			Sample: clip(f.value),
			Bytes:  4 + len(f.key) + 1 + len(f.value),
			Raw:    []byte(f.value),
		})
	}
	vc.fields = out
	rep.Kept += len(out)
}

// flacPictureKey names a FLAC PICTURE block in reports and backups. Cover art
// lives in its own metadata block rather than in a comment field, so it needs
// a key of its own.
const flacPictureKey = "PICTURE"

func stripFLAC(path string, keep KeepSet, apply bool, rep *StripReport) error {
	var mutated bool
	err := updateFLAC(path, func(vc *vorbisComment, other []flacBlock) ([]flacBlock, bool) {
		stripVorbisFields(vc, keep, FormatFLAC, rep)

		kept := make([]flacBlock, 0, len(other))
		for _, b := range other {
			if b.typ == flacPicture && !keep[TagArtwork] {
				rep.Removed = append(rep.Removed, RemovedTag{
					Format: FormatFLAC, Name: flacPictureKey, Tag: TagArtwork,
					Meaning: TagArtwork.Desc(), Sample: byteCount(len(b.body)),
					Bytes: len(b.body) + 4, Raw: clone(b.body),
				})
				continue
			}
			kept = append(kept, b)
			rep.Kept++
		}
		mutated = len(rep.Removed) > 0
		// Only ask for a write when something is going, and never in a dry run.
		return kept, apply && mutated
	})
	return err
}

func stripOgg(path string, keep KeepSet, apply bool, rep *StripReport) error {
	format := FormatOggVorbis
	if FormatForPath(path) == FormatOpus {
		format = FormatOpus
	}
	return updateOgg(path, func(vc *vorbisComment) bool {
		stripVorbisFields(vc, keep, format, rep)
		return apply && len(rep.Removed) > 0
	})
}

func stripMP4(path string, keep KeepSet, apply bool, rep *StripReport) error {
	// A dry run must not open the file for writing, so the item list is read
	// directly rather than through the update path.
	if !apply {
		items, err := readMP4Items(path)
		if err != nil {
			return err
		}
		if items == nil {
			rep.NoTag = true
			return nil
		}
		filterMP4Items(items, keep, rep)
		return nil
	}
	return updateMP4(path, func(items []mp4Item, _ *Metadata) []mp4Item {
		return filterMP4Items(items, keep, rep)
	})
}

// filterMP4Items keeps the items whose meaning is in keep and records the rest.
func filterMP4Items(items []mp4Item, keep KeepSet, rep *StripReport) []mp4Item {
	out := make([]mp4Item, 0, len(items))
	for _, it := range items {
		t := tagForMP4Atom(it.Name, it.Body)
		if keep[t] {
			out = append(out, it)
			if it.Name == atomGenreID {
				rep.noteNonCanonical(t) // gnre holds an ID3v1 genre number
			}
			continue
		}
		rep.Removed = append(rep.Removed, RemovedTag{
			// The name is stored in printable form so it survives a backup
			// file; restoreMP4 converts it back.
			Format: FormatMP4, Name: MP4AtomName(it.Name), Label: describeMP4Item(it.Name, it.Body),
			Tag: t, Meaning: t.Desc(), Sample: mp4ItemSample(it),
			Bytes: len(it.Body) + 8, Raw: clone(it.Body),
		})
	}
	rep.Kept += len(out)
	return out
}

// readMP4Items reads the metadata item list without opening the file for
// writing, for dry runs.
func readMP4Items(path string) ([]mp4Item, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	off, moovSize, err := findMoov(f, fi.Size())
	if err != nil || moovSize <= 8 || moovSize > maxMoovSize {
		return nil, nil
	}
	buf := make([]byte, moovSize-8)
	if _, err := f.ReadAt(buf, off+8); err != nil {
		return nil, nil
	}

	var items []mp4Item
	found := false
	walkAtoms(buf, func(typ string, body []byte) bool {
		if typ != "udta" {
			return true
		}
		walkAtoms(body, func(typ string, body []byte) bool {
			if typ != "meta" || len(body) < 4 {
				return true
			}
			walkAtoms(body[4:], func(typ string, body []byte) bool {
				if typ != "ilst" {
					return true
				}
				found = true
				walkAtoms(body, func(typ string, body []byte) bool {
					items = append(items, mp4Item{Name: typ, Body: body})
					return true
				})
				return false
			})
			return true
		})
		return true
	})
	if !found {
		return nil, nil
	}
	if items == nil {
		items = []mp4Item{}
	}
	return items, nil
}

// mp4ItemSample renders an item's value for a report.
func mp4ItemSample(it mp4Item) string {
	if s := mp4DataString(it.Body); s != "" {
		return clip(s)
	}
	if b := mp4DataBytes(it.Body); len(b) > 0 {
		return byteCount(len(b))
	}
	return byteCount(len(it.Body))
}

// RestoreFile puts previously removed metadata back. Entries whose key is
// already present are skipped, so restoring twice is harmless and a restore
// never overwrites an edit made since the strip.
func RestoreFile(path string, removed []RemovedTag) (int, error) {
	if len(removed) == 0 {
		return 0, nil
	}
	format, err := detectFormat(path)
	if err != nil {
		return 0, err
	}
	switch format {
	case FormatMP3:
		return restoreID3(path, removed)
	case FormatFLAC:
		return restoreFLAC(path, removed)
	case FormatOggVorbis, FormatOpus:
		return restoreOgg(path, removed)
	case FormatMP4:
		return restoreMP4(path, removed)
	}
	return 0, nil
}

func restoreID3(path string, removed []RemovedTag) (int, error) {
	f, size, oldTagSize, tag, err := openID3(path, true)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	upgradeV22Frames(tag)

	present := map[string]bool{}
	for _, fr := range tag.frames {
		present[id3Label(fr.id, fr.payload)] = true
	}
	added := 0
	for _, r := range removed {
		if len(r.Name) != 4 || present[r.Display()] {
			continue
		}
		tag.frames = append(tag.frames, id3Frame{id: r.Name, payload: r.Raw})
		added++
	}
	if added == 0 {
		return 0, nil
	}
	if err := flushID3(path, f, size, oldTagSize, tag); err != nil {
		return 0, err
	}
	return added, nil
}

func restoreFLAC(path string, removed []RemovedTag) (int, error) {
	added := 0
	err := updateFLAC(path, func(vc *vorbisComment, other []flacBlock) ([]flacBlock, bool) {
		added = restoreVorbisFields(vc, removed)
		for _, r := range removed {
			if r.Name == flacPictureKey {
				other = append(other, flacBlock{typ: flacPicture, body: r.Raw})
				added++
			}
		}
		return other, added > 0
	})
	return added, err
}

func restoreOgg(path string, removed []RemovedTag) (int, error) {
	added := 0
	err := updateOgg(path, func(vc *vorbisComment) bool {
		added = restoreVorbisFields(vc, removed)
		return added > 0
	})
	return added, err
}

func restoreVorbisFields(vc *vorbisComment, removed []RemovedTag) int {
	present := map[string]bool{}
	for _, f := range vc.fields {
		present[f.key] = true
	}
	added := 0
	for _, r := range removed {
		if r.Name == flacPictureKey || present[r.Name] {
			continue
		}
		vc.fields = append(vc.fields, vorbisField{key: r.Name, value: string(r.Raw)})
		added++
	}
	return added
}

func restoreMP4(path string, removed []RemovedTag) (int, error) {
	added := 0
	err := updateMP4(path, func(items []mp4Item, _ *Metadata) []mp4Item {
		present := map[string]bool{}
		for _, it := range items {
			present[describeMP4Item(it.Name, it.Body)] = true
		}
		for _, r := range removed {
			if present[r.Display()] {
				continue
			}
			items = append(items, mp4Item{Name: mp4AtomKey(r.Name), Body: r.Raw})
			added++
		}
		return items
	})
	return added, err
}

func clone(b []byte) []byte {
	out := make([]byte, len(b))
	copy(out, b)
	return out
}

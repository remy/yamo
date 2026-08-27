package tags

import (
	"sort"
	"strings"
)

// DefaultKeepFrames is the set of ID3v2 frames a strip preserves.
//
// It is deliberately small. The first group identifies the song; the second
// exists so a library browses correctly rather than because the data describes
// the recording. Everything absent from this list — encoder signatures,
// comments, private blobs, URLs, ratings, external identifiers — is removed.
var DefaultKeepFrames = []string{
	// Identity.
	"TIT2", // title
	"TPE1", // artist
	"TALB", // album
	"TPE2", // album artist
	"TRCK", // track number
	"TPOS", // disc number
	"TCON", // genre
	"TDRC", // recording date (ID3v2.4)
	"TYER", // year (ID3v2.3)

	// Behaviour: without these a library groups and sorts wrongly.
	"TCMP", // compilation flag; keeps Various Artists albums intact
	"TSOT", // title sort order
	"TSOP", // artist sort order
	"TSOA", // album sort order
	"TSO2", // album artist sort order
	"TCOM", // composer
	"APIC", // cover art
}

// KeepSet is a set of frame identifiers to preserve.
type KeepSet map[string]bool

// NewKeepSet builds a keep set, upper-casing identifiers so the flag that
// supplies them can be typed in any case.
func NewKeepSet(ids []string) KeepSet {
	k := make(KeepSet, len(ids))
	for _, id := range ids {
		if id = strings.ToUpper(strings.TrimSpace(id)); id != "" {
			k[id] = true
		}
	}
	return k
}

// Sorted returns the members in a stable order, for display.
func (k KeepSet) Sorted() []string {
	out := make([]string, 0, len(k))
	for id := range k {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// RemovedFrame is one frame a strip took out, retained so the operation can be
// undone.
type RemovedFrame struct {
	ID      string `json:"id"`
	Meaning string `json:"meaning"`
	Payload []byte `json:"payload"`
}

// Size is the frame's on-disk cost, header included.
func (r RemovedFrame) Size() int { return len(r.Payload) + 10 }

// StripReport describes what a strip did, or would do, to one file.
type StripReport struct {
	Path     string
	Removed  []RemovedFrame
	Kept     []string
	Upgraded bool // an ID3v2.2 tag was rewritten as v2.3 while removing frames
	NoTag    bool // the file carries no ID3v2 tag at all
	Changed  bool // frames were removed (or would be, in a dry run)
}

// BytesRemoved totals the on-disk cost of the removed frames.
func (r *StripReport) BytesRemoved() int {
	n := 0
	for _, f := range r.Removed {
		n += f.Size()
	}
	return n
}

// StripID3v2 removes every frame whose identifier is not in keep.
//
// With apply false nothing is written and the report describes what would
// happen, which is the only responsible default for an operation that
// permanently discards data across a whole library.
//
// ID3v2.2 tags are rewritten as v2.3, because there is no v2.2 writer here and
// because the keep list is expressed in v2.3 identifiers. The frames are
// translated first, so a v2.2 "TP1" is matched against "TPE1" and kept.
func StripID3v2(path string, keep KeepSet, apply bool) (*StripReport, error) {
	f, size, oldTagSize, tag, err := openID3(path, apply)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	rep := &StripReport{Path: path}
	if oldTagSize == 0 {
		rep.NoTag = true
		return rep, nil
	}

	wasV22 := tag.major < 3
	upgradeV22Frames(tag)

	kept := tag.frames[:0]
	for _, fr := range tag.frames {
		if keep[fr.id] {
			kept = append(kept, fr)
			rep.Kept = append(rep.Kept, fr.id)
			continue
		}
		// Copy the payload: it aliases the buffer the tag was parsed from,
		// which the caller may retain for a backup long after this returns.
		payload := make([]byte, len(fr.payload))
		copy(payload, fr.payload)
		rep.Removed = append(rep.Removed, RemovedFrame{
			ID: fr.id, Meaning: frameMeaning(fr.id), Payload: payload,
		})
	}
	tag.frames = kept
	rep.Changed = len(rep.Removed) > 0

	// Only write when something is actually being removed. A v2.2 file whose
	// every frame is on the keep list is left alone rather than rewritten as
	// v2.3: a strip removes tags, it does not normalise versions for its own
	// sake, and rewriting untouched files would put the whole library through
	// a needless round of IO.
	if !apply || !rep.Changed {
		return rep, nil
	}
	if err := flushID3(path, f, size, oldTagSize, tag); err != nil {
		return nil, err
	}
	rep.Upgraded = wasV22
	return rep, nil
}

// RestoreID3v2 puts previously removed frames back, skipping any whose
// identifier is present already so a repeated restore does not duplicate them.
func RestoreID3v2(path string, frames []RemovedFrame) (int, error) {
	if len(frames) == 0 {
		return 0, nil
	}
	f, size, oldTagSize, tag, err := openID3(path, true)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	upgradeV22Frames(tag)
	present := make(map[string]bool, len(tag.frames))
	for _, fr := range tag.frames {
		present[fr.id] = true
	}

	added := 0
	for _, fr := range frames {
		if present[fr.ID] || len(fr.ID) != 4 {
			continue
		}
		tag.frames = append(tag.frames, id3Frame{id: fr.ID, payload: fr.Payload})
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

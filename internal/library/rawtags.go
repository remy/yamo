package library

import (
	"errors"
	"fmt"
	"sort"

	"github.com/remy/yamo/internal/tags"
)

// What one file actually holds.
//
// Every other read here answers in the catalogue's vocabulary: an artist, an
// album, a year. That is the right answer nearly always, and exactly the wrong
// one before a strip, where the question is not "what does this track say" but
// "what is in this file" — the iTunes purchase account, the encoder's
// signature, the podcast frames, the 300KB of ratings a ripper left behind.
//
// A strip's dry run answers that across a selection, aggregated. This answers
// it for one file, which is what somebody looks at before deciding whether the
// aggregate is safe to act on.

// RawTag is one piece of metadata as the file holds it.
type RawTag struct {
	// Key is the native name: a frame id for MP3, an atom for MP4, a field
	// name for Vorbis comments. It is what the format calls it, not what this
	// library calls it.
	Key string `json:"key"`

	// Field is the canonical field this maps to, empty when it maps to none —
	// which is the interesting case, since a tag with no meaning here is one
	// nothing else is likely to read either.
	Field string `json:"field,omitempty"`

	Meaning string `json:"meaning,omitempty"`

	// Value is the tag rendered for display. Binary payloads are described
	// rather than dumped: a cover reads as its size and type.
	Value string `json:"value,omitempty"`

	// Text is the whole value, unclipped, when the tag holds text, and
	// Editable says so. Value is for reading down a list; Text is what an edit
	// starts from, and what EditRawTags accepts a replacement for. A cover or
	// a private blob has neither: it can be removed, never typed.
	Text     string `json:"text,omitempty"`
	Editable bool   `json:"editable"`

	Bytes int `json:"bytes"`

	// Kept says whether the default keep list would preserve this tag, which
	// is the question somebody reading this is usually about to ask.
	Kept bool `json:"kept"`
}

// RawTags is everything one file holds.
type RawTags struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	Format string `json:"format"`

	// Version is the track's, so a client editing from this listing can send
	// it back as If-Match without fetching the track as well.
	Version string `json:"version"`

	// Container names the metadata block the tags came out of, which is what
	// a person needs to make sense of the keys: "TPE2" is only meaningful
	// once you know it is an ID3v2 frame.
	Container string `json:"container,omitempty"`

	Tags  []RawTag `json:"tags"`
	Bytes int      `json:"bytes"`

	// NoTag means the file carries no metadata container at all, which is
	// different from carrying an empty one.
	NoTag bool `json:"noTag"`

	// Unsupported means this build can read the file's fields but cannot
	// enumerate its raw metadata. WMA, WAV and AIFF are read-only here, and
	// the strip machinery that produces this listing only covers the formats
	// it can also write.
	Unsupported bool `json:"unsupported"`

	// NonCanonical names kept fields held in a form this library does not
	// write — an ID3v2.2 frame, a genre stored as a number, a date carrying
	// more than the year. The strip endpoint's `normalize` is what tidies
	// them.
	NonCanonical []string `json:"nonCanonical,omitempty"`
}

// RawTags lists everything in one file's metadata.
//
// It is produced by asking the strip machinery what it would remove if the
// keep list were empty, which is the same walk of the container that a strip
// does and therefore agrees with it exactly. Nothing is written: an empty keep
// list is only ever passed here, where the answer is the report rather than
// the file.
func (s *Service) RawTags(id string) (RawTags, error) {
	t, err := s.Get(id)
	if err != nil {
		return RawTags{}, err
	}

	rep, err := tags.StripFile(t.Path, tags.KeepSet{}, false)
	if err != nil {
		return RawTags{}, err
	}

	out := RawTags{
		ID: id, Path: t.Path, Format: t.Format, Version: t.Version,
		Container:   containerName(rep.Format),
		NoTag:       rep.NoTag,
		Unsupported: rep.Unsupported,
		Tags:        []RawTag{},
	}

	keep := tags.NewKeepSet(tags.DefaultKeepTags)
	for _, r := range rep.Removed {
		out.Tags = append(out.Tags, RawTag{
			Key: r.Display(), Field: fieldNameFor(r.Tag), Meaning: r.Meaning,
			Value: r.Sample, Text: r.Text, Editable: r.Editable,
			Bytes: r.Bytes, Kept: keep[r.Tag],
		})
		out.Bytes += r.Bytes
	}
	for _, t := range rep.NonCanonical {
		out.NonCanonical = append(out.NonCanonical, t.Name())
	}
	sort.Strings(out.NonCanonical)

	// Largest first: the reason to look at this list is nearly always that
	// something in the file is bigger than it should be.
	sort.SliceStable(out.Tags, func(i, j int) bool { return out.Tags[i].Bytes > out.Tags[j].Bytes })
	return out, nil
}

// fieldNameFor names the canonical field a tag maps to, or nothing when it
// maps to none.
func fieldNameFor(t tags.Tag) string {
	if t == 0 {
		return ""
	}
	return t.Name()
}

// containerName says what kind of metadata block a format keeps its tags in.
func containerName(f tags.Format) string {
	switch f {
	case tags.FormatMP3:
		return "ID3v2"
	case tags.FormatFLAC, tags.FormatOggVorbis, tags.FormatOpus:
		return "Vorbis comments"
	case tags.FormatMP4:
		return "iTunes atoms"
	}
	return ""
}

// RawEdits maps the keys RawTags reports to new values; null removes the tag.
type RawEdits = tags.NativeEdits

// EditRawTags writes tags by their native keys, for the metadata the catalogue
// has no field for.
//
// It is a single-track operation with the same If-Match rule as Patch, and for
// the same reason: the keys came from a listing of one file, and a listing made
// before somebody else's write is exactly the thing that should not be acted
// on. There is no batch form, because a native key is only meaningful against
// the file it was read from — "COMM:iTunNORM" is a different thing in every
// track that has one.
//
// Like Patch it records no journal. Unlike Patch it cannot update the record
// from the request, since a key may or may not be one the catalogue reads —
// TPE1 is the artist, TXXX:MOOD is nothing — so the file is re-read afterwards,
// as a strip's are.
func (s *Service) EditRawTags(id string, edits RawEdits, ifMatch string) (Track, error) {
	if len(edits) == 0 {
		return Track{}, fmt.Errorf("%w: no changes given", ErrBadRequest)
	}
	s.mu.RLock()
	i, ok := s.lookupLocked(id)
	if !ok {
		s.mu.RUnlock()
		return Track{}, ErrNotFound
	}
	cur := s.cat.Tracks[i]
	s.mu.RUnlock()

	if ifMatch != "" && ifMatch != s.version(&cur) {
		return Track{}, ErrConflict
	}
	if !cur.Format.Writable() {
		return Track{}, fmt.Errorf("library: %s files cannot be written by this build", cur.Format)
	}

	var changed bool
	err := s.locks.withPath(cur.Path, func() error {
		var werr error
		changed, werr = tags.EditNative(cur.Path, edits)
		return werr
	})
	if errors.Is(err, tags.ErrNativeKey) {
		// The client's mistake, not the file's: a key that is not there or a
		// value that is not text. Said as a bad request so it is not mistaken
		// for the 422 a file this build cannot write produces.
		return Track{}, fmt.Errorf("%w: %v", ErrBadRequest, err)
	}
	if err != nil {
		return Track{}, err
	}
	if changed {
		s.bumpRev(cur.Path)
		s.refreshTracks([]string{id})
		s.events.publish(Event{Type: EventTracksChanged, TrackIDs: []string{id}})
	}
	return s.Get(id)
}

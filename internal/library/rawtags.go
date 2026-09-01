package library

import (
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
		ID: id, Path: t.Path, Format: t.Format,
		Container:   containerName(rep.Format),
		NoTag:       rep.NoTag,
		Unsupported: rep.Unsupported,
		Tags:        []RawTag{},
	}

	keep := tags.NewKeepSet(tags.DefaultKeepTags)
	for _, r := range rep.Removed {
		out.Tags = append(out.Tags, RawTag{
			Key: r.Display(), Field: fieldNameFor(r.Tag), Meaning: r.Meaning,
			Value: r.Sample, Bytes: r.Bytes, Kept: keep[r.Tag],
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

// Package catalog holds the in-memory library and its on-disk snapshot.
//
// The whole library lives in memory while the program runs. At 100k tracks
// that is well under 100MB, and it makes every search a linear pass over a
// contiguous slice rather than a query against an external store.
package catalog

import (
	"strings"

	"github.com/remy/yamo/internal/tags"
)

// Track is one file plus its metadata. Field order is chosen so the struct
// packs tightly; the numeric tail shares cache lines during scans.
type Track struct {
	Path string

	Title       string
	Artist      string
	AlbumArtist string
	Album       string
	Genre       string
	Composer    string
	Comment     string

	// The sort forms, held beside the fields they order rather than derived
	// from them: only the file can say that "Plan B" sorts under V for a
	// Various Artists compilation.
	TitleSort       string
	ArtistSort      string
	AlbumSort       string
	AlbumArtistSort string
	ComposerSort    string

	Size    int64
	ModTime int64 // Unix seconds

	Year       int32
	TrackNo    int32
	TrackTotal int32
	Disc       int32
	DiscTotal  int32
	DurationMS int32
	Bitrate    int32
	SampleRate int32

	Channels uint8
	Format   tags.Format
	HasArt   bool

	// Compilation is the Various Artists flag, which is what stops such an
	// album fragmenting into one album per track.
	Compilation bool

	// Changed records which fields have been edited in memory but not yet
	// written back to the file. Tracking the specific fields, rather than a
	// single dirty flag, means a save writes only what the user actually
	// altered and leaves every other tag in the file untouched.
	Changed FieldSet
}

// FieldSet is a bitmask over Field values.
type FieldSet uint32

func (s FieldSet) Has(f Field) bool { return s&(1<<f) != 0 }
func (s FieldSet) Any() bool        { return s != 0 }
func (s *FieldSet) Add(f Field)     { *s |= 1 << f }
func (s *FieldSet) Remove(f Field)  { *s &^= 1 << f }

// Fields lists the set members in Field order.
func (s FieldSet) Fields() []Field {
	var out []Field
	for f := Field(0); f < numFields; f++ {
		if s.Has(f) {
			out = append(out, f)
		}
	}
	return out
}

// Dirty reports whether the track has unsaved edits.
func (t *Track) Dirty() bool { return t.Changed.Any() }

// FromMetadata fills the tag-derived fields of a Track.
func (t *Track) FromMetadata(md *tags.Metadata) {
	t.Title = md.Title
	t.Artist = md.Artist
	t.AlbumArtist = md.AlbumArtist
	t.Album = md.Album
	t.Genre = md.Genre
	t.Composer = md.Composer
	t.Comment = md.Comment
	t.TitleSort = md.TitleSort
	t.ArtistSort = md.ArtistSort
	t.AlbumSort = md.AlbumSort
	t.AlbumArtistSort = md.AlbumArtistSort
	t.ComposerSort = md.ComposerSort
	t.Year = md.Year
	t.TrackNo = md.Track
	t.TrackTotal = md.TrackTotal
	t.Disc = md.Disc
	t.DiscTotal = md.DiscTotal
	t.DurationMS = md.DurationMS
	t.Bitrate = md.Bitrate
	t.SampleRate = md.SampleRate
	t.Channels = md.Channels
	t.HasArt = md.HasArt
	t.Compilation = md.Compilation
	t.Format = md.Format
}

// ToMetadata produces the metadata view used by the tag writer.
func (t *Track) ToMetadata() tags.Metadata {
	return tags.Metadata{
		Title:       t.Title,
		Artist:      t.Artist,
		AlbumArtist: t.AlbumArtist,
		Album:       t.Album,
		Genre:       t.Genre,
		Composer:    t.Composer,
		Comment:     t.Comment,

		TitleSort:       t.TitleSort,
		ArtistSort:      t.ArtistSort,
		AlbumSort:       t.AlbumSort,
		AlbumArtistSort: t.AlbumArtistSort,
		ComposerSort:    t.ComposerSort,

		Year:        t.Year,
		Track:       t.TrackNo,
		TrackTotal:  t.TrackTotal,
		Disc:        t.Disc,
		DiscTotal:   t.DiscTotal,
		Compilation: t.Compilation,
		Format:      t.Format,
	}
}

// Field identifies an editable or searchable metadata field.
type Field uint8

const (
	FieldTitle Field = iota
	FieldArtist
	FieldAlbumArtist
	FieldAlbum
	FieldGenre
	FieldComposer
	FieldComment
	FieldYear
	FieldTrackNo
	FieldDisc
	FieldCompilation
	FieldPath

	// The sort fields come after Path so that adding them cannot renumber the
	// ones above, and so Editable's single exclusion stays a comparison
	// against Path rather than a range.
	FieldTitleSort
	FieldArtistSort
	FieldAlbumSort
	FieldAlbumArtistSort
	FieldComposerSort
	numFields
)

// FieldNames are the canonical lower-case names accepted in search queries.
var FieldNames = [numFields]string{
	FieldTitle:       "title",
	FieldArtist:      "artist",
	FieldAlbumArtist: "albumartist",
	FieldAlbum:       "album",
	FieldGenre:       "genre",
	FieldComposer:    "composer",
	FieldComment:     "comment",
	FieldYear:        "year",
	FieldTrackNo:     "track",
	FieldDisc:        "disc",
	FieldCompilation: "compilation",
	FieldPath:        "path",

	FieldTitleSort:       "titlesort",
	FieldArtistSort:      "artistsort",
	FieldAlbumSort:       "albumsort",
	FieldAlbumArtistSort: "albumartistsort",
	FieldComposerSort:    "composersort",
}

// fieldAliases maps the short forms a query may use to a Field.
var fieldAliases = map[string]Field{
	"title": FieldTitle, "t": FieldTitle, "name": FieldTitle,
	"artist": FieldArtist, "a": FieldArtist, "ar": FieldArtist,
	"albumartist": FieldAlbumArtist, "aa": FieldAlbumArtist, "band": FieldAlbumArtist,
	"album": FieldAlbum, "al": FieldAlbum, "b": FieldAlbum,
	"genre": FieldGenre, "g": FieldGenre,
	"composer": FieldComposer, "c": FieldComposer,
	"comment": FieldComment,
	"year":    FieldYear, "y": FieldYear, "date": FieldYear,
	"track": FieldTrackNo, "trackno": FieldTrackNo, "n": FieldTrackNo,
	"disc": FieldDisc, "d": FieldDisc,
	"compilation": FieldCompilation, "comp": FieldCompilation, "va": FieldCompilation,
	"path": FieldPath, "p": FieldPath, "file": FieldPath,

	"titlesort": FieldTitleSort, "ts": FieldTitleSort,
	"artistsort": FieldArtistSort, "as": FieldArtistSort,
	"albumsort": FieldAlbumSort, "als": FieldAlbumSort,
	"albumartistsort": FieldAlbumArtistSort, "aas": FieldAlbumArtistSort,
	"composersort": FieldComposerSort, "cs": FieldComposerSort,
}

// LookupField resolves a query field name, which may be an alias.
func LookupField(name string) (Field, bool) {
	f, ok := fieldAliases[strings.ToLower(name)]
	return f, ok
}

// String returns the value of a text field. Numeric fields render as decimal.
func (t *Track) String(f Field) string {
	switch f {
	case FieldTitle:
		return t.Title
	case FieldArtist:
		return t.Artist
	case FieldAlbumArtist:
		return t.AlbumArtist
	case FieldAlbum:
		return t.Album
	case FieldGenre:
		return t.Genre
	case FieldComposer:
		return t.Composer
	case FieldComment:
		return t.Comment
	case FieldYear:
		return itoa32(t.Year)
	case FieldTrackNo:
		return itoa32(t.TrackNo)
	case FieldDisc:
		return itoa32(t.Disc)
	case FieldCompilation:
		if t.Compilation {
			return "1"
		}
		return ""
	case FieldPath:
		return t.Path
	case FieldTitleSort:
		return t.TitleSort
	case FieldArtistSort:
		return t.ArtistSort
	case FieldAlbumSort:
		return t.AlbumSort
	case FieldAlbumArtistSort:
		return t.AlbumArtistSort
	case FieldComposerSort:
		return t.ComposerSort
	}
	return ""
}

// IsTrue reads the spellings a client or a tag may use for a flag. Taggers and
// people disagree: "1", "true", "yes" and "on" all turn up, and an empty value
// means the flag is not set rather than that it is unknown.
func IsTrue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "false", "no", "off":
		return false
	}
	return true
}

// SetString assigns a text field, parsing decimals for the numeric ones.
func (t *Track) SetString(f Field, v string) {
	v = strings.TrimSpace(v)
	switch f {
	case FieldTitle:
		t.Title = v
	case FieldArtist:
		t.Artist = v
	case FieldAlbumArtist:
		t.AlbumArtist = v
	case FieldAlbum:
		t.Album = v
	case FieldGenre:
		t.Genre = v
	case FieldComposer:
		t.Composer = v
	case FieldComment:
		t.Comment = v
	case FieldYear:
		t.Year = atoi32(v)
	case FieldTrackNo:
		t.TrackNo = atoi32(v)
	case FieldDisc:
		t.Disc = atoi32(v)
	case FieldCompilation:
		t.Compilation = IsTrue(v)
	case FieldTitleSort:
		t.TitleSort = v
	case FieldArtistSort:
		t.ArtistSort = v
	case FieldAlbumSort:
		t.AlbumSort = v
	case FieldAlbumArtistSort:
		t.AlbumArtistSort = v
	case FieldComposerSort:
		t.ComposerSort = v
	}
}

// Editable reports whether a field can be changed in the editor. Path is
// derived from the filesystem, so it is read-only.
func (f Field) Editable() bool { return f != FieldPath }

func itoa32(v int32) string {
	if v == 0 {
		return ""
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [12]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func atoi32(s string) int32 {
	var n int32
	neg := false
	for i, c := range s {
		if i == 0 && (c == '-' || c == '+') {
			neg = c == '-'
			continue
		}
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int32(c-'0')
		if n < 0 {
			return 0 // overflowed; treat as unset
		}
	}
	if neg {
		return -n
	}
	return n
}

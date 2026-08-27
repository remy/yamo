// Package catalog holds the in-memory library and its on-disk snapshot.
//
// The whole library lives in memory while the program runs. At 100k tracks
// that is well under 100MB, and it makes every search a linear pass over a
// contiguous slice rather than a query against an external store.
package catalog

import (
	"strings"

	"github.com/remy/tag-manager/internal/tags"
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
		Year:        t.Year,
		Track:       t.TrackNo,
		TrackTotal:  t.TrackTotal,
		Disc:        t.Disc,
		DiscTotal:   t.DiscTotal,
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
	FieldPath
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
	FieldPath:        "path",
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
	"path": FieldPath, "p": FieldPath, "file": FieldPath,
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
	case FieldPath:
		return t.Path
	}
	return ""
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

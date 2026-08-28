// Package library is the service layer: it owns the catalogue and performs
// every operation on it.
//
// The command line, the terminal interface and the HTTP API are all clients of
// this package. Nothing above it touches the catalogue or a music file
// directly, which is what keeps the three front ends behaving identically.
package library

import (
	"strconv"

	"github.com/remy/tag-manager/internal/catalog"
	"github.com/remy/tag-manager/internal/tags"
)

// Track is the service's view of one track: the catalogue's fields plus the
// identity and version a client needs to address and safely edit it.
type Track struct {
	ID   string `json:"id"`
	Path string `json:"path"`

	Title       string `json:"title,omitempty"`
	Artist      string `json:"artist,omitempty"`
	AlbumArtist string `json:"albumArtist,omitempty"`
	Album       string `json:"album,omitempty"`
	Genre       string `json:"genre,omitempty"`
	Composer    string `json:"composer,omitempty"`
	Comment     string `json:"comment,omitempty"`

	Year       int32 `json:"year,omitempty"`
	TrackNo    int32 `json:"track,omitempty"`
	TrackTotal int32 `json:"trackTotal,omitempty"`
	Disc       int32 `json:"disc,omitempty"`
	DiscTotal  int32 `json:"discTotal,omitempty"`

	DurationMS int32 `json:"durationMs,omitempty"`
	Bitrate    int32 `json:"bitrate,omitempty"`
	SampleRate int32 `json:"sampleRate,omitempty"`
	Channels   uint8 `json:"channels,omitempty"`

	Format   string `json:"format"`
	Size     int64  `json:"size"`
	ModTime  int64  `json:"modTime"`
	HasArt   bool   `json:"hasArt"`
	Writable bool   `json:"writable"`

	// Version identifies the file's state on disk. A client may send it back
	// as If-Match; a mismatch means the file changed underneath, which is what
	// makes editing the same library from two devices safe.
	Version string `json:"version"`
}

// toTrack converts a catalogue entry into the service's view of it.
func toTrack(t *catalog.Track) Track {
	return Track{
		ID:          TrackID(t.Path),
		Path:        t.Path,
		Title:       t.Title,
		Artist:      t.Artist,
		AlbumArtist: t.AlbumArtist,
		Album:       t.Album,
		Genre:       t.Genre,
		Composer:    t.Composer,
		Comment:     t.Comment,
		Year:        t.Year,
		TrackNo:     t.TrackNo,
		TrackTotal:  t.TrackTotal,
		Disc:        t.Disc,
		DiscTotal:   t.DiscTotal,
		DurationMS:  t.DurationMS,
		Bitrate:     t.Bitrate,
		SampleRate:  t.SampleRate,
		Channels:    t.Channels,
		Format:      t.Format.String(),
		Size:        t.Size,
		ModTime:     t.ModTime,
		HasArt:      t.HasArt,
		Writable:    t.Format.Writable(),
		Version:     TrackVersion(t),
	}
}

// TrackID is a stable identifier for a path.
//
// It is derived from the path rather than stored, so it survives a rescan —
// which reorders the catalogue and invalidates every slice index — and any
// client can compute it without asking. Moving a file changes its identity,
// which is the correct reading: it is a different location on disk.
func TrackID(path string) string {
	return strconv.FormatUint(hash64(path), 16)
}

// TrackVersion identifies a file's state on disk, for optimistic concurrency.
// It covers size and modification time, both of which a tag write changes.
func TrackVersion(t *catalog.Track) string {
	h := hash64(t.Path)
	h = hashMix(h, uint64(t.Size))
	h = hashMix(h, uint64(t.ModTime))
	return strconv.FormatUint(h, 16)
}

// FNV-1a, 64-bit. Not cryptographic: the only requirement is that two paths in
// one library do not collide, and at a hundred thousand entries the chance of
// that is around one in four billion.
const (
	fnvOffset64 = 14695981039346656037
	fnvPrime64  = 1099511628211
)

func hash64(s string) uint64 {
	h := uint64(fnvOffset64)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= fnvPrime64
	}
	return h
}

// hashBytes64 is hash64 over a byte slice, used to identify images.
func hashBytes64(b []byte) uint64 {
	h := uint64(fnvOffset64)
	for _, c := range b {
		h ^= uint64(c)
		h *= fnvPrime64
	}
	return h
}

// hashMix folds an integer into a running hash.
func hashMix(h, v uint64) uint64 {
	for i := 0; i < 8; i++ {
		h ^= v & 0xFF
		h *= fnvPrime64
		v >>= 8
	}
	return h
}

// formatOf resolves a format name back to the tags enumeration, for callers
// that only have the wire representation.
func formatOf(name string) tags.Format {
	for f := tags.Format(0); f < 16; f++ {
		if f.String() == name {
			return f
		}
	}
	return tags.FormatUnknown
}

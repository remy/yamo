// Package tags implements fast, allocation-conscious readers and writers for
// the metadata containers used by common audio formats.
//
// Everything here is built around one rule: never read more of a file than the
// tag actually occupies. A 40MB FLAC and a 3MB MP3 cost the same to catalogue.
package tags

import (
	"errors"
	"path/filepath"
	"strings"
)

// Format identifies the container a file uses.
type Format uint8

const (
	FormatUnknown Format = iota
	FormatMP3
	FormatFLAC
	FormatMP4 // m4a, m4b, mp4, aac in an MP4 container
	FormatOggVorbis
	FormatOpus
	FormatWMA
	FormatWAV
	FormatAIFF
)

var formatNames = [...]string{
	FormatUnknown:   "unknown",
	FormatMP3:       "mp3",
	FormatFLAC:      "flac",
	FormatMP4:       "mp4",
	FormatOggVorbis: "ogg",
	FormatOpus:      "opus",
	FormatWMA:       "wma",
	FormatWAV:       "wav",
	FormatAIFF:      "aiff",
}

func (f Format) String() string {
	if int(f) < len(formatNames) {
		return formatNames[f]
	}
	return "unknown"
}

// Writable reports whether the tag writer supports this container.
func (f Format) Writable() bool {
	switch f {
	case FormatMP3, FormatFLAC, FormatOggVorbis, FormatOpus, FormatMP4:
		return true
	}
	return false
}

// ErrUnsupported is returned when a file's container has no reader or writer.
var ErrUnsupported = errors.New("tags: unsupported format")

// ErrNoTags means the file parsed cleanly but carried no metadata container.
var ErrNoTags = errors.New("tags: no metadata found")

// ErrMalformed means the file is not shaped like the container it claims to
// be. It is separate from ErrUnsupported so callers can tell "this library
// cannot write that format" from "this particular file is broken", and from a
// genuine IO fault: no retry will make a malformed file writable.
var ErrMalformed = errors.New("tags: malformed file")

// Metadata is the normalised, format-independent view of one file's tags.
// Zero values mean "absent" throughout; there is no separate presence flag.
type Metadata struct {
	Title       string
	Artist      string
	AlbumArtist string
	Album       string
	Genre       string
	Composer    string
	Comment     string

	// The sort fields hold the form a library orders by rather than the form
	// it displays: "Beatles, The" beside "The Beatles", "Presley, Elvis"
	// beside "Elvis Presley". Every container gives them tags of their own
	// because the two are genuinely different strings, and a player that has
	// only the display form has to guess — usually by stripping a leading
	// "The", which is wrong for The The and for every name that is not
	// English.
	//
	// AlbumArtistSort earns its place twice over: on a compilation it is
	// routinely the only field that says the album belongs to Various
	// Artists, because iTunes writes the sort tag and no album artist at all.
	TitleSort       string
	ArtistSort      string
	AlbumSort       string
	AlbumArtistSort string
	ComposerSort    string

	Year       int32
	Track      int32
	TrackTotal int32
	Disc       int32
	DiscTotal  int32

	// Audio properties, derived from the stream header rather than the tag.
	DurationMS int32
	Bitrate    int32 // kbps
	SampleRate int32 // Hz
	Channels   uint8

	HasArt bool
	Format Format

	// Compilation is the flag that keeps a Various Artists album together
	// rather than splitting it into one album per track. It is a boolean in
	// every container, stored as "1" in ID3's TCMP and as a single byte in
	// MP4's cpil.
	Compilation bool
}

// FormatForExt maps a file extension (with or without the leading dot) to a
// container. Extension dispatch is the first gate in the scanner: it lets the
// directory walk reject non-audio files without opening them.
func FormatForExt(ext string) Format {
	if len(ext) > 0 && ext[0] == '.' {
		ext = ext[1:]
	}
	switch lowerASCII(ext) {
	case "mp3", "mp2":
		return FormatMP3
	case "flac":
		return FormatFLAC
	case "m4a", "m4b", "m4p", "mp4", "aac", "alac":
		return FormatMP4
	case "ogg", "oga":
		return FormatOggVorbis
	case "opus":
		return FormatOpus
	case "wma":
		return FormatWMA
	case "wav", "wave":
		return FormatWAV
	case "aif", "aiff", "aifc":
		return FormatAIFF
	}
	return FormatUnknown
}

// FormatForPath is FormatForExt applied to a path's extension.
func FormatForPath(p string) Format { return FormatForExt(filepath.Ext(p)) }

// IsAudioPath reports whether the path looks like a supported audio file.
func IsAudioPath(p string) bool { return FormatForPath(p) != FormatUnknown }

// lowerASCII lowercases without allocating for the common already-lower case.
func lowerASCII(s string) string {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= 'A' && c <= 'Z' {
			return strings.ToLower(s)
		}
	}
	return s
}

package tags

import (
	"encoding/binary"
	"strings"
)

// vorbisComment is the FIELD=value list used by FLAC, Ogg Vorbis and Opus.
// Order is preserved so a rewrite keeps fields this package does not model.
type vorbisComment struct {
	vendor string
	fields []vorbisField
}

type vorbisField struct {
	key   string // upper-cased
	value string
}

// get returns the first value for key, which callers pass upper-cased.
func (vc *vorbisComment) get(key string) string {
	for i := range vc.fields {
		if vc.fields[i].key == key {
			return vc.fields[i].value
		}
	}
	return ""
}

// getAny returns the first value found for any of the given aliases, in the
// order listed. Taggers disagree about names far more than about semantics.
func (vc *vorbisComment) getAny(keys ...string) string {
	for _, k := range keys {
		if v := vc.get(k); v != "" {
			return v
		}
	}
	return ""
}

// set replaces every occurrence of key with a single field, or removes the key
// entirely when value is empty.
func (vc *vorbisComment) set(key, value string) {
	out := vc.fields[:0]
	written := false
	for _, f := range vc.fields {
		if f.key != key {
			out = append(out, f)
			continue
		}
		if !written && value != "" {
			out = append(out, vorbisField{key: key, value: value})
			written = true
		}
	}
	if !written && value != "" {
		out = append(out, vorbisField{key: key, value: value})
	}
	vc.fields = out
}

// parseVorbisComment decodes a comment block body (no framing bit).
func parseVorbisComment(b []byte) (*vorbisComment, bool) {
	if len(b) < 4 {
		return nil, false
	}
	vc := &vorbisComment{}
	n := int(binary.LittleEndian.Uint32(b[0:4]))
	p := 4
	if n < 0 || p+n > len(b) {
		return nil, false
	}
	vc.vendor = string(b[p : p+n])
	p += n

	if p+4 > len(b) {
		return nil, false
	}
	count := int(binary.LittleEndian.Uint32(b[p : p+4]))
	p += 4
	// A corrupt count could ask for gigabytes of slice; cap it against the
	// bytes actually available, since each field costs at least 4 bytes.
	if count < 0 || count > (len(b)-p)/4 {
		count = (len(b) - p) / 4
	}
	vc.fields = make([]vorbisField, 0, count)

	for i := 0; i < count; i++ {
		if p+4 > len(b) {
			break
		}
		l := int(binary.LittleEndian.Uint32(b[p : p+4]))
		p += 4
		if l < 0 || p+l > len(b) {
			break
		}
		kv := b[p : p+l]
		p += l
		eq := indexByte(kv, '=')
		if eq <= 0 {
			continue
		}
		vc.fields = append(vc.fields, vorbisField{
			key:   strings.ToUpper(string(kv[:eq])),
			value: strings.TrimSpace(trimNulUTF8(string(kv[eq+1:]))),
		})
	}
	return vc, true
}

// encodeVorbisComment serialises back to the on-disk representation.
func encodeVorbisComment(vc *vorbisComment) []byte {
	size := 4 + len(vc.vendor) + 4
	for _, f := range vc.fields {
		size += 4 + len(f.key) + 1 + len(f.value)
	}
	out := make([]byte, 0, size)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(vc.vendor)))
	out = append(out, vc.vendor...)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(vc.fields)))
	for _, f := range vc.fields {
		out = binary.LittleEndian.AppendUint32(out, uint32(len(f.key)+1+len(f.value)))
		out = append(out, f.key...)
		out = append(out, '=')
		out = append(out, f.value...)
	}
	return out
}

// applyTo folds Vorbis comments into md, accepting the common key aliases.
func (vc *vorbisComment) applyTo(md *Metadata) {
	setIfEmpty(&md.Title, vc.get("TITLE"))
	setIfEmpty(&md.Artist, vc.getAny("ARTIST", "PERFORMER"))
	setIfEmpty(&md.AlbumArtist, vc.getAny("ALBUMARTIST", "ALBUM ARTIST", "ENSEMBLE"))
	setIfEmpty(&md.Album, vc.get("ALBUM"))
	setIfEmpty(&md.Genre, vc.get("GENRE"))
	setIfEmpty(&md.Composer, vc.get("COMPOSER"))
	setIfEmpty(&md.Comment, vc.getAny("COMMENT", "DESCRIPTION"))

	if md.Year == 0 {
		md.Year = parseYear(vc.getAny("DATE", "YEAR", "ORIGINALDATE"))
	}
	if md.Track == 0 {
		md.Track, md.TrackTotal = parsePair(vc.get("TRACKNUMBER"))
		if t := vc.getAny("TRACKTOTAL", "TOTALTRACKS"); t != "" {
			md.TrackTotal = parseIntPrefix(t)
		}
	}
	if md.Disc == 0 {
		md.Disc, md.DiscTotal = parsePair(vc.getAny("DISCNUMBER", "DISC"))
		if t := vc.getAny("DISCTOTAL", "TOTALDISCS"); t != "" {
			md.DiscTotal = parseIntPrefix(t)
		}
	}
	if vc.get("METADATA_BLOCK_PICTURE") != "" {
		md.HasArt = true
	}
}

func indexByte(b []byte, c byte) int {
	for i := range b {
		if b[i] == c {
			return i
		}
	}
	return -1
}

// setVorbisPictures replaces the base64-encoded picture fields Ogg uses to
// carry cover art, which has no block of its own in that container.
func setVorbisPictures(vc *vorbisComment, pics []Picture) {
	const key = "METADATA_BLOCK_PICTURE"
	out := vc.fields[:0]
	for _, f := range vc.fields {
		if f.key != key {
			out = append(out, f)
		}
	}
	vc.fields = out
	for i := range pics {
		vc.fields = append(vc.fields, vorbisField{key: key, value: encodeVorbisPicture(&pics[i])})
	}
}

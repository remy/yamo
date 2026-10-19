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

// getAll returns every value for key, which callers pass upper-cased. A
// Vorbis comment repeats a field rather than packing several values into one.
func (vc *vorbisComment) getAll(key string) []string {
	var out []string
	for i := range vc.fields {
		if vc.fields[i].key == key {
			out = append(out, vc.fields[i].value)
		}
	}
	return out
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

// joined returns every value for key as one string, joined the way frameText
// joins a multi-value ID3 frame.
//
// A Vorbis comment holds a list by repeating the field: three GENRE entries
// are a song with three genres. Reading only the first, as get does, showed
// one of them and left no sign the others existed — and the writer replaces
// every entry under a key, so correcting that one genre silently deleted the
// other two. Joined, the catalogue holds what the file holds, an edit starts
// from all of it, and setList writes it back as the same separate fields.
func (vc *vorbisComment) joined(key string) string {
	var out []string
	for _, v := range vc.getAll(key) {
		if v = strings.TrimSpace(v); v != "" && !containsStr(out, v) {
			out = append(out, v)
		}
	}
	return strings.Join(out, multiValueSep)
}

// joinedAny is joined for the first alias that holds anything, the multi-value
// counterpart of getAny. Aliases are not merged: ALBUMARTIST and ALBUM ARTIST
// both present are one value spelled twice, not two values.
func (vc *vorbisComment) joinedAny(keys ...string) string {
	for _, k := range keys {
		if v := vc.joined(k); v != "" {
			return v
		}
	}
	return ""
}

// setList writes a joined value back as one field per entry, in place of
// every field under key. It is the inverse of joined, and only for the fields
// whose values are lists by nature — see applyEditToVorbis for which.
func (vc *vorbisComment) setList(key, value string) {
	vals := splitMultiValue(value)
	if len(vals) <= 1 {
		vc.set(key, strings.TrimSpace(value))
		return
	}
	// Written where the first of the old entries was, so a file's field order
	// survives an edit the same way set preserves it for a single value.
	out := vc.fields[:0]
	written := false
	for _, f := range vc.fields {
		if f.key != key {
			out = append(out, f)
			continue
		}
		if !written {
			for _, v := range vals {
				out = append(out, vorbisField{key: key, value: v})
			}
			written = true
		}
	}
	if !written {
		for _, v := range vals {
			out = append(out, vorbisField{key: key, value: v})
		}
	}
	vc.fields = out
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
	// Every text field is read joined, not only the list-like ones: a file
	// holding two TITLE entries is unusual, but showing one and hiding the
	// other is never the honest reading of it.
	setIfEmpty(&md.Title, vc.joined("TITLE"))
	setIfEmpty(&md.Artist, vc.joinedAny("ARTIST", "PERFORMER"))
	setIfEmpty(&md.AlbumArtist, vc.joinedAny("ALBUMARTIST", "ALBUM ARTIST", "ENSEMBLE"))
	setIfEmpty(&md.Album, vc.joined("ALBUM"))
	setIfEmpty(&md.Genre, vc.joined("GENRE"))
	setIfEmpty(&md.Composer, vc.joined("COMPOSER"))
	setIfEmpty(&md.Comment, vc.joinedAny("COMMENT", "DESCRIPTION"))

	setIfEmpty(&md.TitleSort, vc.joined("TITLESORT"))
	setIfEmpty(&md.ArtistSort, vc.joined("ARTISTSORT"))
	setIfEmpty(&md.AlbumSort, vc.joined("ALBUMSORT"))
	setIfEmpty(&md.AlbumArtistSort, vc.joinedAny("ALBUMARTISTSORT", "ALBUM ARTIST SORT"))
	setIfEmpty(&md.ComposerSort, vc.joined("COMPOSERSORT"))

	if md.Year == 0 {
		md.Year = parseYear(vc.getAny("DATE", "YEAR", "RELEASEDATE", "ORIGINALDATE"))
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
	md.Compilation = isTrueFlag(vc.get("COMPILATION"))
	applyMusicBrainz(md, "MUSICBRAINZ_ARTISTID", vc.getAll("MUSICBRAINZ_ARTISTID")...)
	applyMusicBrainz(md, "MUSICBRAINZ_ALBUMARTISTID", vc.getAll("MUSICBRAINZ_ALBUMARTISTID")...)
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

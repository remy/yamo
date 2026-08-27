package tags

// Edit describes a set of metadata changes. A nil pointer leaves the field
// untouched; a non-nil pointer sets it, and an empty string removes it.
//
// Writes are expressed as edits rather than as a whole Metadata value on
// purpose. Rewriting every field would silently normalise tags the user never
// asked to change — a genre stored as "(17)" would become "Rock", a stray
// comment would be reformatted — and on a library this size those unrequested
// edits are impossible to review.
type Edit struct {
	Title       *string
	Artist      *string
	AlbumArtist *string
	Album       *string
	Genre       *string
	Composer    *string
	Comment     *string

	Year       *int32
	Track      *int32
	TrackTotal *int32
	Disc       *int32
	DiscTotal  *int32
}

// Empty reports whether the edit would change nothing.
func (e *Edit) Empty() bool {
	return e.Title == nil && e.Artist == nil && e.AlbumArtist == nil &&
		e.Album == nil && e.Genre == nil && e.Composer == nil &&
		e.Comment == nil && e.Year == nil && e.Track == nil &&
		e.TrackTotal == nil && e.Disc == nil && e.DiscTotal == nil
}

// SetString records a string field change by name. Names match FieldNames in
// the catalog package; unknown names are ignored.
func (e *Edit) SetString(field, value string) {
	v := value
	switch field {
	case "title":
		e.Title = &v
	case "artist":
		e.Artist = &v
	case "albumartist":
		e.AlbumArtist = &v
	case "album":
		e.Album = &v
	case "genre":
		e.Genre = &v
	case "composer":
		e.Composer = &v
	case "comment":
		e.Comment = &v
	}
}

// SetInt records a numeric field change by name.
func (e *Edit) SetInt(field string, value int32) {
	v := value
	switch field {
	case "year":
		e.Year = &v
	case "track":
		e.Track = &v
	case "tracktotal":
		e.TrackTotal = &v
	case "disc":
		e.Disc = &v
	case "disctotal":
		e.DiscTotal = &v
	}
}

// pairString renders a number/total pair as ID3 and Vorbis expect it.
func pairString(num, total int32) string {
	if num <= 0 && total <= 0 {
		return ""
	}
	if total > 0 {
		return itoa(int64(num)) + "/" + itoa(int64(total))
	}
	return itoa(int64(num))
}

// resolvePair merges an edit's number and total against the current values,
// since both live in one tag field and either may be unchanged.
func resolvePair(num, total *int32, curNum, curTotal int32) (string, bool) {
	if num == nil && total == nil {
		return "", false
	}
	n, t := curNum, curTotal
	if num != nil {
		n = *num
	}
	if total != nil {
		t = *total
	}
	return pairString(n, t), true
}

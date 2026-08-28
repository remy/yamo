package tags

import (
	"sort"
	"strings"
)

// Tag is a piece of metadata identified by what it means rather than by how a
// particular container spells it.
//
// The keep list has to work across MP3, FLAC, MP4 and Ogg, and those formats
// have no vocabulary in common: an album artist is TPE2, ALBUMARTIST or aART
// depending on the file. Expressing the list in native keys would mean writing
// it four times and getting it wrong once. So the list is expressed in these
// canonical tags, and each format maps its own keys onto them.
type Tag uint8

const (
	TagUnknown Tag = iota
	TagTitle
	TagArtist
	TagAlbum
	TagAlbumArtist
	TagTrack
	TagDisc
	TagGenre
	TagDate
	TagCompilation
	TagComposer
	TagTitleSort
	TagArtistSort
	TagAlbumSort
	TagAlbumArtistSort
	TagComposerSort
	TagArtwork
	TagComment
	TagGapless
	TagSoundCheck
	TagLyrics
	TagPublisher
	TagISRC
	TagBPM
	TagKey
	TagMood
	TagLanguage
	TagCopyright
	TagEncoder
	TagGrouping
	TagMovement
	TagConductor
	TagLyricist
	TagRemixer
	TagSubtitle
	TagOriginalDate
	TagMediaType
	TagReplayGain
	TagMusicBrainz
	TagAcoustID
	TagRating
	TagURL
	TagPrivate
	TagChapters
	numTags
)

// tagSpec records a canonical tag's name and the native keys that mean it.
type tagSpec struct {
	Name   string
	Desc   string
	ID3    []string
	Vorbis []string
	MP4    []string
}

var tagSpecs = [numTags]tagSpec{
	TagTitle:           {Name: "title", Desc: "song title", ID3: []string{"TIT2"}, Vorbis: []string{"TITLE"}, MP4: []string{"\xa9nam"}},
	TagArtist:          {Name: "artist", Desc: "track artist", ID3: []string{"TPE1"}, Vorbis: []string{"ARTIST", "PERFORMER"}, MP4: []string{"\xa9ART"}},
	TagAlbum:           {Name: "album", Desc: "album name", ID3: []string{"TALB"}, Vorbis: []string{"ALBUM"}, MP4: []string{"\xa9alb"}},
	TagAlbumArtist:     {Name: "albumartist", Desc: "album artist; keeps compilations together", ID3: []string{"TPE2"}, Vorbis: []string{"ALBUMARTIST", "ALBUM ARTIST", "ENSEMBLE"}, MP4: []string{"aART"}},
	TagTrack:           {Name: "track", Desc: "track number", ID3: []string{"TRCK"}, Vorbis: []string{"TRACKNUMBER", "TRACKTOTAL", "TOTALTRACKS"}, MP4: []string{"trkn"}},
	TagDisc:            {Name: "disc", Desc: "disc number", ID3: []string{"TPOS"}, Vorbis: []string{"DISCNUMBER", "DISCTOTAL", "TOTALDISCS"}, MP4: []string{"disk"}},
	TagGenre:           {Name: "genre", Desc: "genre", ID3: []string{"TCON"}, Vorbis: []string{"GENRE"}, MP4: []string{"\xa9gen", "gnre"}},
	TagDate:            {Name: "date", Desc: "release year or date", ID3: []string{"TDRC", "TYER", "TDRL", "TDAT", "TRDA"}, Vorbis: []string{"DATE", "YEAR"}, MP4: []string{"\xa9day"}},
	TagCompilation:     {Name: "compilation", Desc: "compilation flag; stops Various Artists albums splitting", ID3: []string{"TCMP"}, Vorbis: []string{"COMPILATION"}, MP4: []string{"cpil"}},
	TagComposer:        {Name: "composer", Desc: "composer", ID3: []string{"TCOM"}, Vorbis: []string{"COMPOSER"}, MP4: []string{"\xa9wrt"}},
	TagTitleSort:       {Name: "titlesort", Desc: "title sort order", ID3: []string{"TSOT"}, Vorbis: []string{"TITLESORT"}, MP4: []string{"sonm"}},
	TagArtistSort:      {Name: "artistsort", Desc: "artist sort order", ID3: []string{"TSOP"}, Vorbis: []string{"ARTISTSORT"}, MP4: []string{"soar"}},
	TagAlbumSort:       {Name: "albumsort", Desc: "album sort order", ID3: []string{"TSOA"}, Vorbis: []string{"ALBUMSORT"}, MP4: []string{"soal"}},
	TagAlbumArtistSort: {Name: "albumartistsort", Desc: "album artist sort order", ID3: []string{"TSO2"}, Vorbis: []string{"ALBUMARTISTSORT"}, MP4: []string{"soaa"}},
	TagComposerSort:    {Name: "composersort", Desc: "composer sort order", ID3: []string{"TSOC"}, Vorbis: []string{"COMPOSERSORT"}, MP4: []string{"soco"}},
	TagArtwork:         {Name: "artwork", Desc: "embedded cover art", ID3: []string{"APIC"}, Vorbis: []string{"METADATA_BLOCK_PICTURE", "COVERART", "COVERARTMIME"}, MP4: []string{"covr"}},
	TagComment:         {Name: "comment", Desc: "free-text comment", ID3: []string{"COMM"}, Vorbis: []string{"COMMENT", "DESCRIPTION"}, MP4: []string{"\xa9cmt"}},
	TagGapless:         {Name: "gapless", Desc: "gapless playback data (iTunSMPB); losing it breaks gapless", ID3: []string{}, Vorbis: []string{}, MP4: []string{"pgap"}},
	TagSoundCheck:      {Name: "soundcheck", Desc: "iTunes volume normalisation (iTunNORM)", ID3: []string{}, Vorbis: []string{}, MP4: []string{}},
	TagLyrics:          {Name: "lyrics", Desc: "lyrics", ID3: []string{"USLT", "SYLT"}, Vorbis: []string{"LYRICS", "UNSYNCEDLYRICS"}, MP4: []string{"\xa9lyr"}},
	TagPublisher:       {Name: "publisher", Desc: "label or publisher", ID3: []string{"TPUB"}, Vorbis: []string{"PUBLISHER", "LABEL", "ORGANIZATION"}, MP4: []string{"\xa9pub"}},
	TagISRC:            {Name: "isrc", Desc: "ISRC recording identifier", ID3: []string{"TSRC"}, Vorbis: []string{"ISRC"}, MP4: []string{}},
	TagBPM:             {Name: "bpm", Desc: "beats per minute", ID3: []string{"TBPM"}, Vorbis: []string{"BPM"}, MP4: []string{"tmpo"}},
	TagKey:             {Name: "key", Desc: "musical key", ID3: []string{"TKEY"}, Vorbis: []string{"INITIALKEY", "KEY"}, MP4: []string{}},
	TagMood:            {Name: "mood", Desc: "mood", ID3: []string{"TMOO"}, Vorbis: []string{"MOOD"}, MP4: []string{}},
	TagLanguage:        {Name: "language", Desc: "language", ID3: []string{"TLAN"}, Vorbis: []string{"LANGUAGE"}, MP4: []string{}},
	TagCopyright:       {Name: "copyright", Desc: "copyright notice", ID3: []string{"TCOP"}, Vorbis: []string{"COPYRIGHT", "LICENSE"}, MP4: []string{"cprt"}},
	TagEncoder:         {Name: "encoder", Desc: "encoder name and settings", ID3: []string{"TENC", "TSSE"}, Vorbis: []string{"ENCODER", "ENCODEDBY", "ENCODED-BY", "ENCODER_OPTIONS"}, MP4: []string{"\xa9too", "\xa9enc"}},
	TagGrouping:        {Name: "grouping", Desc: "work or grouping", ID3: []string{"GRP1", "TIT1"}, Vorbis: []string{"GROUPING", "CONTENTGROUP"}, MP4: []string{"\xa9grp"}},
	TagMovement:        {Name: "movement", Desc: "movement name and number", ID3: []string{}, Vorbis: []string{"MOVEMENTNAME", "MOVEMENT", "MOVEMENTTOTAL"}, MP4: []string{"\xa9mvn", "\xa9mvi", "\xa9mvc", "shwm"}},
	TagConductor:       {Name: "conductor", Desc: "conductor", ID3: []string{"TPE3"}, Vorbis: []string{"CONDUCTOR"}, MP4: []string{"\xa9con"}},
	TagLyricist:        {Name: "lyricist", Desc: "lyricist", ID3: []string{"TEXT"}, Vorbis: []string{"LYRICIST"}, MP4: []string{}},
	TagRemixer:         {Name: "remixer", Desc: "remixer", ID3: []string{"TPE4"}, Vorbis: []string{"REMIXER", "MIXARTIST"}, MP4: []string{}},
	TagSubtitle:        {Name: "subtitle", Desc: "subtitle or version", ID3: []string{"TIT3"}, Vorbis: []string{"SUBTITLE", "VERSION"}, MP4: []string{"\xa9st3"}},
	TagOriginalDate:    {Name: "originaldate", Desc: "original release date", ID3: []string{"TORY", "TDOR", "TOAL", "TOPE"}, Vorbis: []string{"ORIGINALDATE", "ORIGINALYEAR"}, MP4: []string{}},
	TagMediaType:       {Name: "mediatype", Desc: "source media", ID3: []string{"TMED"}, Vorbis: []string{"MEDIA", "MEDIATYPE"}, MP4: []string{}},
	TagReplayGain:      {Name: "replaygain", Desc: "loudness normalisation", ID3: []string{"RVA2", "RVAD"}, Vorbis: []string{}, MP4: []string{}},
	TagMusicBrainz:     {Name: "musicbrainz", Desc: "MusicBrainz identifiers", ID3: []string{"UFID"}, Vorbis: []string{}, MP4: []string{}},
	TagAcoustID:        {Name: "acoustid", Desc: "AcoustID fingerprint", ID3: []string{}, Vorbis: []string{}, MP4: []string{}},
	TagRating:          {Name: "rating", Desc: "rating and play count", ID3: []string{"POPM", "PCNT"}, Vorbis: []string{"RATING", "FMPS_RATING"}, MP4: []string{"rtng"}},
	TagURL:             {Name: "url", Desc: "web links", ID3: []string{"WXXX", "WOAR", "WOAF", "WOAS", "WCOM", "WCOP", "WPUB", "WORS", "WPAY"}, Vorbis: []string{"WWW", "CONTACT"}, MP4: []string{"\xa9url", "purl", "egid"}},
	TagPrivate:         {Name: "private", Desc: "application private data", ID3: []string{"PRIV", "NCON", "GEOB", "MCDI"}, Vorbis: []string{}, MP4: []string{}},
	TagChapters:        {Name: "chapters", Desc: "chapter markers", ID3: []string{"CHAP", "CTOC"}, Vorbis: []string{}, MP4: []string{"chpl"}},
}

// Name returns the canonical name used on the command line.
func (t Tag) Name() string {
	if int(t) < len(tagSpecs) && tagSpecs[t].Name != "" {
		return tagSpecs[t].Name
	}
	return "unknown"
}

// Desc returns a short description of what the tag holds.
func (t Tag) Desc() string {
	if int(t) < len(tagSpecs) {
		return tagSpecs[t].Desc
	}
	return ""
}

// NativeKeys lists the keys this tag maps to in the given format, for reports
// that need to show what will actually be touched in a file.
func (t Tag) NativeKeys(f Format) []string {
	if int(t) >= len(tagSpecs) {
		return nil
	}
	switch f {
	case FormatMP3, FormatWAV, FormatAIFF:
		return tagSpecs[t].ID3
	case FormatFLAC, FormatOggVorbis, FormatOpus:
		return tagSpecs[t].Vorbis
	case FormatMP4:
		out := make([]string, 0, len(tagSpecs[t].MP4))
		for _, k := range tagSpecs[t].MP4 {
			out = append(out, MP4AtomName(k))
		}
		return out
	}
	return nil
}

// LookupTag resolves a tag by name.
//
// Any of the vocabularies is accepted: the canonical name, an ID3 frame
// identifier, a Vorbis field name, or an MP4 atom. Someone who knows a keep
// list should contain aART should not have to look up that it is called
// albumartist here.
func LookupTag(name string) (Tag, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return TagUnknown, false
	}
	if t, ok := tagByName[strings.ToLower(name)]; ok {
		return t, true
	}
	if t, ok := tagByID3[strings.ToUpper(name)]; ok {
		return t, true
	}
	if t, ok := tagByVorbis[strings.ToUpper(name)]; ok {
		return t, true
	}
	// MP4 atom names are case-sensitive, and Apple's begin with a raw 0xA9
	// byte that a person would type as the copyright sign.
	if t, ok := tagByMP4[name]; ok {
		return t, true
	}
	if rest, cut := strings.CutPrefix(name, "©"); cut {
		if t, ok := tagByMP4["\xa9"+rest]; ok {
			return t, true
		}
	}
	return TagUnknown, false
}

// AllTags lists every canonical tag in name order.
func AllTags() []Tag {
	out := make([]Tag, 0, numTags-1)
	for t := Tag(1); t < numTags; t++ {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

var (
	tagByName   = map[string]Tag{}
	tagByID3    = map[string]Tag{}
	tagByVorbis = map[string]Tag{}
	tagByMP4    = map[string]Tag{}
)

func init() {
	for t := Tag(1); t < numTags; t++ {
		s := &tagSpecs[t]
		tagByName[s.Name] = t
		for _, k := range s.ID3 {
			tagByID3[k] = t
		}
		for _, k := range s.Vorbis {
			tagByVorbis[k] = t
		}
		for _, k := range s.MP4 {
			tagByMP4[k] = t
		}
	}
}

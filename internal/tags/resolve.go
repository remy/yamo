package tags

import "strings"

// Resolving a native key to a canonical tag is not always a table lookup. The
// formats all have an extensible slot — ID3's TXXX and COMM, MP4's "----"
// freeform atoms — whose meaning lives in a description string rather than in
// the key. That is where iTunes hides gapless data among comments, and where
// MusicBrainz and ReplayGain live at all, so a strip that ignored descriptions
// would treat all of them as one indivisible blob.

// tagForDescription maps the description of an extensible field to a canonical
// tag. Descriptions are compared case-insensitively because the applications
// that write them do not agree on case.
func tagForDescription(desc string) (Tag, bool) {
	d := strings.ToUpper(strings.TrimSpace(desc))
	switch d {
	case "ITUNSMPB", "ITUNPGAP":
		return TagGapless, true
	case "ITUNNORM":
		return TagSoundCheck, true
	case "ITUNMOVI", "ITUNEXTC":
		return TagPrivate, true
	}
	switch {
	case strings.HasPrefix(d, "REPLAYGAIN"):
		return TagReplayGain, true
	case strings.HasPrefix(d, "MUSICBRAINZ"), strings.HasPrefix(d, "MUSICIP"):
		return TagMusicBrainz, true
	case strings.HasPrefix(d, "ACOUSTID"):
		return TagAcoustID, true
	}
	// Descriptions borrow whatever vocabulary the writing application had to
	// hand. ffmpeg, for one, writes an MP3 compilation flag as TXXX:TCMP and
	// a comment as TXXX:comment, so both the Vorbis field names and the ID3
	// frame identifiers have to be recognised here.
	if t, ok := tagByVorbis[d]; ok {
		return t, true
	}
	if t, ok := tagByID3[d]; ok {
		return t, true
	}
	if t, ok := tagByName[strings.ToLower(desc)]; ok {
		return t, true
	}
	return TagUnknown, false
}

// tagForID3Frame resolves an ID3v2 frame to a canonical tag, reading the
// description of the frames whose meaning depends on one.
func tagForID3Frame(id string, payload []byte) Tag {
	switch id {
	case "TXXX", "TXX":
		desc, _ := userText(payload)
		if t, ok := tagForDescription(desc); ok {
			return t
		}
		return TagUnknown
	case "COMM", "COM":
		if t, ok := tagForDescription(id3CommentDescription(payload)); ok {
			return t
		}
		return TagComment
	}
	if t, ok := tagByID3[id]; ok {
		return t
	}
	return TagUnknown
}

// id3CommentDescription returns a COMM frame's short description, which is
// what separates a listener's comment from an application's private data.
func id3CommentDescription(p []byte) string {
	if len(p) < 5 {
		return ""
	}
	d, _, ok := splitTerminated(p[4:], p[0])
	if !ok {
		return ""
	}
	return decodeText(p[0], d)
}

// tagForVorbisField resolves a Vorbis comment field name.
func tagForVorbisField(key string) Tag {
	k := strings.ToUpper(key)
	if t, ok := tagByVorbis[k]; ok {
		return t
	}
	if t, ok := tagForDescription(k); ok {
		return t
	}
	return TagUnknown
}

// tagForMP4Atom resolves an MP4 metadata item, unwrapping the freeform "----"
// container to read the name it carries.
func tagForMP4Atom(name string, body []byte) Tag {
	if name == "----" {
		if t, ok := tagForDescription(mp4FreeformName(body)); ok {
			return t
		}
		return TagUnknown
	}
	if t, ok := tagByMP4[name]; ok {
		return t
	}
	return TagUnknown
}

// mp4FreeformName reads the "name" atom inside a freeform metadata item. The
// item also carries a "mean" atom holding the reverse-DNS namespace, which is
// not needed to decide what the value is.
func mp4FreeformName(item []byte) string {
	var name string
	walkAtoms(item, func(typ string, body []byte) bool {
		if typ == "name" && len(body) >= 4 {
			name = string(body[4:]) // skip version and flags
			return false
		}
		return true
	})
	return strings.TrimRight(name, "\x00")
}

// describeMP4Item renders an item's key for a report, spelling out the
// namespace of a freeform atom so "----" is not the only thing shown.
func describeMP4Item(name string, body []byte) string {
	if name != "----" {
		return MP4AtomName(name)
	}
	if n := mp4FreeformName(body); n != "" {
		return "----:" + n
	}
	return "----"
}

// MP4AtomName makes an atom name printable and safe to serialise.
//
// Apple's atom names begin with a raw 0xA9 byte: a copyright sign in Latin-1,
// but not valid UTF-8. Printing such a name unchanged produces a replacement
// character, and — the reason this matters beyond cosmetics — encoding one to
// JSON silently substitutes U+FFFD, so a name that goes through a backup file
// comes back corrupt. Names are therefore held in this printable form
// everywhere outside the file itself.
func MP4AtomName(name string) string {
	if len(name) > 0 && name[0] == 0xA9 {
		return "©" + name[1:]
	}
	return name
}

// mp4AtomKey is the inverse of MP4AtomName, turning a printable name back into
// the bytes an atom header actually carries.
func mp4AtomKey(name string) string {
	if rest, cut := strings.CutPrefix(name, "©"); cut {
		return "\xa9" + rest
	}
	return name
}

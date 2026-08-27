package tags

import "strings"

// FrameMeaning gives a short human description of an ID3v2 frame identifier,
// so a report can say what is about to be removed rather than just naming it.
func FrameMeaning(id string) string { return frameMeaning(id) }

func frameMeaning(id string) string {
	if m, ok := frameMeanings[id]; ok {
		return m
	}
	// ID3v2.2 uses three-character identifiers; describe them by their v2.3
	// equivalent rather than duplicating the table.
	if v23, ok := v22ToV23[id]; ok {
		if m, ok := frameMeanings[v23]; ok {
			return m + " (v2.2)"
		}
	}
	return "unknown"
}

var frameMeanings = map[string]string{
	"AENC": "audio encryption",
	"APIC": "attached picture (cover art)",
	"ASPI": "audio seek point index",
	"CHAP": "chapter",
	"COMM": "comment",
	"COMR": "commercial",
	"CTOC": "table of contents",
	"ENCR": "encryption",
	"EQU2": "equalisation",
	"ETCO": "event timing",
	"GEOB": "general encapsulated object",
	"GRID": "group identification",
	"GRP1": "grouping (iTunes)",
	"IPLS": "involved people (v2.3)",
	"LINK": "linked information",
	"MCDI": "music CD identifier",
	"MLLT": "MPEG location lookup",
	"MVIN": "movement number (iTunes)",
	"MVNM": "movement name (iTunes)",
	"NCON": "MusicMatch binary blob",
	"OWNE": "ownership",
	"PCNT": "play counter",
	"POPM": "popularimeter (rating / play count)",
	"POSS": "position synchronisation",
	"PRIV": "private data",
	"RGAD": "replay gain (non-standard)",
	"RVA2": "relative volume adjustment",
	"RVAD": "relative volume adjustment (v2.3)",
	"SEEK": "seek frame",
	"SYLT": "synchronised lyrics",
	"SYTC": "synchronised tempo",
	"TALB": "album",
	"TBPM": "beats per minute",
	"TCMP": "part of a compilation (iTunes)",
	"TCOM": "composer",
	"TCON": "genre",
	"TCOP": "copyright",
	"TDAT": "date DDMM (v2.3)",
	"TDEN": "encoding time",
	"TDLY": "playlist delay",
	"TDOR": "original release date",
	"TDRC": "recording date (v2.4)",
	"TDRL": "release date",
	"TDTG": "tagging time",
	"TENC": "encoded by",
	"TEXT": "lyricist",
	"TFLT": "file type",
	"TIME": "time (v2.3)",
	"TIPL": "involved people (v2.4)",
	"TIT1": "content group",
	"TIT2": "title",
	"TIT3": "subtitle",
	"TKEY": "initial key",
	"TLAN": "language",
	"TLEN": "length in ms",
	"TMCL": "musician credits",
	"TMED": "media type",
	"TMOO": "mood",
	"TOAL": "original album",
	"TOFN": "original filename",
	"TOLY": "original lyricist",
	"TOPE": "original artist",
	"TORY": "original release year",
	"TOWM": "owner",
	"TOWN": "owner",
	"TPE1": "artist",
	"TPE2": "album artist / band",
	"TPE3": "conductor",
	"TPE4": "remixer",
	"TPOS": "disc number",
	"TPRO": "produced notice",
	"TPUB": "publisher / label",
	"TRCK": "track number",
	"TRDA": "recording dates (v2.3)",
	"TSO2": "album artist sort (iTunes)",
	"TSOA": "album sort order",
	"TSOC": "composer sort (iTunes)",
	"TSOP": "artist sort order",
	"TSOT": "title sort order",
	"TSRC": "ISRC",
	"TSSE": "encoder settings",
	"TSST": "set subtitle",
	"TXXX": "user-defined text",
	"TYER": "year (v2.3)",
	"UFID": "unique file id (MusicBrainz)",
	"USER": "terms of use",
	"USLT": "unsynchronised lyrics",
	"WCOM": "commercial info URL",
	"WCOP": "copyright URL",
	"WOAF": "file web page",
	"WOAR": "artist web page",
	"WOAS": "source web page",
	"WORS": "radio station URL",
	"WPAY": "payment URL",
	"WPUB": "publisher URL",
	"WXXX": "user-defined URL",
}

// DescribeFrame renders a short sample of a frame's content, for reports that
// need to show what is actually in the data rather than only its identifier.
// It returns an empty string for frames whose payload is not worth quoting.
func DescribeFrame(id string, payload []byte) string {
	switch {
	case id == "TXXX" || id == "TXX":
		desc, val := userText(payload)
		if desc == "" {
			return clip(val)
		}
		return clip(desc + "=" + val)
	case id == "COMM" || id == "COM":
		// The description distinguishes a real comment from an application's
		// private data, which is exactly the distinction a strip report needs.
		if len(payload) >= 5 {
			d, _, ok := splitTerminated(payload[4:], payload[0])
			if ok {
				if desc := decodeText(payload[0], d); desc != "" {
					return clip(desc + ": " + commentText(payload))
				}
			}
		}
		return clip(commentText(payload))
	case id == "APIC" || id == "PIC":
		return byteCount(len(payload))
	case id == "PRIV":
		if i := indexByte(payload, 0); i > 0 {
			return clip(string(payload[:i]))
		}
		return byteCount(len(payload))
	case id == "UFID":
		if i := indexByte(payload, 0); i > 0 {
			return clip(string(payload[:i]))
		}
		return byteCount(len(payload))
	case strings.HasPrefix(id, "T"):
		return clip(frameText(payload))
	case strings.HasPrefix(id, "W"):
		return clip(strings.TrimRight(string(payload), "\x00"))
	}
	return byteCount(len(payload))
}

func byteCount(n int) string { return itoa(int64(n)) + " bytes" }

func clip(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	r := []rune(s)
	if len(r) > 32 {
		return string(r[:32]) + "…"
	}
	return s
}

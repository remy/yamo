package tags

import "bytes"

// v22ToV23 maps ID3v2.2's three-character frame identifiers to their v2.3
// equivalents. ID3v2.2 has no writer here, so a v2.2 tag is upgraded on the
// first edit; without this mapping the upgraded frames would carry identifiers
// the serialiser does not recognise and every existing tag would be dropped.
var v22ToV23 = map[string]string{
	"TT1": "TIT1", "TT2": "TIT2", "TT3": "TIT3",
	"TP1": "TPE1", "TP2": "TPE2", "TP3": "TPE3", "TP4": "TPE4",
	"TAL": "TALB", "TCO": "TCON", "TCM": "TCOM", "TXT": "TEXT",
	"TYE": "TYER", "TDA": "TDAT", "TIM": "TIME", "TRD": "TRDA",
	"TRK": "TRCK", "TPA": "TPOS", "TBP": "TBPM", "TLE": "TLEN",
	"TKE": "TKEY", "TLA": "TLAN", "TMT": "TMED", "TOA": "TOPE",
	"TOF": "TOFN", "TOL": "TOLY", "TOR": "TORY", "TOT": "TOAL",
	"TCR": "TCOP", "TPB": "TPUB", "TEN": "TENC", "TSS": "TSSE",
	"TRC": "TSRC", "TP0": "TPE1", "TST": "TSOT", "TSA": "TSOA",
	"TSP": "TSOP", "TCP": "TCMP", "TS2": "TSO2", "TSC": "TSOC",
	"COM": "COMM", "ULT": "USLT", "SLT": "SYLT",
	"WAF": "WOAF", "WAR": "WOAR", "WAS": "WOAS", "WCM": "WCOM",
	"WCP": "WCOP", "WPB": "WPUB", "WXX": "WXXX",
	"TXX": "TXXX", "UFI": "UFID", "IPL": "IPLS", "MCI": "MCDI",
	"PIC": "APIC", "GEO": "GEOB", "CNT": "PCNT", "POP": "POPM",
}

// upgradeV22Frames converts a parsed v2.2 frame list to v2.3 in place, dropping
// frames with no v2.3 counterpart rather than emitting identifiers no reader
// would understand.
func upgradeV22Frames(t *id3Tag) {
	if t.major > 2 {
		return
	}
	out := t.frames[:0]
	for _, f := range t.frames {
		id, ok := v22ToV23[f.id]
		if !ok {
			continue
		}
		payload := f.payload
		if f.id == "PIC" {
			var converted bool
			if payload, converted = picToAPIC(payload); !converted {
				continue
			}
		}
		out = append(out, id3Frame{id: id, payload: payload})
	}
	t.frames = out
	t.major = 3
}

// picToAPIC rewrites a v2.2 PIC body as a v2.3 APIC body. The two differ in one
// field: PIC carries a fixed three-byte image format ("JPG", "PNG"), while APIC
// carries a NUL-terminated MIME type.
func picToAPIC(p []byte) ([]byte, bool) {
	// PIC: encoding(1) format(3) type(1) description(NUL-terminated) data
	if len(p) < 6 {
		return nil, false
	}
	enc := p[0]
	format := string(bytes.ToUpper(p[1:4]))
	rest := p[4:] // picture type, then description and data

	mime := "image/" + lowerASCII(format)
	switch format {
	case "JPG", "JPE":
		mime = "image/jpeg"
	case "PNG":
		mime = "image/png"
	case "GIF":
		mime = "image/gif"
	case "BMP":
		mime = "image/bmp"
	case "-->":
		mime = "-->" // a URL rather than embedded data; APIC keeps this form
	}

	out := make([]byte, 0, len(p)+len(mime))
	out = append(out, enc)
	out = append(out, mime...)
	out = append(out, 0)
	out = append(out, rest...)
	return out, true
}

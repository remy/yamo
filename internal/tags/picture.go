package tags

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	_ "image/gif"  // registered so DecodeConfig can measure GIF covers
	_ "image/jpeg" // ...and JPEG, which is what almost all of them are
	_ "image/png"  // ...and PNG
	"strings"
)

// PictureKind is the role an image plays, numbered as ID3v2 and FLAC both
// number it. Front cover is the only one that matters in practice, but the
// others are preserved rather than flattened.
type PictureKind uint8

const (
	PictureOther      PictureKind = 0
	PictureFileIcon   PictureKind = 1
	PictureOtherIcon  PictureKind = 2
	PictureFrontCover PictureKind = 3
	PictureBackCover  PictureKind = 4
	PictureLeaflet    PictureKind = 5
	PictureMedia      PictureKind = 6
	PictureArtist     PictureKind = 7
)

var pictureKindNames = map[PictureKind]string{
	PictureOther: "other", PictureFileIcon: "file icon", PictureOtherIcon: "icon",
	PictureFrontCover: "front cover", PictureBackCover: "back cover",
	PictureLeaflet: "leaflet", PictureMedia: "media", PictureArtist: "artist",
}

func (k PictureKind) String() string {
	if n, ok := pictureKindNames[k]; ok {
		return n
	}
	return fmt.Sprintf("type %d", uint8(k))
}

// Picture is one embedded image, in a form independent of the container it
// came out of.
type Picture struct {
	Kind        PictureKind
	MIME        string
	Description string
	Width       int
	Height      int
	Depth       int // bits per pixel; only FLAC records it
	Data        []byte
}

// ErrNoPicture means the file carries no embedded image.
var ErrNoPicture = errors.New("tags: no embedded artwork")

// Size is the image's byte count.
func (p *Picture) Size() int { return len(p.Data) }

// Ext returns a file extension for the image, for exporting it.
func (p *Picture) Ext() string {
	switch {
	case strings.Contains(p.MIME, "png"):
		return ".png"
	case strings.Contains(p.MIME, "gif"):
		return ".gif"
	case strings.Contains(p.MIME, "bmp"):
		return ".bmp"
	case strings.Contains(p.MIME, "webp"):
		return ".webp"
	}
	return ".jpg"
}

// Summary renders the image for a status line or a report.
func (p *Picture) Summary() string {
	dims := "?×?"
	if p.Width > 0 && p.Height > 0 {
		dims = fmt.Sprintf("%d×%d", p.Width, p.Height)
	}
	kind := strings.TrimPrefix(p.MIME, "image/")
	if kind == "" {
		kind = "image"
	}
	s := fmt.Sprintf("%s %s %s", dims, kind, humanBytes(len(p.Data)))
	if p.Kind != PictureFrontCover && p.Kind != PictureOther {
		s += " (" + p.Kind.String() + ")"
	}
	return s
}

func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// NewPicture builds a front-cover Picture from raw image bytes, measuring it
// and deciding its MIME type from the content rather than trusting a filename.
func NewPicture(data []byte) (*Picture, error) {
	if len(data) == 0 {
		return nil, errors.New("tags: empty image")
	}
	p := &Picture{Kind: PictureFrontCover, Data: data}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		// An image this package cannot measure is still storable; refusing it
		// would reject formats players handle perfectly well.
		p.MIME = sniffImageMIME(data)
		if p.MIME == "" {
			return nil, fmt.Errorf("tags: unrecognised image: %w", err)
		}
		return p, nil
	}
	p.Width, p.Height = cfg.Width, cfg.Height
	p.MIME = "image/" + format
	return p, nil
}

// measure fills in dimensions for a picture read out of a file, where the
// container may not have recorded them.
func (p *Picture) measure() {
	if p.Width > 0 && p.Height > 0 {
		return
	}
	if cfg, format, err := image.DecodeConfig(bytes.NewReader(p.Data)); err == nil {
		p.Width, p.Height = cfg.Width, cfg.Height
		if p.MIME == "" {
			p.MIME = "image/" + format
		}
	}
}

// sniffImageMIME identifies an image from its magic bytes, for the formats
// the standard library cannot decode.
func sniffImageMIME(b []byte) string {
	switch {
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return "image/jpeg"
	case len(b) >= 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png"
	case len(b) >= 6 && (string(b[:6]) == "GIF87a" || string(b[:6]) == "GIF89a"):
		return "image/gif"
	case len(b) >= 2 && b[0] == 'B' && b[1] == 'M':
		return "image/bmp"
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "image/webp"
	}
	return ""
}

// --- ID3v2 APIC ---------------------------------------------------------

// parseAPIC decodes an ID3v2.3/2.4 attached picture frame.
func parseAPIC(payload []byte) (*Picture, bool) {
	if len(payload) < 4 {
		return nil, false
	}
	enc := payload[0]
	mime, rest, ok := splitTerminated(payload[1:], encISO8859) // MIME is always Latin-1
	if !ok || len(rest) < 2 {
		return nil, false
	}
	p := &Picture{Kind: PictureKind(rest[0]), MIME: strings.TrimSpace(string(mime))}
	desc, data, ok := splitTerminated(rest[1:], enc)
	if !ok {
		return nil, false
	}
	p.Description = decodeText(enc, desc)
	p.Data = data
	if p.MIME == "" || !strings.Contains(p.MIME, "/") {
		// Some taggers write "JPG" here rather than a MIME type.
		if m := sniffImageMIME(p.Data); m != "" {
			p.MIME = m
		}
	}
	p.measure()
	return p, true
}

// encodeAPIC builds an attached picture frame body. The description is written
// as Latin-1 so the frame stays readable by ID3v2.3 players; covers rarely
// carry one, and never one that needs more.
func encodeAPIC(p *Picture) []byte {
	desc := p.Description
	if !latin1Encodable(desc) {
		desc = ""
	}
	out := make([]byte, 0, len(p.Data)+len(p.MIME)+len(desc)+8)
	out = append(out, encISO8859)
	out = append(out, p.MIME...)
	out = append(out, 0)
	out = append(out, byte(p.Kind))
	out = append(out, encodeLatin1(desc)...)
	out = append(out, 0)
	return append(out, p.Data...)
}

// --- FLAC PICTURE block -------------------------------------------------

// parseFLACPicture decodes a FLAC PICTURE metadata block, which is the only
// container that records the image's dimensions itself.
func parseFLACPicture(b []byte) (*Picture, bool) {
	r := byteReader{b: b}
	kind := r.u32()
	mime := r.str32()
	desc := r.str32()
	width, height, depth := r.u32(), r.u32(), r.u32()
	r.u32() // indexed-colour count, not modelled
	data := r.bytes32()
	if r.err {
		return nil, false
	}
	p := &Picture{
		Kind: PictureKind(kind), MIME: mime, Description: desc,
		Width: int(width), Height: int(height), Depth: int(depth), Data: data,
	}
	p.measure()
	return p, true
}

// encodeFLACPicture builds a PICTURE block body.
func encodeFLACPicture(p *Picture) []byte {
	out := make([]byte, 0, len(p.Data)+len(p.MIME)+len(p.Description)+32)
	out = binary.BigEndian.AppendUint32(out, uint32(p.Kind))
	out = binary.BigEndian.AppendUint32(out, uint32(len(p.MIME)))
	out = append(out, p.MIME...)
	out = binary.BigEndian.AppendUint32(out, uint32(len(p.Description)))
	out = append(out, p.Description...)
	out = binary.BigEndian.AppendUint32(out, uint32(p.Width))
	out = binary.BigEndian.AppendUint32(out, uint32(p.Height))
	out = binary.BigEndian.AppendUint32(out, uint32(p.Depth))
	out = binary.BigEndian.AppendUint32(out, 0) // no indexed-colour palette
	out = binary.BigEndian.AppendUint32(out, uint32(len(p.Data)))
	return append(out, p.Data...)
}

// --- Vorbis METADATA_BLOCK_PICTURE --------------------------------------

// Ogg has no picture block of its own, so it carries a base64 encoding of the
// FLAC one in an ordinary comment field.

func parseVorbisPicture(value string) (*Picture, bool) {
	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, false
	}
	return parseFLACPicture(raw)
}

func encodeVorbisPicture(p *Picture) string {
	return base64.StdEncoding.EncodeToString(encodeFLACPicture(p))
}

// --- MP4 covr -----------------------------------------------------------

// MP4 well-known data types for images.
const (
	mp4DataJPEG = 13
	mp4DataPNG  = 14
	mp4DataBMP  = 27
)

// parseMP4Cover decodes a covr item body.
func parseMP4Cover(body []byte) (*Picture, bool) {
	var p *Picture
	walkAtoms(body, func(typ string, b []byte) bool {
		if typ != "data" || len(b) < 8 {
			return true
		}
		mime := ""
		switch binary.BigEndian.Uint32(b[0:4]) & 0x00FFFFFF {
		case mp4DataJPEG:
			mime = "image/jpeg"
		case mp4DataPNG:
			mime = "image/png"
		case mp4DataBMP:
			mime = "image/bmp"
		}
		data := b[8:]
		if mime == "" {
			mime = sniffImageMIME(data)
		}
		p = &Picture{Kind: PictureFrontCover, MIME: mime, Data: data}
		p.measure()
		return false
	})
	return p, p != nil
}

// encodeMP4Cover builds a covr item body.
func encodeMP4Cover(p *Picture) []byte {
	kind := uint32(mp4DataJPEG)
	switch {
	case strings.Contains(p.MIME, "png"):
		kind = mp4DataPNG
	case strings.Contains(p.MIME, "bmp"):
		kind = mp4DataBMP
	}
	data := make([]byte, 8, 8+len(p.Data))
	binary.BigEndian.PutUint32(data[0:4], kind)
	data = append(data, p.Data...)
	return atom("data", data)
}

// byteReader reads the big-endian, length-prefixed fields a FLAC picture block
// is made of, tracking overrun so each read does not need a bounds check.
type byteReader struct {
	b   []byte
	p   int
	err bool
}

func (r *byteReader) u32() uint32 {
	if r.err || r.p+4 > len(r.b) {
		r.err = true
		return 0
	}
	v := binary.BigEndian.Uint32(r.b[r.p : r.p+4])
	r.p += 4
	return v
}

func (r *byteReader) bytes32() []byte {
	n := int(r.u32())
	if r.err || n < 0 || r.p+n > len(r.b) {
		r.err = true
		return nil
	}
	v := r.b[r.p : r.p+n]
	r.p += n
	return v
}

func (r *byteReader) str32() string { return string(r.bytes32()) }

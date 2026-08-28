package tags

import (
	"io"
	"os"
)

// ReadPictures extracts every embedded image from a file.
//
// This is deliberately separate from reading tags. A catalogue scan visits a
// hundred thousand files and must not pull megabytes of image data into memory
// to do it, so the scanner only records whether artwork is present; the bytes
// are fetched on demand, for one file at a time.
func ReadPictures(path string) ([]Picture, error) {
	format, err := detectFormat(path)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := fi.Size()

	switch format {
	case FormatMP3, FormatWAV, FormatAIFF:
		return readID3Pictures(f, size)
	case FormatFLAC:
		return readFLACPictures(f, size)
	case FormatOggVorbis, FormatOpus:
		return readOggPictures(f, size)
	case FormatMP4:
		return readMP4Pictures(f, size)
	}
	return nil, ErrUnsupported
}

// ReadCover returns the front cover, or the first image if none is marked as
// one. Most callers want exactly one picture and do not care about the rest.
func ReadCover(path string) (*Picture, error) {
	pics, err := ReadPictures(path)
	if err != nil {
		return nil, err
	}
	return PickCover(pics)
}

// PickCover chooses the image that best represents a release.
func PickCover(pics []Picture) (*Picture, error) {
	if len(pics) == 0 {
		return nil, ErrNoPicture
	}
	for i := range pics {
		if pics[i].Kind == PictureFrontCover {
			return &pics[i], nil
		}
	}
	return &pics[0], nil
}

func readID3Pictures(f *os.File, size int64) ([]Picture, error) {
	var header [10]byte
	if n, _ := f.ReadAt(header[:], 0); n < 10 {
		return nil, ErrNoPicture
	}
	tagSize := id3v2Size(header[:])
	if tagSize == 0 || int64(tagSize) > size {
		return nil, ErrNoPicture
	}
	buf := make([]byte, tagSize)
	if _, err := f.ReadAt(buf, 0); err != nil && err != io.EOF {
		return nil, err
	}
	tag, err := parseID3v2(buf)
	if err != nil {
		return nil, err
	}
	upgradeV22Frames(tag)

	var out []Picture
	for _, fr := range tag.frames {
		if fr.id != "APIC" {
			continue
		}
		if p, ok := parseAPIC(fr.payload); ok {
			out = append(out, *p)
		}
	}
	return out, nil
}

func readFLACPictures(f *os.File, size int64) ([]Picture, error) {
	blocks, _, err := readFLACMetadata(f, size)
	if err != nil {
		return nil, err
	}
	var out []Picture
	for _, b := range blocks {
		switch b.typ {
		case flacPicture:
			if p, ok := parseFLACPicture(b.body); ok {
				out = append(out, *p)
			}
		case flacVorbisComment:
			// A FLAC file may also carry a base64 picture in a comment field,
			// usually because it was converted from Ogg.
			if vc, ok := parseVorbisComment(b.body); ok {
				out = append(out, vorbisPictures(vc)...)
			}
		}
	}
	return out, nil
}

func readOggPictures(f *os.File, size int64) ([]Picture, error) {
	r := NewReader()
	head, err := r.readHead(f, size, 1<<20)
	if err != nil {
		return nil, err
	}
	var md Metadata
	vc, ok := parseOgg(head, &md)
	if !ok || vc == nil {
		return nil, ErrNoPicture
	}
	return vorbisPictures(vc), nil
}

// vorbisPictures decodes the base64 picture fields of a comment block.
func vorbisPictures(vc *vorbisComment) []Picture {
	var out []Picture
	for _, f := range vc.fields {
		if f.key != "METADATA_BLOCK_PICTURE" {
			continue
		}
		if p, ok := parseVorbisPicture(f.value); ok {
			out = append(out, *p)
		}
	}
	return out
}

func readMP4Pictures(f *os.File, size int64) ([]Picture, error) {
	off, moovSize, err := findMoov(f, size)
	if err != nil || moovSize <= 8 || moovSize > maxMoovSize {
		return nil, ErrNoPicture
	}
	buf := make([]byte, moovSize-8)
	if _, err := f.ReadAt(buf, off+8); err != nil && err != io.EOF {
		return nil, err
	}

	var out []Picture
	walkAtoms(buf, func(typ string, body []byte) bool {
		if typ != "udta" {
			return true
		}
		walkAtoms(body, func(typ string, body []byte) bool {
			if typ != "meta" || len(body) < 4 {
				return true
			}
			walkAtoms(body[4:], func(typ string, body []byte) bool {
				if typ != "ilst" {
					return true
				}
				walkAtoms(body, func(typ string, body []byte) bool {
					if typ == atomCover {
						if p, ok := parseMP4Cover(body); ok {
							out = append(out, *p)
						}
					}
					return true
				})
				return false
			})
			return true
		})
		return true
	})
	return out, nil
}

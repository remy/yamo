package tags

import (
	"fmt"
	"os"
)

// Write applies an edit to the file at path, preserving every tag field the
// edit does not mention.
//
// Where possible the tag is rewritten in place, inside the padding the format
// reserves for exactly this. That path touches only the first few kilobytes of
// the file; the fallback rebuilds the file next to the original and renames it
// over the top, so an interrupted write never leaves a damaged track.
func Write(path string, e *Edit) error {
	if e == nil || e.Empty() {
		return nil
	}
	format, err := detectFormat(path)
	if err != nil {
		return err
	}
	switch format {
	case FormatMP3, FormatWAV, FormatAIFF:
		// These all carry metadata in an ID3v2 tag at the head of the file,
		// but only MP3 conventionally does so; the others need chunk surgery.
		if format != FormatMP3 {
			return fmt.Errorf("%w: writing %s tags", ErrUnsupported, format)
		}
		return writeID3v2(path, e)
	case FormatFLAC:
		return writeFLAC(path, e)
	case FormatMP4:
		return writeMP4(path, e)
	case FormatOggVorbis, FormatOpus:
		return writeOgg(path, e)
	default:
		return fmt.Errorf("%w: writing %s tags", ErrUnsupported, format)
	}
}

// detectFormat sniffs the file, falling back to its extension.
func detectFormat(path string) (Format, error) {
	f, err := os.Open(path)
	if err != nil {
		return FormatUnknown, err
	}
	defer f.Close()

	var head [16]byte
	n, _ := f.ReadAt(head[:], 0)
	if s := sniff(head[:n]); s != FormatUnknown {
		return s, nil
	}
	if ext := FormatForPath(path); ext != FormatUnknown {
		return ext, nil
	}
	return FormatUnknown, ErrUnsupported
}

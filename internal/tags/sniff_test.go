package tags

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSniffLeadingBytes covers the identification rules directly, so the cases
// that need no encoder still run when ffmpeg is absent.
func TestSniffLeadingBytes(t *testing.T) {
	id3 := func(rest ...byte) []byte {
		// A well-formed ID3v2.4 header declaring a 1 KB tag.
		b := []byte{'I', 'D', '3', 4, 0, 0, 0, 0, 0x08, 0x00}
		return append(b, rest...)
	}
	cases := []struct {
		name string
		head []byte
		want Format
	}{
		{"flac", []byte("fLaCsomething more"), FormatFLAC},
		{"mp4", []byte("\x00\x00\x00\x20ftypM4A \x00\x00\x00\x00"), FormatMP4},
		{"id3v2", id3(), FormatMP3},
		{"bare frame", []byte{0xFF, 0xFB, 0x90, 0x64}, FormatMP3},
		{"reserved layer is not a frame", []byte{0xFF, 0xF9, 0x90, 0x64}, FormatUnknown},
		{"bad id3 version", []byte{'I', 'D', '3', 0xFF, 0, 0, 0, 0, 0x08, 0x00}, FormatUnknown},
		{"ogg defers to the first packet", []byte("OggS\x00\x02\x00\x00\x00\x00\x00\x00"), FormatUnknown},
		{"too short", []byte("ID"), FormatUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sniff(tc.head); got != tc.want {
				t.Fatalf("sniff = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestMisnamedExtension covers a real case from a library assembled over
// decades: an MP3 saved as .m4a. Trusting the extension sent it to the MP4
// reader, which walked the ID3 tag as though it were an atom chain, found no
// moov and reported the file as having no tags at all. Editing it then failed
// with "no moov atom to write into".
func TestMisnamedExtension(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name     string
		args     []string
		misnamed string
		want     Format
	}{
		{"mp3 as m4a", []string{"-c:a", "libmp3lame"}, "song.m4a", FormatMP3},
		{"mp3 without a tag as m4a", []string{"-c:a", "libmp3lame", "-write_id3v2", "0", "-write_id3v1", "0"}, "untagged.m4a", FormatMP3},
		{"m4a as mp3", []string{"-c:a", "aac"}, "song.mp3", FormatMP4},
		{"flac as mp3", nil, "song2.mp3", FormatFLAC},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Encode under an honest extension, then rename: ffmpeg picks the
			// muxer from the name, so the lie has to come afterwards.
			ext := map[Format]string{FormatMP3: ".mp3", FormatMP4: ".m4a", FormatFLAC: ".flac"}[tc.want]
			src := genFile(t, dir, "src-"+tc.misnamed+ext, tc.args...)
			path := filepath.Join(dir, tc.misnamed)
			if err := os.Rename(src, path); err != nil {
				t.Fatal(err)
			}

			var r Reader
			md, err := r.ReadFile(path)
			if err != nil && err != ErrNoTags {
				t.Fatalf("read: %v", err)
			}
			if md.Format != tc.want {
				t.Fatalf("format = %v, want %v", md.Format, tc.want)
			}

			// The untagged fixture has nothing to read back, but every case
			// must still be writable: that is what failed.
			e := &Edit{Artist: str("Plan B")}
			if err := Write(path, e); err != nil {
				t.Fatalf("write: %v", err)
			}
			md, err = r.ReadFile(path)
			if err != nil {
				t.Fatalf("re-read: %v", err)
			}
			if md.Artist != "Plan B" {
				t.Fatalf("artist = %q, want %q", md.Artist, "Plan B")
			}
			decodes(t, path)
		})
	}
}

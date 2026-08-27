package tags

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// buildV22Frame assembles one ID3v2.2 frame: a three-character identifier, a
// three-byte big-endian size, then the payload.
func buildV22Frame(id string, payload []byte) []byte {
	out := make([]byte, 0, 6+len(payload))
	out = append(out, id...)
	n := len(payload)
	out = append(out, byte(n>>16), byte(n>>8), byte(n))
	return append(out, payload...)
}

func latin1Frame(id, text string) []byte {
	return buildV22Frame(id, append([]byte{encISO8859}, text...))
}

// buildV22Tag wraps frames in an ID3v2.2 header with some trailing padding.
func buildV22Tag(frames ...[]byte) []byte {
	var body []byte
	for _, f := range frames {
		body = append(body, f...)
	}
	body = append(body, make([]byte, 128)...) // padding

	tag := make([]byte, 10, 10+len(body))
	copy(tag, "ID3")
	tag[3], tag[4], tag[5] = 2, 0, 0
	putSynchsafe(tag[6:10], len(body))
	return append(tag, body...)
}

// fakeJPEG is enough of a JPEG for the tag reader; nothing decodes it.
var fakeJPEG = append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, bytes.Repeat([]byte{0x42}, 512)...)

// writeV22File produces a real MP3 with an ID3v2.2 tag on the front. ffmpeg
// cannot write v2.2, so the tag is built by hand and prepended to a stream
// ffmpeg produced.
func writeV22File(t *testing.T, dir string) string {
	t.Helper()
	ff := ffmpegPath(t)

	bare := filepath.Join(dir, "bare.mp3")
	cmd := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2",
		"-c:a", "libmp3lame", "-map_metadata", "-1", "-write_xing", "0", bare)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, b)
	}
	audio, err := os.ReadFile(bare)
	if err != nil {
		t.Fatal(err)
	}

	pic := []byte{encISO8859}
	pic = append(pic, "JPG"...)
	pic = append(pic, 0x03) // picture type: front cover
	pic = append(pic, 0)    // empty description
	pic = append(pic, fakeJPEG...)

	tag := buildV22Tag(
		latin1Frame("TT2", "Original Title"),
		latin1Frame("TP1", "Elvis Presley"),
		latin1Frame("TAL", "Original Album"),
		latin1Frame("TCM", "Arthur Crudup"),
		latin1Frame("TCO", "(17)"),
		latin1Frame("TRK", "3/12"),
		latin1Frame("TYE", "1956"),
		buildV22Frame("PIC", pic),
	)

	path := filepath.Join(dir, "v22.mp3")
	if err := os.WriteFile(path, append(tag, audio...), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadID3v22(t *testing.T) {
	path := writeV22File(t, t.TempDir())
	md, err := NewReader().ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if md.Title != "Original Title" {
		t.Errorf("title = %q", md.Title)
	}
	if md.Artist != "Elvis Presley" {
		t.Errorf("artist = %q", md.Artist)
	}
	if md.Album != "Original Album" {
		t.Errorf("album = %q", md.Album)
	}
	if md.Composer != "Arthur Crudup" {
		t.Errorf("composer = %q", md.Composer)
	}
	if md.Genre != "Rock" {
		t.Errorf("genre = %q, want Rock (resolved from the numeric reference)", md.Genre)
	}
	if md.Track != 3 || md.TrackTotal != 12 {
		t.Errorf("track = %d/%d, want 3/12", md.Track, md.TrackTotal)
	}
	if md.Year != 1956 {
		t.Errorf("year = %d", md.Year)
	}
	if !md.HasArt {
		t.Error("artwork not detected")
	}
}

// TestWriteUpgradesID3v22 is the regression this file exists for: editing one
// field of a v2.2 tag must not discard the frames the edit did not mention.
func TestWriteUpgradesID3v22(t *testing.T) {
	path := writeV22File(t, t.TempDir())

	if err := Write(path, &Edit{Album: str("The Sun Sessions")}); err != nil {
		t.Fatalf("write: %v", err)
	}
	decodes(t, path)

	md, err := NewReader().ReadFile(path)
	if err != nil {
		t.Fatalf("read after write: %v", err)
	}
	if md.Album != "The Sun Sessions" {
		t.Errorf("album = %q, want the edited value", md.Album)
	}
	for _, c := range []struct{ name, got, want string }{
		{"title", md.Title, "Original Title"},
		{"artist", md.Artist, "Elvis Presley"},
		{"composer", md.Composer, "Arthur Crudup"},
		{"genre", md.Genre, "Rock"},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q preserved through the upgrade", c.name, c.got, c.want)
		}
	}
	if md.Track != 3 || md.TrackTotal != 12 {
		t.Errorf("track = %d/%d, want 3/12 preserved", md.Track, md.TrackTotal)
	}
	if md.Year != 1956 {
		t.Errorf("year = %d, want 1956 preserved", md.Year)
	}
	if !md.HasArt {
		t.Error("artwork was lost in the upgrade from v2.2")
	}

	// The tag on disk must now be v2.3, and the artwork must still be a
	// well-formed APIC frame with a MIME type rather than a v2.2 format code.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if raw[3] != 3 {
		t.Errorf("tag version on disk is 2.%d, want 2.3", raw[3])
	}
	tag, err := parseID3v2(raw)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	var apic []byte
	for _, f := range tag.frames {
		if f.id == "APIC" {
			apic = f.payload
		}
		if len(f.id) != 4 {
			t.Errorf("frame %q still has a v2.2 identifier", f.id)
		}
	}
	if apic == nil {
		t.Fatal("no APIC frame after the upgrade")
	}
	if !bytes.HasPrefix(apic[1:], []byte("image/jpeg\x00")) {
		t.Errorf("APIC mime type = %q, want image/jpeg", apic[1:16])
	}
	if !bytes.Contains(apic, fakeJPEG) {
		t.Error("the image data did not survive the PIC to APIC conversion")
	}
}

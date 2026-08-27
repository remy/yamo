package tags

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ffmpegPath finds ffmpeg, or skips the test when it is unavailable. The
// round-trip tests need real encoder output: hand-built fixtures would only
// prove the writer agrees with itself.
func ffmpegPath(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	return p
}

// genFile encodes two seconds of tone into dir/name with the given metadata.
func genFile(t *testing.T, dir, name string, extraArgs ...string) string {
	t.Helper()
	ff := ffmpegPath(t)
	out := filepath.Join(dir, name)
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2",
	}
	args = append(args, extraArgs...)
	args = append(args,
		"-metadata", "title=Original Title",
		"-metadata", "artist=Elvis Presley",
		"-metadata", "album=Original Album",
		"-metadata", "genre=Rock",
		"-metadata", "date=1956",
		"-metadata", "track=3",
		out)
	cmd := exec.Command(ff, args...)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg %s: %v\n%s", name, err, b)
	}
	return out
}

// decodes checks the audio stream still plays after a tag write. A tag writer
// that corrupts the stream can still produce a file whose tags read back
// perfectly, so this is the assertion that actually matters.
//
// Only the audio stream is decoded. Embedded cover art appears to ffmpeg as a
// video stream, and whether a test fixture's artwork is a well-formed image
// says nothing about whether the tag writer preserved the audio.
func decodes(t *testing.T, path string) {
	t.Helper()
	ff := ffmpegPath(t)
	cmd := exec.Command(ff, "-hide_banner", "-v", "error", "-i", path,
		"-map", "0:a:0", "-vn", "-f", "null", "-")
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("audio no longer decodes after write: %v\n%s", err, b)
	}
	if len(b) > 0 && strings.Contains(string(b), "error") {
		t.Fatalf("decoder reported errors after write:\n%s", b)
	}
}

func str(s string) *string { return &s }
func i32(v int32) *int32   { return &v }

func TestRoundTrip(t *testing.T) {
	cases := []struct {
		name  string
		file  string
		args  []string
		codec Format
	}{
		{name: "mp3-id3v24", file: "a.mp3", args: []string{"-c:a", "libmp3lame", "-id3v2_version", "4"}, codec: FormatMP3},
		{name: "mp3-id3v23", file: "b.mp3", args: []string{"-c:a", "libmp3lame", "-id3v2_version", "3"}, codec: FormatMP3},
		{name: "flac", file: "c.flac", args: []string{"-c:a", "flac"}, codec: FormatFLAC},
		{name: "m4a", file: "d.m4a", args: []string{"-c:a", "aac"}, codec: FormatMP4},
		{name: "ogg", file: "e.ogg", args: []string{"-c:a", "libvorbis"}, codec: FormatOggVorbis},
		{name: "opus", file: "f.opus", args: []string{"-c:a", "libopus"}, codec: FormatOpus},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := genFile(t, dir, tc.file, tc.args...)

			r := NewReader()
			before, err := r.ReadFile(path)
			if err != nil {
				t.Fatalf("initial read: %v", err)
			}
			if before.Artist != "Elvis Presley" {
				t.Fatalf("initial artist = %q, want %q", before.Artist, "Elvis Presley")
			}
			if before.Album != "Original Album" {
				t.Fatalf("initial album = %q", before.Album)
			}
			if before.DurationMS < 1500 || before.DurationMS > 2500 {
				t.Errorf("duration = %dms, want about 2000ms", before.DurationMS)
			}

			// A modest edit that should fit any padding the encoder left.
			e := &Edit{
				Album: str("Sun Sessions"),
				Title: str("Blue Moon"),
				Year:  i32(1954),
				Track: i32(7),
			}
			if err := Write(path, e); err != nil {
				t.Fatalf("write: %v", err)
			}
			decodes(t, path)

			after, err := r.ReadFile(path)
			if err != nil {
				t.Fatalf("read after write: %v", err)
			}
			if after.Album != "Sun Sessions" {
				t.Errorf("album = %q, want %q", after.Album, "Sun Sessions")
			}
			if after.Title != "Blue Moon" {
				t.Errorf("title = %q, want %q", after.Title, "Blue Moon")
			}
			if after.Year != 1954 {
				t.Errorf("year = %d, want 1954", after.Year)
			}
			if after.Track != 7 {
				t.Errorf("track = %d, want 7", after.Track)
			}
			// Untouched fields must survive.
			if after.Artist != "Elvis Presley" {
				t.Errorf("artist = %q, want it preserved", after.Artist)
			}
			if after.Genre != "Rock" {
				t.Errorf("genre = %q, want it preserved", after.Genre)
			}
			if after.DurationMS < 1500 || after.DurationMS > 2500 {
				t.Errorf("duration after write = %dms", after.DurationMS)
			}

			// A large value forces the tag past any reserved padding and
			// exercises the rewrite path.
			big := strings.TrimSpace(strings.Repeat("The Very Long Album Title ", 400))
			if err := Write(path, &Edit{Album: str(big)}); err != nil {
				t.Fatalf("write large: %v", err)
			}
			decodes(t, path)

			grown, err := r.ReadFile(path)
			if err != nil {
				t.Fatalf("read after large write: %v", err)
			}
			if grown.Album != big {
				t.Errorf("large album did not round-trip (got %d bytes, want %d)", len(grown.Album), len(big))
			}
			if grown.Title != "Blue Moon" {
				t.Errorf("title lost during rewrite: %q", grown.Title)
			}
			if grown.Artist != "Elvis Presley" {
				t.Errorf("artist lost during rewrite: %q", grown.Artist)
			}

			// And shrink again, which is where in-place padding maths bites.
			if err := Write(path, &Edit{Album: str("OK")}); err != nil {
				t.Fatalf("write small: %v", err)
			}
			decodes(t, path)
			shrunk, err := r.ReadFile(path)
			if err != nil {
				t.Fatalf("read after shrink: %v", err)
			}
			if shrunk.Album != "OK" {
				t.Errorf("album after shrink = %q", shrunk.Album)
			}
			if shrunk.Artist != "Elvis Presley" {
				t.Errorf("artist after shrink = %q", shrunk.Artist)
			}
		})
	}
}

// TestWriteUnicode checks that non-Latin text survives every container, which
// exercises the UTF-16 path in ID3v2.3 in particular.
func TestWriteUnicode(t *testing.T) {
	const want = "Björk — Homogénic 東京"
	for _, tc := range []struct {
		name string
		file string
		args []string
	}{
		{"mp3v23", "a.mp3", []string{"-c:a", "libmp3lame", "-id3v2_version", "3"}},
		{"mp3v24", "b.mp3", []string{"-c:a", "libmp3lame", "-id3v2_version", "4"}},
		{"flac", "c.flac", []string{"-c:a", "flac"}},
		{"m4a", "d.m4a", []string{"-c:a", "aac"}},
		{"opus", "e.opus", []string{"-c:a", "libopus"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := genFile(t, dir, tc.file, tc.args...)
			if err := Write(path, &Edit{Album: str(want), Artist: str(want)}); err != nil {
				t.Fatalf("write: %v", err)
			}
			decodes(t, path)
			md, err := NewReader().ReadFile(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if md.Album != want {
				t.Errorf("album = %q, want %q", md.Album, want)
			}
			if md.Artist != want {
				t.Errorf("artist = %q, want %q", md.Artist, want)
			}
		})
	}
}

// TestWriteCreatesTag covers files that start with no metadata at all.
func TestWriteCreatesTag(t *testing.T) {
	ff := ffmpegPath(t)
	for _, tc := range []struct {
		name string
		file string
		args []string
	}{
		{"mp3", "a.mp3", []string{"-c:a", "libmp3lame"}},
		{"flac", "b.flac", []string{"-c:a", "flac"}},
		{"m4a", "c.m4a", []string{"-c:a", "aac"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, tc.file)
			args := []string{"-hide_banner", "-loglevel", "error", "-y",
				"-f", "lavfi", "-i", "sine=frequency=440:duration=1"}
			args = append(args, tc.args...)
			args = append(args, "-map_metadata", "-1", "-write_xing", "0", path)
			if b, err := exec.Command(ff, args...).CombinedOutput(); err != nil {
				t.Fatalf("ffmpeg: %v\n%s", err, b)
			}

			if err := Write(path, &Edit{Artist: str("New Artist"), Album: str("New Album")}); err != nil {
				t.Fatalf("write: %v", err)
			}
			decodes(t, path)
			md, err := NewReader().ReadFile(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if md.Artist != "New Artist" || md.Album != "New Album" {
				t.Errorf("got artist=%q album=%q", md.Artist, md.Album)
			}
		})
	}
}

// TestNoEditIsNoOp asserts an empty edit leaves the file byte-identical.
func TestNoEditIsNoOp(t *testing.T) {
	dir := t.TempDir()
	path := genFile(t, dir, "a.mp3", "-c:a", "libmp3lame")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Write(path, &Edit{}); err != nil {
		t.Fatalf("write: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatalf("file size changed from %d to %d", len(before), len(after))
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("file changed at byte %d", i)
		}
	}
}

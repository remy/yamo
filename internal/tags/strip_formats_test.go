package tags

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// genTagged encodes a file carrying both the metadata a strip keeps and the
// metadata it removes, so every format is exercised on the same material.
func genTagged(t *testing.T, dir, name string, codecArgs ...string) string {
	t.Helper()
	ff := ffmpegPath(t)
	out := filepath.Join(dir, name)
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2",
	}
	args = append(args, codecArgs...)
	args = append(args,
		// Kept.
		"-metadata", "title=Hound Dog",
		"-metadata", "artist=Elvis Presley",
		"-metadata", "album=Sun Sessions",
		"-metadata", "album_artist=Various Artists",
		"-metadata", "track=3/12",
		"-metadata", "disc=1/1",
		"-metadata", "genre=Rock",
		"-metadata", "date=1956",
		"-metadata", "composer=Leiber/Stoller",
		"-metadata", "compilation=1",
		// Removed.
		"-metadata", "comment=a real comment",
		"-metadata", "publisher=RCA Victor",
		"-metadata", "copyright=1956 RCA",
		"-metadata", "encoder=some encoder",
		"-metadata", "TSRC=USRC17607839",
		"-metadata", "MUSICBRAINZ_TRACKID=c0ffee00-dead-beef",
		out)
	if b, err := exec.Command(ff, args...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg %s: %v\n%s", name, err, b)
	}
	return out
}

// TestStripAcrossFormats is the point of the canonical tag model: one keep
// list, expressed once, applied to four different containers.
func TestStripAcrossFormats(t *testing.T) {
	cases := []struct {
		name  string
		file  string
		args  []string
		codec Format
	}{
		{"mp3", "a.mp3", []string{"-c:a", "libmp3lame"}, FormatMP3},
		{"flac", "b.flac", []string{"-c:a", "flac"}, FormatFLAC},
		{"m4a", "c.m4a", []string{"-c:a", "aac"}, FormatMP4},
		{"ogg", "d.ogg", []string{"-c:a", "libvorbis"}, FormatOggVorbis},
		{"opus", "e.opus", []string{"-c:a", "libopus"}, FormatOpus},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := genTagged(t, dir, tc.file, tc.args...)

			r := NewReader()
			before, err := r.ReadFile(path)
			if err != nil {
				t.Fatalf("initial read: %v", err)
			}
			if before.Artist != "Elvis Presley" || before.Album != "Sun Sessions" {
				t.Fatalf("fixture did not tag correctly: %+v", before)
			}

			keep := NewKeepSet(DefaultKeepTags)

			// The dry run must report work to do and change nothing.
			dry, err := StripFile(path, keep, false)
			if err != nil {
				t.Fatalf("dry run: %v", err)
			}
			if !dry.Changed {
				t.Fatalf("dry run found nothing to remove in a file with a comment and a publisher")
			}
			if dry.Format != tc.codec {
				t.Errorf("format = %v, want %v", dry.Format, tc.codec)
			}

			rep, err := StripFile(path, keep, true)
			if err != nil {
				t.Fatalf("strip: %v", err)
			}
			if !rep.Changed {
				t.Fatal("strip reported no change")
			}
			decodes(t, path)

			after, err := r.ReadFile(path)
			if err != nil {
				t.Fatalf("read after strip: %v", err)
			}
			// Everything on the keep list survives...
			for _, c := range []struct{ name, got, want string }{
				{"title", after.Title, "Hound Dog"},
				{"artist", after.Artist, "Elvis Presley"},
				{"album", after.Album, "Sun Sessions"},
				{"album artist", after.AlbumArtist, "Various Artists"},
				{"genre", after.Genre, "Rock"},
				{"composer", after.Composer, "Leiber/Stoller"},
			} {
				if c.got != c.want {
					t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
				}
			}
			if after.Year != 1956 {
				t.Errorf("year = %d, want 1956", after.Year)
			}
			if after.Track != 3 {
				t.Errorf("track = %d, want 3", after.Track)
			}
			// ...and the comment, which is not on it, does not.
			if after.Comment != "" {
				t.Errorf("comment survived the strip: %q", after.Comment)
			}
			// The audio properties must be untouched.
			if after.DurationMS < 1500 || after.DurationMS > 2500 {
				t.Errorf("duration after strip = %dms", after.DurationMS)
			}

			// Restoring must bring back exactly what went.
			n, err := RestoreFile(path, rep.Removed)
			if err != nil {
				t.Fatalf("restore: %v", err)
			}
			if n == 0 {
				t.Fatal("restore added nothing")
			}
			decodes(t, path)
			back, err := r.ReadFile(path)
			if err != nil {
				t.Fatalf("read after restore: %v", err)
			}
			if back.Comment != "a real comment" {
				t.Errorf("comment did not come back: %q", back.Comment)
			}
			if back.Artist != "Elvis Presley" {
				t.Errorf("restore disturbed the artist: %q", back.Artist)
			}
		})
	}
}

// TestStripKeepsArtworkAcrossFormats covers cover art, which lives in a
// different place in each container: an APIC frame, a FLAC PICTURE block, an
// MP4 covr atom, and a base64 Vorbis field.
func TestStripKeepsArtworkAcrossFormats(t *testing.T) {
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
			art := filepath.Join(dir, "cover.jpg")
			if b, err := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-y",
				"-f", "lavfi", "-i", "color=c=red:s=64x64", "-frames:v", "1", art).CombinedOutput(); err != nil {
				t.Fatalf("ffmpeg cover: %v\n%s", err, b)
			}

			out := filepath.Join(dir, tc.file)
			args := []string{"-hide_banner", "-loglevel", "error", "-y",
				"-f", "lavfi", "-i", "sine=frequency=440:duration=2", "-i", art,
				"-map", "0:a", "-map", "1:v"}
			args = append(args, tc.args...)
			args = append(args, "-c:v", "copy", "-disposition:v", "attached_pic",
				"-metadata", "artist=Elvis Presley",
				"-metadata", "comment=remove me", out)
			if b, err := exec.Command(ff, args...).CombinedOutput(); err != nil {
				t.Skipf("ffmpeg cannot embed art in %s here: %v\n%s", tc.name, err, b)
			}

			r := NewReader()
			before, err := r.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			if !before.HasArt {
				t.Skip("fixture has no embedded art to preserve")
			}

			if _, err := StripFile(out, NewKeepSet(DefaultKeepTags), true); err != nil {
				t.Fatalf("strip: %v", err)
			}
			decodes(t, out)

			after, err := r.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			if !after.HasArt {
				t.Error("cover art was removed despite artwork being on the keep list")
			}
			if after.Artist != "Elvis Presley" {
				t.Errorf("artist = %q", after.Artist)
			}
			if after.Comment != "" {
				t.Errorf("comment survived: %q", after.Comment)
			}
		})
	}
}

// TestStripDropsArtworkWhenNotKept is the other half: artwork must actually go
// when it is left off the list, including from a FLAC PICTURE block.
func TestStripDropsArtworkWhenNotKept(t *testing.T) {
	dir := t.TempDir()
	path := kitchenSink(t, dir)

	keep := KeepSet{}
	for _, tg := range DefaultKeepTags {
		if tg != TagArtwork {
			keep[tg] = true
		}
	}
	if _, err := StripFile(path, keep, true); err != nil {
		t.Fatalf("strip: %v", err)
	}
	md, err := NewReader().ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if md.HasArt {
		t.Error("artwork survived even though it was left off the keep list")
	}
	if md.Title != "Hound Dog" {
		t.Errorf("title = %q", md.Title)
	}
}

// TestStripSkipsUnwritableFormats checks that formats this build cannot write
// are reported rather than damaged.
func TestStripSkipsUnwritableFormats(t *testing.T) {
	ff := ffmpegPath(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "a.wav")
	if b, err := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=1",
		"-metadata", "artist=Elvis Presley", out).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, b)
	}
	rep, err := StripFile(out, NewKeepSet(DefaultKeepTags), true)
	if err != nil {
		t.Fatalf("strip: %v", err)
	}
	if !rep.Unsupported {
		t.Error("a WAV should be reported as unsupported, not stripped")
	}
	if rep.Changed {
		t.Error("an unsupported file must not be reported as changed")
	}
	md, err := NewReader().ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if md.Artist != "Elvis Presley" {
		t.Errorf("the file was modified anyway: artist = %q", md.Artist)
	}
}

// TestVorbisFieldResolution covers the field names FLAC and Ogg actually use.
func TestVorbisFieldResolution(t *testing.T) {
	cases := map[string]Tag{
		"TITLE": TagTitle, "ALBUMARTIST": TagAlbumArtist, "ALBUM ARTIST": TagAlbumArtist,
		"TRACKNUMBER": TagTrack, "TOTALTRACKS": TagTrack, "DISCNUMBER": TagDisc,
		"DATE": TagDate, "COMPILATION": TagCompilation, "ALBUMSORT": TagAlbumSort,
		"METADATA_BLOCK_PICTURE": TagArtwork, "COMMENT": TagComment,
		"REPLAYGAIN_TRACK_GAIN": TagReplayGain, "MUSICBRAINZ_ALBUMID": TagMusicBrainz,
		"ENCODER": TagEncoder, "PUBLISHER": TagPublisher,
	}
	for field, want := range cases {
		if got := tagForVorbisField(field); got != want {
			t.Errorf("tagForVorbisField(%q) = %v, want %v", field, got.Name(), want.Name())
		}
	}
	if got := tagForVorbisField("SOMETHING_NOBODY_WRITES"); got != TagUnknown {
		t.Errorf("unknown field resolved to %v", got.Name())
	}
}

// TestMP4AtomResolution covers the iTunes atoms, including the freeform ones
// whose meaning is in a nested name.
func TestMP4AtomResolution(t *testing.T) {
	for atomName, want := range map[string]Tag{
		"\xa9nam": TagTitle, "\xa9ART": TagArtist, "aART": TagAlbumArtist,
		"trkn": TagTrack, "disk": TagDisc, "cpil": TagCompilation,
		"covr": TagArtwork, "soaa": TagAlbumArtistSort, "\xa9too": TagEncoder,
		"pgap": TagGapless,
	} {
		if got := tagForMP4Atom(atomName, nil); got != want {
			t.Errorf("tagForMP4Atom(%q) = %v, want %v", MP4AtomName(atomName), got.Name(), want.Name())
		}
	}

	// A freeform item resolves through the name it carries.
	nameBody := append([]byte{0, 0, 0, 0}, "iTunNORM"...)
	item := append(atom("mean", append([]byte{0, 0, 0, 0}, "com.apple.iTunes"...)), atom("name", nameBody)...)
	if got := tagForMP4Atom("----", item); got != TagSoundCheck {
		t.Errorf("freeform iTunNORM resolved to %v, want soundcheck", got.Name())
	}
	if got := describeMP4Item("----", item); !strings.Contains(got, "iTunNORM") {
		t.Errorf("describeMP4Item = %q, want it to name the freeform atom", got)
	}
}

// TestRemovedTagSurvivesJSON guards the backup file.
//
// MP4 atom names begin with a byte that is not valid UTF-8, and Go's JSON
// encoder replaces invalid bytes rather than failing. A backup written without
// care therefore restores some tags and silently drops the rest, which an
// in-memory round-trip test would never notice.
func TestRemovedTagSurvivesJSON(t *testing.T) {
	dir := t.TempDir()
	path := genTagged(t, dir, "a.m4a", "-c:a", "aac")

	rep, err := StripFile(path, NewKeepSet(DefaultKeepTags), true)
	if err != nil {
		t.Fatalf("strip: %v", err)
	}
	if len(rep.Removed) == 0 {
		t.Fatal("nothing was removed")
	}
	// At least one removed atom must be one of Apple's, or the test proves
	// nothing about the encoding.
	sawApple := false
	for _, r := range rep.Removed {
		if strings.HasPrefix(r.Name, "©") {
			sawApple = true
		}
	}
	if !sawApple {
		t.Fatal("fixture removed no ©-prefixed atoms")
	}

	encoded, err := json.Marshal(rep.Removed)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded []RemovedTag
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for i := range decoded {
		if decoded[i].Name != rep.Removed[i].Name {
			t.Fatalf("name %q became %q through JSON", rep.Removed[i].Name, decoded[i].Name)
		}
		if !bytes.Equal(decoded[i].Raw, rep.Removed[i].Raw) {
			t.Fatalf("payload of %q did not survive JSON", decoded[i].Name)
		}
	}

	n, err := RestoreFile(path, decoded)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if n != len(decoded) {
		t.Errorf("restored %d of %d tags after a JSON round-trip", n, len(decoded))
	}
	decodes(t, path)

	md, err := NewReader().ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if md.Comment != "a real comment" {
		t.Errorf("comment = %q, want it restored through the backup format", md.Comment)
	}
}

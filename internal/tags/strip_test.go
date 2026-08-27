package tags

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// buildV23Frame assembles one ID3v2.3 frame: a four-character identifier, a
// four-byte big-endian size, then two flag bytes and the payload.
func buildV23Frame(id string, payload []byte) []byte {
	out := make([]byte, 0, 10+len(payload))
	out = append(out, id...)
	n := len(payload)
	out = append(out, byte(n>>24), byte(n>>16), byte(n>>8), byte(n), 0, 0)
	return append(out, payload...)
}

func v23Text(id, text string) []byte {
	return buildV23Frame(id, append([]byte{encISO8859}, text...))
}

// v23Comment builds a COMM frame with the given short description, which is
// how iTunes hides its private data among real comments.
func v23Comment(desc, text string) []byte {
	p := []byte{encISO8859}
	p = append(p, "eng"...)
	p = append(p, desc...)
	p = append(p, 0)
	p = append(p, text...)
	return buildV23Frame("COMM", p)
}

func buildV23Tag(frames ...[]byte) []byte {
	var body []byte
	for _, f := range frames {
		body = append(body, f...)
	}
	body = append(body, make([]byte, 64)...) // padding

	tag := make([]byte, 10, 10+len(body))
	copy(tag, "ID3")
	tag[3], tag[4], tag[5] = 3, 0, 0
	putSynchsafe(tag[6:10], len(body))
	return append(tag, body...)
}

// bareAudio returns an MP3 stream with no tag on the front.
func bareAudio(t *testing.T) []byte {
	t.Helper()
	ff := ffmpegPath(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "bare.mp3")
	cmd := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2", "-c:a", "libmp3lame",
		"-map_metadata", "-1", "-write_xing", "0", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	// ffmpeg writes its own encoder tag even with -map_metadata -1, so the
	// output is not actually bare until that tag is removed.
	if n := id3v2Size(b); n > 0 && n < len(b) {
		b = b[n:]
	}
	return b
}

// kitchenSink writes a file carrying one frame of every kind a strip has to
// decide about: keepers, junk, and the private data that hides inside COMM.
func kitchenSink(t *testing.T, dir string) string {
	t.Helper()
	pic := []byte{encISO8859}
	pic = append(pic, "image/jpeg"...)
	pic = append(pic, 0, 0x03, 0)
	pic = append(pic, fakeJPEG...)

	txxx := []byte{encISO8859}
	txxx = append(txxx, "MusicBrainz Album Id"...)
	txxx = append(txxx, 0)
	txxx = append(txxx, "c0ffee00-dead-beef-1234-000000000000"...)

	tag := buildV23Tag(
		// Keepers.
		v23Text("TIT2", "Hound Dog"),
		v23Text("TPE1", "Elvis Presley"),
		v23Text("TALB", "Sun Sessions"),
		v23Text("TPE2", "Various Artists"),
		v23Text("TRCK", "3/12"),
		v23Text("TPOS", "1/1"),
		v23Text("TCON", "Rock"),
		v23Text("TYER", "1956"),
		v23Text("TCMP", "1"),
		v23Text("TSOP", "Presley, Elvis"),
		v23Text("TSOA", "Sun Sessions"),
		v23Text("TSOT", "Hound Dog"),
		v23Text("TSO2", "Various Artists"),
		v23Text("TCOM", "Leiber/Stoller"),
		buildV23Frame("APIC", pic),
		// Not on the keep list.
		v23Text("TENC", "iTunes v4.9"),
		v23Text("TSSE", "LAME 3.99"),
		v23Text("TPUB", "RCA Victor"),
		v23Text("TSRC", "USRC17607839"),
		v23Comment("", "a real comment"),
		v23Comment("iTunSMPB", "00000000 00000210"),
		buildV23Frame("TXXX", txxx),
		buildV23Frame("PRIV", append([]byte("WM/Provider"), 0, 1, 2, 3)),
	)

	path := filepath.Join(dir, "kitchen.mp3")
	if err := os.WriteFile(path, append(tag, bareAudio(t)...), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func frameIDs(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tag, err := parseID3v2(raw)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	out := make([]string, 0, len(tag.frames))
	for _, f := range tag.frames {
		out = append(out, f.id)
	}
	return out
}

func has(ids []string, id string) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

func TestStripKeepsOnlyTheKeepList(t *testing.T) {
	path := kitchenSink(t, t.TempDir())
	keep := NewKeepSet(DefaultKeepTags)

	rep, err := StripFile(path, keep, true)
	if err != nil {
		t.Fatalf("strip: %v", err)
	}
	if !rep.Changed {
		t.Fatal("strip reported no change")
	}
	decodes(t, path)

	got := frameIDs(t, path)
	for _, want := range []string{
		"TIT2", "TPE1", "TALB", "TPE2", "TRCK", "TPOS", "TCON", "TYER",
		"TCMP", "TSOP", "TSOA", "TSOT", "TSO2", "TCOM", "APIC",
	} {
		if !has(got, want) {
			t.Errorf("%s was removed but is on the keep list", want)
		}
	}
	for _, unwanted := range []string{"TENC", "TSSE", "TPUB", "TSRC", "COMM", "TXXX", "PRIV"} {
		if has(got, unwanted) {
			t.Errorf("%s survived the strip", unwanted)
		}
	}
	if len(got) != 15 {
		t.Errorf("kept %d frames (%v), want 15", len(got), got)
	}

	// The metadata that matters must read back unchanged.
	md, err := NewReader().ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if md.Title != "Hound Dog" || md.Artist != "Elvis Presley" ||
		md.Album != "Sun Sessions" || md.AlbumArtist != "Various Artists" ||
		md.Genre != "Rock" || md.Year != 1956 || md.Track != 3 || md.TrackTotal != 12 {
		t.Errorf("metadata changed: %+v", md)
	}
	if !md.HasArt {
		t.Error("cover art was lost")
	}
}

// TestStripDryRunWritesNothing is the guarantee the default depends on.
func TestStripDryRunWritesNothing(t *testing.T) {
	path := kitchenSink(t, t.TempDir())
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	rep, err := StripFile(path, NewKeepSet(DefaultKeepTags), false)
	if err != nil {
		t.Fatalf("strip: %v", err)
	}
	if !rep.Changed || len(rep.Removed) == 0 {
		t.Fatal("dry run should still report what it would remove")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("a dry run modified the file")
	}
}

func TestStripRestoreRoundTrip(t *testing.T) {
	path := kitchenSink(t, t.TempDir())

	rep, err := StripFile(path, NewKeepSet(DefaultKeepTags), true)
	if err != nil {
		t.Fatalf("strip: %v", err)
	}
	removed := len(rep.Removed)
	if removed == 0 {
		t.Fatal("nothing was removed")
	}

	n, err := RestoreFile(path, rep.Removed)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if n != removed {
		t.Errorf("restored %d frames, want %d", n, removed)
	}
	decodes(t, path)

	got := frameIDs(t, path)
	for _, want := range []string{"TENC", "TSSE", "TPUB", "TSRC", "COMM", "TXXX", "PRIV"} {
		if !has(got, want) {
			t.Errorf("%s did not come back", want)
		}
	}
	// Both COMM frames must return: they differ by description, not by id.
	comms := 0
	for _, id := range got {
		if id == "COMM" {
			comms++
		}
	}
	if comms != 2 {
		t.Errorf("restored %d COMM frames, want 2", comms)
	}

	// Restoring twice must not duplicate anything.
	again, err := RestoreFile(path, rep.Removed)
	if err != nil {
		t.Fatalf("second restore: %v", err)
	}
	if again != 0 {
		t.Errorf("a second restore added %d frames, want 0", again)
	}
}

// TestStripUpgradesV22 covers the iTunes-era files that make up much of a
// library assembled over decades.
func TestStripUpgradesV22(t *testing.T) {
	path := writeV22File(t, t.TempDir())

	rep, err := StripFile(path, NewKeepSet(DefaultKeepTags), true)
	if err != nil {
		t.Fatalf("strip: %v", err)
	}
	if !rep.Upgraded {
		t.Error("the report should note the v2.2 to v2.3 rewrite")
	}
	decodes(t, path)

	md, err := NewReader().ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// v2.2 identifiers must be translated before the keep list is applied, or
	// every tag in the file would be treated as unknown and removed.
	if md.Title != "Original Title" || md.Artist != "Elvis Presley" ||
		md.Album != "Original Album" || md.Composer != "Arthur Crudup" ||
		md.Genre != "Rock" || md.Year != 1956 || md.Track != 3 {
		t.Errorf("v2.2 metadata did not survive the strip: %+v", md)
	}
	if !md.HasArt {
		t.Error("cover art was lost in the v2.2 strip")
	}
	for _, id := range frameIDs(t, path) {
		if len(id) != 4 {
			t.Errorf("frame %q still has a v2.2 identifier", id)
		}
	}
}

func TestStripNoTagAndNoChange(t *testing.T) {
	dir := t.TempDir()
	keep := NewKeepSet(DefaultKeepTags)

	// A file with no ID3v2 tag at all.
	bare := filepath.Join(dir, "bare.mp3")
	if err := os.WriteFile(bare, bareAudio(t), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := StripFile(bare, keep, true)
	if err != nil {
		t.Fatalf("strip: %v", err)
	}
	if !rep.NoTag || rep.Changed {
		t.Errorf("untagged file: %+v", rep)
	}

	// A file that already contains nothing but keepers must be left alone.
	clean := filepath.Join(dir, "clean.mp3")
	tag := buildV23Tag(v23Text("TIT2", "Hound Dog"), v23Text("TPE1", "Elvis Presley"))
	if err := os.WriteFile(clean, append(tag, bareAudio(t)...), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(clean)
	if err != nil {
		t.Fatal(err)
	}
	rep, err = StripFile(clean, keep, true)
	if err != nil {
		t.Fatalf("strip: %v", err)
	}
	if rep.Changed {
		t.Error("a file with only kept frames should not be reported as changed")
	}
	after, err := os.ReadFile(clean)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("a file with only kept frames was rewritten anyway")
	}
}

// TestParseKeepSet checks that the list can be written in either vocabulary.
func TestParseKeepSet(t *testing.T) {
	k, unknown := ParseKeepSet([]string{"title", " TPE1 ", "", "AlbumArtist", "aART"})
	if len(unknown) != 0 {
		t.Fatalf("unexpected unknown names: %v", unknown)
	}
	for _, want := range []Tag{TagTitle, TagArtist, TagAlbumArtist} {
		if !k[want] {
			t.Errorf("%s missing from %v", want.Name(), k.Sorted())
		}
	}
	if len(k) != 3 {
		t.Errorf("aART and AlbumArtist should resolve to one tag: %v", k.Sorted())
	}
	if _, unknown := ParseKeepSet([]string{"nonsense"}); len(unknown) != 1 {
		t.Error("an unknown name should be reported")
	}
}

// TestDescriptionsResolveSeparately is what lets gapless data be kept while
// ordinary comments are dropped: both live in COMM.
func TestDescriptionsResolveSeparately(t *testing.T) {
	cases := []struct {
		desc string
		want Tag
	}{
		{"iTunSMPB", TagGapless},
		{"iTunNORM", TagSoundCheck},
		{"REPLAYGAIN_TRACK_GAIN", TagReplayGain},
		{"MusicBrainz Album Id", TagMusicBrainz},
		{"Acoustid Id", TagAcoustID},
		{"ALBUMARTIST", TagAlbumArtist},
	}
	for _, c := range cases {
		got, ok := tagForDescription(c.desc)
		if !ok || got != c.want {
			t.Errorf("tagForDescription(%q) = %v, want %v", c.desc, got.Name(), c.want.Name())
		}
	}
	if _, ok := tagForDescription("something nobody has heard of"); ok {
		t.Error("an unrecognised description should not resolve")
	}

	// A plain comment must still resolve as a comment, not as unknown.
	p := append([]byte{encISO8859}, "eng"...)
	p = append(p, 0)
	p = append(p, "a real comment"...)
	if got := tagForID3Frame("COMM", p); got != TagComment {
		t.Errorf("plain COMM resolved to %v, want comment", got.Name())
	}
	gapless := append([]byte{encISO8859}, "eng"...)
	gapless = append(gapless, "iTunSMPB"...)
	gapless = append(gapless, 0, '0')
	if got := tagForID3Frame("COMM", gapless); got != TagGapless {
		t.Errorf("COMM:iTunSMPB resolved to %v, want gapless", got.Name())
	}
}

// TestKeepGaplessOnly checks the case the report is meant to make actionable:
// keeping iTunes gapless data without keeping every other comment.
func TestKeepGaplessOnly(t *testing.T) {
	path := kitchenSink(t, t.TempDir())
	keep := NewKeepSet(append(append([]Tag{}, DefaultKeepTags...), TagGapless))

	if _, err := StripFile(path, keep, true); err != nil {
		t.Fatalf("strip: %v", err)
	}
	decodes(t, path)

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tag, err := parseID3v2(raw)
	if err != nil {
		t.Fatal(err)
	}
	var descs []string
	for _, f := range tag.frames {
		if f.id == "COMM" {
			descs = append(descs, id3CommentDescription(f.payload))
		}
	}
	if len(descs) != 1 || descs[0] != "iTunSMPB" {
		t.Errorf("COMM frames left = %v, want only iTunSMPB", descs)
	}
}

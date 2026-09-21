package library

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/remy/yamo/internal/tags"
)

// transcodeService scans one hi-res FLAC, tagged through this library's own
// writer so that every field is set, and turns transcoding on.
func transcodeService(t *testing.T) (*Service, string) {
	t.Helper()
	ff := ffmpegOrSkip(t)
	ffmpeg, err := CheckFFmpeg(ff)
	if err != nil {
		t.Skipf("ffmpeg cannot transcode here: %v", err)
	}

	root := t.TempDir()
	music := filepath.Join(root, "music")
	if err := os.MkdirAll(music, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(music, "01 Blue Moon.flac")
	if b, err := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2:sample_rate=96000",
		"-c:a", "flac", src).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, b)
	}
	cover := filepath.Join(root, "cover.jpg")
	if b, err := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "color=c=blue:s=64x64", "-frames:v", "1", cover).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg cover: %v\n%s", err, b)
	}
	art, err := os.ReadFile(cover)
	if err != nil {
		t.Fatal(err)
	}

	s32 := func(v string) *string { return &v }
	n32 := func(v int32) *int32 { return &v }
	yes := true
	e := &tags.Edit{
		Title: s32("Blue Moon"), Artist: s32("Elvis Presley"),
		AlbumArtist: s32("Various Artists"), Album: s32("Sun Sessions"),
		Genre: s32("Rockabilly"), Composer: s32("Rodgers & Hart"),
		Comment: s32("take 3"), ArtistSort: s32("Presley, Elvis"),
		AlbumArtistSort: s32("Various"), Year: n32(1954),
		Track: n32(1), TrackTotal: n32(16), Disc: n32(1), DiscTotal: n32(2),
		Compilation: &yes,
	}
	e.SetArtwork([]tags.Picture{{Kind: tags.PictureFrontCover, MIME: "image/jpeg", Data: art}})
	if err := tags.Write(src, e); err != nil {
		t.Fatal(err)
	}

	s, err := Open(Options{
		CatalogPath:  filepath.Join(root, "catalog.db"),
		SaveInterval: 50 * time.Millisecond,
		FFmpeg:       ffmpeg,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	job, err := s.Scan(ScanRequest{Roots: []string{music}})
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, s, job.ID)
	return s, TrackID(src)
}

// The result has to be something an iPod will take as it stands: AAC, at a
// sample rate it plays, carrying every tag the source had — including the
// ones ffmpeg's own mapping would have dropped — and still decoding.
func TestTranscodeToAAC(t *testing.T) {
	s, id := transcodeService(t)

	out, err := s.Transcode(context.Background(), id, TranscodeRequest{As: "AAC", Bitrate: 128})
	if err != nil {
		t.Fatal(err)
	}
	path := out.File.Name()
	if out.Name != "01 Blue Moon.m4a" || out.MIME != "audio/mp4" {
		t.Errorf("name %q, mime %q", out.Name, out.MIME)
	}
	if !strings.HasSuffix(out.ETag, "-aac128") {
		t.Errorf("ETag %q does not name the settings", out.ETag)
	}
	if etag, _ := s.TranscodeETag(id, TranscodeRequest{As: "aac", Bitrate: 128}); etag != out.ETag {
		t.Errorf("TranscodeETag %q, but the rendition says %q", etag, out.ETag)
	}

	md, err := tags.NewReader().ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if md.Format != tags.FormatMP4 {
		t.Fatalf("format %v, want mp4", md.Format)
	}
	checks := []struct{ name, got, want string }{
		{"title", md.Title, "Blue Moon"},
		{"artist", md.Artist, "Elvis Presley"},
		{"albumartist", md.AlbumArtist, "Various Artists"},
		{"album", md.Album, "Sun Sessions"},
		{"genre", md.Genre, "Rockabilly"},
		{"composer", md.Composer, "Rodgers & Hart"},
		{"comment", md.Comment, "take 3"},
		{"artistsort", md.ArtistSort, "Presley, Elvis"},
		{"albumartistsort", md.AlbumArtistSort, "Various"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
	if md.Year != 1954 || md.Track != 1 || md.TrackTotal != 16 || md.Disc != 1 || md.DiscTotal != 2 {
		t.Errorf("numbers: year %d track %d/%d disc %d/%d", md.Year, md.Track, md.TrackTotal, md.Disc, md.DiscTotal)
	}
	if !md.Compilation {
		t.Error("the compilation flag was lost")
	}
	if !md.HasArt {
		t.Error("the cover was lost")
	}
	// 96kHz is more than an iPod plays.
	if md.SampleRate != 44100 {
		t.Errorf("sample rate %d, want 44100", md.SampleRate)
	}
	if md.DurationMS < 1900 || md.DurationMS > 2200 {
		t.Errorf("duration %dms, want about 2000", md.DurationMS)
	}

	ff := ffmpegOrSkip(t)
	if b, err := exec.Command(ff, "-hide_banner", "-v", "error", "-i", path,
		"-map", "0:a:0", "-f", "null", "-").CombinedOutput(); err != nil || len(b) > 0 {
		t.Fatalf("the result does not decode cleanly: %v\n%s", err, b)
	}

	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the temporary file outlived Close: %v", err)
	}
}

func TestTranscodeRefusals(t *testing.T) {
	s, id := transcodeService(t)
	ctx := context.Background()

	for _, req := range []TranscodeRequest{
		{As: "wav"},
		{As: ""},
		{As: "aac", Bitrate: 64},
		{As: "aac", Bitrate: 1000},
	} {
		if _, err := s.Transcode(ctx, id, req); !errors.Is(err, ErrBadRequest) {
			t.Errorf("%+v: %v, want ErrBadRequest", req, err)
		}
	}
	if _, err := s.Transcode(ctx, "nosuchtrack", TranscodeRequest{As: "aac"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown track: %v, want ErrNotFound", err)
	}

	// A client that has gone away does not get an encode.
	gone, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.Transcode(gone, id, TranscodeRequest{As: "aac"}); err == nil {
		t.Error("a cancelled request was transcoded anyway")
	}

	off, err := Open(Options{CatalogPath: filepath.Join(t.TempDir(), "catalog.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer off.Close()
	if _, err := off.Transcode(ctx, id, TranscodeRequest{As: "aac"}); !errors.Is(err, ErrNoTranscode) {
		t.Errorf("without ffmpeg: %v, want ErrNoTranscode", err)
	}
	if c := off.Capabilities(Serving{}); c.Features.Transcode || len(c.TranscodeFormats) != 0 {
		t.Errorf("capabilities claim transcoding without ffmpeg: %+v %v", c.Features, c.TranscodeFormats)
	}
	if c := s.Capabilities(Serving{}); !c.Features.Transcode || len(c.TranscodeFormats) != 1 {
		t.Errorf("capabilities do not report transcoding: %+v %v", c.Features, c.TranscodeFormats)
	}
}

func TestHasEncoder(t *testing.T) {
	list := []byte(` A..... = Audio
 ------
 A....D aac                  AAC (Advanced Audio Coding)
 A..... aac_at               aac (AudioToolbox) (codec aac)
 V....D libx264              H.264
`)
	if !hasEncoder(list, "aac") {
		t.Error("aac not found")
	}
	if hasEncoder(list, "libx264") || hasEncoder(list, "libmp3lame") {
		t.Error("found an encoder that is not an audio one in the list")
	}
	if hasEncoder([]byte(" A..... aac_at  AudioToolbox\n"), "aac") {
		t.Error("aac_at was taken for aac")
	}
}

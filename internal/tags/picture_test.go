package tags

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// makeImage produces a real encodable image, so the dimension measuring is
// exercised against something a decoder actually accepts.
func makeImage(t *testing.T, w, h int, asPNG bool) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 0x80, A: 0xFF})
		}
	}
	var buf bytes.Buffer
	var err error
	if asPNG {
		err = png.Encode(&buf, img)
	} else {
		err = jpeg.Encode(&buf, img, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestNewPictureMeasures(t *testing.T) {
	for _, c := range []struct {
		name  string
		asPNG bool
		mime  string
		ext   string
	}{
		{"jpeg", false, "image/jpeg", ".jpg"},
		{"png", true, "image/png", ".png"},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, err := NewPicture(makeImage(t, 120, 90, c.asPNG))
			if err != nil {
				t.Fatalf("NewPicture: %v", err)
			}
			if p.Width != 120 || p.Height != 90 {
				t.Errorf("measured %d×%d, want 120×90", p.Width, p.Height)
			}
			if p.MIME != c.mime {
				t.Errorf("mime = %q, want %q", p.MIME, c.mime)
			}
			if p.Ext() != c.ext {
				t.Errorf("ext = %q, want %q", p.Ext(), c.ext)
			}
			if p.Kind != PictureFrontCover {
				t.Errorf("kind = %v, want front cover", p.Kind)
			}
		})
	}
	if _, err := NewPicture([]byte("not an image at all")); err == nil {
		t.Error("NewPicture accepted something that is not an image")
	}
	if _, err := NewPicture(nil); err == nil {
		t.Error("NewPicture accepted an empty image")
	}
}

// TestPictureCodecs round-trips a picture through each container's encoding.
func TestPictureCodecs(t *testing.T) {
	src, err := NewPicture(makeImage(t, 64, 48, false))
	if err != nil {
		t.Fatal(err)
	}
	src.Description = "Front"

	t.Run("apic", func(t *testing.T) {
		got, ok := parseAPIC(encodeAPIC(src))
		if !ok {
			t.Fatal("parseAPIC rejected its own output")
		}
		checkPicture(t, got, src)
		if got.Description != "Front" {
			t.Errorf("description = %q", got.Description)
		}
	})

	t.Run("flac", func(t *testing.T) {
		got, ok := parseFLACPicture(encodeFLACPicture(src))
		if !ok {
			t.Fatal("parseFLACPicture rejected its own output")
		}
		checkPicture(t, got, src)
		// FLAC records the dimensions itself rather than inferring them.
		if got.Width != src.Width || got.Height != src.Height {
			t.Errorf("dimensions = %d×%d, want %d×%d", got.Width, got.Height, src.Width, src.Height)
		}
	})

	t.Run("vorbis", func(t *testing.T) {
		got, ok := parseVorbisPicture(encodeVorbisPicture(src))
		if !ok {
			t.Fatal("parseVorbisPicture rejected its own output")
		}
		checkPicture(t, got, src)
	})

	t.Run("mp4", func(t *testing.T) {
		got, ok := parseMP4Cover(encodeMP4Cover(src))
		if !ok {
			t.Fatal("parseMP4Cover rejected its own output")
		}
		checkPicture(t, got, src)
	})
}

func checkPicture(t *testing.T, got, want *Picture) {
	t.Helper()
	if !bytes.Equal(got.Data, want.Data) {
		t.Errorf("image data changed: %d bytes became %d", len(want.Data), len(got.Data))
	}
	if got.MIME != want.MIME {
		t.Errorf("mime = %q, want %q", got.MIME, want.MIME)
	}
	if got.Kind != want.Kind {
		t.Errorf("kind = %v, want %v", got.Kind, want.Kind)
	}
}

// TestArtworkRoundTripAcrossFormats writes a cover into every writable
// container and reads it back out.
func TestArtworkRoundTripAcrossFormats(t *testing.T) {
	cases := []struct {
		name string
		file string
		args []string
	}{
		{"mp3", "a.mp3", []string{"-c:a", "libmp3lame"}},
		{"flac", "b.flac", []string{"-c:a", "flac"}},
		{"m4a", "c.m4a", []string{"-c:a", "aac"}},
		{"ogg", "d.ogg", []string{"-c:a", "libvorbis"}},
		{"opus", "e.opus", []string{"-c:a", "libopus"}},
	}
	cover := makeImage(t, 300, 300, false)
	replacement := makeImage(t, 150, 200, true)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := genFile(t, dir, tc.file, tc.args...)

			if _, err := ReadCover(path); err == nil {
				t.Fatal("fixture unexpectedly already has a cover")
			}

			// Add one.
			p, err := NewPicture(cover)
			if err != nil {
				t.Fatal(err)
			}
			e := &Edit{}
			e.SetArtwork([]Picture{*p})
			if err := Write(path, e); err != nil {
				t.Fatalf("write artwork: %v", err)
			}
			decodes(t, path)

			got, err := ReadCover(path)
			if err != nil {
				t.Fatalf("read cover: %v", err)
			}
			if !bytes.Equal(got.Data, cover) {
				t.Fatalf("cover data changed: wrote %d bytes, read %d", len(cover), len(got.Data))
			}
			if got.Width != 300 || got.Height != 300 {
				t.Errorf("dimensions = %d×%d, want 300×300", got.Width, got.Height)
			}
			if got.MIME != "image/jpeg" {
				t.Errorf("mime = %q", got.MIME)
			}

			// Tags must be untouched by an artwork write.
			md, err := NewReader().ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if md.Artist != "Elvis Presley" || md.Album != "Original Album" {
				t.Errorf("artwork write disturbed the tags: %+v", md)
			}
			if !md.HasArt {
				t.Error("HasArt is false after embedding a cover")
			}

			// Replace it, with a different format and size.
			p2, err := NewPicture(replacement)
			if err != nil {
				t.Fatal(err)
			}
			e2 := &Edit{}
			e2.SetArtwork([]Picture{*p2})
			if err := Write(path, e2); err != nil {
				t.Fatalf("replace artwork: %v", err)
			}
			decodes(t, path)

			pics, err := ReadPictures(path)
			if err != nil {
				t.Fatalf("read after replace: %v", err)
			}
			if len(pics) != 1 {
				t.Fatalf("found %d images after replacing one, want 1", len(pics))
			}
			if !bytes.Equal(pics[0].Data, replacement) {
				t.Error("the replacement image did not round-trip")
			}
			if pics[0].MIME != "image/png" {
				t.Errorf("mime after replace = %q, want image/png", pics[0].MIME)
			}

			// And remove it.
			e3 := &Edit{}
			e3.RemoveArtwork()
			if err := Write(path, e3); err != nil {
				t.Fatalf("remove artwork: %v", err)
			}
			decodes(t, path)
			if pics, err := ReadPictures(path); err == nil && len(pics) > 0 {
				t.Errorf("%d images survived removal", len(pics))
			}
			md, err = NewReader().ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if md.HasArt {
				t.Error("HasArt is still true after removing the cover")
			}
			if md.Artist != "Elvis Presley" {
				t.Errorf("removing artwork disturbed the tags: artist = %q", md.Artist)
			}
		})
	}
}

// TestReadCoverPrefersFrontCover checks the selection rule.
func TestReadCoverPrefersFrontCover(t *testing.T) {
	back := Picture{Kind: PictureBackCover, MIME: "image/jpeg", Data: []byte("back")}
	front := Picture{Kind: PictureFrontCover, MIME: "image/jpeg", Data: []byte("front")}

	got, err := PickCover([]Picture{back, front})
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Data) != "front" {
		t.Errorf("picked %q, want the front cover", got.Data)
	}
	// With nothing marked as a front cover, the first image is used.
	got, err = PickCover([]Picture{back})
	if err != nil || string(got.Data) != "back" {
		t.Errorf("picked %v, %v", got, err)
	}
	if _, err := PickCover(nil); err == nil {
		t.Error("PickCover accepted an empty set")
	}
}

// TestUnsupportedFormatArtwork checks that formats without a writer say so.
func TestUnsupportedFormatArtwork(t *testing.T) {
	dir := t.TempDir()
	path := genFile(t, dir, "a.wav", "-c:a", "pcm_s16le")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	e := &Edit{}
	e.SetArtwork([]Picture{{MIME: "image/jpeg", Data: makeImage(t, 10, 10, false)}})
	if err := Write(path, e); err == nil {
		t.Error("writing artwork to a WAV should fail rather than appear to work")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("the file was modified despite the write failing")
	}
	_ = filepath.Base(path)
}

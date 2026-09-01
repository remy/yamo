package library

import (
	"bytes"
	"image"
	"testing"

	"github.com/remy/yamo/internal/tags"
)

func TestThumbnailScalesDown(t *testing.T) {
	pic, err := tags.NewPicture(testJPEG(t, 1200, 1200))
	if err != nil {
		t.Fatal(err)
	}
	out, err := thumbnail(pic, 160)
	if err != nil {
		t.Fatal(err)
	}
	if out.Width != 160 || out.Height != 160 {
		t.Errorf("thumbnail is %d×%d, want 160×160", out.Width, out.Height)
	}
	if len(out.Data) >= len(pic.Data) {
		t.Errorf("the thumbnail is %d bytes and the original %d, which defeats the point",
			len(out.Data), len(pic.Data))
	}
	// It must still decode as an image, at the size it claims.
	cfg, _, err := image.DecodeConfig(bytes.NewReader(out.Data))
	if err != nil {
		t.Fatalf("the thumbnail does not decode: %v", err)
	}
	if cfg.Width != 160 || cfg.Height != 160 {
		t.Errorf("decoded %d×%d, but the picture claims %d×%d", cfg.Width, cfg.Height, out.Width, out.Height)
	}
}

// A cover is not always square, and squashing one to fit would be worse than
// not scaling it at all.
func TestThumbnailKeepsAspect(t *testing.T) {
	pic, err := tags.NewPicture(testJPEG(t, 1000, 500))
	if err != nil {
		t.Fatal(err)
	}
	out, err := thumbnail(pic, 200)
	if err != nil {
		t.Fatal(err)
	}
	if out.Width != 200 || out.Height != 100 {
		t.Errorf("thumbnail is %d×%d, want 200×100", out.Width, out.Height)
	}
}

// Enlarging costs bytes to add nothing, so an image already small enough
// comes back exactly as it was.
func TestThumbnailDoesNotEnlarge(t *testing.T) {
	pic, err := tags.NewPicture(testPNGSize(t, 64, 64))
	if err != nil {
		t.Fatal(err)
	}
	out, err := thumbnail(pic, 512)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Data, pic.Data) {
		t.Error("a small image was re-encoded rather than returned as it was")
	}
}

// The cache is keyed by the track's version, so an edit that rewrites the file
// must not serve the cover the file used to have.
func TestThumbnailCacheFollowsVersion(t *testing.T) {
	s, _ := realService(t, 1)
	id := s.matchIDs("")[0]

	first, err := tags.NewPicture(testJPEG(t, 400, 400))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetArtwork(id, first, ""); err != nil {
		t.Fatal(err)
	}
	before, err := s.Thumbnail(id, 64)
	if err != nil {
		t.Fatal(err)
	}

	// A different cover: same dimensions, different content.
	second, err := tags.NewPicture(testPNGSize(t, 400, 400))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetArtwork(id, second, ""); err != nil {
		t.Fatal(err)
	}
	after, err := s.Thumbnail(id, 64)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(before.Data, after.Data) {
		t.Error("the thumbnail did not change when the cover did")
	}
}

func TestThumbnailBounds(t *testing.T) {
	s, _ := realService(t, 1)
	id := s.matchIDs("")[0]
	for _, size := range []int{0, -1, MinThumbSize - 1, MaxThumbSize + 1} {
		if _, err := s.Thumbnail(id, size); err == nil {
			t.Errorf("Thumbnail(%d) was accepted", size)
		}
	}
}

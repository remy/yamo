package library

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

// Test images, made here rather than by ffmpeg.
//
// The tag tests need real encoder output because what they check is that a
// real file survives a round trip. An image only has to decode, so the
// standard library's own encoders are enough — and they run where ffmpeg is
// not installed, which keeps the artwork tests from silently skipping.

// testImage builds a w×h image with a gradient, so a scaled copy differs
// visibly from a flat colour and an averaging bug shows up as a wrong pixel
// rather than the same pixel either way.
func testImage(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{
				R: uint8(x * 255 / max(w-1, 1)),
				G: uint8(y * 255 / max(h-1, 1)),
				B: 0x40,
				A: 0xff,
			})
		}
	}
	return img
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	return testPNGSize(t, 64, 64)
}

func testPNGSize(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, testImage(w, h)); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func testJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, testImage(w, h), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

package library

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"sync"

	"github.com/remy/yamo/internal/tags"
)

// Thumbnails.
//
// An album grid asks for one cover per tile, and an embedded cover on a real
// library is routinely 1500×1500 and half a megabyte. Forty of those is twenty
// megabytes to draw forty postage stamps, which over a phone's connection is
// the difference between a grid that appears and one that does not.
//
// The scaling is done here rather than in the client for the same reason the
// Discogs download is: the client cannot avoid paying for the bytes it did not
// want. It is a box filter, which is the right choice for the ratios involved —
// a large downscale averages away the aliasing that a nearest-neighbour or
// bilinear sample leaves behind, and costs one pass over the source.

// Thumbnail bounds. Below the minimum an image is not a cover any more, and
// above the maximum a client should be asking for the original.
const (
	MinThumbSize = 16
	MaxThumbSize = 1024

	// thumbQuality is the JPEG quality thumbnails are written at. At these
	// sizes 82 is indistinguishable from 95 and about half the bytes.
	thumbQuality = 82
)

// thumbCache holds recently scaled covers.
//
// A grid asks for the same forty images every time it is scrolled back to, and
// scaling a cover costs a decode of the full-size original. The cache is keyed
// by the track's version and the size asked for, so an edit that rewrites the
// file produces a different key and the stale thumbnail is never served.
type thumbCache struct {
	mu    sync.Mutex
	items map[string][]byte
	order []string
}

// thumbCacheEntries is how many thumbnails are kept. A grid page is a few
// dozen; this covers scrolling back and forth over several screens without
// holding a library's worth of images.
const thumbCacheEntries = 256

func newThumbCache() *thumbCache {
	return &thumbCache{items: map[string][]byte{}}
}

func (c *thumbCache) get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.items[key]
	return b, ok
}

func (c *thumbCache) put(key string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, seen := c.items[key]; seen {
		return
	}
	c.items[key] = data
	c.order = append(c.order, key)
	// Oldest first rather than least recently used: a grid reads each cover
	// once per pass, so recency of use says little and insertion order is
	// exactly the scroll position moving on.
	if len(c.order) > thumbCacheEntries {
		delete(c.items, c.order[0])
		c.order = c.order[1:]
	}
}

// Thumbnail returns a track's cover scaled so its longest side is at most
// size, encoded as JPEG.
//
// An image already smaller than the requested size is returned as it is rather
// than scaled up: enlarging it would cost bytes to add nothing.
func (s *Service) Thumbnail(id string, size int) (*tags.Picture, error) {
	if size < MinThumbSize || size > MaxThumbSize {
		return nil, fmt.Errorf("%w: a thumbnail must be between %d and %d pixels across",
			ErrBadRequest, MinThumbSize, MaxThumbSize)
	}

	t, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	key := fmt.Sprintf("%s@%d", t.Version, size)
	if data, ok := s.thumbs.get(key); ok {
		p, err := tags.NewPicture(data)
		if err == nil {
			return p, nil
		}
	}

	pic, err := s.Artwork(id)
	if err != nil {
		return nil, err
	}
	out, err := thumbnail(pic, size)
	if err != nil {
		return nil, err
	}
	s.thumbs.put(key, out.Data)
	return out, nil
}

// thumbnail scales one picture down to fit a box.
func thumbnail(pic *tags.Picture, size int) (*tags.Picture, error) {
	if pic == nil || len(pic.Data) == 0 {
		return nil, tags.ErrNoPicture
	}
	// Already small enough. Re-encoding would cost a decode and a generation
	// of JPEG quality to produce something no smaller.
	if pic.Width > 0 && pic.Height > 0 && pic.Width <= size && pic.Height <= size {
		return pic, nil
	}

	src, _, err := image.Decode(bytes.NewReader(pic.Data))
	if err != nil {
		// A cover in a format this build cannot decode — WebP is the one that
		// turns up — is still a perfectly good cover. Handing back the
		// original is better than failing the request.
		return pic, nil
	}

	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("library: the cover measures %d×%d", w, h)
	}
	if w <= size && h <= size {
		return pic, nil
	}
	dw, dh := w, h
	if w >= h {
		dw, dh = size, max(1, h*size/w)
	} else {
		dw, dh = max(1, w*size/h), size
	}

	dst := boxScale(src, dw, dh)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: thumbQuality}); err != nil {
		return nil, err
	}
	return &tags.Picture{
		Kind: pic.Kind, MIME: "image/jpeg", Description: pic.Description,
		Width: dw, Height: dh, Data: buf.Bytes(),
	}, nil
}

// boxScale averages each destination pixel over the source pixels it covers.
//
// The source is drawn into an RGBA image first. Decoders return YCbCr for a
// JPEG and paletted for some PNGs, and going through At() on those costs a
// colour conversion per sample — which at a few million samples is most of the
// work. One conversion pass up front makes the averaging a slice read.
func boxScale(src image.Image, dw, dh int) *image.RGBA {
	b := src.Bounds()
	rgba, ok := src.(*image.RGBA)
	if !ok || rgba.Bounds() != b {
		rgba = image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
		draw.Draw(rgba, rgba.Bounds(), src, b.Min, draw.Src)
	}
	sw, sh := rgba.Bounds().Dx(), rgba.Bounds().Dy()

	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		y0, y1 := y*sh/dh, (y+1)*sh/dh
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < dw; x++ {
			x0, x1 := x*sw/dw, (x+1)*sw/dw
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var r, g, bl, a, n uint32
			for sy := y0; sy < y1; sy++ {
				row := rgba.Pix[sy*rgba.Stride:]
				for sx := x0; sx < x1; sx++ {
					p := row[sx*4:]
					r += uint32(p[0])
					g += uint32(p[1])
					bl += uint32(p[2])
					a += uint32(p[3])
					n++
				}
			}
			o := dst.PixOffset(x, y)
			dst.Pix[o] = uint8(r / n)
			dst.Pix[o+1] = uint8(g / n)
			dst.Pix[o+2] = uint8(bl / n)
			dst.Pix[o+3] = uint8(a / n)
		}
	}
	return dst
}

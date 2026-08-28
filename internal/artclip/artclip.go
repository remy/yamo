// Package artclip stores one image between a copy and a paste.
//
// It persists to disk rather than living in memory so that a cover yanked in
// the browser can be pasted by a later command, and one loaded from a file on
// the command line can be pasted in the browser. That is the whole point of a
// clipboard: the two halves of the operation happen at different times, and
// often in different processes.
package artclip

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/remy/tag-manager/internal/tags"
)

// Clip is the held image plus a note of where it came from.
type Clip struct {
	Picture tags.Picture
	Source  string    // the file or track it was copied from
	Copied  time.Time // when
}

// ErrEmpty means nothing has been copied yet.
var ErrEmpty = errors.New("artclip: nothing copied")

// meta is the sidecar written next to the image bytes. The image itself is
// kept as a plain file so it can be opened by anything else on the machine.
type meta struct {
	Kind        uint8     `json:"kind"`
	MIME        string    `json:"mime"`
	Description string    `json:"description,omitempty"`
	Width       int       `json:"width"`
	Height      int       `json:"height"`
	Depth       int       `json:"depth,omitempty"`
	Source      string    `json:"source"`
	Copied      time.Time `json:"copied"`
}

// Store is a clipboard rooted at a directory.
type Store struct{ dir string }

// New returns the clipboard for the given directory, which is created lazily.
func New(dir string) *Store { return &Store{dir: dir} }

// DefaultDir is where the clipboard lives when none is given: beside the
// catalogue, since both are derived state rather than anything to back up.
func DefaultDir(catalogPath string) string {
	if catalogPath == "" {
		return "."
	}
	return filepath.Join(filepath.Dir(catalogPath), "clipboard")
}

func (s *Store) imagePath(ext string) string { return filepath.Join(s.dir, "cover"+ext) }
func (s *Store) metaPath() string            { return filepath.Join(s.dir, "cover.json") }

// Copy replaces the clipboard contents.
func (s *Store) Copy(p *tags.Picture, source string) error {
	if p == nil || len(p.Data) == 0 {
		return errors.New("artclip: no image to copy")
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	// Remove any previous image first: the extension may differ, and two
	// stale covers in the directory would make the next read ambiguous.
	s.clearImages()

	if err := os.WriteFile(s.imagePath(p.Ext()), p.Data, 0o644); err != nil {
		return err
	}
	m := meta{
		Kind: uint8(p.Kind), MIME: p.MIME, Description: p.Description,
		Width: p.Width, Height: p.Height, Depth: p.Depth,
		Source: source, Copied: time.Now(),
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.metaPath(), b, 0o644)
}

// CopyFile loads an image file into the clipboard.
func (s *Store) CopyFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	p, err := tags.NewPicture(data)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return s.Copy(p, path)
}

// Paste returns the held image.
func (s *Store) Paste() (*Clip, error) {
	b, err := os.ReadFile(s.metaPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrEmpty
		}
		return nil, err
	}
	var m meta
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	p := tags.Picture{
		Kind: tags.PictureKind(m.Kind), MIME: m.MIME, Description: m.Description,
		Width: m.Width, Height: m.Height, Depth: m.Depth,
	}
	data, err := os.ReadFile(s.imagePath(p.Ext()))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrEmpty
		}
		return nil, err
	}
	p.Data = data
	return &Clip{Picture: p, Source: m.Source, Copied: m.Copied}, nil
}

// Clear empties the clipboard.
func (s *Store) Clear() error {
	s.clearImages()
	if err := os.Remove(s.metaPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// clearImages removes any stored image regardless of its extension.
func (s *Store) clearImages() {
	for _, ext := range []string{".jpg", ".png", ".gif", ".bmp", ".webp"} {
		os.Remove(s.imagePath(ext))
	}
}

package library

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/remy/tag-manager/internal/tags"
)

// coverNames are the filenames album art conventionally goes by, in the order
// they are preferred.
var coverNames = []string{
	"cover", "folder", "front", "album", "albumart", "artwork", "thumb",
}

var coverExts = []string{".jpg", ".jpeg", ".png", ".webp", ".gif", ".bmp"}

// ArtworkSource says where a batch artwork operation gets its image.
type ArtworkSource string

const (
	// ArtFromClipboard uses the server-side clipboard, so a cover copied on
	// one client can be pasted from another.
	ArtFromClipboard ArtworkSource = "clipboard"

	// ArtFromUpload uses an image supplied with the request.
	ArtFromUpload ArtworkSource = "upload"

	// ArtFromFolder uses the cover.jpg or folder.jpg beside each track. This
	// is how a downloaded library usually stores art, and the usual reason
	// none of it shows up on a phone.
	ArtFromFolder ArtworkSource = "folder"

	// ArtRemove takes the artwork off.
	ArtRemove ArtworkSource = "remove"
)

// Artwork returns a track's cover.
func (s *Service) Artwork(id string) (*tags.Picture, error) {
	path, err := s.Path(id)
	if err != nil {
		return nil, err
	}
	return tags.ReadCover(path)
}

// SetArtwork replaces one track's cover, or removes it when pic is nil.
func (s *Service) SetArtwork(id string, pic *tags.Picture) error {
	changed, err := s.setArtwork(id, pic, false)
	if err != nil {
		return err
	}
	if changed {
		s.markDirty()
		s.events.publish(Event{Type: EventTracksChanged, TrackIDs: []string{id}})
	}
	return nil
}

// setArtwork writes one track's cover and reports whether the file changed.
func (s *Service) setArtwork(id string, pic *tags.Picture, dryRun bool) (bool, error) {
	s.mu.RLock()
	i, ok := s.lookupLocked(id)
	if !ok {
		s.mu.RUnlock()
		return false, ErrNotFound
	}
	cur := s.cat.Tracks[i]
	s.mu.RUnlock()

	if !cur.Format.Writable() {
		return false, fmt.Errorf("library: %s files cannot be written by this build", cur.Format)
	}

	if pic == nil {
		if !cur.HasArt {
			return false, nil
		}
	} else if cur.HasArt {
		// Re-embedding identical bytes would rewrite the file for nothing,
		// which across a library is the difference between minutes and hours.
		if existing, err := tags.ReadCover(cur.Path); err == nil && bytesEqual(existing.Data, pic.Data) {
			return false, nil
		}
	}
	if dryRun {
		return true, nil
	}

	e := &tags.Edit{}
	if pic == nil {
		e.RemoveArtwork()
	} else {
		e.SetArtwork([]tags.Picture{*pic})
	}
	if err := s.locks.withPath(cur.Path, func() error { return tags.Write(cur.Path, e) }); err != nil {
		return false, err
	}

	size, modTime := cur.Size, cur.ModTime
	if fi, serr := os.Stat(cur.Path); serr == nil {
		size, modTime = fi.Size(), fi.ModTime().Unix()
	}
	s.mu.Lock()
	if j, ok := s.lookupLocked(id); ok {
		t := &s.cat.Tracks[j]
		t.HasArt = pic != nil
		t.Size, t.ModTime = size, modTime
		s.cat.Touch(int(j))
	}
	s.mu.Unlock()
	return true, nil
}

// BatchArtworkRequest applies one cover across many tracks.
type BatchArtworkRequest struct {
	Selector Selector      `json:"selector"`
	Source   ArtworkSource `json:"source"`
	Image    []byte        `json:"-"` // supplied out of band for uploads
	DryRun   bool          `json:"dryRun,omitempty"`
}

// BatchArtwork starts a job that sets or clears artwork across a selection.
func (s *Service) BatchArtwork(req BatchArtworkRequest) (*Job, error) {
	ids, err := s.Resolve(req.Selector)
	if err != nil {
		return nil, err
	}

	// Resolve the image once, up front, so a bad clipboard or an unreadable
	// upload fails immediately rather than after a thousand files.
	var fixed *tags.Picture
	switch req.Source {
	case ArtFromClipboard:
		held, err := s.clip.Paste()
		if err != nil {
			return nil, err
		}
		fixed = &held.Picture
	case ArtFromUpload:
		p, err := tags.NewPicture(req.Image)
		if err != nil {
			return nil, err
		}
		fixed = p
	case ArtFromFolder, ArtRemove:
		// Resolved per track, or nothing to resolve.
	default:
		return nil, fmt.Errorf("library: unknown artwork source %q", req.Source)
	}

	folders := newFolderCache()
	return s.jobs.Start(JobArtwork, func(ctx context.Context, j *Job) (any, error) {
		res := BatchResult{Matched: len(ids), DryRun: req.DryRun}
		j.SetProgress(Progress{Total: int64(len(ids))})

		var touched []string
		for n, id := range ids {
			if ctx.Err() != nil {
				break
			}
			pic := fixed
			if req.Source == ArtFromFolder {
				path, err := s.Path(id)
				if err != nil {
					res.Skipped++
					continue
				}
				if pic = folders.find(filepath.Dir(path)); pic == nil {
					res.Skipped++ // no folder art beside this track
					continue
				}
			}

			changed, err := s.setArtwork(id, pic, req.DryRun)
			switch {
			case errors.Is(err, ErrNotFound):
				res.Skipped++
			case err != nil:
				path, _ := s.Path(id)
				res.fail(id, path, err)
			case changed:
				res.Changed++
				touched = append(touched, id)
			default:
				res.Skipped++
			}
			if n%16 == 0 || n == len(ids)-1 {
				j.SetProgress(Progress{Done: int64(n + 1), Total: int64(len(ids))})
			}
		}

		if len(touched) > 0 {
			s.markDirty()
			s.events.publish(Event{Type: EventTracksChanged, TrackIDs: touched})
		}
		return res, ctx.Err()
	}), nil
}

// ArtworkGroup is one distinct cover and how widely it is used.
type ArtworkGroup struct {
	Hash       string `json:"hash"`
	Summary    string `json:"summary"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
	MIME       string `json:"mime,omitempty"`
	Tracks     int    `json:"tracks"`
	Bytes      int64  `json:"bytes"`
	SampleID   string `json:"sampleTrackId"`
	ExampleDir string `json:"exampleAlbum,omitempty"`
}

// ArtworkReport summarises the artwork across a selection.
type ArtworkReport struct {
	Groups      []ArtworkGroup `json:"groups"`
	Tracks      int            `json:"tracks"`
	WithoutArt  int            `json:"withoutArtwork"`
	TotalBytes  int64          `json:"totalBytes"`
	UniqueBytes int64          `json:"uniqueBytes"`
}

// ArtworkSummary groups the covers of matching tracks.
//
// The grouping is the useful part: on a real library the great majority of
// embedded artwork is the same image repeated once per track, and that is only
// visible when identical covers are counted together.
func (s *Service) ArtworkSummary(query string) ArtworkReport {
	ids := s.matchIDs(query)
	rep := ArtworkReport{Tracks: len(ids), Groups: []ArtworkGroup{}}
	groups := map[string]*ArtworkGroup{}

	for _, id := range ids {
		path, err := s.Path(id)
		if err != nil {
			continue
		}
		pic, err := tags.ReadCover(path)
		if err != nil || pic == nil {
			rep.WithoutArt++
			continue
		}
		key := strconv.FormatUint(hashBytes64(pic.Data), 16)
		g := groups[key]
		if g == nil {
			g = &ArtworkGroup{
				Hash: key, Summary: pic.Summary(), Width: pic.Width, Height: pic.Height,
				MIME: pic.MIME, SampleID: id, ExampleDir: filepath.Base(filepath.Dir(path)),
			}
			groups[key] = g
			rep.UniqueBytes += int64(len(pic.Data))
		}
		g.Tracks++
		g.Bytes += int64(len(pic.Data))
		rep.TotalBytes += int64(len(pic.Data))
	}

	for _, g := range groups {
		rep.Groups = append(rep.Groups, *g)
	}
	sort.Slice(rep.Groups, func(i, j int) bool { return rep.Groups[i].Tracks > rep.Groups[j].Tracks })
	return rep
}

// folderCache finds the cover image beside a track, remembering the answer per
// directory so an album costs one directory read rather than one per track.
type folderCache struct {
	mu   sync.Mutex
	seen map[string]*tags.Picture
}

func newFolderCache() *folderCache {
	return &folderCache{seen: map[string]*tags.Picture{}}
}

func (fc *folderCache) find(dir string) *tags.Picture {
	fc.mu.Lock()
	if p, ok := fc.seen[dir]; ok {
		fc.mu.Unlock()
		return p
	}
	fc.mu.Unlock()

	p := findFolderArt(dir)
	fc.mu.Lock()
	fc.seen[dir] = p
	fc.mu.Unlock()
	return p
}

// findFolderArt looks for a conventionally named image in a directory.
func findFolderArt(dir string) *tags.Picture {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	byName := map[string]string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := strings.ToLower(e.Name())
		ext := filepath.Ext(name)
		if !containsString(coverExts, ext) {
			continue
		}
		byName[strings.TrimSuffix(name, ext)] = filepath.Join(dir, e.Name())
	}
	for _, want := range coverNames {
		path, ok := byName[want]
		if !ok {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if p, err := tags.NewPicture(data); err == nil {
			return p
		}
	}
	return nil
}

func containsString(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

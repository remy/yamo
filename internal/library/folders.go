package library

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/remy/yamo/internal/catalog"
)

// Browsing by folder.
//
// Albums and artists are the right way in when the tags are right. Before they
// are, they are the wrong way in entirely: an album grid built from broken
// tags shows "Unknown Album" four thousand times, and the one thing that is
// still correct — that these forty files sit in a directory together, and
// whoever ripped them meant them as a record — is the thing it throws away.
//
// The path field has always been searchable, so this is reachable by hand
// (`q=path:^/music/Elvis`). What it is not is enumerable: nothing lists the
// directories, so a client cannot offer a tree without walking every track's
// path itself. This lists them, one level at a time, the way a file browser
// does.

// Folder is one directory and what is in it.
type Folder struct {
	// Path is the directory itself, absolute.
	Path string `json:"path"`

	// Name is the last segment, which is what a list displays.
	Name string `json:"name"`

	// Tracks counts matching tracks directly in this directory; Descendants
	// counts them in everything below it too. A folder holding only other
	// folders has Tracks of zero and is still worth listing, because it is
	// how you get to the ones that do.
	Tracks      int `json:"tracks"`
	Descendants int `json:"descendants"`

	// Folders counts the immediate subdirectories that hold matching tracks.
	Folders int `json:"folders"`

	Bytes      int64 `json:"bytes"`
	DurationMS int64 `json:"durationMs"`
	WithArt    int   `json:"withArtwork"`

	// Albums and Artists count the distinct values among the tracks directly
	// in this folder. A directory holding one album reads as one of each,
	// which is what says the folder is a record; several of either says it is
	// a shelf, or that the tags are wrong.
	Albums  int `json:"albums"`
	Artists int `json:"artists"`

	// SampleTrackID is a track in this folder, for fetching a cover to head
	// the entry with.
	SampleTrackID string `json:"sampleTrackId,omitempty"`

	// Query reselects every track at or below this folder.
	Query string `json:"query"`
}

// FolderPage is one page of folders.
type FolderPage struct {
	Items  []Folder `json:"items"`
	Total  int      `json:"total"`
	Limit  int      `json:"limit"`
	Offset int      `json:"offset"`

	// Parent is the directory listed, empty when listing the roots.
	Parent string `json:"parent,omitempty"`
}

// FolderParams asks for the contents of one directory.
type FolderParams struct {
	// Path is the directory to list the contents of. Empty lists the library
	// roots, which is where a tree starts.
	Path string

	Query  string
	Limit  int
	Offset int
}

// Folders lists the directories immediately under one path.
func (s *Service) Folders(p FolderParams) FolderPage {
	if p.Limit <= 0 {
		p.Limit = DefaultLimit
	}
	if p.Limit > MaxLimit {
		p.Limit = MaxLimit
	}
	if p.Offset < 0 {
		p.Offset = 0
	}

	parent := strings.TrimSpace(p.Path)
	if parent != "" {
		parent = filepath.Clean(parent)
	}

	s.mu.RLock()
	hits := s.cat.Index().Search(catalog.ParseQuery(p.Query))
	roots := append([]string(nil), s.cat.Roots...)

	type agg struct {
		Folder
		albums  map[string]bool
		artists map[string]bool
		subs    map[string]bool
	}
	byPath := map[string]*agg{}

	for _, i := range hits {
		t := &s.cat.Tracks[i]
		dir := filepath.Dir(t.Path)

		// The child of parent that this track sits at or under. Listing one
		// level means a track six directories down still counts towards the
		// one directory that leads to it.
		child, direct, ok := childUnder(dir, parent, roots)
		if !ok {
			continue
		}
		a := byPath[child]
		if a == nil {
			a = &agg{
				Folder:  Folder{Path: child, Name: filepath.Base(child)},
				albums:  map[string]bool{},
				artists: map[string]bool{},
				subs:    map[string]bool{},
			}
			byPath[child] = a
		}
		a.Descendants++
		a.Bytes += t.Size
		a.DurationMS += int64(t.DurationMS)
		if t.HasArt {
			a.WithArt++
			if a.SampleTrackID == "" {
				a.SampleTrackID = TrackID(t.Path)
			}
		}
		if direct {
			a.Tracks++
			if t.Album != "" {
				a.albums[catalog.Fold(t.Album)] = true
			}
			artist := t.AlbumArtist
			if artist == "" {
				artist = t.Artist
			}
			if artist != "" {
				a.artists[catalog.Fold(artist)] = true
			}
		} else {
			// The immediate subdirectory of child that this track is under,
			// counted so a shelf can say how many records are on it.
			if sub, _, ok := childUnder(dir, child, roots); ok {
				a.subs[sub] = true
			}
		}
		if a.SampleTrackID == "" {
			a.SampleTrackID = TrackID(t.Path)
		}
	}
	s.mu.RUnlock()

	items := make([]Folder, 0, len(byPath))
	for _, a := range byPath {
		a.Albums, a.Artists, a.Folders = len(a.albums), len(a.artists), len(a.subs)
		a.Query = folderQuery(a.Path)
		items = append(items, a.Folder)
	}
	sort.Slice(items, func(i, j int) bool {
		return catalog.Fold(items[i].Path) < catalog.Fold(items[j].Path)
	})

	out := FolderPage{
		Total: len(items), Limit: p.Limit, Offset: p.Offset,
		Parent: parent, Items: []Folder{},
	}
	if p.Offset >= len(items) {
		return out
	}
	out.Items = items[p.Offset:min(p.Offset+p.Limit, len(items))]
	return out
}

// childUnder returns the child of parent that dir is at or under.
//
// It reports direct when dir is that child exactly rather than something
// deeper, which is what separates the tracks in a folder from the tracks in
// its subfolders. With an empty parent the children are the library roots,
// so a tree starts where the library does rather than at the filesystem's.
func childUnder(dir, parent string, roots []string) (child string, direct bool, ok bool) {
	if parent == "" {
		for _, r := range roots {
			r = filepath.Clean(r)
			if dir == r {
				return r, true, true
			}
			if under(dir, r) {
				return r, false, true
			}
		}
		// No roots recorded, or a track outside all of them: its own
		// directory is a top-level entry rather than being dropped.
		return dir, true, true
	}
	if dir == parent {
		return "", false, false // the parent itself is not one of its children
	}
	if !under(dir, parent) {
		return "", false, false
	}
	rest := strings.TrimPrefix(dir, parent+string(filepath.Separator))
	first := rest
	if i := strings.Index(rest, string(filepath.Separator)); i >= 0 {
		first = rest[:i]
	}
	child = filepath.Join(parent, first)
	return child, child == dir, true
}

// folderQuery selects every track at or below a directory.
//
// Anchored to the start of the path and carrying the trailing separator, so
// "/music/Elvis" does not also select "/music/Elvis Costello".
func folderQuery(dir string) string {
	return `path:"^` + strings.ReplaceAll(dir, `"`, "") + string(filepath.Separator) + `"`
}

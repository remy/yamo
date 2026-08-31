package ui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/remy/yamo/internal/client"
	"github.com/remy/yamo/internal/library"
)

// pageSize is how many tracks are fetched at once.
//
// Large enough that scrolling a screenful rarely crosses a boundary, small
// enough that a single request stays quick over a network. The interface can
// no longer hold the library in memory, so everything below is about asking
// for the right window rather than owning the whole thing.
const pageSize = 200

// source is the interface's window onto the library.
//
// It caches pages of results and the edits made against them. Edits are staged
// here rather than sent immediately: the API writes through, but the browser
// keeps its own notion of unsaved work so that undo, the dirty marker and one
// deliberate ^s all still mean something.
type source struct {
	c *client.Client

	query  string
	sortBy string

	// gen increments whenever the query or sort changes. A response carrying
	// an older generation is discarded: search runs on every keystroke, so
	// replies routinely arrive for a query the user has already moved past.
	gen int

	total   int
	pages   map[int][]library.Track
	loading map[int]bool

	staged map[string]*stagedEdit
}

// stagedEdit is one track's unsaved changes.
type stagedEdit struct {
	changes library.Changes

	// version is the track's state when it was first edited here. It is sent
	// as If-Match on save, so an edit made elsewhere in the meantime is
	// reported rather than silently overwritten.
	version string
	track   library.Track
}

func newSource(c *client.Client) *source {
	return &source{
		c:       c,
		pages:   map[int][]library.Track{},
		loading: map[int]bool{},
		staged:  map[string]*stagedEdit{},
	}
}

// pageLoadedMsg carries a fetched window back to the update loop.
type pageLoadedMsg struct {
	gen   int
	page  int
	items []library.Track
	total int
	err   error
}

// setQuery changes what is being looked at, discarding the cached window.
func (s *source) setQuery(query, sortBy string) {
	if query == s.query && sortBy == s.sortBy {
		return
	}
	s.query, s.sortBy = query, sortBy
	s.gen++
	s.pages = map[int][]library.Track{}
	s.loading = map[int]bool{}
	s.total = 0
}

// invalidate drops the cache without changing the query, after an edit made
// elsewhere or an operation that rewrote files.
func (s *source) invalidate() {
	s.gen++
	s.pages = map[int][]library.Track{}
	s.loading = map[int]bool{}
}

// rowAt returns the track at a row, with any staged edits applied, and whether
// it has been fetched yet.
func (s *source) rowAt(i int) (library.Track, bool) {
	if i < 0 || i >= s.total {
		return library.Track{}, false
	}
	items, ok := s.pages[i/pageSize]
	if !ok {
		return library.Track{}, false
	}
	off := i % pageSize
	if off >= len(items) {
		return library.Track{}, false
	}
	t := items[off]
	if edit, staged := s.staged[t.ID]; staged {
		applyChanges(&t, edit.changes)
	}
	return t, true
}

// isDirty reports whether a track has unsaved edits.
func (s *source) isDirty(id string) bool {
	_, ok := s.staged[id]
	return ok
}

// ensure returns commands to fetch any unfetched pages covering a row range.
func (s *source) ensure(from, to int) []tea.Cmd {
	if from < 0 {
		from = 0
	}
	var cmds []tea.Cmd
	first, last := from/pageSize, to/pageSize
	for p := first; p <= last; p++ {
		if p < 0 || s.loading[p] {
			continue
		}
		if _, have := s.pages[p]; have {
			continue
		}
		// The first page is always worth fetching; later ones only once the
		// total says they exist.
		if p > 0 && s.total > 0 && p*pageSize >= s.total {
			continue
		}
		s.loading[p] = true
		cmds = append(cmds, s.fetch(p))
	}
	return cmds
}

// fetchTimeout bounds a page request. Long enough for a slow link, short
// enough that a dead server does not leave the interface waiting forever.
const fetchTimeout = 20 * time.Second

func (s *source) fetch(page int) tea.Cmd {
	gen, query, sortBy, c := s.gen, s.query, s.sortBy, s.c
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		res, err := c.ListTracks(ctx, library.ListParams{
			Query: query, Sort: sortBy, Limit: pageSize, Offset: page * pageSize,
		})
		if err != nil {
			return pageLoadedMsg{gen: gen, page: page, err: err}
		}
		return pageLoadedMsg{gen: gen, page: page, items: res.Items, total: res.Total}
	}
}

// apply folds a fetched page into the cache, ignoring stale replies.
func (s *source) apply(msg pageLoadedMsg) bool {
	if msg.gen != s.gen {
		return false // the query moved on while this was in flight
	}
	delete(s.loading, msg.page)
	if msg.err != nil {
		return false
	}
	s.pages[msg.page] = msg.items
	s.total = msg.total
	return true
}

// stage records an edit without sending it.
func (s *source) stage(t library.Track, field, value string) {
	e := s.staged[t.ID]
	if e == nil {
		e = &stagedEdit{changes: library.Changes{}, version: t.Version, track: t}
		s.staged[t.ID] = e
	}
	v := value
	e.changes[field] = &v
}

// unstage removes a track's pending edits entirely.
func (s *source) unstage(id string) { delete(s.staged, id) }

// dirtyCount is how many tracks have unsaved edits.
func (s *source) dirtyCount() int { return len(s.staged) }

// applyChanges overlays staged values onto a fetched track, so the list shows
// what the user typed rather than what the server last said.
func applyChanges(t *library.Track, ch library.Changes) {
	for field, v := range ch {
		if v == nil {
			setTrackField(t, field, "")
			continue
		}
		setTrackField(t, field, *v)
	}
}

func setTrackField(t *library.Track, field, value string) {
	switch field {
	case "title":
		t.Title = value
	case "artist":
		t.Artist = value
	case "albumartist":
		t.AlbumArtist = value
	case "album":
		t.Album = value
	case "genre":
		t.Genre = value
	case "composer":
		t.Composer = value
	case "comment":
		t.Comment = value
	case "year":
		t.Year = atoi32(value)
	case "track":
		t.TrackNo = atoi32(value)
	case "tracktotal":
		t.TrackTotal = atoi32(value)
	case "disc":
		t.Disc = atoi32(value)
	case "disctotal":
		t.DiscTotal = atoi32(value)
	}
}

// trackField reads a field by canonical name, for the editor and the columns.
func trackField(t *library.Track, field string) string {
	switch field {
	case "title":
		return t.Title
	case "artist":
		return t.Artist
	case "albumartist":
		return t.AlbumArtist
	case "album":
		return t.Album
	case "genre":
		return t.Genre
	case "composer":
		return t.Composer
	case "comment":
		return t.Comment
	case "year":
		return itoa32(t.Year)
	case "track":
		return itoa32(t.TrackNo)
	case "tracktotal":
		return itoa32(t.TrackTotal)
	case "disc":
		return itoa32(t.Disc)
	case "disctotal":
		return itoa32(t.DiscTotal)
	case "path":
		return t.Path
	}
	return ""
}

func itoa32(v int32) string {
	if v == 0 {
		return ""
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [12]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func atoi32(s string) int32 {
	var n int32
	neg := false
	for i, c := range s {
		if i == 0 && (c == '-' || c == '+') {
			neg = c == '-'
			continue
		}
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int32(c-'0')
		if n < 0 {
			return 0
		}
	}
	if neg {
		return -n
	}
	return n
}

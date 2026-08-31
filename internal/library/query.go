package library

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/remy/yamo/internal/catalog"
)

// Paging bounds. A windowed table and a phone list both want a page and a
// total; the cap stops one client asking for the whole library as JSON.
const (
	DefaultLimit = 100
	MaxLimit     = 1000
)

// ListParams is a query for tracks.
type ListParams struct {
	Query  string // the same language the search bar and the CLI use
	Sort   string // comma separated, "-" prefix for descending
	Limit  int
	Offset int
}

// ListResult is one page of tracks.
type ListResult struct {
	Items  []Track `json:"items"`
	Total  int     `json:"total"`
	Limit  int     `json:"limit"`
	Offset int     `json:"offset"`
}

// List searches, sorts and pages the catalogue.
func (s *Service) List(p ListParams) ListResult {
	if p.Limit <= 0 {
		p.Limit = DefaultLimit
	}
	if p.Limit > MaxLimit {
		p.Limit = MaxLimit
	}
	if p.Offset < 0 {
		p.Offset = 0
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	q := catalog.ParseQuery(p.Query)
	scored := q.Fuzzy()
	hits := s.cat.Index().SearchScored(q)
	sortHits(s.cat, hits, p.Sort, scored)

	out := ListResult{Total: len(hits), Limit: p.Limit, Offset: p.Offset, Items: []Track{}}
	if p.Offset >= len(hits) {
		return out
	}
	end := min(p.Offset+p.Limit, len(hits))
	out.Items = make([]Track, 0, end-p.Offset)
	for _, h := range hits[p.Offset:end] {
		t := toTrack(&s.cat.Tracks[h.Index])
		if scored {
			// Rounded because the extra digits are noise: they encode which
			// tier and bonuses fired, which no client should be reading.
			t.Score = math.Round(h.Score*1000) / 1000
		}
		out.Items = append(out.Items, t)
	}
	return out
}

// matchIDs returns the ids of every track matching a query.
func (s *Service) matchIDs(query string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	hits := s.cat.Index().Search(catalog.ParseQuery(query))
	out := make([]string, 0, len(hits))
	for _, i := range hits {
		out = append(out, TrackID(s.cat.Tracks[i].Path))
	}
	return out
}

// Count reports how many tracks match a query.
func (s *Service) Count(query string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.cat.Index().Search(catalog.ParseQuery(query)))
}

// sortHits orders search results in place.
//
// A fuzzy query with no sort of its own is ordered by score, because a search
// that admits near misses is asking to be ranked; a caller that gave a sort
// still gets exactly the one it asked for.
func sortHits(c *catalog.Catalog, hits []catalog.Hit, spec string, scored bool) {
	keys := parseSort(spec)
	if len(keys) == 0 {
		if !scored {
			// Catalogue order is path order, which groups albums naturally and
			// is stable across requests.
			sort.Slice(hits, func(i, j int) bool { return hits[i].Index < hits[j].Index })
			return
		}
		keys = []sortKey{{score: true, desc: true}}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		a, b := &c.Tracks[hits[i].Index], &c.Tracks[hits[j].Index]
		for _, k := range keys {
			v := 0
			if k.score {
				v = cmpFloat(hits[i].Score, hits[j].Score)
			} else {
				v = k.cmp(a, b)
			}
			if v != 0 {
				if k.desc {
					return v > 0
				}
				return v < 0
			}
		}
		return a.Path < b.Path // stable, meaningful tiebreak
	})
}

type sortKey struct {
	cmp   func(a, b *catalog.Track) int
	score bool // ranks on the hit's relevance, which is not a field of a track
	desc  bool
}

// parseSort compiles "artist,-year" into comparators, ignoring names it does
// not recognise rather than failing: a half-typed sort should degrade, not
// error.
func parseSort(spec string) []sortKey {
	var keys []sortKey
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		desc := false
		if part[0] == '-' {
			desc, part = true, part[1:]
		} else if part[0] == '+' {
			part = part[1:]
		}
		if part == "score" || part == "relevance" {
			// Highest first is the only ordering anyone means by "score", so
			// it is the default direction and "-score" is the way to invert it.
			keys = append(keys, sortKey{score: true, desc: !desc})
			continue
		}
		if cmp, ok := comparatorFor(part); ok {
			keys = append(keys, sortKey{cmp: cmp, desc: desc})
		}
	}
	return keys
}

// SortFields are the names accepted by the sort parameter.
var SortFields = []string{
	"title", "artist", "albumartist", "album", "genre", "composer", "comment",
	"year", "track", "disc", "path", "duration", "size", "bitrate", "format",
	"modified", "score",
}

func comparatorFor(name string) (func(a, b *catalog.Track) int, bool) {
	switch strings.ToLower(name) {
	case "duration", "time", "length":
		return func(a, b *catalog.Track) int { return cmpInt64(int64(a.DurationMS), int64(b.DurationMS)) }, true
	case "size":
		return func(a, b *catalog.Track) int { return cmpInt64(a.Size, b.Size) }, true
	case "bitrate":
		return func(a, b *catalog.Track) int { return cmpInt64(int64(a.Bitrate), int64(b.Bitrate)) }, true
	case "modified", "modtime":
		return func(a, b *catalog.Track) int { return cmpInt64(a.ModTime, b.ModTime) }, true
	case "format":
		return func(a, b *catalog.Track) int { return strings.Compare(a.Format.String(), b.Format.String()) }, true
	}

	f, ok := catalog.LookupField(name)
	if !ok {
		return nil, false
	}
	switch f {
	case catalog.FieldYear:
		return func(a, b *catalog.Track) int { return cmpInt64(int64(a.Year), int64(b.Year)) }, true
	case catalog.FieldTrackNo:
		return func(a, b *catalog.Track) int { return cmpInt64(int64(a.TrackNo), int64(b.TrackNo)) }, true
	case catalog.FieldDisc:
		return func(a, b *catalog.Track) int { return cmpInt64(int64(a.Disc), int64(b.Disc)) }, true
	}
	// Text sorts fold case and accents, so that Björk files where a reader
	// would look for it rather than after Z.
	return func(a, b *catalog.Track) int {
		return strings.Compare(catalog.Fold(a.String(f)), catalog.Fold(b.String(f)))
	}, true
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func cmpInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// Values returns distinct values of a field for autocomplete, most used first.
//
// This takes the write lock, not the read lock. The catalogue builds each
// field's value set on first request and caches it on the index, so a read
// here is a write underneath.
func (s *Service) Values(field, prefix string, limit int) ([]catalog.ValueCount, error) {
	f, ok := catalog.LookupField(field)
	if !ok {
		return nil, ErrNotFound
	}
	if limit <= 0 {
		limit = 20
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cat.Index().Values(f).Complete(prefix, limit), nil
}

// Stats summarises the catalogue.
type Stats struct {
	Tracks     int            `json:"tracks"`
	Roots      []string       `json:"roots"`
	ScannedAt  time.Time      `json:"scannedAt"`
	TotalBytes int64          `json:"totalBytes"`
	TotalMS    int64          `json:"totalDurationMs"`
	WithArt    int            `json:"withArtwork"`
	Artists    int            `json:"artists"`
	Albums     int            `json:"albums"`
	Genres     int            `json:"genres"`
	Formats    map[string]int `json:"formats"`
	Missing    map[string]int `json:"missing"`

	// RescanEveryMS is the periodic rescan interval, absent when the timer is
	// off — which is the default, and the thing worth knowing: nothing
	// watches the filesystem, so without it the catalogue is only as current
	// as the last scan somebody asked for.
	RescanEveryMS int64      `json:"rescanEveryMs,omitempty"`
	NextRescanAt  *time.Time `json:"nextRescanAt,omitempty"`
}

// Stats computes the library summary. It takes the write lock because the
// distinct-value counts build the same cached sets that Values does.
func (s *Service) Stats() Stats {
	// The rescan schedule has its own lock. Read it before the catalogue lock
	// rather than nesting inside it, so the two never acquire in an order
	// anything else has to match.
	every, next := s.RescanSchedule()

	s.mu.Lock()
	defer s.mu.Unlock()

	st := Stats{
		Roots:     s.cat.Roots,
		ScannedAt: s.cat.ScannedAt,
		Tracks:    len(s.cat.Tracks),
		Formats:   map[string]int{},
		Missing:   map[string]int{},
	}
	if every > 0 {
		st.RescanEveryMS = every.Milliseconds()
		if !next.IsZero() {
			at := next
			st.NextRescanAt = &at
		}
	}
	if st.Roots == nil {
		st.Roots = []string{}
	}
	for i := range s.cat.Tracks {
		t := &s.cat.Tracks[i]
		st.TotalBytes += t.Size
		st.TotalMS += int64(t.DurationMS)
		st.Formats[t.Format.String()]++
		if t.HasArt {
			st.WithArt++
		}
		// The fields worth knowing the gaps in. Comment is deliberately absent:
		// most tracks have none and that is not a gap.
		for _, f := range []catalog.Field{
			catalog.FieldTitle, catalog.FieldArtist, catalog.FieldAlbum,
			catalog.FieldAlbumArtist, catalog.FieldGenre, catalog.FieldYear,
			catalog.FieldComposer, catalog.FieldTrackNo,
		} {
			if t.String(f) == "" {
				st.Missing[catalog.FieldNames[f]]++
			}
		}
	}
	ix := s.cat.Index()
	st.Artists = len(ix.Values(catalog.FieldArtist).Values)
	st.Albums = len(ix.Values(catalog.FieldAlbum).Values)
	st.Genres = len(ix.Values(catalog.FieldGenre).Values)
	return st
}

// Album groups the tracks of one release. It is the natural entry point for a
// browsing interface, where the unit of work is usually a record rather than a
// track.
type Album struct {
	ID          string `json:"id"`
	Album       string `json:"album"`
	AlbumArtist string `json:"albumArtist,omitempty"`
	Tracks      int    `json:"tracks"`
	WithArt     int    `json:"withArtwork"`
	Year        int32  `json:"year,omitempty"`
	DurationMS  int64  `json:"durationMs"`
	Query       string `json:"query"` // reselects exactly this album
}

// AlbumsResult is one page of albums.
type AlbumsResult struct {
	Items  []Album `json:"items"`
	Total  int     `json:"total"`
	Limit  int     `json:"limit"`
	Offset int     `json:"offset"`
}

// Albums groups matching tracks by release.
func (s *Service) Albums(p ListParams) AlbumsResult {
	if p.Limit <= 0 {
		p.Limit = DefaultLimit
	}
	if p.Limit > MaxLimit {
		p.Limit = MaxLimit
	}

	s.mu.RLock()
	hits := s.cat.Index().Search(catalog.ParseQuery(p.Query))
	byKey := map[string]*albumAgg{}
	for _, i := range hits {
		t := &s.cat.Tracks[i]
		// Grouping on album artist as well as album keeps two different
		// records that happen to share a title apart.
		artist := t.AlbumArtist
		if artist == "" {
			artist = t.Artist
		}
		folded := catalog.Fold(artist)
		key := folded + "\x00" + catalog.Fold(t.Album)
		a := byKey[key]
		if a == nil {
			a = &albumAgg{Album: Album{ID: TrackID(key), Album: t.Album, AlbumArtist: artist}}
			byKey[key] = a
		}
		// Which field names this group, counted rather than assumed: see
		// artistField.
		if t.AlbumArtist != "" {
			a.byAlbumArtist++
		}
		if catalog.Fold(t.Artist) == folded {
			a.byArtist++
		}
		a.Tracks++
		a.DurationMS += int64(t.DurationMS)
		if t.HasArt {
			a.WithArt++
		}
		if a.Year == 0 || (t.Year > 0 && t.Year < a.Year) {
			a.Year = t.Year
		}
	}
	s.mu.RUnlock()

	items := make([]Album, 0, len(byKey))
	for _, a := range byKey {
		a.Query = albumQuery(a.artistField(), a.AlbumArtist, a.Album.Album)
		items = append(items, a.Album)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].AlbumArtist != items[j].AlbumArtist {
			return catalog.Fold(items[i].AlbumArtist) < catalog.Fold(items[j].AlbumArtist)
		}
		return catalog.Fold(items[i].Album) < catalog.Fold(items[j].Album)
	})

	out := AlbumsResult{Total: len(items), Limit: p.Limit, Offset: p.Offset, Items: []Album{}}
	if p.Offset >= len(items) {
		return out
	}
	out.Items = items[p.Offset:min(p.Offset+p.Limit, len(items))]
	return out
}

// albumAgg accumulates one group while it is being built. The counters exist
// only to choose the query field and are not part of the API.
type albumAgg struct {
	Album
	byAlbumArtist int // tracks that carry an album artist of their own
	byArtist      int // tracks whose artist is the one naming this group
}

// artistField names the field that reselects the whole group.
//
// Grouping falls back to the artist when a file has no album artist, so a
// query on albumartist would match none of those files — and in a library
// where most files never had an album artist written, that is most albums.
// Neither field can express a group that is genuinely mixed, so the one
// covering more of it wins, and album artist breaks the tie as the more
// specific of the two.
func (a *albumAgg) artistField() string {
	if a.byAlbumArtist >= a.byArtist {
		return "albumartist"
	}
	return "artist"
}

// albumQuery builds a query that selects exactly one album, quoting the values
// so that titles containing spaces survive the round trip.
func albumQuery(field, artist, album string) string {
	var b strings.Builder
	if artist != "" {
		b.WriteString(field + `:"` + strings.ReplaceAll(artist, `"`, "") + `" `)
	}
	b.WriteString(`album:"` + strings.ReplaceAll(album, `"`, "") + `"`)
	return b.String()
}

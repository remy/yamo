package library

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/remy/yamo/internal/discogs"
	"github.com/remy/yamo/internal/tags"
)

// ErrNoDiscogs means the server was built or configured without the lookup.
var ErrNoDiscogs = errors.New("library: the Discogs lookup is not enabled")

// defaultCoverCandidates is how many search hits get their cover fetched.
//
// The number is a rate-limit decision rather than a UI one. Search returns no
// images, so each candidate costs one more request out of 25 a minute; eight
// makes a search cost nine and leaves room to do it twice before waiting. A
// tokened client pays only for the search, so it is allowed more.
const (
	defaultCoverCandidates = 8
	tokenCoverCandidates   = 20
	maxCoverCandidates     = 50
)

// coverWorkers bounds how many masters are fetched at once. The limiter is
// what actually paces them; this only stops fifty goroutines forming a queue
// inside it.
const coverWorkers = 4

// DiscogsCandidate is one album cover offered to the user.
type DiscogsCandidate struct {
	MasterID int64    `json:"masterId"`
	Title    string   `json:"title"`
	Year     string   `json:"year,omitempty"`
	Country  string   `json:"country,omitempty"`
	Formats  []string `json:"formats,omitempty"`
	Label    string   `json:"label,omitempty"`

	// Cover is the front image, and Thumb the small form to lay out with.
	// Both are absent when Discogs holds no image for the master, which is
	// common on thin entries and is why a candidate can come back with none.
	Cover  string `json:"cover,omitempty"`
	Thumb  string `json:"thumb,omitempty"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`

	// ImageCount is how many pictures the master has in total, which is what
	// makes expanding it worth offering: a release with fourteen has the back
	// cover and the disc in there too.
	ImageCount int `json:"imageCount"`
}

// DiscogsSearchResult is a page of candidates plus the state of the budget.
type DiscogsSearchResult struct {
	Items []DiscogsCandidate `json:"items"`

	// Limit and Remaining are the Discogs per-minute budget. They are reported
	// because the user is the one who has to pace their searching, and a
	// silent refusal a minute from now is worse than a visible counter.
	Limit     int  `json:"rateLimit"`
	Remaining int  `json:"rateRemaining"`
	Tokened   bool `json:"tokened"`

	// Partial is set when covers were not fetched for every hit because the
	// budget ran out. The items are still returned; some just have no image.
	Partial bool `json:"partial,omitempty"`
}

// DiscogsSearch finds album art for a query.
//
// It is two round trips deep by necessity: an unauthenticated search returns
// empty image fields, so every candidate has to be fetched by id to find out
// what its cover is. Those fetches run concurrently and are cached, and the
// number of them is capped, because the budget is 25 requests a minute.
func (s *Service) DiscogsSearch(ctx context.Context, query string, limit int) (*DiscogsSearchResult, error) {
	if s.discogs == nil {
		return nil, ErrNoDiscogs
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("library: a Discogs search needs something to look for")
	}

	max := defaultCoverCandidates
	if s.discogs.Authenticated() {
		max = tokenCoverCandidates
	}
	if limit > 0 && limit < max {
		max = limit
	}
	if max > maxCoverCandidates {
		max = maxCoverCandidates
	}

	hits, err := s.discogs.Search(ctx, query, max)
	if err != nil {
		return nil, err
	}
	if len(hits) > max {
		hits = hits[:max]
	}

	out := &DiscogsSearchResult{
		Items:   make([]DiscogsCandidate, len(hits)),
		Tokened: s.discogs.Authenticated(),
	}

	var (
		wg      sync.WaitGroup
		partial sync.Once
		sem     = make(chan struct{}, coverWorkers)
	)
	for i, h := range hits {
		c := DiscogsCandidate{
			MasterID: h.MasterID, Title: h.Title, Year: h.Year,
			Country: h.Country, Formats: h.Formats, Label: h.Label,
		}
		// An authenticated search already carries the images, so the second
		// request is pure waste for a tokened client.
		if h.Cover != "" {
			c.Cover, c.Thumb, c.ImageCount = h.Cover, h.Thumb, 1
			out.Items[i] = c
			continue
		}
		out.Items[i] = c

		wg.Add(1)
		go func(i int, id int64) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			m, err := s.discogs.MasterByID(ctx, id)
			if err != nil {
				// A candidate that could not be fetched keeps its text and
				// loses its picture. Failing the whole search because one
				// master out of eight is missing would be worse than showing
				// seven covers and a gap.
				if errors.Is(err, discogs.ErrRateLimited) {
					partial.Do(func() { out.Partial = true })
				}
				return
			}
			out.Items[i].ImageCount = len(m.Images)
			if p := m.Primary(); p != nil {
				out.Items[i].Cover = p.URI
				out.Items[i].Thumb = p.Thumb
				out.Items[i].Width, out.Items[i].Height = p.Width, p.Height
			}
		}(i, h.MasterID)
	}
	wg.Wait()

	// A hit with no picture is not a candidate for album art. They are dropped
	// rather than shown as empty tiles, but only after the fetches, since
	// until then there is no way to know which ones they are.
	kept := out.Items[:0]
	for _, c := range out.Items {
		if c.Cover != "" {
			kept = append(kept, c)
		}
	}
	out.Items = kept

	out.Limit, out.Remaining = s.discogs.Budget()
	return out, nil
}

// DiscogsAlbumInfo is what a lookup found out about one album: the fields the
// Get Info sheet can fill in, and the state of the budget it cost.
type DiscogsAlbumInfo struct {
	MasterID int64  `json:"masterId"`
	Title    string `json:"title"`
	Artists  string `json:"artists,omitempty"`

	// Year and Genre are the two values worth writing into a tag. Genre is the
	// first of Genres: Discogs allows several and a genre tag holds one.
	Year  string `json:"year,omitempty"`
	Genre string `json:"genre,omitempty"`

	// Genres and Styles are the rest of the classification, kept so a client
	// can offer the narrower style — "Synth-pop" — instead of "Electronic".
	Genres []string `json:"genres,omitempty"`
	Styles []string `json:"styles,omitempty"`

	Limit     int  `json:"rateLimit"`
	Remaining int  `json:"rateRemaining"`
	Tokened   bool `json:"tokened"`
}

// albumInfoCandidates is how many hits a lookup considers.
//
// Small on purpose. Only the first usable one is returned, and the rest exist
// so that a lead hit with no year and no genre — Discogs has plenty — does not
// end the lookup empty-handed. It costs nothing extra: per_page does not
// change what a search charges.
const albumInfoCandidates = 5

// DiscogsAlbum looks an album up for its year and genre, rather than its
// cover.
//
// It is the cheap half of the Discogs feature. A search carries genres, styles
// and a year without a token, so the usual cost is the one search request; the
// master is only fetched when the leading hit came back missing both, which is
// the one case where a second request buys something.
func (s *Service) DiscogsAlbum(ctx context.Context, artist, album string) (*DiscogsAlbumInfo, error) {
	if s.discogs == nil {
		return nil, ErrNoDiscogs
	}
	artist, album = strings.TrimSpace(artist), strings.TrimSpace(album)
	if album == "" {
		return nil, errors.New("library: a Discogs album lookup needs an album name")
	}

	hits, err := s.discogs.Search(ctx, strings.TrimSpace(artist+" "+album), albumInfoCandidates)
	if err != nil {
		return nil, err
	}

	var out *DiscogsAlbumInfo
	for _, h := range hits {
		if h.MasterID == 0 {
			continue
		}
		info := &DiscogsAlbumInfo{
			MasterID: h.MasterID, Title: h.Title,
			Year: h.Year, Genres: h.Genres, Styles: h.Styles,
		}
		if out == nil {
			out = info // the best match, whatever it holds
		}
		if info.Year != "" && len(info.Genres) > 0 {
			out = info // a complete one is worth preferring
			break
		}
	}
	if out == nil {
		return nil, fmt.Errorf("%w: nothing on Discogs matched %q", ErrNotFound, strings.TrimSpace(artist+" "+album))
	}

	// Only when the search left something out: a master fetch is a second
	// request out of 25 a minute, and it is the whole difference between this
	// lookup being cheap and being as expensive as searching for a cover.
	if out.Year == "" || len(out.Genres) == 0 {
		if m, err := s.discogs.MasterByID(ctx, out.MasterID); err == nil {
			out.Title, out.Artists = m.Title, m.Artists
			if out.Year == "" && m.Year > 0 {
				out.Year = strconv.Itoa(m.Year)
			}
			if len(out.Genres) == 0 {
				out.Genres, out.Styles = m.Genres, m.Styles
			}
		}
		// A failure here is not fatal. Whatever the search gave is still the
		// answer, and refusing it because the extra request was rate limited
		// would throw away the part that already worked.
	}

	if len(out.Genres) > 0 {
		out.Genre = out.Genres[0]
	}
	out.Tokened = s.discogs.Authenticated()
	out.Limit, out.Remaining = s.discogs.Budget()
	return out, nil
}

// DiscogsMaster returns every image on a master, for the user who wants the
// back cover or the label rather than the front.
func (s *Service) DiscogsMaster(ctx context.Context, id int64) (*discogs.Master, error) {
	if s.discogs == nil {
		return nil, ErrNoDiscogs
	}
	m, err := s.discogs.MasterByID(ctx, id)
	if err != nil {
		return nil, err
	}
	// Front cover first, then the rest as Discogs ordered them: the primary is
	// what someone came for, and it is not always first in the raw list.
	sort.SliceStable(m.Images, func(i, j int) bool {
		return m.Images[i].Type == "primary" && m.Images[j].Type != "primary"
	})
	return m, nil
}

// CopyArtworkFromURL downloads a Discogs cover onto the artwork clipboard.
//
// Going through the clipboard rather than straight into the files is what
// keeps this small: applying the clipboard to one track or to a whole album is
// already built, already reports progress as a job, and already skips files
// whose art matches. The only genuinely new thing here is fetching the bytes,
// which has to happen on the server because Discogs' image host sends no CORS
// header and a browser therefore cannot read them.
func (s *Service) CopyArtworkFromURL(ctx context.Context, rawURL string) (*tags.Picture, error) {
	if s.discogs == nil {
		return nil, ErrNoDiscogs
	}
	data, mime, err := s.discogs.FetchImage(ctx, rawURL)
	if err != nil {
		return nil, err
	}

	// The declared type is not trusted: the picture is identified from its own
	// bytes, the same way an uploaded one is, so a mislabelled response cannot
	// put something that is not an image into a music file.
	pic, err := tags.NewPicture(data)
	if err != nil {
		return nil, fmt.Errorf("library: the download was not a usable image (%s)", mime)
	}
	pic.Description = "Discogs"
	if err := s.clip.Copy(pic, rawURL); err != nil {
		return nil, err
	}
	return pic, nil
}

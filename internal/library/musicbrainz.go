package library

import (
	"context"
	"errors"
	"strings"

	"github.com/remy/yamo/internal/musicbrainz"
)

// ErrNoMusicBrainz means the server was configured without the lookup.
var ErrNoMusicBrainz = errors.New("library: the MusicBrainz lookup is not enabled")

// defaultArtistCandidates is how many artists a search offers.
//
// A name is ambiguous far more often than an album is: "Genesis" is three
// bands, "Nirvana" two, and a common surname dozens. Ten is enough to have the
// right one on the list for all but the hopeless cases, and the count does not
// change what a search costs.
const defaultArtistCandidates = 10

// MusicBrainzArtists is a list of artists to choose an id from.
type MusicBrainzArtists struct {
	Query string               `json:"query"`
	Items []musicbrainz.Artist `json:"items"`
}

// MusicBrainzArtistSearch finds artists by name.
//
// It returns candidates rather than an answer, which is the difference from
// the Discogs album lookup. Taking the top hit for an album is usually right
// because an artist and a title together are specific; a name alone is not,
// and a wrong id is worse than none because nothing downstream can tell.
func (s *Service) MusicBrainzArtistSearch(ctx context.Context, name string, limit int) (*MusicBrainzArtists, error) {
	if s.mb == nil {
		return nil, ErrNoMusicBrainz
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("library: an artist search needs a name")
	}
	if limit <= 0 {
		limit = defaultArtistCandidates
	}
	items, err := s.mb.SearchArtists(ctx, name, limit)
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = []musicbrainz.Artist{}
	}
	return &MusicBrainzArtists{Query: name, Items: items}, nil
}

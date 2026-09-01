package library

import (
	"sort"
	"strings"

	"github.com/remy/yamo/internal/catalog"
)

// Ordering the browse listings.
//
// The track listing has taken a sort since the start and the album and artist
// listings did not, which made a browsing interface — the thing they exist for
// — the one place that could not order what it showed. An album grid wants by
// year as often as by name, and a client that has to page the whole listing
// and sort it itself has given up on paging.
//
// The keys are the aggregates rather than the track fields: an album's year is
// the earliest of its tracks', its duration is their total, and neither is a
// field of anything the track sort could name. The name and direction syntax
// is the track sort's, so one explanation covers all three.

// AlbumSortFields are the names /albums accepts.
var AlbumSortFields = []string{
	"album", "albumartist", "artist", "year", "tracks", "duration", "artwork",
}

// ArtistSortFields are the names /artists accepts.
var ArtistSortFields = []string{
	"artist", "albums", "tracks", "duration", "artwork",
}

// sortSpec is one parsed sort term.
type sortSpec struct {
	name string
	desc bool
}

// parseSortSpec splits "artist,-year" into terms, ignoring empties. An
// unrecognised name is dropped by the comparator lookup rather than here, on
// the same principle the track sort follows: a half-typed sort degrades.
func parseSortSpec(spec string) []sortSpec {
	var out []sortSpec
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		desc := false
		switch part[0] {
		case '-':
			desc, part = true, part[1:]
		case '+':
			part = part[1:]
		}
		if part == "" {
			continue
		}
		out = append(out, sortSpec{name: strings.ToLower(part), desc: desc})
	}
	return out
}

// sortAlbums orders an album listing in place.
//
// With no sort, or one naming nothing recognised, the order is by album artist
// then album — which groups a discography together and is what the grid has
// always shown.
func sortAlbums(items []Album, spec string) {
	cmps := albumComparators(parseSortSpec(spec))
	if len(cmps) == 0 {
		sort.Slice(items, func(i, j int) bool {
			if !strings.EqualFold(items[i].AlbumArtist, items[j].AlbumArtist) {
				return catalog.Fold(items[i].AlbumArtist) < catalog.Fold(items[j].AlbumArtist)
			}
			return catalog.Fold(items[i].Album) < catalog.Fold(items[j].Album)
		})
		return
	}
	sort.SliceStable(items, func(i, j int) bool {
		for _, c := range cmps {
			if v := c(&items[i], &items[j]); v != 0 {
				return v < 0
			}
		}
		// A stable, meaningful tiebreak, so two albums equal on the sort do
		// not swap places between one request and the next.
		return catalog.Fold(items[i].Album) < catalog.Fold(items[j].Album)
	})
}

func albumComparators(specs []sortSpec) []func(a, b *Album) int {
	var out []func(a, b *Album) int
	for _, s := range specs {
		var cmp func(a, b *Album) int
		switch s.name {
		case "album", "name", "title":
			cmp = func(a, b *Album) int { return strings.Compare(catalog.Fold(a.Album), catalog.Fold(b.Album)) }
		case "albumartist", "artist":
			cmp = func(a, b *Album) int {
				return strings.Compare(catalog.Fold(a.AlbumArtist), catalog.Fold(b.AlbumArtist))
			}
		case "year", "date":
			cmp = func(a, b *Album) int { return cmpInt64(int64(a.Year), int64(b.Year)) }
		case "tracks", "count":
			cmp = func(a, b *Album) int { return cmpInt64(int64(a.Tracks), int64(b.Tracks)) }
		case "duration", "time", "length":
			cmp = func(a, b *Album) int { return cmpInt64(a.DurationMS, b.DurationMS) }
		case "artwork", "withartwork":
			cmp = func(a, b *Album) int { return cmpInt64(int64(a.WithArt), int64(b.WithArt)) }
		default:
			continue
		}
		out = append(out, flipAlbum(cmp, s.desc))
	}
	return out
}

func flipAlbum(cmp func(a, b *Album) int, desc bool) func(a, b *Album) int {
	if !desc {
		return cmp
	}
	return func(a, b *Album) int { return -cmp(a, b) }
}

// sortArtists orders an artist listing in place. With no recognised sort the
// order is by name, which is what the listing has always shown.
func sortArtists(items []Artist, spec string) {
	cmps := artistComparators(parseSortSpec(spec))
	if len(cmps) == 0 {
		sort.Slice(items, func(i, j int) bool {
			return catalog.Fold(items[i].Artist) < catalog.Fold(items[j].Artist)
		})
		return
	}
	sort.SliceStable(items, func(i, j int) bool {
		for _, c := range cmps {
			if v := c(&items[i], &items[j]); v != 0 {
				return v < 0
			}
		}
		return catalog.Fold(items[i].Artist) < catalog.Fold(items[j].Artist)
	})
}

func artistComparators(specs []sortSpec) []func(a, b *Artist) int {
	var out []func(a, b *Artist) int
	for _, s := range specs {
		var cmp func(a, b *Artist) int
		switch s.name {
		case "artist", "albumartist", "name":
			cmp = func(a, b *Artist) int { return strings.Compare(catalog.Fold(a.Artist), catalog.Fold(b.Artist)) }
		case "albums":
			cmp = func(a, b *Artist) int { return cmpInt64(int64(a.Albums), int64(b.Albums)) }
		case "tracks", "count":
			cmp = func(a, b *Artist) int { return cmpInt64(int64(a.Tracks), int64(b.Tracks)) }
		case "duration", "time", "length":
			cmp = func(a, b *Artist) int { return cmpInt64(a.DurationMS, b.DurationMS) }
		case "artwork", "withartwork":
			cmp = func(a, b *Artist) int { return cmpInt64(int64(a.WithArt), int64(b.WithArt)) }
		default:
			continue
		}
		out = append(out, flipArtist(cmp, s.desc))
	}
	return out
}

func flipArtist(cmp func(a, b *Artist) int, desc bool) func(a, b *Artist) int {
	if !desc {
		return cmp
	}
	return func(a, b *Artist) int { return -cmp(a, b) }
}

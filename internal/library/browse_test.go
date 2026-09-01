package library

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/remy/yamo/internal/catalog"
	"github.com/remy/yamo/internal/tags"
)

// dupService builds a library with deliberate duplicates: one track ripped
// twice, one title shared by two different artists, and one track that is not
// duplicated at all.
func dupService(t *testing.T) *Service {
	t.Helper()
	dir := t.TempDir()
	c := catalog.New()
	add := func(path, artist, album, title string, dur int32, size int64) {
		c.Tracks = append(c.Tracks, catalog.Track{
			Path: filepath.Join(dir, "music", path), Artist: artist, AlbumArtist: artist,
			Album: album, Title: title, DurationMS: dur, Size: size,
			Format: tags.FormatMP3, ModTime: time.Now().Unix(),
		})
	}
	// The same recording, ripped twice into different folders and at
	// different bitrates — the duplicate people mean.
	add("flac-rip/01.mp3", "Elvis Presley", "The Sun Sessions", "Blue Suede Shoes", 121_400, 9<<20)
	add("cd-rip/03.mp3", "Elvis Presley", "Sun Sessions", "Blue Suede Shoes", 121_900, 4<<20)
	// A different performer's version of the same song: not a duplicate on
	// artist and title, but is one on title alone.
	add("covers/07.mp3", "Carl Perkins", "Dance Album", "Blue Suede Shoes", 133_000, 4<<20)
	// Not duplicated at all.
	add("cd-rip/04.mp3", "Elvis Presley", "Sun Sessions", "Hound Dog", 136_000, 4<<20)
	// No title: cannot be said to match anything on a key that names it.
	add("junk/unknown.mp3", "Elvis Presley", "", "", 90_000, 1<<20)

	return openService(t, dir, c)
}

func TestDuplicatesDefaultKey(t *testing.T) {
	s := dupService(t)
	page := s.Duplicates(DuplicateParams{})

	if page.Total != 1 {
		t.Fatalf("found %d groups, want 1: %+v", page.Total, page.Items)
	}
	g := page.Items[0]
	if g.Tracks != 2 {
		t.Errorf("group has %d tracks, want 2", g.Tracks)
	}
	// Keeping the larger copy is what the waste is measured against.
	if g.Wasted != 4<<20 {
		t.Errorf("wasted = %d, want %d (the smaller of the two)", g.Wasted, 4<<20)
	}
	if page.Wasted != g.Wasted || page.Tracks != g.Tracks {
		t.Errorf("page totals (%d wasted, %d tracks) do not match the one group (%d, %d)",
			page.Wasted, page.Tracks, g.Wasted, g.Tracks)
	}
	if len(page.By) != 2 || page.By[0] != "artist" || page.By[1] != "title" {
		t.Errorf("by = %v, want [artist title]", page.By)
	}
}

// The key is a field list rather than a rule because what counts as the same
// recording depends on what went wrong.
func TestDuplicatesByTitleAlone(t *testing.T) {
	s := dupService(t)
	page := s.Duplicates(DuplicateParams{By: []string{"title"}})
	if page.Total != 1 {
		t.Fatalf("found %d groups, want 1", page.Total)
	}
	// All three versions of the song, Carl Perkins included.
	if page.Items[0].Tracks != 3 {
		t.Errorf("group has %d tracks, want 3", page.Items[0].Tracks)
	}
}

// Two rips differ by a few hundred milliseconds, so comparing durations
// exactly would find nothing at all.
func TestDuplicatesBucketsDuration(t *testing.T) {
	s := dupService(t)
	page := s.Duplicates(DuplicateParams{By: []string{"artist", "title", "duration"}})
	if page.Total != 1 {
		t.Fatalf("found %d groups with the default bucket, want 1", page.Total)
	}
	// A bucket of one second still catches 121.4s and 121.9s only because
	// they round into the same second; the point is that the bucket exists
	// and is applied.
	if got := s.Duplicates(DuplicateParams{
		By: []string{"title", "duration"}, DurationSeconds: 60,
	}); got.Items[0].Tracks != 3 {
		t.Errorf("a coarse bucket grouped %d, want all 3", got.Items[0].Tracks)
	}
}

// A track missing a value the key names cannot be said to match anything on
// it. Grouping the gaps together would put every untitled track in one
// enormous false group.
func TestDuplicatesSkipsEmptyValues(t *testing.T) {
	s := dupService(t)
	page := s.Duplicates(DuplicateParams{By: []string{"album"}})
	for _, g := range page.Items {
		if strings.TrimSpace(g.Key) == "" {
			t.Errorf("a group formed on an empty album: %+v", g)
		}
	}
}

func TestDuplicatesQueryReselects(t *testing.T) {
	s := dupService(t)
	page := s.Duplicates(DuplicateParams{})
	if page.Total != 1 {
		t.Fatal("expected one group")
	}
	got := s.Count(page.Items[0].Query)
	if got != 2 {
		t.Errorf("the group's query selects %d tracks, want the 2 in the group (%s)",
			got, page.Items[0].Query)
	}
}

// Matching folds case and accents, which is exactly the duplicate a merged
// library has.
func TestDuplicatesFoldAccents(t *testing.T) {
	dir := t.TempDir()
	c := catalog.New()
	for i, artist := range []string{"Björk", "Bjork"} {
		c.Tracks = append(c.Tracks, catalog.Track{
			Path:   filepath.Join(dir, "music", fmt.Sprintf("%d.mp3", i)),
			Artist: artist, Title: "Jóga", Size: int64(4<<20 + i),
			Format: tags.FormatMP3, ModTime: time.Now().Unix(),
		})
	}
	s := openService(t, dir, c)

	page := s.Duplicates(DuplicateParams{})
	if page.Total != 1 || page.Items[0].Tracks != 2 {
		t.Errorf("Björk and Bjork were not grouped: %+v", page.Items)
	}
}

// folderService builds a shelf of records, so one level of listing has
// something below it as well as in it.
func folderService(t *testing.T) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	music := filepath.Join(dir, "music")
	c := catalog.New()
	add := func(path, artist, album, title string) {
		c.Tracks = append(c.Tracks, catalog.Track{
			Path: filepath.Join(music, path), Artist: artist, AlbumArtist: artist,
			Album: album, Title: title, Size: 4 << 20, DurationMS: 120_000,
			Format: tags.FormatMP3, ModTime: time.Now().Unix(), HasArt: true,
		})
	}
	add("Elvis Presley/Sun Sessions/01.mp3", "Elvis Presley", "Sun Sessions", "Blue Suede Shoes")
	add("Elvis Presley/Sun Sessions/02.mp3", "Elvis Presley", "Sun Sessions", "Hound Dog")
	add("Elvis Presley/Elvis/01.mp3", "Elvis Presley", "Elvis", "Rip It Up")
	add("The Clash/London Calling/01.mp3", "The Clash", "London Calling", "London Calling")
	// A track sitting loose at the top of the library.
	add("stray.mp3", "Nobody", "", "Stray")

	return openService(t, dir, c), music
}

// With no path the children are the library roots, so a tree starts where the
// library does rather than at the filesystem's.
func TestFoldersStartAtTheRoots(t *testing.T) {
	s, music := folderService(t)
	page := s.Folders(FolderParams{})
	if page.Total != 1 || page.Items[0].Path != music {
		t.Fatalf("root listing = %+v, want just %s", page.Items, music)
	}
	if page.Items[0].Descendants != 5 {
		t.Errorf("the root holds %d descendants, want 5", page.Items[0].Descendants)
	}
	if page.Items[0].Tracks != 1 {
		t.Errorf("the root holds %d tracks directly, want 1 (the stray)", page.Items[0].Tracks)
	}
}

// One level at a time, the way a file browser does it: a track six
// directories down still counts towards the one directory that leads to it.
func TestFoldersOneLevel(t *testing.T) {
	s, music := folderService(t)
	page := s.Folders(FolderParams{Path: music})

	if page.Total != 2 {
		t.Fatalf("listed %d folders, want 2: %+v", page.Total, page.Items)
	}
	byName := map[string]Folder{}
	for _, f := range page.Items {
		byName[f.Name] = f
	}
	elvis, ok := byName["Elvis Presley"]
	if !ok {
		t.Fatalf("no Elvis Presley folder in %+v", page.Items)
	}
	if elvis.Descendants != 3 {
		t.Errorf("Elvis has %d descendants, want 3", elvis.Descendants)
	}
	if elvis.Tracks != 0 {
		t.Errorf("Elvis has %d tracks directly, want 0 — they are all in album folders", elvis.Tracks)
	}
	// A shelf, not a record: two albums below it.
	if elvis.Folders != 2 {
		t.Errorf("Elvis has %d subfolders, want 2", elvis.Folders)
	}
	if elvis.SampleTrackID == "" {
		t.Error("no sample track, so a grid has no cover to head the entry with")
	}
}

// A folder holding one album reads as one of each, which is what says it is a
// record rather than a shelf.
func TestFolderCountsAlbumsAndArtists(t *testing.T) {
	s, music := folderService(t)
	page := s.Folders(FolderParams{Path: filepath.Join(music, "Elvis Presley")})

	for _, f := range page.Items {
		if f.Name != "Sun Sessions" {
			continue
		}
		if f.Tracks != 2 || f.Albums != 1 || f.Artists != 1 {
			t.Errorf("Sun Sessions: %d tracks, %d albums, %d artists; want 2, 1, 1",
				f.Tracks, f.Albums, f.Artists)
		}
		return
	}
	t.Fatalf("no Sun Sessions folder in %+v", page.Items)
}

// Each entry carries the query that reselects everything at or below it, so a
// folder can be handed straight to a batch operation.
func TestFolderQueryReselects(t *testing.T) {
	s, music := folderService(t)
	page := s.Folders(FolderParams{Path: music})
	for _, f := range page.Items {
		if got := s.Count(f.Query); got != f.Descendants {
			t.Errorf("%s: query selects %d, folder claims %d descendants (%s)",
				f.Name, got, f.Descendants, f.Query)
		}
	}
}

// The filter applies to the tracks before the folders are derived, which is
// what makes "show me the folders with no album tag" a single request.
func TestFoldersFilter(t *testing.T) {
	s, music := folderService(t)
	page := s.Folders(FolderParams{Path: music, Query: "artist:clash"})
	if page.Total != 1 || page.Items[0].Name != "The Clash" {
		t.Errorf("filtered listing = %+v, want just The Clash", page.Items)
	}
}

// The browse listings could not be ordered at all, which made a browsing
// interface the one place that could not order what it showed.
func TestAlbumSort(t *testing.T) {
	s := newService(t, 40)

	byYear := s.Albums(ListParams{Sort: "-year", Limit: 100}).Items
	if len(byYear) < 2 {
		t.Fatal("not enough albums to order")
	}
	for i := 1; i < len(byYear); i++ {
		if byYear[i-1].Year < byYear[i].Year {
			t.Fatalf("-year is not descending: %d before %d", byYear[i-1].Year, byYear[i].Year)
		}
	}

	byTracks := s.Albums(ListParams{Sort: "-tracks", Limit: 100}).Items
	for i := 1; i < len(byTracks); i++ {
		if byTracks[i-1].Tracks < byTracks[i].Tracks {
			t.Fatalf("-tracks is not descending: %d before %d", byTracks[i-1].Tracks, byTracks[i].Tracks)
		}
	}

	// An unrecognised name degrades to the default rather than failing, the
	// same rule the track sort follows.
	if got := s.Albums(ListParams{Sort: "nonsense", Limit: 100}).Items; len(got) != len(byYear) {
		t.Errorf("an unrecognised sort returned %d albums, want %d", len(got), len(byYear))
	}
}

func TestArtistSort(t *testing.T) {
	s := newService(t, 40)

	byTracks := s.Artists(ListParams{Sort: "-tracks", Limit: 100}).Items
	if len(byTracks) < 2 {
		t.Fatal("not enough artists to order")
	}
	for i := 1; i < len(byTracks); i++ {
		if byTracks[i-1].Tracks < byTracks[i].Tracks {
			t.Fatalf("-tracks is not descending: %d before %d", byTracks[i-1].Tracks, byTracks[i].Tracks)
		}
	}

	byName := s.Artists(ListParams{Sort: "artist", Limit: 100}).Items
	for i := 1; i < len(byName); i++ {
		if catalog.Fold(byName[i-1].Artist) > catalog.Fold(byName[i].Artist) {
			t.Fatalf("artist is not ascending: %q before %q", byName[i-1].Artist, byName[i].Artist)
		}
	}
}

// A grid needs a cover to draw, and finding one otherwise means listing an
// album's tracks before it can draw its tile.
func TestAlbumCarriesSampleTrack(t *testing.T) {
	s := newService(t, 40)
	for _, a := range s.Albums(ListParams{Limit: 100}).Items {
		if a.WithArt > 0 && a.SampleTrackID == "" {
			t.Errorf("%q has artwork on %d tracks but names no sample", a.Album, a.WithArt)
		}
		if a.SampleTrackID != "" {
			if _, err := s.Get(a.SampleTrackID); err != nil {
				t.Errorf("%q names sample %q, which does not resolve", a.Album, a.SampleTrackID)
			}
		}
	}
}

package library

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/remy/yamo/internal/discogs"
	"github.com/remy/yamo/internal/tags"
)

// fakeDiscogs stands in for the real API, with the shape that matters: a
// search that returns no images, and masters that do.
func fakeDiscogs(t *testing.T, masters int) (*discogs.Client, *atomic.Int32, string) {
	t.Helper()

	var calls atomic.Int32
	var base string
	mux := http.NewServeMux()
	mux.HandleFunc("/database/search", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		// A thin Discogs entry: no year and no genre, which is the only case
		// where reading the master afterwards buys anything.
		if strings.Contains(r.URL.Query().Get("q"), "sparse") {
			fmt.Fprint(w, `{"results":[{"id":7,"master_id":7,"title":"Artist - Sparse",
			  "year":"","country":"","format":[],"label":[],"thumb":"","cover_image":""}]}`)
			return
		}
		var hits []string
		for i := 1; i <= masters; i++ {
			// Empty thumb and cover_image, exactly as an unauthenticated
			// search returns them. This is the fact the whole design is built
			// around, so the fake must reproduce it.
			hits = append(hits, fmt.Sprintf(
				`{"id":%d,"master_id":%d,"title":"Artist - Album %d","year":"200%d",
				  "country":"UK","format":["CD"],"label":["Label"],
				  "genre":["Rock"],"style":["Pop Rock","New Wave"],
				  "thumb":"","cover_image":""}`,
				i, i, i, i%10))
		}
		fmt.Fprintf(w, `{"results":[%s]}`, strings.Join(hits, ","))
	})
	mux.HandleFunc("/masters/", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		id := strings.TrimPrefix(r.URL.Path, "/masters/")
		// Master 2 has no images at all, which Discogs entries often do not.
		if id == "2" {
			fmt.Fprintf(w, `{"id":2,"title":"Album 2","images":[]}`)
			return
		}
		if id == "7" {
			fmt.Fprint(w, `{"id":7,"title":"Sparse","year":1999,
			  "artists":[{"name":"Artist"}],"genres":["Jazz"],"styles":["Bebop"],"images":[]}`)
			return
		}
		fmt.Fprintf(w, `{"id":%s,"title":"Album %s","year":2001,
			"artists":[{"name":"Artist"}],
			"images":[
			 {"type":"secondary","uri":"%s/back-%s.jpg","uri150":"%s/t.jpg","width":600,"height":600},
			 {"type":"primary","uri":"%s/front-%s.jpg","uri150":"%s/t.jpg","width":600,"height":600}
			]}`, id, id, base, id, base, base, id, base)
	})

	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	base = srv.URL

	c := discogs.New("")
	c.BaseURL = srv.URL
	c.HTTP = srv.Client()
	u, _ := url.Parse(srv.URL)
	c.ImageHosts = map[string]bool{u.Hostname(): true}
	return c, &calls, srv.URL
}

// jpegBytes is a real one-pixel JPEG, so tags.NewPicture accepts it.
func jpegBytes() []byte {
	return []byte{
		0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0x01,
		0x01, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00,
		0xFF, 0xDB, 0x00, 0x43, 0x00,
	}
}

// A search costs one request plus one per candidate, because Discogs will not
// return images without a token. That is the central cost of the feature, so
// it is asserted rather than assumed.
func TestDiscogsSearchFetchesEachMaster(t *testing.T) {
	s, _ := realService(t, 1)
	client, calls, _ := fakeDiscogs(t, 4)
	s.discogs = client

	res, err := s.DiscogsSearch(context.Background(), "artist album", 0)
	if err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 5 {
		t.Errorf("made %d requests, want 5 — one search plus one per candidate", n)
	}

	// Master 2 has no images, so it is no use as album art and is dropped
	// rather than shown as an empty tile.
	if len(res.Items) != 3 {
		t.Fatalf("got %d candidates, want the imageless one dropped", len(res.Items))
	}
	for _, it := range res.Items {
		if it.Cover == "" {
			t.Errorf("candidate %d has no cover but was kept", it.MasterID)
		}
		// The front cover, not merely the first image: the fake lists the back
		// cover first for exactly this reason.
		if !strings.Contains(it.Cover, "front-") {
			t.Errorf("cover = %q, want the primary image", it.Cover)
		}
		if it.ImageCount != 2 {
			t.Errorf("ImageCount = %d, want 2", it.ImageCount)
		}
	}

	// The budget is reported so the user can pace themselves.
	if res.Limit != 25 || res.Remaining != 20 {
		t.Errorf("budget = %d of %d, want 20 of 25", res.Remaining, res.Limit)
	}
	if res.Tokened {
		t.Error("Tokened is set on a client with no token")
	}
}

// Candidates are capped so one search cannot spend the whole minute's budget.
func TestDiscogsSearchCapsCandidates(t *testing.T) {
	s, _ := realService(t, 1)
	client, calls, _ := fakeDiscogs(t, 40)
	s.discogs = client

	if _, err := s.DiscogsSearch(context.Background(), "lots", 0); err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n > defaultCoverCandidates+1 {
		t.Errorf("made %d requests for one search; the cap is %d candidates",
			n, defaultCoverCandidates)
	}
}

func TestDiscogsSearchNeedsAQuery(t *testing.T) {
	s, _ := realService(t, 1)
	client, _, _ := fakeDiscogs(t, 1)
	s.discogs = client

	if _, err := s.DiscogsSearch(context.Background(), "   ", 0); err == nil {
		t.Error("an empty query was accepted")
	}
}

// With the lookup off, nothing reaches the network and every entry point says
// so rather than failing obscurely.
func TestDiscogsDisabled(t *testing.T) {
	s, _ := realService(t, 1)
	s.discogs = nil

	if _, err := s.DiscogsSearch(context.Background(), "x", 0); !errors.Is(err, ErrNoDiscogs) {
		t.Errorf("search error = %v, want ErrNoDiscogs", err)
	}
	if _, err := s.DiscogsMaster(context.Background(), 1); !errors.Is(err, ErrNoDiscogs) {
		t.Errorf("master error = %v, want ErrNoDiscogs", err)
	}
	if _, err := s.DiscogsAlbum(context.Background(), "x", "y"); !errors.Is(err, ErrNoDiscogs) {
		t.Errorf("album error = %v, want ErrNoDiscogs", err)
	}
	if _, err := s.CopyArtworkFromURL(context.Background(), "https://i.discogs.com/x.jpg"); !errors.Is(err, ErrNoDiscogs) {
		t.Errorf("copy error = %v, want ErrNoDiscogs", err)
	}
}

// Looking an album up for its year and genre costs one request, not nine: the
// search response already carries both, with or without a token. That is the
// whole reason this is a separate call from the cover search.
func TestDiscogsAlbumCostsOneRequest(t *testing.T) {
	s, _ := realService(t, 1)
	client, calls, _ := fakeDiscogs(t, 4)
	s.discogs = client

	info, err := s.DiscogsAlbum(context.Background(), "Artist", "Album 1")
	if err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("made %d requests, want 1 — the search alone", n)
	}
	if info.Year != "2001" {
		t.Errorf("Year = %q, want 2001", info.Year)
	}
	// One genre goes in the tag, and it is the first: Discogs allows several
	// and the field holds one.
	if info.Genre != "Rock" {
		t.Errorf("Genre = %q, want Rock", info.Genre)
	}
	if len(info.Styles) != 2 {
		t.Errorf("Styles = %v, want both kept for a client that prefers them", info.Styles)
	}
	if info.Limit != 25 || info.Remaining != 24 {
		t.Errorf("budget = %d of %d, want 24 of 25", info.Remaining, info.Limit)
	}
}

// A hit with neither year nor genre is worth a second request, since without
// one the lookup has nothing to say.
func TestDiscogsAlbumFallsBackToTheMaster(t *testing.T) {
	s, _ := realService(t, 1)
	client, calls, _ := fakeDiscogs(t, 1)
	s.discogs = client

	info, err := s.DiscogsAlbum(context.Background(), "Artist", "sparse")
	if err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("made %d requests, want 2 — the search and the master", n)
	}
	if info.Year != "1999" || info.Genre != "Jazz" {
		t.Errorf("year/genre = %q/%q, want 1999/Jazz from the master", info.Year, info.Genre)
	}
}

func TestDiscogsAlbumNeedsAnAlbum(t *testing.T) {
	s, _ := realService(t, 1)
	client, calls, _ := fakeDiscogs(t, 1)
	s.discogs = client

	if _, err := s.DiscogsAlbum(context.Background(), "Artist", "  "); err == nil {
		t.Error("a lookup with no album name was accepted")
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("made %d requests for a lookup that cannot work", n)
	}
}

// Expanding a master puts the front cover first, since that is what someone
// came for even when Discogs lists it second.
func TestDiscogsMasterPutsPrimaryFirst(t *testing.T) {
	s, _ := realService(t, 1)
	client, _, _ := fakeDiscogs(t, 1)
	s.discogs = client

	m, err := s.DiscogsMaster(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Images) != 2 || m.Images[0].Type != "primary" {
		t.Errorf("images = %+v, want the primary first", m.Images)
	}
}

// The download lands on the clipboard, which is what lets the existing paste
// apply it to one track or to a whole album without a second code path.
func TestCopyArtworkFromURLLandsOnClipboard(t *testing.T) {
	s, _ := realService(t, 2)
	want := jpegBytes()

	var served atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served.Add(1)
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(want)
	}))
	defer srv.Close()

	client := discogs.New("")
	client.HTTP = srv.Client()
	u, _ := url.Parse(srv.URL)
	client.ImageHosts = map[string]bool{u.Hostname(): true}
	s.discogs = client

	pic, err := s.CopyArtworkFromURL(context.Background(), srv.URL+"/cover.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pic.Data, want) {
		t.Error("the returned picture is not the bytes that were served")
	}
	if pic.Kind != tags.PictureFrontCover {
		t.Errorf("kind = %v, want a front cover", pic.Kind)
	}

	held, err := s.Clipboard().Paste()
	if err != nil {
		t.Fatalf("nothing on the clipboard: %v", err)
	}
	if !bytes.Equal(held.Picture.Data, want) {
		t.Error("the clipboard holds different bytes than were downloaded")
	}
}

// A URL off the Discogs image hosts must be refused: the server fetches what
// the client names, which without this is a request-forgery hole.
func TestCopyArtworkFromURLRefusesForeignHosts(t *testing.T) {
	s, _ := realService(t, 1)
	client, _, _ := fakeDiscogs(t, 1)
	s.discogs = client

	for _, raw := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"https://evil.example.com/cover.jpg",
		"file:///etc/passwd",
	} {
		_, err := s.CopyArtworkFromURL(context.Background(), raw)
		if !errors.Is(err, discogs.ErrNotAllowed) {
			t.Errorf("CopyArtworkFromURL(%q) = %v, want ErrNotAllowed", raw, err)
		}
	}
}

// A response that is not an image must be refused before it can be written
// into a music file, whatever it claimed to be.
func TestCopyArtworkFromURLRejectsNonImages(t *testing.T) {
	s, _ := realService(t, 1)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg") // a lie
		w.Write([]byte("<!doctype html><title>not an image</title>"))
	}))
	defer srv.Close()

	client := discogs.New("")
	client.HTTP = srv.Client()
	u, _ := url.Parse(srv.URL)
	client.ImageHosts = map[string]bool{u.Hostname(): true}
	s.discogs = client

	if _, err := s.CopyArtworkFromURL(context.Background(), srv.URL+"/x.jpg"); err == nil {
		t.Error("HTML labelled as a JPEG was accepted as cover art")
	}
}

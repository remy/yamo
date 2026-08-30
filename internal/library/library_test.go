package library

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/remy/tag-manager/internal/catalog"
	"github.com/remy/tag-manager/internal/tags"
)

// newService opens a service over a synthetic catalogue. Most behaviour can be
// checked without touching real audio; the write paths get real files below.
func newService(t *testing.T, n int) *Service {
	t.Helper()
	dir := t.TempDir()
	c := catalog.New()
	artists := []string{"Elvis Presley", "Björk", "Miles Davis", "The Clash"}
	albums := []string{"Sun Sessions", "Homogénic", "Kind of Blue", "London Calling"}
	c.Tracks = make([]catalog.Track, n)
	for i := range c.Tracks {
		c.Tracks[i] = catalog.Track{
			Path:        filepath.Join(dir, "music", artists[i%4], albums[i%4], fmt.Sprintf("%02d song.mp3", i%12+1)),
			Title:       fmt.Sprintf("Song %d", i),
			Artist:      artists[i%len(artists)],
			AlbumArtist: artists[i%len(artists)],
			Album:       albums[i%len(albums)],
			Genre:       []string{"Rock", "Electronic", "Jazz", "Punk"}[i%4],
			Year:        int32(1960 + i%50),
			TrackNo:     int32(i%12 + 1),
			DurationMS:  int32(120000 + i*37%180000),
			Size:        int64(4<<20 + i),
			ModTime:     time.Now().Unix(),
			Format:      tags.FormatMP3,
			HasArt:      i%3 == 0,
		}
	}
	// Paths must be unique or the identity map collapses.
	for i := range c.Tracks {
		c.Tracks[i].Path = fmt.Sprintf("%s-%d.mp3", strings.TrimSuffix(c.Tracks[i].Path, ".mp3"), i)
	}
	return openService(t, dir, c)
}

// openService saves a hand-built catalogue and opens a service over it.
func openService(t *testing.T, dir string, c *catalog.Catalog) *Service {
	t.Helper()
	c.Roots = []string{filepath.Join(dir, "music")}
	c.ScannedAt = time.Now()

	path := filepath.Join(dir, "catalog.db")
	if err := catalog.Save(path, c); err != nil {
		t.Fatal(err)
	}
	s, err := Open(Options{CatalogPath: path, SaveInterval: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestListSortAndPage(t *testing.T) {
	s := newService(t, 200)

	all := s.List(ListParams{Limit: MaxLimit})
	if all.Total != 200 || len(all.Items) != 200 {
		t.Fatalf("listed %d of %d, want 200", len(all.Items), all.Total)
	}

	// Paging must cover the set exactly once.
	seen := map[string]bool{}
	for off := 0; off < 200; off += 30 {
		page := s.List(ListParams{Limit: 30, Offset: off, Sort: "artist,album,track"})
		if page.Total != 200 {
			t.Fatalf("page at %d reported total %d", off, page.Total)
		}
		for _, it := range page.Items {
			if seen[it.ID] {
				t.Fatalf("track %s appeared on two pages", it.ID)
			}
			seen[it.ID] = true
		}
	}
	if len(seen) != 200 {
		t.Errorf("paging covered %d tracks, want 200", len(seen))
	}

	// Sorting.
	byArtist := s.List(ListParams{Sort: "artist", Limit: MaxLimit})
	prev := ""
	for _, it := range byArtist.Items {
		if cur := catalog.Fold(it.Artist); prev != "" && cur < prev {
			t.Fatalf("artist sort out of order: %q after %q", cur, prev)
		} else {
			prev = cur
		}
	}
	desc := s.List(ListParams{Sort: "-year", Limit: MaxLimit})
	for i := 1; i < len(desc.Items); i++ {
		if desc.Items[i].Year > desc.Items[i-1].Year {
			t.Fatalf("descending year sort out of order at %d", i)
		}
	}
	// An unrecognised sort key degrades rather than failing.
	if got := s.List(ListParams{Sort: "nonsense"}).Total; got != 200 {
		t.Errorf("unknown sort key changed the result set: %d", got)
	}

	// Filtering uses the same language as the search bar.
	if got := s.List(ListParams{Query: "artist:elvis"}).Total; got != 50 {
		t.Errorf("artist:elvis matched %d, want 50", got)
	}
	if got := s.List(ListParams{Query: "artist:bjork"}).Total; got != 50 {
		t.Errorf("folded search matched %d, want 50", got)
	}
	// Offset past the end is empty, not an error.
	if page := s.List(ListParams{Offset: 10_000}); len(page.Items) != 0 || page.Total != 200 {
		t.Errorf("offset past the end returned %d items", len(page.Items))
	}
	// The limit is capped rather than honoured blindly.
	if page := s.List(ListParams{Limit: 99999}); page.Limit != MaxLimit {
		t.Errorf("limit was not capped: %d", page.Limit)
	}
}

func TestIdentityIsStable(t *testing.T) {
	s := newService(t, 50)
	page := s.List(ListParams{Limit: 50})

	for _, it := range page.Items {
		got, err := s.Get(it.ID)
		if err != nil {
			t.Fatalf("Get(%s): %v", it.ID, err)
		}
		if got.Path != it.Path {
			t.Errorf("id %s resolved to %q, want %q", it.ID, got.Path, it.Path)
		}
		// A client can compute the same id from the path alone.
		if TrackID(it.Path) != it.ID {
			t.Errorf("TrackID(%q) = %q, want %q", it.Path, TrackID(it.Path), it.ID)
		}
	}
	if _, err := s.Get("nosuchid"); err == nil {
		t.Error("Get accepted an unknown id")
	}
}

func TestValuesAndStats(t *testing.T) {
	s := newService(t, 200)

	vals, err := s.Values("artist", "el", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(vals) == 0 || vals[0].Value != "Elvis Presley" {
		t.Fatalf("autocomplete gave %+v", vals)
	}
	if _, err := s.Values("nosuchfield", "", 5); err == nil {
		t.Error("Values accepted an unknown field")
	}

	st := s.Stats()
	if st.Tracks != 200 {
		t.Errorf("stats counted %d tracks", st.Tracks)
	}
	if st.Artists != 4 || st.Albums != 4 {
		t.Errorf("stats found %d artists and %d albums, want 4 and 4", st.Artists, st.Albums)
	}
	if st.Formats["mp3"] != 200 {
		t.Errorf("format counts = %v", st.Formats)
	}
	if st.Missing["composer"] != 200 {
		t.Errorf("missing counts = %v", st.Missing)
	}
}

func TestAlbumsGrouping(t *testing.T) {
	s := newService(t, 200)
	res := s.Albums(ListParams{Limit: 100})
	if res.Total != 4 {
		t.Fatalf("grouped into %d albums, want 4", res.Total)
	}
	for _, a := range res.Items {
		if a.Tracks != 50 {
			t.Errorf("album %q has %d tracks, want 50", a.Album, a.Tracks)
		}
		// The query an album carries must reselect exactly it.
		if got := s.Count(a.Query); got != a.Tracks {
			t.Errorf("album query %q matched %d, want %d", a.Query, got, a.Tracks)
		}
	}
}

// TestAlbumQueryWithoutAlbumArtist covers the case that made the album grid
// useless on a real library: grouping falls back to the artist when a file has
// no album artist, but the query the album carried always named albumartist,
// so clicking an album whose files never had one selected nothing at all.
func TestAlbumQueryWithoutAlbumArtist(t *testing.T) {
	dir := t.TempDir()
	c := catalog.New()
	add := func(album, artist, albumArtist string, n int) {
		for i := 0; i < n; i++ {
			c.Tracks = append(c.Tracks, catalog.Track{
				Path:        filepath.Join(dir, "music", album, fmt.Sprintf("%02d.mp3", len(c.Tracks))),
				Title:       fmt.Sprintf("Song %d", i),
				Artist:      artist,
				AlbumArtist: albumArtist,
				Album:       album,
				Format:      tags.FormatMP3,
			})
		}
	}
	add("Tagged", "Plan B", "Plan B", 4) // every file has an album artist
	add("Untagged", "Plan B", "", 4)     // none has one: the reported bug
	add("Partly", "Plan B", "Plan B", 3) // and a release tagged halfway through
	add("Partly", "Plan B", "", 2)

	// A compilation, where the album artist is the only thing holding the
	// release together and the artist would scatter it.
	for i, artist := range []string{"Ella Fitzgerald", "Nina Simone", "Etta James"} {
		c.Tracks = append(c.Tracks, catalog.Track{
			Path:        filepath.Join(dir, "music", "Compilation", fmt.Sprintf("%02d.mp3", i)),
			Title:       fmt.Sprintf("Song %d", i),
			Artist:      artist,
			AlbumArtist: "Various Artists",
			Album:       "Compilation",
			Format:      tags.FormatMP3,
		})
	}
	s := openService(t, dir, c)

	res := s.Albums(ListParams{Limit: 100})
	if res.Total != 4 {
		t.Fatalf("grouped into %d albums, want 4", res.Total)
	}
	for _, a := range res.Items {
		if got := s.Count(a.Query); got != a.Tracks {
			t.Errorf("album %q carries query %q, which matches %d of its %d tracks",
				a.Album, a.Query, got, a.Tracks)
		}
	}
}

func TestSelectorResolution(t *testing.T) {
	s := newService(t, 100)

	ids, err := s.Resolve(Selector{Query: "artist:elvis"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 25 {
		t.Fatalf("query selector matched %d, want 25", len(ids))
	}

	// Explicit ids win over a query.
	got, err := s.Resolve(Selector{IDs: ids[:3]})
	if err != nil || len(got) != 3 {
		t.Fatalf("id selector gave %d, %v", len(got), err)
	}

	// Exclusions.
	got, err = s.Resolve(Selector{Query: "artist:elvis", ExcludeIDs: ids[:5]})
	if err != nil || len(got) != 20 {
		t.Fatalf("exclusion gave %d, %v", len(got), err)
	}

	// An empty selector must not mean "everything".
	if _, err := s.Resolve(Selector{}); err == nil {
		t.Error("an empty selector was accepted")
	}
	if all, err := s.Resolve(Selector{All: true}); err != nil || len(all) != 100 {
		t.Errorf("All selector gave %d, %v", len(all), err)
	}

	// The count guard refuses when the selection has moved under the client.
	n := 25
	if _, err := s.Resolve(Selector{Query: "artist:elvis", ExpectCount: &n}); err != nil {
		t.Errorf("matching expectCount was rejected: %v", err)
	}
	wrong := 24
	_, err = s.Resolve(Selector{Query: "artist:elvis", ExpectCount: &wrong})
	var mismatch *CountMismatchError
	if err == nil {
		t.Fatal("a mismatched expectCount was accepted")
	}
	if !asCountMismatch(err, &mismatch) || mismatch.Actual != 25 || mismatch.Expected != 24 {
		t.Errorf("expected a count mismatch error, got %v", err)
	}
}

func asCountMismatch(err error, out **CountMismatchError) bool {
	if e, ok := err.(*CountMismatchError); ok {
		*out = e
		return true
	}
	return false
}

func TestChangesValidation(t *testing.T) {
	if err := (Changes{"artist": strptr("X")}).Validate(); err != nil {
		t.Errorf("a valid change was rejected: %v", err)
	}
	if err := (Changes{"nonsense": strptr("X")}).Validate(); err == nil {
		t.Error("an unknown field was accepted")
	}
	// Path is derived from the filesystem and must not be settable.
	if err := (Changes{"path": strptr("/tmp/x")}).Validate(); err == nil {
		t.Error("path was accepted as an editable field")
	}
	if err := (Changes{"tracktotal": strptr("12")}).Validate(); err != nil {
		t.Errorf("tracktotal was rejected: %v", err)
	}
}

func strptr(s string) *string { return &s }

// TestConcurrentAccess is the reason this package exists. The catalogue builds
// its index lazily and mutates itself while doing so, which is invisible in a
// single-threaded command and a data race in a server.
func TestConcurrentAccess(t *testing.T) {
	s := newService(t, 500)
	var wg sync.WaitGroup
	// A deadline rather than a shared time.After channel: that fires exactly
	// once, so only the first goroutine to select on it would ever stop.
	deadline := time.Now().Add(300 * time.Millisecond)

	worker := func(fn func()) {
		defer wg.Done()
		for time.Now().Before(deadline) {
			fn()
		}
	}

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go worker(func() { s.List(ListParams{Query: "elvis", Sort: "artist,-year", Limit: 50}) })
	}
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go worker(func() { s.Values("album", "s", 10) })
	}
	wg.Add(1)
	go worker(func() { s.Stats() })
	wg.Add(1)
	go worker(func() { s.Albums(ListParams{Limit: 20}) })
	wg.Add(1)
	go worker(func() { s.Count("genre:jazz") })

	// A writer, mutating the catalogue while the readers run.
	wg.Add(1)
	go func() {
		defer wg.Done()
		ids := s.matchIDs("artist:elvis")
		if len(ids) == 0 {
			t.Error("the fixture matched no tracks to mutate")
			return
		}
		for n := 0; time.Now().Before(deadline); n++ {
			s.mu.Lock()
			if i, ok := s.lookupLocked(ids[n%len(ids)]); ok {
				s.cat.Tracks[i].Comment = fmt.Sprintf("touched %d", n)
				s.cat.Touch(int(i))
			}
			s.mu.Unlock()
		}
	}()

	wg.Wait()
}

// --- operations against real files --------------------------------------

func ffmpegOrSkip(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	return p
}

// realService scans a directory of genuine encoder output.
func realService(t *testing.T, n int) (*Service, string) {
	t.Helper()
	ff := ffmpegOrSkip(t)
	root := t.TempDir()
	music := filepath.Join(root, "music", "Elvis Presley", "Sun Sessions")
	if err := os.MkdirAll(music, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= n; i++ {
		out := filepath.Join(music, fmt.Sprintf("%02d track.mp3", i))
		cmd := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-y",
			"-f", "lavfi", "-i", "sine=frequency=440:duration=1", "-c:a", "libmp3lame",
			"-metadata", fmt.Sprintf("title=Track %d", i),
			"-metadata", "artist=Elvis Presley",
			"-metadata", "album=Sun Sessions",
			"-metadata", fmt.Sprintf("track=%d", i), out)
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg: %v\n%s", err, b)
		}
	}

	s, err := Open(Options{
		CatalogPath:  filepath.Join(root, "catalog.db"),
		SaveInterval: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	job, err := s.Scan(ScanRequest{Roots: []string{filepath.Join(root, "music")}})
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, s, job.ID)
	return s, root
}

// waitJob blocks until a job finishes and returns its final state.
func waitJob(t *testing.T, s *Service, id string) *Job {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		j, err := s.jobs.Get(id)
		if err != nil {
			t.Fatalf("job %s vanished: %v", id, err)
		}
		if j.State != JobRunning {
			return j
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job %s did not finish", id)
	return nil
}

func TestScanJob(t *testing.T) {
	s, _ := realService(t, 4)
	if got := s.Count(""); got != 4 {
		t.Fatalf("scan catalogued %d tracks, want 4", got)
	}
	st := s.Stats()
	if len(st.Roots) != 1 {
		t.Errorf("roots = %v", st.Roots)
	}
	if st.Missing["genre"] != 4 {
		t.Errorf("missing genre = %d, want 4", st.Missing["genre"])
	}

	// A rescan reuses everything and does not duplicate rows.
	job, err := s.Scan(ScanRequest{})
	if err != nil {
		t.Fatal(err)
	}
	done := waitJob(t, s, job.ID)
	if done.State != JobSucceeded {
		t.Fatalf("rescan %s: %s", done.State, done.Error)
	}
	if got := s.Count(""); got != 4 {
		t.Errorf("rescan left %d tracks, want 4", got)
	}
}

func TestPatchWritesThrough(t *testing.T) {
	s, _ := realService(t, 3)
	first := s.List(ListParams{Sort: "track"}).Items[0]

	updated, err := s.Patch(first.ID, Changes{
		"genre":  strptr("Rockabilly"),
		"year":   strptr("1954"),
		"artist": strptr("Elvis Aaron Presley"),
	}, "")
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	if updated.Genre != "Rockabilly" || updated.Year != 1954 {
		t.Fatalf("patch returned %+v", updated)
	}
	// The version must move, or the next If-Match would be stale.
	if updated.Version == first.Version {
		t.Error("the version did not change after a write")
	}

	// Read it back off the file, not the catalogue.
	md, err := tags.NewReader().ReadFile(first.Path)
	if err != nil {
		t.Fatal(err)
	}
	if md.Genre != "Rockabilly" || md.Year != 1954 || md.Artist != "Elvis Aaron Presley" {
		t.Errorf("the file on disk says %+v", md)
	}
	// Untouched tags survive.
	if md.Album != "Sun Sessions" {
		t.Errorf("album was disturbed: %q", md.Album)
	}

	// Clearing a field.
	if _, err := s.Patch(first.ID, Changes{"genre": nil}, ""); err != nil {
		t.Fatalf("clearing: %v", err)
	}
	md, _ = tags.NewReader().ReadFile(first.Path)
	if md.Genre != "" {
		t.Errorf("genre survived clearing: %q", md.Genre)
	}
}

func TestPatchConflictDetection(t *testing.T) {
	s, _ := realService(t, 2)
	track := s.List(ListParams{}).Items[0]
	stale := track.Version

	if _, err := s.Patch(track.ID, Changes{"comment": strptr("first")}, stale); err != nil {
		t.Fatalf("patch with a current version failed: %v", err)
	}
	// The same version is now stale, which is exactly the case where a second
	// device would otherwise overwrite the first.
	_, err := s.Patch(track.ID, Changes{"comment": strptr("second")}, stale)
	if err == nil {
		t.Fatal("a stale If-Match was accepted")
	}
	if err != ErrConflict {
		t.Errorf("got %v, want ErrConflict", err)
	}
	md, _ := tags.NewReader().ReadFile(track.Path)
	if md.Comment != "first" {
		t.Errorf("the losing write landed anyway: %q", md.Comment)
	}
}

func TestPatchIsIdempotent(t *testing.T) {
	s, _ := realService(t, 2)
	track := s.List(ListParams{}).Items[0]

	if _, err := s.Patch(track.ID, Changes{"genre": strptr("Rock")}, ""); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Get(track.ID)

	// Setting the same value again must not rewrite the file, or a repeated
	// batch across a library would churn every one of them.
	if _, err := s.Patch(track.ID, Changes{"genre": strptr("Rock")}, ""); err != nil {
		t.Fatal(err)
	}
	again, _ := s.Get(track.ID)
	if again.Version != after.Version {
		t.Error("setting an unchanged value rewrote the file")
	}
}

func TestBatchSet(t *testing.T) {
	s, _ := realService(t, 6)

	job, err := s.BatchSet(BatchSetRequest{
		Selector: Selector{Query: "artist:elvis"},
		Set:      Changes{"albumartist": strptr("Various Artists"), "genre": strptr("Rock")},
	})
	if err != nil {
		t.Fatal(err)
	}
	done := waitJob(t, s, job.ID)
	if done.State != JobSucceeded {
		t.Fatalf("batch %s: %s", done.State, done.Error)
	}

	res, ok := done.Result.(BatchResult)
	if !ok {
		t.Fatalf("result was %T", done.Result)
	}
	if res.Matched != 6 || res.Changed != 6 {
		t.Fatalf("batch matched %d and changed %d, want 6 and 6", res.Matched, res.Changed)
	}

	r := tags.NewReader()
	for _, it := range s.List(ListParams{Limit: 100}).Items {
		md, err := r.ReadFile(it.Path)
		if err != nil {
			t.Fatal(err)
		}
		if md.AlbumArtist != "Various Artists" || md.Genre != "Rock" {
			t.Errorf("%s on disk: albumartist=%q genre=%q", filepath.Base(it.Path), md.AlbumArtist, md.Genre)
		}
		// The per-track title must be untouched by a batch.
		if md.Title == "" {
			t.Errorf("%s lost its title", filepath.Base(it.Path))
		}
	}

	// Re-running changes nothing.
	job2, _ := s.BatchSet(BatchSetRequest{
		Selector: Selector{Query: "artist:elvis"},
		Set:      Changes{"genre": strptr("Rock")},
	})
	res2 := waitJob(t, s, job2.ID).Result.(BatchResult)
	if res2.Changed != 0 || res2.Skipped != 6 {
		t.Errorf("a repeated batch changed %d and skipped %d", res2.Changed, res2.Skipped)
	}
}

func TestBatchDryRunWritesNothing(t *testing.T) {
	s, _ := realService(t, 3)
	before := s.List(ListParams{Limit: 10})

	job, err := s.BatchSet(BatchSetRequest{
		Selector: Selector{All: true},
		Set:      Changes{"genre": strptr("Something Else")},
		DryRun:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	res := waitJob(t, s, job.ID).Result.(BatchResult)
	if res.Changed != 3 || !res.DryRun {
		t.Errorf("dry run reported %+v", res)
	}

	r := tags.NewReader()
	for _, it := range before.Items {
		md, _ := r.ReadFile(it.Path)
		if md.Genre != "" {
			t.Errorf("a dry run wrote to %s", filepath.Base(it.Path))
		}
	}
}

func TestArtworkThroughService(t *testing.T) {
	s, root := realService(t, 3)
	ff := ffmpegOrSkip(t)

	cover := filepath.Join(root, "cover.jpg")
	if b, err := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "color=c=red:s=120x120", "-frames:v", "1", cover).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg cover: %v\n%s", err, b)
	}
	data, err := os.ReadFile(cover)
	if err != nil {
		t.Fatal(err)
	}

	job, err := s.BatchArtwork(BatchArtworkRequest{
		Selector: Selector{All: true},
		Source:   ArtFromUpload,
		Image:    data,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := waitJob(t, s, job.ID)
	if done.State != JobSucceeded {
		t.Fatalf("artwork job %s: %s", done.State, done.Error)
	}
	if res := done.Result.(BatchResult); res.Changed != 3 {
		t.Fatalf("artwork applied to %d tracks, want 3", res.Changed)
	}

	for _, it := range s.List(ListParams{Limit: 10}).Items {
		if !it.HasArt {
			t.Errorf("%s is not marked as having art", filepath.Base(it.Path))
		}
		pic, err := s.Artwork(it.ID)
		if err != nil {
			t.Fatalf("reading art back: %v", err)
		}
		if pic.Width != 120 || pic.Height != 120 {
			t.Errorf("art is %d×%d, want 120×120", pic.Width, pic.Height)
		}
	}

	rep := s.ArtworkSummary("")
	if len(rep.Groups) != 1 || rep.Groups[0].Tracks != 3 {
		t.Fatalf("summary grouped into %d, %+v", len(rep.Groups), rep.Groups)
	}
	// The grouping is the point: three copies, one distinct image.
	if rep.UniqueBytes*3 != rep.TotalBytes {
		t.Errorf("unique %d and total %d bytes do not reflect three copies",
			rep.UniqueBytes, rep.TotalBytes)
	}

	// Removing it.
	rmJob, _ := s.BatchArtwork(BatchArtworkRequest{Selector: Selector{All: true}, Source: ArtRemove})
	waitJob(t, s, rmJob.ID)
	for _, it := range s.List(ListParams{Limit: 10}).Items {
		if it.HasArt {
			t.Errorf("%s still has art", filepath.Base(it.Path))
		}
	}
}

func TestStripAndRestoreThroughService(t *testing.T) {
	s, _ := realService(t, 3)
	// Give the tracks something a strip will remove.
	batch, _ := s.BatchSet(BatchSetRequest{
		Selector: Selector{All: true},
		Set:      Changes{"comment": strptr("remove me")},
	})
	waitJob(t, s, batch.ID)

	dry, err := s.Strip(StripRequest{Selector: Selector{All: true}, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	dryRes := waitJob(t, s, dry.ID).Result.(StripResult)
	if dryRes.Changed == 0 {
		t.Fatal("a dry run found nothing to remove")
	}
	r := tags.NewReader()
	for _, it := range s.List(ListParams{Limit: 10}).Items {
		md, _ := r.ReadFile(it.Path)
		if md.Comment != "remove me" {
			t.Fatal("the dry run modified a file")
		}
	}

	job, err := s.Strip(StripRequest{Selector: Selector{All: true}, Backup: true})
	if err != nil {
		t.Fatal(err)
	}
	res := waitJob(t, s, job.ID).Result.(StripResult)
	if res.Changed != 3 || res.BackupID == "" {
		t.Fatalf("strip reported %+v", res)
	}
	for _, it := range s.List(ListParams{Limit: 10}).Items {
		md, _ := r.ReadFile(it.Path)
		if md.Comment != "" {
			t.Errorf("%s kept its comment", filepath.Base(it.Path))
		}
		if md.Artist != "Elvis Presley" {
			t.Errorf("%s lost its artist", filepath.Base(it.Path))
		}
	}

	backups, err := s.Backups()
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups = %v, %v", backups, err)
	}
	restore, err := s.Restore(RestoreRequest{BackupID: res.BackupID})
	if err != nil {
		t.Fatal(err)
	}
	rr := waitJob(t, s, restore.ID).Result.(BatchResult)
	if rr.Changed != 3 {
		t.Fatalf("restore changed %d, want 3", rr.Changed)
	}
	for _, it := range s.List(ListParams{Limit: 10}).Items {
		md, _ := r.ReadFile(it.Path)
		if md.Comment != "remove me" {
			t.Errorf("%s did not get its comment back", filepath.Base(it.Path))
		}
	}
}

// TestNormalizeMovesValuesIntoStandardFields covers the clean-up the web app's
// button runs. A genre of "(19)" reads as Industrial through the resolver, so
// nothing downstream looks wrong; the value is simply not where the next tool
// along will write it. The dry run must find it and leave the file alone.
func TestNormalizeMovesValuesIntoStandardFields(t *testing.T) {
	ff := ffmpegOrSkip(t)
	root := t.TempDir()
	music := filepath.Join(root, "music")
	if err := os.MkdirAll(music, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(music, "01 numeric genre.mp3")
	cmd := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=1", "-c:a", "libmp3lame",
		"-metadata", "title=Down In It", "-metadata", "artist=Nine Inch Nails",
		"-metadata", "genre=(19)", path)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, b)
	}

	s, err := Open(Options{CatalogPath: filepath.Join(root, "catalog.db"), SaveInterval: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	scan, err := s.Scan(ScanRequest{Roots: []string{music}})
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, s, scan.ID)

	// The resolver hides the problem, which is exactly why it needs finding.
	r := tags.NewReader()
	if md, _ := r.ReadFile(path); md.Genre != "Industrial" {
		t.Fatalf("genre read as %q, want Industrial", md.Genre)
	}

	strip := func(dry bool) StripResult {
		t.Helper()
		j, err := s.Strip(StripRequest{Selector: Selector{All: true}, DryRun: dry, Normalize: true})
		if err != nil {
			t.Fatal(err)
		}
		return waitJob(t, s, j.ID).Result.(StripResult)
	}

	dry := strip(true)
	if dry.Normalized != 1 || len(dry.NormalizeFields) != 1 || dry.NormalizeFields[0] != "genre" {
		t.Fatalf("dry run reported normalized=%d fields=%v", dry.Normalized, dry.NormalizeFields)
	}
	if again := strip(true); again.Normalized != 1 {
		t.Fatal("the dry run wrote to the file")
	}

	if got := strip(false); got.Normalized != 1 {
		t.Fatalf("apply reported normalized=%d", got.Normalized)
	}
	if after := strip(true); after.Normalized != 0 {
		t.Fatalf("still non-canonical after normalising: %v", after.NormalizeFields)
	}
	if md, _ := r.ReadFile(path); md.Genre != "Industrial" || md.Title != "Down In It" {
		t.Fatalf("normalising changed the values: %+v", md)
	}
}

// The date form the same clean-up now finds, which is what a purchased file
// carries: ©day holding "2011-08-29" where everything here writes a bare year.
// It reads as 2011 either way, so the sheet shows 2011, typing 2011 changes
// nothing, and the edit is dropped as a no-op — the clean-up is the only thing
// that can tell the two apart.
func TestNormalizeReducesAFullDateToTheYear(t *testing.T) {
	ff := ffmpegOrSkip(t)
	root := t.TempDir()
	music := filepath.Join(root, "music")
	if err := os.MkdirAll(music, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(music, "01 dated.m4a")
	cmd := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=1", "-c:a", "aac",
		"-metadata", "title=Where Them Girls At", "-metadata", "artist=David Guetta",
		"-metadata", "date=2011-08-29", path)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, b)
	}

	s, err := Open(Options{CatalogPath: filepath.Join(root, "catalog.db"), SaveInterval: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	scan, err := s.Scan(ScanRequest{Roots: []string{music}})
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, s, scan.ID)

	strip := func(dry bool) StripResult {
		t.Helper()
		j, err := s.Strip(StripRequest{Selector: Selector{All: true}, DryRun: dry, Normalize: true})
		if err != nil {
			t.Fatal(err)
		}
		return waitJob(t, s, j.ID).Result.(StripResult)
	}

	dry := strip(true)
	if dry.Normalized != 1 || len(dry.NormalizeFields) != 1 || dry.NormalizeFields[0] != "date" {
		t.Fatalf("dry run reported normalized=%d fields=%v", dry.Normalized, dry.NormalizeFields)
	}
	if got := strip(false); got.Normalized != 1 {
		t.Fatalf("apply reported normalized=%d", got.Normalized)
	}
	if after := strip(true); after.Normalized != 0 {
		t.Fatalf("still non-canonical after normalising: %v", after.NormalizeFields)
	}

	// The year survives, and now the file says only what it reads as — so
	// setting the year to something else is a change the editor can see.
	r := tags.NewReader()
	if md, _ := r.ReadFile(path); md.Year != 2011 || md.Title != "Where Them Girls At" {
		t.Fatalf("normalising changed the values: %+v", md)
	}
}

func TestEventsArePublished(t *testing.T) {
	s, _ := realService(t, 2)
	ch, cancel := s.Events().Subscribe()
	defer cancel()

	track := s.List(ListParams{}).Items[0]
	if _, err := s.Patch(track.ID, Changes{"genre": strptr("Rock")}, ""); err != nil {
		t.Fatal(err)
	}

	select {
	case e := <-ch:
		if e.Type != EventTracksChanged || len(e.TrackIDs) != 1 || e.TrackIDs[0] != track.ID {
			t.Errorf("event was %+v", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event was published for an edit")
	}
}

func TestJobCancellation(t *testing.T) {
	s := newService(t, 2000)
	job, err := s.BatchSet(BatchSetRequest{
		Selector: Selector{All: true},
		Set:      Changes{"comment": strptr("x")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.jobs.Cancel(job.ID); err != nil && !strings.Contains(err.Error(), "already finished") {
		t.Fatalf("cancel: %v", err)
	}
	done := waitJob(t, s, job.ID)
	if done.State == JobRunning {
		t.Error("the job kept running after cancellation")
	}
	if err := s.jobs.Cancel("nosuchjob"); err == nil {
		t.Error("cancelling an unknown job succeeded")
	}
}

func TestCatalogPersistsAcrossRestart(t *testing.T) {
	s, root := realService(t, 3)
	track := s.List(ListParams{}).Items[0]
	if _, err := s.Patch(track.ID, Changes{"genre": strptr("Rockabilly")}, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	again, err := Open(Options{CatalogPath: filepath.Join(root, "catalog.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if got := again.Count("genre:rockabilly"); got != 1 {
		t.Errorf("after restart, genre:rockabilly matched %d, want 1", got)
	}
}

func TestOpenWithoutCatalogue(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(Options{CatalogPath: filepath.Join(dir, "missing.db")})
	if err != nil {
		t.Fatalf("opening with no catalogue should start empty, not fail: %v", err)
	}
	defer s.Close()
	if s.Count("") != 0 {
		t.Error("an empty service reported tracks")
	}
	if _, err := s.Scan(ScanRequest{}); err == nil {
		t.Error("scanning with no roots and no catalogue should fail")
	}
	_ = context.Background()
}

// TestPatchSortFields walks the sort tags the whole way down: through the API
// field names, into the file, and back out through the catalogue.
//
// This is the case the whole feature came from — an iTunes m4a carrying an
// album artist sort of "Various Artists" and no album artist at all, which
// nothing above the tags package could see.
func TestPatchSortFields(t *testing.T) {
	s, _ := realService(t, 2)
	first := s.List(ListParams{Sort: "track"}).Items[0]

	updated, err := s.Patch(first.ID, Changes{
		"albumartistsort": strptr("Various Artists"),
		"artistsort":      strptr("Presley, Elvis"),
	}, "")
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	if updated.AlbumArtistSort != "Various Artists" || updated.ArtistSort != "Presley, Elvis" {
		t.Fatalf("patch returned %+v", updated)
	}

	md, err := tags.NewReader().ReadFile(first.Path)
	if err != nil {
		t.Fatal(err)
	}
	if md.AlbumArtistSort != "Various Artists" || md.ArtistSort != "Presley, Elvis" {
		t.Errorf("the file on disk says albumartistsort=%q artistsort=%q",
			md.AlbumArtistSort, md.ArtistSort)
	}
	// A sort name is not a rename: the display fields are untouched.
	if md.Artist != first.Artist {
		t.Errorf("artist changed from %q to %q", first.Artist, md.Artist)
	}

	// Reachable by name in a query, but not by a bare term — otherwise
	// searching for an artist would also return every track that merely sorts
	// under that name.
	if n := s.List(ListParams{Query: "albumartistsort:various"}).Total; n != 1 {
		t.Errorf("albumartistsort:various matched %d, want 1", n)
	}
	if n := s.List(ListParams{Query: "artistsort:presley"}).Total; n != 1 {
		t.Errorf("artistsort:presley matched %d, want 1", n)
	}

	if _, err := s.Patch(first.ID, Changes{"albumartistsort": nil}, ""); err != nil {
		t.Fatalf("clearing: %v", err)
	}
	md, _ = tags.NewReader().ReadFile(first.Path)
	if md.AlbumArtistSort != "" {
		t.Errorf("albumartistsort survived clearing: %q", md.AlbumArtistSort)
	}
}

package library

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/remy/yamo/internal/catalog"
	"github.com/remy/yamo/internal/tags"
)

func TestCompileRenameRejects(t *testing.T) {
	for _, tc := range []struct {
		tmpl, why string
	}{
		{"", "an empty template"},
		{"nothing here", "a template naming no fields"},
		{"$", "a bare dollar"},
		{"$nosuchfield", "an unknown field"},
		{"$path/$title", "$path, which is where the file already is"},
		{"/$artist/$title", "an absolute path"},
	} {
		if _, err := compileRename(tc.tmpl); !errors.Is(err, ErrBadTemplate) {
			t.Errorf("compileRename(%q) accepted %s: %v", tc.tmpl, tc.why, err)
		}
	}
}

func TestRenderTemplate(t *testing.T) {
	track := &catalog.Track{
		Title: "Blue Suede Shoes", Artist: "Elvis Presley",
		AlbumArtist: "Elvis Presley", Album: "The Sun Sessions",
		TrackNo: 3, Disc: 1, Year: 1976,
	}

	for _, tc := range []struct{ tmpl, want string }{
		{"$track $title", "03 Blue Suede Shoes"},
		{"$albumartist/$album/$track $title", "Elvis Presley/The Sun Sessions/03 Blue Suede Shoes"},
		{"$artist - $title", "Elvis Presley - Blue Suede Shoes"},
		{"$year - $album/$disc-$track $title", "1976 - The Sun Sessions/01-03 Blue Suede Shoes"},
		{"$$ $title", "$ Blue Suede Shoes"},
	} {
		rt, err := compileRename(tc.tmpl)
		if err != nil {
			t.Fatalf("compileRename(%q): %v", tc.tmpl, err)
		}
		got, complete := rt.render(track)
		if !complete {
			t.Errorf("render(%q) reported incomplete", tc.tmpl)
		}
		if got != tc.want {
			t.Errorf("render(%q) = %q, want %q", tc.tmpl, got, tc.want)
		}
	}
}

// The padding is the reason to put a number first at all: without it a
// directory listing puts track 10 before track 2 and the folder no longer
// reads as the record.
func TestRenderPadsTrackNumbers(t *testing.T) {
	rt, err := compileRename("$track $title")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		n    int32
		want string
	}{{2, "02 A"}, {10, "10 A"}, {100, "100 A"}} {
		got, _ := rt.render(&catalog.Track{Title: "A", TrackNo: tc.n})
		if got != tc.want {
			t.Errorf("track %d rendered %q, want %q", tc.n, got, tc.want)
		}
	}
}

// A separator inside a value must never become a directory: "AC/DC" under
// "$artist/$album" would otherwise file the album under "DC" inside "AC".
func TestRenderSanitisesSeparators(t *testing.T) {
	rt, err := compileRename("$artist/$title")
	if err != nil {
		t.Fatal(err)
	}
	got, complete := rt.render(&catalog.Track{Artist: "AC/DC", Title: "Back in Black"})
	if !complete {
		t.Fatal("reported incomplete")
	}
	if got != "AC-DC/Back in Black" {
		t.Errorf("render = %q, want %q", got, "AC-DC/Back in Black")
	}
	if strings.Count(got, "/") != 1 {
		t.Errorf("render = %q, which has more path segments than the template names", got)
	}
}

func TestRenderIncomplete(t *testing.T) {
	rt, err := compileRename("$albumartist/$album/$track $title")
	if err != nil {
		t.Fatal(err)
	}
	// No album at all: the template cannot be filled in, and the track is
	// left alone rather than filed under an empty folder.
	if _, complete := rt.render(&catalog.Track{
		AlbumArtist: "Elvis Presley", Title: "Blue Suede Shoes", TrackNo: 3,
	}); complete {
		t.Error("a track with no album rendered as complete")
	}
	// Neither album artist nor artist: nothing to file it under at all.
	if _, complete := rt.render(&catalog.Track{
		Album: "A", Title: "T", TrackNo: 1,
	}); complete {
		t.Error("a track credited to nobody rendered as complete")
	}
	// A track number of zero is the absence of a value rather than the
	// number nought.
	if _, complete := rt.render(&catalog.Track{
		AlbumArtist: "E", Album: "A", Title: "T",
	}); complete {
		t.Error("a track with no track number rendered as complete")
	}
}

// $albumartist falls back to the artist, because that is the key the browse
// listings group on — and in a library where most files never had an album
// artist written, a stricter reading would file most of it as incomplete.
func TestRenderAlbumArtistFallsBackToArtist(t *testing.T) {
	rt, err := compileRename("$albumartist/$title")
	if err != nil {
		t.Fatal(err)
	}
	got, complete := rt.render(&catalog.Track{Artist: "Elvis Presley", Title: "Blue Suede Shoes"})
	if !complete {
		t.Fatal("a track with an artist but no album artist rendered as incomplete")
	}
	if got != "Elvis Presley/Blue Suede Shoes" {
		t.Errorf("render = %q, want %q", got, "Elvis Presley/Blue Suede Shoes")
	}
	// The album artist still wins when there is one.
	got, _ = rt.render(&catalog.Track{
		Artist: "Michael Jackson", AlbumArtist: "Various Artists", Title: "Beat It",
	})
	if got != "Various Artists/Beat It" {
		t.Errorf("render = %q, want the album artist to win", got)
	}
}

func TestTidyPath(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"a//b", "a/b"},
		{"/a/b/", "a/b"},
		{" a / b ", "a/b"},
		{"a - /b", "a/b"},
		{"a./b", "a/b"},
		{"../etc/passwd", "etc/passwd"},
	} {
		if got := tidyPath(tc.in); got != tc.want {
			t.Errorf("tidyPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The whole point of the operation, end to end: files named after the tags
// they carry, in folders named after the record.
func TestRenameTracksMovesFiles(t *testing.T) {
	s, root := realService(t, 3)

	job, err := s.RenameTracks(RenameTracksRequest{
		Selector: Selector{All: true},
		Template: "$albumartist/$album/$track $title",
		Backup:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	res := waitJob(t, s, job.ID).Result.(RenameResult)
	if res.Changed != 3 || res.Failed != 0 {
		t.Fatalf("renamed %d and failed %d of 3: %+v", res.Changed, res.Failed, res)
	}

	want := filepath.Join(root, "music", "Elvis Presley", "Sun Sessions", "01 Track 1.mp3")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("%s was not created: %v", want, err)
	}
	// The catalogue follows the file, under the new identity.
	if _, err := s.Get(TrackID(want)); err != nil {
		t.Errorf("the catalogue does not know %s: %v", want, err)
	}
	if res.BackupID == "" {
		t.Error("the rename recorded no journal, so it cannot be undone")
	}
}

// The dry run is what makes the operation safe to try, so it has to move
// nothing and still say what it would have done.
func TestRenameTracksDryRun(t *testing.T) {
	s, root := realService(t, 2)
	before := filepath.Join(root, "music", "Elvis Presley", "Sun Sessions", "01 track.mp3")

	job, err := s.RenameTracks(RenameTracksRequest{
		Selector: Selector{All: true},
		Template: "$track $title",
		DryRun:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	res := waitJob(t, s, job.ID).Result.(RenameResult)
	if res.Changed != 2 {
		t.Errorf("a dry run reported %d changes, want 2", res.Changed)
	}
	if len(res.Samples) == 0 {
		t.Error("a dry run carried no samples, which is what makes it worth running")
	}
	if _, err := os.Stat(before); err != nil {
		t.Errorf("a dry run moved %s", before)
	}
}

// Two tracks wanting one name must be reported rather than one landing on the
// other, and a dry run has to report it too or it would promise a run that
// then fails.
func TestRenameTracksCollides(t *testing.T) {
	s, _ := realService(t, 3)

	job, err := s.RenameTracks(RenameTracksRequest{
		Selector: Selector{All: true},
		Template: "$album", // every track renders to the same name
		DryRun:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	res := waitJob(t, s, job.ID).Result.(RenameResult)
	if res.Collisions != 2 {
		t.Errorf("collisions = %d, want 2 (three tracks wanting one name)", res.Collisions)
	}
	if res.Changed != 1 {
		t.Errorf("changed = %d, want 1: the first claim on a name is a real one", res.Changed)
	}
}

// A rename is the operation most worth being able to take back, since undoing
// it by hand means a second pass over the library.
func TestRenameTracksUndo(t *testing.T) {
	s, root := realService(t, 3)
	original := filepath.Join(root, "music", "Elvis Presley", "Sun Sessions", "01 track.mp3")

	job, err := s.RenameTracks(RenameTracksRequest{
		Selector: Selector{All: true},
		Template: "$artist - $title",
		Backup:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := waitJob(t, s, job.ID)
	if _, err := os.Stat(original); err == nil {
		t.Fatal("the rename did not move the file")
	}

	undo, err := s.Undo(done.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res := waitJob(t, s, undo.ID).Result.(BatchResult); res.Changed != 3 {
		t.Fatalf("undo moved %d of 3 files back: %+v", res.Changed, res)
	}
	if _, err := os.Stat(original); err != nil {
		t.Errorf("%s did not come back: %v", original, err)
	}
	if _, err := s.Get(TrackID(original)); err != nil {
		t.Errorf("the catalogue did not follow the file back: %v", err)
	}
}

// Renaming resolves against the library root rather than the file's own
// directory, so a template that files by artist can rescue a track from the
// wrong folder rather than nesting the right one inside it.
func TestRenameResolvesAgainstRoot(t *testing.T) {
	s, root := realService(t, 1)
	music := filepath.Join(root, "music")

	rt, err := compileRename("$albumartist/$album/$title")
	if err != nil {
		t.Fatal(err)
	}
	ids := s.matchIDs("")
	_, dest, complete, err := s.renderFor(ids[0], rt)
	if err != nil || !complete {
		t.Fatalf("renderFor: %v, complete=%v", err, complete)
	}
	want := filepath.Join(music, "Elvis Presley", "Sun Sessions", "Track 1.mp3")
	if dest != want {
		t.Errorf("destination %q, want %q", dest, want)
	}
}

func TestValidCoverName(t *testing.T) {
	for _, ok := range []string{"cover.jpg", "folder.jpg", "front.png", "cover"} {
		if err := validCoverName(ok); err != nil {
			t.Errorf("validCoverName(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"../cover.jpg", "/etc/cover.jpg", "a/b.jpg", ".cover.jpg", "cover.txt"} {
		if err := validCoverName(bad); err == nil {
			t.Errorf("validCoverName(%q) was accepted", bad)
		}
	}
}

// The other direction from source: folder — the covers are in the files and
// nothing that reads a directory can see them.
func TestExportArtwork(t *testing.T) {
	s, root := realService(t, 3)
	music := filepath.Join(root, "music", "Elvis Presley", "Sun Sessions")

	// Give the tracks a cover to export.
	pic, err := tags.NewPicture(testPNG(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range s.matchIDs("") {
		if err := s.SetArtwork(id, pic, ""); err != nil {
			t.Fatal(err)
		}
	}

	job, err := s.ExportArtwork(ExportArtworkRequest{Selector: Selector{All: true}})
	if err != nil {
		t.Fatal(err)
	}
	res := waitJob(t, s, job.ID).Result.(ExportResult)

	// One image per directory, not one per track: three tracks in one folder
	// is one write.
	if res.Directories != 1 || res.Changed != 1 {
		t.Fatalf("wrote %d images across %d directories, want 1 and 1: %+v",
			res.Changed, res.Directories, res)
	}
	// The extension follows the image rather than the requested name.
	written := filepath.Join(music, "cover.png")
	if _, err := os.Stat(written); err != nil {
		t.Fatalf("%s was not written: %v", written, err)
	}
	if res.Filename != "cover.png" {
		t.Errorf("filename = %q, want cover.png", res.Filename)
	}

	// A second run leaves the existing cover alone.
	again, err := s.ExportArtwork(ExportArtworkRequest{Selector: Selector{All: true}})
	if err != nil {
		t.Fatal(err)
	}
	second := waitJob(t, s, again.ID).Result.(ExportResult)
	if second.Changed != 0 || second.Existing != 1 {
		t.Errorf("a second export wrote %d and skipped %d, want 0 and 1", second.Changed, second.Existing)
	}
}

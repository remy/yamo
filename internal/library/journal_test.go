package library

import (
	"testing"

	"github.com/remy/yamo/internal/tags"
)

// The gap the journal exists to close: a batch edit that set the artist on
// everything and forgot what the files said before.
func TestBatchEditUndo(t *testing.T) {
	s, _ := realService(t, 3)

	before := map[string]string{}
	for _, it := range s.List(ListParams{Limit: 10}).Items {
		before[it.ID] = it.Artist
	}

	set := "Elvis Aaron Presley"
	job, err := s.BatchSet(BatchSetRequest{
		Selector: Selector{All: true},
		Set:      Changes{"artist": &set},
		Backup:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := waitJob(t, s, job.ID)
	res := done.Result.(BatchResult)
	if res.Changed != 3 {
		t.Fatalf("edited %d of 3: %+v", res.Changed, res)
	}
	if done.BackupID == "" {
		t.Fatal("the job recorded no journal, so it cannot be undone")
	}
	if res.BackupID != done.BackupID {
		t.Errorf("the result names journal %q and the job names %q", res.BackupID, done.BackupID)
	}

	undo, err := s.Undo(done.ID)
	if err != nil {
		t.Fatal(err)
	}
	if u := waitJob(t, s, undo.ID); u.State != JobSucceeded {
		t.Fatalf("undo %s: %s", u.State, u.Error)
	}

	// Back to what each file said, read from the files rather than the
	// catalogue: an undo that only fixed the index would be worthless.
	r := tags.NewReader()
	for id, was := range before {
		path, err := s.Path(id)
		if err != nil {
			t.Fatal(err)
		}
		md, err := r.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if md.Artist != was {
			t.Errorf("%s reads %q, want %q back", path, md.Artist, was)
		}
	}
}

// Clearing a field and putting it back are different requests, so a journal
// that recorded an empty value as "" rather than null would restore the wrong
// thing — it would write an empty string over a field that should be absent.
func TestUndoRestoresEmptyFields(t *testing.T) {
	s, _ := realService(t, 1)
	id := s.matchIDs("")[0]

	// The fixtures carry no genre, so setting one is a write over nothing.
	genre := "Rockabilly"
	job, err := s.BatchSet(BatchSetRequest{
		Selector: Selector{IDs: []string{id}},
		Set:      Changes{"genre": &genre},
		Backup:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := waitJob(t, s, job.ID)
	if got, _ := s.Get(id); got.Genre != genre {
		t.Fatalf("genre is %q, want %q", got.Genre, genre)
	}

	undo, err := s.Undo(done.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, s, undo.ID)

	if got, _ := s.Get(id); got.Genre != "" {
		t.Errorf("genre is %q after the undo, want it empty again", got.Genre)
	}
}

// A split writes a different value into every file, so there is nothing a
// client could send to put them back by hand — which is why it journals by
// default.
func TestSplitUndo(t *testing.T) {
	s, _ := realService(t, 2)

	// Give the titles something to split on.
	for i, id := range s.matchIDs("") {
		title := []string{"Carl Perkins - Blue Suede Shoes", "Elvis Presley - Hound Dog"}[i]
		if _, err := s.Patch(id, Changes{"title": &title}, ""); err != nil {
			t.Fatal(err)
		}
	}

	job, err := s.Split(SplitRequest{
		Selector: Selector{All: true},
		Template: "$artist - $title",
		Backup:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := waitJob(t, s, job.ID)
	if done.Kind != JobSplit {
		t.Errorf("a split reported kind %q, want %q", done.Kind, JobSplit)
	}
	res := done.Result.(SplitResult)
	if res.Changed != 2 {
		t.Fatalf("split %d of 2: %+v", res.Changed, res)
	}
	if got, _ := s.Get(s.matchIDs("")[0]); got.Title == "Carl Perkins - Blue Suede Shoes" {
		t.Fatal("the split did not rewrite the title")
	}

	undo, err := s.Undo(done.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, s, undo.ID)

	titles := map[string]bool{}
	for _, it := range s.List(ListParams{Limit: 10}).Items {
		titles[it.Title] = true
	}
	for _, want := range []string{"Carl Perkins - Blue Suede Shoes", "Elvis Presley - Hound Dog"} {
		if !titles[want] {
			t.Errorf("%q did not come back; have %v", want, titles)
		}
	}
}

// An artwork journal holds whole images, which is why it is opt-in — and why
// it has to actually work when asked for.
func TestArtworkUndo(t *testing.T) {
	s, _ := realService(t, 2)

	first, err := tags.NewPicture(testPNGSize(t, 32, 32))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range s.matchIDs("") {
		if err := s.SetArtwork(id, first, ""); err != nil {
			t.Fatal(err)
		}
	}

	job, err := s.BatchArtwork(BatchArtworkRequest{
		Selector: Selector{All: true},
		Source:   ArtFromUpload,
		Image:    testJPEG(t, 48, 48),
		Backup:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := waitJob(t, s, job.ID)
	if done.State != JobSucceeded {
		t.Fatalf("artwork job %s: %s", done.State, done.Error)
	}
	if done.BackupID == "" {
		t.Fatal("an artwork job asked to back up recorded no journal")
	}
	if pic, err := s.Artwork(s.matchIDs("")[0]); err != nil || pic.MIME != "image/jpeg" {
		t.Fatalf("the paste did not take: %v %+v", err, pic)
	}

	undo, err := s.Undo(done.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, s, undo.ID)

	for _, id := range s.matchIDs("") {
		pic, err := s.Artwork(id)
		if err != nil {
			t.Fatalf("cover gone after the undo: %v", err)
		}
		if pic.MIME != "image/png" {
			t.Errorf("cover is %s after the undo, want the PNG back", pic.MIME)
		}
	}
}

// Pasting over a track that had no cover records that it had none, so undoing
// takes the new one off rather than leaving it there.
func TestArtworkUndoRemovesWhereThereWasNone(t *testing.T) {
	s, _ := realService(t, 1)
	id := s.matchIDs("")[0]

	job, err := s.BatchArtwork(BatchArtworkRequest{
		Selector: Selector{IDs: []string{id}},
		Source:   ArtFromUpload,
		Image:    testJPEG(t, 40, 40),
		Backup:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := waitJob(t, s, job.ID)
	if got, _ := s.Get(id); !got.HasArt {
		t.Fatal("the paste did not take")
	}

	undo, err := s.Undo(done.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, s, undo.ID)

	if got, _ := s.Get(id); got.HasArt {
		t.Error("the track still has a cover after undoing the paste that gave it one")
	}
}

// A journal has to be describable without applying it: it is addressed by an
// opaque id and may have been written by an operation this client never made.
func TestBackupDetail(t *testing.T) {
	s, _ := realService(t, 2)

	set := "Rockabilly"
	job, err := s.BatchSet(BatchSetRequest{
		Selector: Selector{All: true},
		Set:      Changes{"genre": &set},
		Backup:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := waitJob(t, s, job.ID)

	page, err := s.Backups(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Total != 1 {
		t.Fatalf("backups = %+v", page)
	}
	if page.Items[0].Kind != JournalEdit {
		t.Errorf("kind = %q, want %q", page.Items[0].Kind, JournalEdit)
	}
	if page.Items[0].JobID != done.ID {
		t.Errorf("journal names job %q, want %q", page.Items[0].JobID, done.ID)
	}

	detail, err := s.Backup(done.BackupID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Tracks != 2 {
		t.Errorf("detail covers %d tracks, want 2", detail.Tracks)
	}
	if len(detail.Fields) != 1 || detail.Fields[0] != "genre" {
		t.Errorf("fields = %v, want [genre]", detail.Fields)
	}
	if len(detail.Samples) == 0 {
		t.Error("no samples, so there is no way to see what it covers")
	}

	// And deleted, since nothing expires them.
	if err := s.DeleteBackup(done.BackupID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Backup(done.BackupID); err == nil {
		t.Error("the journal survived being deleted")
	}
}

// An id that could name a file outside the backup directory is not a lookup
// that returns nothing; it is one that must never be attempted.
func TestBackupIDIsBounded(t *testing.T) {
	s, _ := realService(t, 1)
	for _, bad := range []string{"", "../../etc/passwd", "a/b", "..", "x.json"} {
		if _, err := s.Backup(bad); err == nil {
			t.Errorf("Backup(%q) was accepted", bad)
		}
		if err := s.DeleteBackup(bad); err == nil {
			t.Errorf("DeleteBackup(%q) was accepted", bad)
		}
	}
}

// A dry run changes nothing, so journalling it would leave an entry offering
// an undo for something that never happened.
func TestDryRunWritesNoJournal(t *testing.T) {
	s, _ := realService(t, 2)

	set := "Nobody"
	job, err := s.BatchSet(BatchSetRequest{
		Selector: Selector{All: true},
		Set:      Changes{"artist": &set},
		DryRun:   true,
		Backup:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := waitJob(t, s, job.ID)
	if done.BackupID != "" {
		t.Errorf("a dry run wrote journal %q", done.BackupID)
	}
	page, err := s.Backups(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 {
		t.Errorf("a dry run left %d journals", len(page.Items))
	}
}

// A journal that recorded nothing describes an operation that changed
// nothing, and leaving it would fill the list with entries offering an undo
// that would do nothing.
func TestEmptyJournalIsDiscarded(t *testing.T) {
	s, _ := realService(t, 2)

	// Setting a field to what it already says rewrites no file.
	same := "Sun Sessions"
	job, err := s.BatchSet(BatchSetRequest{
		Selector: Selector{All: true},
		Set:      Changes{"album": &same},
		Backup:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	res := waitJob(t, s, job.ID).Result.(BatchResult)
	if res.Changed != 0 {
		t.Fatalf("changed %d tracks, want 0", res.Changed)
	}
	page, err := s.Backups(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 {
		t.Errorf("an operation that changed nothing left %d journals", len(page.Items))
	}
}

// Undo is only offered where there is something to undo from, and the failure
// has to be distinguishable from an unknown job.
func TestUndoWithoutJournal(t *testing.T) {
	s, _ := realService(t, 1)

	set := "Nobody"
	job, err := s.BatchSet(BatchSetRequest{
		Selector: Selector{All: true},
		Set:      Changes{"artist": &set},
		Backup:   false,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := waitJob(t, s, job.ID)
	if done.BackupID != "" {
		t.Fatalf("backup: false still wrote journal %q", done.BackupID)
	}
	if _, err := s.Undo(done.ID); err == nil {
		t.Error("a job with no journal was undoable")
	}
	if _, err := s.Undo("nosuchjob"); err == nil {
		t.Error("an unknown job was undoable")
	}
}

// A strip's own backup still works through the shared journal, and still says
// it is a strip so a restore puts the frames back rather than trying to write
// fields.
func TestStripJournalKind(t *testing.T) {
	s, _ := realService(t, 2)

	job, err := s.Strip(StripRequest{
		Selector: Selector{All: true},
		Keep:     []string{"title"},
		Backup:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := waitJob(t, s, job.ID)
	if done.State != JobSucceeded {
		t.Fatalf("strip %s: %s", done.State, done.Error)
	}
	if done.BackupID == "" {
		t.Fatal("a strip asked to back up recorded no journal")
	}
	detail, err := s.Backup(done.BackupID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Kind != JournalStrip {
		t.Errorf("kind = %q, want %q", detail.Kind, JournalStrip)
	}
	if len(detail.Groups) == 0 {
		t.Error("a strip journal describes no keys")
	}

	// And it still restores.
	undo, err := s.Undo(done.ID)
	if err != nil {
		t.Fatal(err)
	}
	if u := waitJob(t, s, undo.ID); u.State != JobSucceeded {
		t.Fatalf("undo %s: %s", u.State, u.Error)
	}
	for _, it := range s.List(ListParams{Limit: 10}).Items {
		if it.Artist == "" {
			t.Errorf("%s did not get its artist back", it.Path)
		}
	}
}

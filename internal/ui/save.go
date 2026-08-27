package ui

import (
	"runtime"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/remy/tag-manager/internal/catalog"
	"github.com/remy/tag-manager/internal/tags"
)

// saveResult reports one file's write outcome.
type saveResult struct {
	idx int32
	err error
}

// saveFinishedMsg is delivered once every write has been accounted for.
type saveFinishedMsg struct{}

// saveErr records a failure for display after the run.
type saveErr struct {
	path string
	err  error
}

// saver tracks an in-flight save.
type saver struct {
	ch      chan saveResult
	total   int
	done    int
	failed  int
	errs    []saveErr
	started time.Time
}

// saveWorkers bounds write concurrency. Tag writes are dominated by the write
// and fsync round trip rather than by computation, so a handful of workers
// hides the latency without hammering a network share.
func saveWorkers() int {
	n := runtime.NumCPU()
	if n > 8 {
		n = 8
	}
	if n < 2 {
		n = 2
	}
	return n
}

// startSave writes every changed track back to its file.
func (m *Model) startSave() tea.Cmd {
	if m.saver != nil {
		return nil // a save is already running
	}
	dirty := m.dirtyTracks()
	if len(dirty) == 0 {
		m.setStatus(statusInfo, "nothing to save")
		return nil
	}

	ch := make(chan saveResult, len(dirty))
	m.saver = &saver{ch: ch, total: len(dirty), started: time.Now()}

	jobs := make(chan int32)
	var wg sync.WaitGroup
	for i := 0; i < saveWorkers(); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				t := &m.cat.Tracks[idx]
				ch <- saveResult{idx: idx, err: tags.Write(t.Path, buildEdit(t))}
			}
		}()
	}
	go func() {
		for _, idx := range dirty {
			jobs <- idx
		}
		close(jobs)
		wg.Wait()
		close(ch)
	}()

	m.setStatus(statusInfo, "saving %d files…", len(dirty))
	return waitForSave(ch)
}

// waitForSave blocks on the next write result and hands it to the update loop.
func waitForSave(ch chan saveResult) tea.Cmd {
	return func() tea.Msg {
		r, ok := <-ch
		if !ok {
			return saveFinishedMsg{}
		}
		return r
	}
}

// buildEdit turns a track's changed-field set into a tag edit, so only the
// fields the user actually touched are written.
func buildEdit(t *catalog.Track) *tags.Edit {
	e := &tags.Edit{}
	for _, f := range t.Changed.Fields() {
		switch f {
		case catalog.FieldYear, catalog.FieldTrackNo, catalog.FieldDisc:
			e.SetInt(catalog.FieldNames[f], numericField(t, f))
		default:
			e.SetString(catalog.FieldNames[f], t.String(f))
		}
	}
	return e
}

func numericField(t *catalog.Track, f catalog.Field) int32 {
	switch f {
	case catalog.FieldYear:
		return t.Year
	case catalog.FieldTrackNo:
		return t.TrackNo
	case catalog.FieldDisc:
		return t.Disc
	}
	return 0
}

// applySaveResult folds one write outcome into the model.
func (m *Model) applySaveResult(r saveResult) tea.Cmd {
	s := m.saver
	if s == nil {
		return nil
	}
	s.done++
	t := &m.cat.Tracks[r.idx]
	if r.err != nil {
		s.failed++
		if len(s.errs) < 20 {
			s.errs = append(s.errs, saveErr{path: t.Path, err: r.err})
		}
	} else {
		// The file now matches the catalogue, and its modification time has
		// moved; refresh it so the next scan does not re-read the file.
		t.Changed = 0
		if fi, err := statFile(t.Path); err == nil {
			t.Size, t.ModTime = fi.Size(), fi.ModTime().Unix()
		}
	}
	return waitForSave(s.ch)
}

// finishSave reports the outcome and persists the refreshed catalogue.
func (m *Model) finishSave() {
	s := m.saver
	m.saver = nil
	if s == nil {
		return
	}
	elapsed := time.Since(s.started)
	if s.failed > 0 {
		m.setStatus(statusError, "saved %d of %d  ·  %d failed  ·  %s  ·  first: %s",
			s.done-s.failed, s.total, s.failed, FormatDuration(elapsed), firstErr(s.errs))
	} else {
		m.setStatus(statusOK, "saved %d files in %s", s.done, FormatDuration(elapsed))
	}
	// Persist the catalogue too: the sizes and modification times just changed
	// and a stale snapshot would make the next scan re-read every edited file.
	if m.catalogPath != "" {
		if err := catalog.Save(m.catalogPath, m.cat); err != nil {
			m.setStatus(statusWarn, "files saved, but the catalogue could not be updated: %v", err)
		}
	}
}

func firstErr(errs []saveErr) string {
	if len(errs) == 0 {
		return ""
	}
	return errs[0].err.Error()
}

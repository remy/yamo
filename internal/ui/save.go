package ui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/remy/yamo/internal/client"
	"github.com/remy/yamo/internal/library"
)

// saveState tracks a save in flight.
type saveState struct {
	total    int
	done     int
	failed   int
	conflict int
	started  time.Time
	firstErr string
}

// saveResultMsg reports one track's write.
type saveResultMsg struct {
	id       string
	err      error
	conflict bool
}

// saveFinishedMsg is delivered once every write has been accounted for.
type saveFinishedMsg struct{}

// saveTimeout bounds one file's write.
const saveTimeout = 60 * time.Second

// startSave writes every staged edit back through the API.
//
// Each track is a separate request carrying the version it was read at, so an
// edit made elsewhere since is reported as a conflict rather than silently
// overwritten. The API would take these as one batch, but a batch applies the
// same values to every track, and this is a set of individual corrections.
func (m *Model) startSave() tea.Cmd {
	if m.saving != nil {
		return nil
	}
	if m.src.dirtyCount() == 0 {
		m.setStatus(statusInfo, "nothing to save")
		return nil
	}

	type job struct {
		id      string
		changes library.Changes
		version string
	}
	jobs := make([]job, 0, len(m.src.staged))
	for id, e := range m.src.staged {
		jobs = append(jobs, job{id: id, changes: e.changes, version: e.version})
	}

	m.saving = &saveState{total: len(jobs), started: time.Now()}
	m.setStatus(statusInfo, "saving %s tracks…", FormatCount(len(jobs)))

	c := m.c
	cmds := make([]tea.Cmd, 0, len(jobs))
	for _, j := range jobs {
		j := j
		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), saveTimeout)
			defer cancel()
			_, err := c.PatchTrack(ctx, j.id, j.changes, j.version)
			return saveResultMsg{id: j.id, err: err, conflict: client.IsConflict(err)}
		})
	}
	return tea.Batch(cmds...)
}

// applySaveResult folds one write outcome into the model.
func (m *Model) applySaveResult(msg saveResultMsg) tea.Cmd {
	s := m.saving
	if s == nil {
		return nil
	}
	s.done++
	switch {
	case msg.conflict:
		s.conflict++
		if s.firstErr == "" {
			s.firstErr = "changed elsewhere since you read it"
		}
	case msg.err != nil:
		s.failed++
		if s.firstErr == "" {
			s.firstErr = msg.err.Error()
		}
	default:
		// Written; the edit is no longer pending. A conflicted one is kept, so
		// it stays visible and can be retried after a refresh.
		m.src.unstage(msg.id)
	}
	if s.done < s.total {
		return nil
	}
	return func() tea.Msg { return saveFinishedMsg{} }
}

// finishSave reports the outcome.
func (m *Model) finishSave() []tea.Cmd {
	s := m.saving
	m.saving = nil
	if s == nil {
		return nil
	}
	written := s.total - s.failed - s.conflict
	switch {
	case s.conflict > 0:
		m.setStatus(statusError,
			"saved %d of %d  ·  %d changed elsewhere and were kept pending  ·  press R to refresh",
			written, s.total, s.conflict)
	case s.failed > 0:
		m.setStatus(statusError, "saved %d of %d  ·  %d failed: %s", written, s.total, s.failed, s.firstErr)
	default:
		m.setStatus(statusOK, "saved %s tracks in %s", FormatCount(written), FormatDuration(time.Since(s.started)))
	}

	// The rows on screen came from before the write, so they must be refetched
	// rather than trusted.
	m.src.invalidate()
	return m.ensureVisible()
}

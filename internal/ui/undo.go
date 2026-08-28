package ui

import "github.com/remy/tag-manager/internal/library"

// fieldEdit is one field of one track changing value.
type fieldEdit struct {
	id    string
	field string
	old   string
	new   string
	track library.Track
}

// undoBatch groups the edits made by a single action. Bulk edits are the
// reason this exists: changing an album title across three hundred tracks has
// to be one undo step, not three hundred.
type undoBatch struct {
	label string
	edits []fieldEdit
}

// apply stages the new values.
func (b undoBatch) apply(s *source) {
	for _, e := range b.edits {
		s.stage(e.track, e.field, e.new)
	}
}

// revert stages the old values back.
//
// It does not clear the pending edit: the file may already have been saved
// once, in which case putting the original value back is itself a change that
// has to be written.
func (b undoBatch) revert(s *source) {
	for _, e := range b.edits {
		s.stage(e.track, e.field, e.old)
	}
}

// pushUndo records a batch and clears the redo stack, which is no longer
// reachable once a new edit branches off it.
func (m *Model) pushUndo(b undoBatch) {
	if len(b.edits) == 0 {
		return
	}
	m.undo = append(m.undo, b)
	m.redo = m.redo[:0]
	if len(m.undo) > maxUndo {
		m.undo = append(m.undo[:0], m.undo[len(m.undo)-maxUndo:]...)
	}
}

const maxUndo = 200

func (m *Model) doUndo() {
	if len(m.undo) == 0 {
		m.setStatus(statusInfo, "nothing to undo")
		return
	}
	b := m.undo[len(m.undo)-1]
	m.undo = m.undo[:len(m.undo)-1]
	b.revert(m.src)
	m.redo = append(m.redo, b)
	m.setStatus(statusOK, "undid %s on %d tracks", b.label, len(b.edits))
}

func (m *Model) doRedo() {
	if len(m.redo) == 0 {
		m.setStatus(statusInfo, "nothing to redo")
		return
	}
	b := m.redo[len(m.redo)-1]
	m.redo = m.redo[:len(m.redo)-1]
	b.apply(m.src)
	m.undo = append(m.undo, b)
	m.setStatus(statusOK, "redid %s on %d tracks", b.label, len(b.edits))
}

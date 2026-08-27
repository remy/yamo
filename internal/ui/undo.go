package ui

import "github.com/remy/tag-manager/internal/catalog"

// fieldEdit is one field of one track changing value.
type fieldEdit struct {
	idx   int32
	field catalog.Field
	old   string
	new   string
}

// undoBatch groups the edits made by a single user action. Bulk edits are the
// reason this exists: changing an album title across three hundred tracks has
// to be one undo step, not three hundred.
type undoBatch struct {
	label string
	edits []fieldEdit
}

// apply writes the new values.
func (b undoBatch) apply(c *catalog.Catalog) {
	for _, e := range b.edits {
		t := &c.Tracks[e.idx]
		t.SetString(e.field, e.new)
		t.Changed.Add(e.field)
		c.Touch(int(e.idx))
	}
}

// revert restores the old values. A track whose fields all match what is on
// disk again is no longer dirty, but that is not tracked here: the disk state
// is unknown once a save has happened, so reverting keeps the dirty flag and
// a later save simply writes the original values back.
func (b undoBatch) revert(c *catalog.Catalog) {
	for _, e := range b.edits {
		t := &c.Tracks[e.idx]
		t.SetString(e.field, e.old)
		t.Changed.Add(e.field)
		c.Touch(int(e.idx))
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

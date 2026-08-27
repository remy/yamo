package ui

import (
	"github.com/remy/tag-manager/internal/catalog"
)

// editField describes one slot in the edit panel.
type editField struct {
	Label    string
	Field    catalog.Field
	Numeric  bool
	Complete bool // offer suggestions drawn from values already in the library
}

// editFields is laid out in two columns of five, filled column-major: the
// first five entries form the left column, the rest the right. Text fields
// that benefit from a wide input sit on the left; the short numeric ones sit
// on the right where a narrow column costs nothing.
var editFields = []editField{
	{Label: "Title", Field: catalog.FieldTitle, Complete: true},
	{Label: "Artist", Field: catalog.FieldArtist, Complete: true},
	{Label: "Album", Field: catalog.FieldAlbum, Complete: true},
	{Label: "Album Artist", Field: catalog.FieldAlbumArtist, Complete: true},
	{Label: "Composer", Field: catalog.FieldComposer, Complete: true},

	{Label: "Track", Field: catalog.FieldTrackNo, Numeric: true},
	{Label: "Disc", Field: catalog.FieldDisc, Numeric: true},
	{Label: "Year", Field: catalog.FieldYear, Numeric: true},
	{Label: "Genre", Field: catalog.FieldGenre, Complete: true},
	{Label: "Comment", Field: catalog.FieldComment},
}

const editRows = 5 // rows per column; len(editFields) is 2*editRows

// mixedMarker stands in for a field whose value differs across a multi-track
// selection. Committing a different value overwrites all of them; leaving it
// alone leaves each track's own value untouched.
const mixedMarker = "⟨multiple⟩"

// editor holds the state of the edit panel.
type editor struct {
	active  bool
	focus   int  // index into editFields
	editing bool // true while the focused field's input is live
	in      input
	sug     suggestions
}

// suggestions is the autocomplete list shown under a field being edited.
type suggestions struct {
	items []catalog.ValueCount
	sel   int
	open  bool
}

func (s *suggestions) close() {
	s.items = nil
	s.sel = 0
	s.open = false
}

func (s *suggestions) move(delta int) {
	if len(s.items) == 0 {
		return
	}
	s.sel = (s.sel + delta + len(s.items)) % len(s.items)
}

func (s *suggestions) current() (catalog.ValueCount, bool) {
	if !s.open || s.sel < 0 || s.sel >= len(s.items) {
		return catalog.ValueCount{}, false
	}
	return s.items[s.sel], true
}

// maxSuggestions caps the dropdown so the panel cannot grow without bound.
const maxSuggestions = 6

// moveFocus walks the field grid. With two columns, vertical movement stays
// within a column and horizontal movement jumps between them; with one column
// the grid collapses to a single list and vertical movement covers all of it.
func (e *editor) moveFocus(cols, dx, dy int) {
	if cols < 2 {
		e.focus = clamp(e.focus+dy, 0, len(editFields)-1)
		return
	}
	col, row := e.focus/editRows, e.focus%editRows
	col = clamp(col+dx, 0, 1)
	row = clamp(row+dy, 0, editRows-1)
	e.focus = col*editRows + row
}

// nextFocus advances linearly, wrapping, for Tab and Shift-Tab.
func (e *editor) nextFocus(delta int) {
	n := len(editFields)
	e.focus = (e.focus + delta + n) % n
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// fieldValue returns the value to display for a field across the given tracks,
// and whether the tracks disagree about it.
func fieldValue(cat *catalog.Catalog, idxs []int32, f catalog.Field) (value string, mixed bool) {
	if len(idxs) == 0 {
		return "", false
	}
	first := cat.Tracks[idxs[0]].String(f)
	for _, i := range idxs[1:] {
		if cat.Tracks[i].String(f) != first {
			return first, true
		}
	}
	return first, false
}

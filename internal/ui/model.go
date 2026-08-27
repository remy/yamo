package ui

import (
	"sort"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/remy/tag-manager/internal/catalog"
)

// Mode is the interface's current input context. Every keystroke is dispatched
// through it, which keeps the key handling readable as the bindings grow.
type Mode int

const (
	ModeBrowse Mode = iota
	ModeSearch
	ModeEdit
	ModeHelp
	ModeConfirmQuit
)

// statusKind selects how a status message is coloured.
type statusKind int

const (
	statusInfo statusKind = iota
	statusOK
	statusWarn
	statusError
)

// Model is the whole application state.
type Model struct {
	cat         *catalog.Catalog
	catalogPath string
	theme       Theme

	width, height int
	ready         bool

	mode Mode

	search   input
	query    *catalog.Query
	results  []int32
	searchMS time.Duration

	cursor   int // index into results
	offset   int // first visible row
	selected map[int32]struct{}
	anchor   int

	cols     []Column
	sortCol  int // index into cols; -1 means catalogue order
	sortDesc bool

	ed editor

	undo []undoBatch
	redo []undoBatch

	status     string
	statusKind statusKind

	saver *saver

	showDetail  bool
	filterStale bool
	helpOffset  int
	quitting    bool
}

// New builds a model over an already-loaded catalogue.
func New(cat *catalog.Catalog, catalogPath string) *Model {
	m := &Model{
		cat:         cat,
		catalogPath: catalogPath,
		theme:       NewTheme(),
		cols:        DefaultColumns,
		sortCol:     -1,
		selected:    map[int32]struct{}{},
		query:       catalog.ParseQuery(""),
		showDetail:  true,
	}
	m.runSearch()
	m.setStatus(statusInfo, "%s tracks loaded  ·  press ? for help", FormatCount(cat.Len()))
	return m
}

// Init satisfies tea.Model.
func (m *Model) Init() tea.Cmd { return nil }

// Run starts the full-screen program.
func Run(cat *catalog.Catalog, catalogPath string) error {
	p := tea.NewProgram(New(cat, catalogPath), tea.WithAltScreen())
	_, err := p.Run()
	return err
}

func (m *Model) setStatus(kind statusKind, format string, args ...any) {
	m.status = sprintf(format, args...)
	m.statusKind = kind
}

// runSearch re-filters and re-sorts, keeping the cursor on the same track when
// it survives the new filter. Losing your place on every keystroke would make
// live search unusable.
func (m *Model) runSearch() {
	var keep int32 = -1
	if m.cursor >= 0 && m.cursor < len(m.results) {
		keep = m.results[m.cursor]
	}

	m.filterStale = false
	start := time.Now()
	m.query = catalog.ParseQuery(m.search.Value())
	m.results = m.cat.Index().Search(m.query)
	m.searchMS = time.Since(start)
	m.sortResults()

	m.cursor = 0
	if keep >= 0 {
		for i, r := range m.results {
			if r == keep {
				m.cursor = i
				break
			}
		}
	}
	m.clampCursor()
}

// sortResults orders the current result set by the active sort column.
func (m *Model) sortResults() {
	if m.sortCol < 0 || m.sortCol >= len(m.cols) {
		// Catalogue order is path order, which groups albums naturally.
		sort.Slice(m.results, func(i, j int) bool { return m.results[i] < m.results[j] })
		return
	}
	col := m.cols[m.sortCol]
	tracks := m.cat.Tracks
	less := func(i, j int) bool {
		a, b := &tracks[m.results[i]], &tracks[m.results[j]]
		c := compareTracks(a, b, col)
		if c == 0 {
			return a.Path < b.Path // stable, meaningful tiebreak
		}
		if m.sortDesc {
			return c > 0
		}
		return c < 0
	}
	sort.SliceStable(m.results, less)
}

// compareTracks orders two tracks by a column, numerically where the field is
// numeric and by folded text otherwise so case and accents do not scatter
// related values.
func compareTracks(a, b *catalog.Track, col Column) int {
	switch col.Field {
	case catalog.FieldYear:
		return cmpInt(a.Year, b.Year)
	case catalog.FieldTrackNo:
		return cmpInt(a.TrackNo, b.TrackNo)
	case catalog.FieldDisc:
		return cmpInt(a.Disc, b.Disc)
	}
	if col.Title == "Time" {
		return cmpInt(a.DurationMS, b.DurationMS)
	}
	if col.Render == nil {
		return 0
	}
	return cmpString(catalog.Fold(col.Render(a)), catalog.Fold(col.Render(b)))
}

func cmpInt(a, b int32) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func cmpString(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func (m *Model) clampCursor() {
	if len(m.results) == 0 {
		m.cursor, m.offset = 0, 0
		return
	}
	m.cursor = clamp(m.cursor, 0, len(m.results)-1)
	rows := m.visibleRows()
	if rows <= 0 {
		m.offset = 0
		return
	}
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+rows {
		m.offset = m.cursor - rows + 1
	}
	maxOff := len(m.results) - rows
	if maxOff < 0 {
		maxOff = 0
	}
	m.offset = clamp(m.offset, 0, maxOff)
}

// moveCursor shifts the selection by delta rows.
func (m *Model) moveCursor(delta int) {
	if len(m.results) == 0 {
		return
	}
	m.cursor = clamp(m.cursor+delta, 0, len(m.results)-1)
	m.clampCursor()
}

// currentTrack returns the index of the track under the cursor.
func (m *Model) currentTrack() (int32, bool) {
	if m.cursor < 0 || m.cursor >= len(m.results) {
		return 0, false
	}
	return m.results[m.cursor], true
}

// editTargets returns the tracks an edit would apply to: the marked set if
// there is one, otherwise just the track under the cursor.
func (m *Model) editTargets() []int32 {
	if len(m.selected) > 0 {
		out := make([]int32, 0, len(m.selected))
		// Iterate the result order so the "first" value is predictable rather
		// than whatever the map hands back.
		for _, r := range m.results {
			if _, ok := m.selected[r]; ok {
				out = append(out, r)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	if i, ok := m.currentTrack(); ok {
		return []int32{i}
	}
	return nil
}

// toggleSelect marks or unmarks the track under the cursor.
func (m *Model) toggleSelect() {
	i, ok := m.currentTrack()
	if !ok {
		return
	}
	if _, marked := m.selected[i]; marked {
		delete(m.selected, i)
	} else {
		m.selected[i] = struct{}{}
	}
	m.anchor = m.cursor
}

// selectRange marks every row between the anchor and the cursor.
func (m *Model) selectRange() {
	lo, hi := m.anchor, m.cursor
	if lo > hi {
		lo, hi = hi, lo
	}
	for i := lo; i <= hi && i < len(m.results); i++ {
		if i >= 0 {
			m.selected[m.results[i]] = struct{}{}
		}
	}
}

// commitField applies a value to every edit target as one undoable batch.
func (m *Model) commitField(f catalog.Field, value string) int {
	targets := m.editTargets()
	batch := undoBatch{label: catalog.FieldNames[f], edits: make([]fieldEdit, 0, len(targets))}
	for _, i := range targets {
		old := m.cat.Tracks[i].String(f)
		if old == value {
			continue
		}
		batch.edits = append(batch.edits, fieldEdit{idx: i, field: f, old: old, new: value})
	}
	if len(batch.edits) == 0 {
		return 0
	}
	batch.apply(m.cat)
	m.pushUndo(batch)
	m.refreshAfterEdit()
	return len(batch.edits)
}

// refreshAfterEdit updates the view after an edit without re-filtering.
//
// Re-running the search here would be the obvious thing to do and is the wrong
// thing to do: renaming an album that you found by searching for its old name
// makes every row you are working on fail the filter, so the list empties and
// the selection disappears at the exact moment you want to see the result. The
// result set is instead treated as a snapshot of the last query, and the user
// refreshes it when they are ready.
func (m *Model) refreshAfterEdit() {
	if !m.query.Empty() {
		m.filterStale = true
	}
	m.clampCursor()
}

// refreshFilter re-applies the current query, dropping tracks that no longer
// match and picking up ones that now do.
func (m *Model) refreshFilter() {
	before := len(m.results)
	m.runSearch()
	m.filterStale = false
	// Marks on tracks that fell out of the result set are no longer reachable,
	// so drop them rather than leaving an invisible selection behind.
	if len(m.selected) > 0 {
		visible := make(map[int32]struct{}, len(m.results))
		for _, r := range m.results {
			visible[r] = struct{}{}
		}
		for id := range m.selected {
			if _, ok := visible[id]; !ok {
				delete(m.selected, id)
			}
		}
	}
	m.setStatus(statusInfo, "filter refreshed: %s tracks (was %s)",
		FormatCount(len(m.results)), FormatCount(before))
}

// unwritableTargets counts edit targets whose container this build cannot
// write, grouped by format name. Reporting it while editing turns what would
// otherwise be a batch of save-time failures into something the user knows
// before they type.
func (m *Model) unwritableTargets() map[string]int {
	var out map[string]int
	for _, i := range m.editTargets() {
		f := m.cat.Tracks[i].Format
		if f.Writable() {
			continue
		}
		if out == nil {
			out = map[string]int{}
		}
		out[f.String()]++
	}
	return out
}

// dirtyTracks lists the indices of tracks with unsaved changes.
func (m *Model) dirtyTracks() []int32 {
	var out []int32
	for i := range m.cat.Tracks {
		if m.cat.Tracks[i].Dirty() {
			out = append(out, int32(i))
		}
	}
	return out
}

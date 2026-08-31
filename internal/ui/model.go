package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/remy/yamo/internal/client"
	"github.com/remy/yamo/internal/library"
)

// Mode is the interface's current input context.
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

// selection is the set of tracks an operation applies to.
//
// It can be a list of identifiers or, when everything matching is wanted, the
// query itself. Marking two thousand tracks must not mean holding two thousand
// identifiers and sending them back, so the query form is carried through to
// the server unchanged.
type selection struct {
	ids      map[string]bool
	all      bool
	query    string
	excluded map[string]bool
}

func newSelection() selection {
	return selection{ids: map[string]bool{}, excluded: map[string]bool{}}
}

func (s *selection) contains(id string) bool {
	if s.all {
		return !s.excluded[id]
	}
	return s.ids[id]
}

func (s *selection) toggle(id string) {
	if s.all {
		if s.excluded[id] {
			delete(s.excluded, id)
		} else {
			s.excluded[id] = true
		}
		return
	}
	if s.ids[id] {
		delete(s.ids, id)
	} else {
		s.ids[id] = true
	}
}

func (s *selection) add(id string) {
	if s.all {
		delete(s.excluded, id)
		return
	}
	s.ids[id] = true
}

func (s *selection) selectAll(query string) {
	*s = newSelection()
	s.all, s.query = true, query
}

func (s *selection) clear() { *s = newSelection() }

func (s *selection) empty() bool {
	return !s.all && len(s.ids) == 0
}

// count is how many tracks are marked. With a query selection the total comes
// from the server's match count.
func (s *selection) count(matching int) int {
	if s.all {
		return matching - len(s.excluded)
	}
	return len(s.ids)
}

// selector converts to the form the API takes.
func (s *selection) selector(expect *int) library.Selector {
	if s.all {
		sel := library.Selector{Query: s.query, ExpectCount: expect}
		if s.query == "" {
			sel = library.Selector{All: true, ExpectCount: expect}
		}
		for id := range s.excluded {
			sel.ExcludeIDs = append(sel.ExcludeIDs, id)
		}
		return sel
	}
	ids := make([]string, 0, len(s.ids))
	for id := range s.ids {
		ids = append(ids, id)
	}
	return library.Selector{IDs: ids}
}

// Model is the whole application state.
type Model struct {
	c     *client.Client
	src   *source
	theme Theme

	width, height int
	ready         bool
	mode          Mode

	search      input
	roots       []string
	filterStale bool

	cursor int // row index across the whole result set
	offset int // first visible row
	sel    selection
	anchor int

	cols     []Column
	sortCol  int
	sortDesc bool

	ed   editor
	undo []undoBatch
	redo []undoBatch

	art        map[string]*artInfo
	imgProto   ImageProtocol
	showDetail bool
	showArt    bool
	helpOffset int

	status     string
	statusKind statusKind

	saving     *saveState
	artWriting int
	quitting   bool
}

// New builds a model over a server connection.
func New(c *client.Client, roots []string) *Model {
	m := &Model{
		c:          c,
		src:        newSource(c),
		theme:      NewTheme(),
		cols:       DefaultColumns,
		sortCol:    -1,
		sel:        newSelection(),
		showDetail: true,
		art:        map[string]*artInfo{},
		imgProto:   DetectImageProtocol(),
		roots:      roots,
	}
	m.setStatus(statusInfo, "connected  ·  press ? for help")
	return m
}

// Init asks for the first page.
func (m *Model) Init() tea.Cmd { return tea.Batch(m.ensureVisible()...) }

// Run starts the full-screen browser against a server.
func Run(c *client.Client, roots []string) error {
	p := tea.NewProgram(New(c, roots), tea.WithAltScreen())
	_, err := p.Run()
	return err
}

func (m *Model) setStatus(kind statusKind, format string, args ...any) {
	m.status = sprintf(format, args...)
	m.statusKind = kind
}

// total is how many tracks match the current query.
func (m *Model) total() int { return m.src.total }

// trackAt returns the track at a row if it has been fetched.
func (m *Model) trackAt(row int) (library.Track, bool) { return m.src.rowAt(row) }

// currentTrack returns the track under the cursor.
func (m *Model) currentTrack() (library.Track, bool) { return m.trackAt(m.cursor) }

// ensureVisible requests any pages the viewport needs, plus a little either
// side so that scrolling does not stall at every boundary.
func (m *Model) ensureVisible() []tea.Cmd {
	rows := m.visibleRows()
	return m.src.ensure(m.offset-rows, m.offset+rows*2)
}

// sortSpec renders the active sort for the API.
func (m *Model) sortSpec() string {
	if m.sortCol < 0 || m.sortCol >= len(m.cols) {
		return ""
	}
	col := m.cols[m.sortCol]
	name := col.SortKey
	if name == "" {
		return ""
	}
	if m.sortDesc {
		return "-" + name
	}
	return name
}

// runSearch sends the current query, keeping the cursor where it can.
func (m *Model) runSearch() []tea.Cmd {
	m.src.setQuery(m.search.Value(), m.sortSpec())
	m.filterStale = false
	m.cursor, m.offset = 0, 0
	return m.ensureVisible()
}

// refreshFilter re-runs the query after edits have made the view stale.
func (m *Model) refreshFilter() []tea.Cmd {
	m.src.invalidate()
	m.filterStale = false
	m.setStatus(statusInfo, "refreshed")
	return m.ensureVisible()
}

func (m *Model) clampCursor() {
	total := m.total()
	if total == 0 {
		m.cursor, m.offset = 0, 0
		return
	}
	m.cursor = clamp(m.cursor, 0, total-1)
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
	m.offset = clamp(m.offset, 0, max(total-rows, 0))
}

// moveCursor shifts the selection by delta rows.
func (m *Model) moveCursor(delta int) []tea.Cmd {
	if m.total() == 0 {
		return nil
	}
	m.cursor = clamp(m.cursor+delta, 0, m.total()-1)
	m.clampCursor()
	return m.ensureVisible()
}

// editTargets returns the marked tracks that have been fetched, for showing
// values in the editor. The operation itself uses the selector, which may name
// far more than are in memory.
func (m *Model) editTargets() []library.Track {
	var out []library.Track
	if m.sel.empty() {
		if t, ok := m.currentTrack(); ok {
			return []library.Track{t}
		}
		return nil
	}
	for row := 0; row < m.total(); row++ {
		t, ok := m.trackAt(row)
		if !ok {
			continue
		}
		if m.sel.contains(t.ID) {
			out = append(out, t)
		}
		if len(out) >= 500 {
			break // enough to decide whether the values agree
		}
	}
	return out
}

// targetCount is how many tracks an edit would apply to.
func (m *Model) targetCount() int {
	if m.sel.empty() {
		if _, ok := m.currentTrack(); ok {
			return 1
		}
		return 0
	}
	return m.sel.count(m.total())
}

// toggleSelect marks or unmarks the track under the cursor.
func (m *Model) toggleSelect() {
	if t, ok := m.currentTrack(); ok {
		m.sel.toggle(t.ID)
		m.anchor = m.cursor
	}
}

// selectRange marks every fetched row between the anchor and the cursor.
func (m *Model) selectRange() {
	lo, hi := m.anchor, m.cursor
	if lo > hi {
		lo, hi = hi, lo
	}
	for i := lo; i <= hi; i++ {
		if t, ok := m.trackAt(i); ok {
			m.sel.add(t.ID)
		}
	}
}

// stageField records an edit locally, to be written on save.
//
// The API writes through, but the browser stages: it is where undo lives, and
// where a batch of related corrections is assembled before one deliberate
// keystroke commits them.
func (m *Model) stageField(field, value string) int {
	targets := m.editTargets()
	batch := undoBatch{label: field}
	for _, t := range targets {
		old := trackField(&t, field)
		if old == value {
			continue
		}
		batch.edits = append(batch.edits, fieldEdit{
			id: t.ID, field: field, old: old, new: value, track: t,
		})
	}
	if len(batch.edits) == 0 {
		return 0
	}
	batch.apply(m.src)
	m.pushUndo(batch)
	if m.search.Value() != "" {
		m.filterStale = true
	}
	return len(batch.edits)
}

// selectionSummary describes what an edit would touch, for the panel header.
func (m *Model) selectionSummary() string {
	n := m.targetCount()
	if n == 1 {
		return "1 track"
	}
	s := FormatCount(n) + " tracks"
	if m.sel.all {
		s += " (everything matching)"
	}
	loaded := len(m.editTargets())
	if loaded < n {
		s += "  ·  showing values from the " + FormatCount(loaded) + " read so far"
	}
	return s
}

func (m *Model) sortLabel() string {
	if m.sortCol < 0 || m.sortCol >= len(m.cols) {
		return "by path"
	}
	dir := "ascending"
	if m.sortDesc {
		dir = "descending"
	}
	return strings.ToLower(m.cols[m.sortCol].Title) + " " + dir
}

// cycleSort advances to the next sortable column, through the unsorted state.
func (m *Model) cycleSort(delta int) []tea.Cmd {
	for i := 0; i < len(m.cols)+1; i++ {
		m.sortCol += delta
		if m.sortCol >= len(m.cols) {
			m.sortCol = -1
		}
		if m.sortCol < -1 {
			m.sortCol = len(m.cols) - 1
		}
		if m.sortCol == -1 || m.cols[m.sortCol].SortKey != "" {
			break
		}
	}
	m.setStatus(statusInfo, "sort %s", m.sortLabel())
	return m.applySort()
}

// applySort re-requests the current query in the new order.
func (m *Model) applySort() []tea.Cmd {
	keep := ""
	if t, ok := m.currentTrack(); ok {
		keep = t.ID
	}
	m.src.setQuery(m.search.Value(), m.sortSpec())
	m.cursor, m.offset = 0, 0
	_ = keep // the row a track lands on is only known once the page arrives
	return m.ensureVisible()
}

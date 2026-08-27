package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// Update handles one message. Key handling is split by mode so that each
// context owns its whole binding set rather than sharing one large switch with
// mode checks scattered through it.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		m.clampCursor()
		return m, nil

	case saveResult:
		return m, m.applySaveResult(msg)

	case saveFinishedMsg:
		m.finishSave()
		if m.quitting {
			return m, tea.Quit
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *Model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	// A save in flight blocks editing, but must not trap the user.
	if m.saver != nil {
		if k.String() == "ctrl+c" {
			return m, tea.Quit
		}
		return m, nil
	}

	switch m.mode {
	case ModeSearch:
		return m.keySearch(k)
	case ModeEdit:
		return m.keyEdit(k)
	case ModeHelp:
		return m.keyHelp(k)
	case ModeConfirmQuit:
		return m.keyConfirmQuit(k)
	default:
		return m.keyBrowse(k)
	}
}

// keyHelp scrolls the key reference; anything else closes it.
func (m *Model) keyHelp(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "down", "j":
		m.helpOffset++
	case "up", "k":
		m.helpOffset--
	case "pgdown", "ctrl+d", " ":
		m.helpOffset += m.visibleRows()
	case "pgup", "ctrl+u":
		m.helpOffset -= m.visibleRows()
	case "g", "home":
		m.helpOffset = 0
	default:
		m.mode = ModeBrowse
		m.helpOffset = 0
	}
	if m.helpOffset < 0 {
		m.helpOffset = 0
	}
	return m, nil
}

func (m *Model) keyBrowse(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "q":
		if m.cat.DirtyCount() > 0 {
			m.mode = ModeConfirmQuit
			return m, nil
		}
		return m, tea.Quit

	case "/":
		m.mode = ModeSearch
		m.search.End()
		return m, nil

	case "esc":
		switch {
		case len(m.selected) > 0:
			m.selected = map[int32]struct{}{}
			m.setStatus(statusInfo, "selection cleared")
		case !m.search.Empty():
			m.search.Clear()
			m.runSearch()
			m.setStatus(statusInfo, "filter cleared")
		}
		return m, nil

	case "j", "down":
		m.moveCursor(1)
	case "k", "up":
		m.moveCursor(-1)
	case "ctrl+d":
		m.moveCursor(m.visibleRows() / 2)
	case "ctrl+u":
		m.moveCursor(-m.visibleRows() / 2)
	case "pgdown", "ctrl+f":
		m.moveCursor(m.visibleRows())
	case "pgup", "ctrl+b":
		m.moveCursor(-m.visibleRows())
	case "g", "home":
		m.cursor = 0
		m.clampCursor()
	case "G", "end":
		m.cursor = len(m.results) - 1
		m.clampCursor()

	case " ":
		m.toggleSelect()
		m.moveCursor(1)
	case "v":
		m.selectRange()
		m.setStatus(statusInfo, "%d selected", len(m.selected))
	case "a":
		for _, r := range m.results {
			m.selected[r] = struct{}{}
		}
		m.setStatus(statusInfo, "%d selected", len(m.selected))
	case "n":
		m.selected = map[int32]struct{}{}
		m.setStatus(statusInfo, "selection cleared")

	case "e", "enter":
		if len(m.results) == 0 {
			m.setStatus(statusWarn, "nothing to edit")
			return m, nil
		}
		m.mode = ModeEdit
		m.ed = editor{active: true}
		return m, nil

	case "s":
		m.cycleSort(1)
	case "S":
		m.sortDesc = !m.sortDesc
		m.sortResults()
		m.clampCursor()
		m.setStatus(statusInfo, "sort %s", m.sortLabel())

	case "ctrl+s":
		return m, m.startSave()

	case "u":
		m.doUndo()
	case "ctrl+r":
		m.doRedo()

	case "i":
		m.showDetail = !m.showDetail
		m.clampCursor()

	case "R", "ctrl+l":
		m.refreshFilter()

	case "?":
		m.mode = ModeHelp
		m.helpOffset = 0
	}
	return m, nil
}

func (m *Model) keySearch(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "enter":
		m.mode = ModeBrowse
		return m, nil
	case "up":
		m.moveCursor(-1)
		return m, nil
	case "down":
		m.moveCursor(1)
		return m, nil
	case "pgup":
		m.moveCursor(-m.visibleRows())
		return m, nil
	case "pgdown":
		m.moveCursor(m.visibleRows())
		return m, nil
	case "left":
		m.search.Left()
		return m, nil
	case "right":
		m.search.Right()
		return m, nil
	case "home", "ctrl+a":
		m.search.Home()
		return m, nil
	case "end", "ctrl+e":
		m.search.End()
		return m, nil
	case "backspace":
		m.search.Backspace()
	case "delete":
		m.search.Delete()
	case "ctrl+w":
		m.search.DeleteWordBack()
	case "ctrl+u":
		m.search.Clear()
	case "ctrl+k":
		m.search.DeleteToEnd()
	default:
		if !insertPrintable(&m.search, k) {
			return m, nil
		}
	}
	m.runSearch()
	return m, nil
}

// insertPrintable appends typed text to an input, ignoring control keys.
func insertPrintable(in *input, k tea.KeyMsg) bool {
	switch k.Type {
	case tea.KeyRunes:
		in.InsertString(string(k.Runes))
		return true
	case tea.KeySpace:
		in.Insert(' ')
		return true
	}
	return false
}

func (m *Model) keyEdit(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	e := &m.ed
	key := k.String()

	if key == "ctrl+c" {
		return m, tea.Quit
	}
	if key == "ctrl+s" {
		if e.editing {
			m.commitEditingField()
		}
		return m, m.startSave()
	}

	if !e.editing {
		return m.keyEditNavigate(k)
	}

	// The field's input is live.
	switch key {
	case "esc":
		e.editing = false
		e.sug.close()
		return m, nil
	case "enter":
		m.commitEditingField()
		return m, nil
	case "tab":
		// Tab takes the highlighted suggestion when one is showing, and
		// otherwise moves on to the next field.
		if v, ok := e.sug.current(); ok {
			e.in.SetValue(v.Value)
			e.sug.close()
			return m, nil
		}
		m.commitEditingField()
		e.nextFocus(1)
		m.beginEditing()
		return m, nil
	case "shift+tab":
		m.commitEditingField()
		e.nextFocus(-1)
		m.beginEditing()
		return m, nil
	case "down":
		if e.sug.open {
			e.sug.move(1)
			return m, nil
		}
		m.commitEditingField()
		e.moveFocus(m.editColumns(), 0, 1)
		m.beginEditing()
		return m, nil
	case "up":
		if e.sug.open {
			e.sug.move(-1)
			return m, nil
		}
		m.commitEditingField()
		e.moveFocus(m.editColumns(), 0, -1)
		m.beginEditing()
		return m, nil
	case "left":
		e.in.Left()
		return m, nil
	case "right":
		e.in.Right()
		return m, nil
	case "home", "ctrl+a":
		e.in.Home()
		return m, nil
	case "end", "ctrl+e":
		e.in.End()
		return m, nil
	case "backspace":
		e.in.Backspace()
	case "delete":
		e.in.Delete()
	case "ctrl+w":
		e.in.DeleteWordBack()
	case "ctrl+u":
		e.in.Clear()
	case "ctrl+k":
		e.in.DeleteToEnd()
	default:
		if !insertPrintable(&e.in, k) {
			return m, nil
		}
	}
	m.refreshSuggestions()
	return m, nil
}

func (m *Model) keyEditNavigate(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	e := &m.ed
	switch k.String() {
	case "esc", "q":
		m.mode = ModeBrowse
		e.active = false
	case "enter", "i":
		m.beginEditing()
	case "tab":
		e.nextFocus(1)
	case "shift+tab":
		e.nextFocus(-1)
	case "up", "k":
		e.moveFocus(m.editColumns(), 0, -1)
	case "down", "j":
		e.moveFocus(m.editColumns(), 0, 1)
	case "left", "h":
		e.moveFocus(m.editColumns(), -1, 0)
	case "right", "l":
		e.moveFocus(m.editColumns(), 1, 0)
	case "J":
		m.moveCursor(1)
	case "K":
		m.moveCursor(-1)
	case "u":
		m.doUndo()
	case "ctrl+r":
		m.doRedo()
	case "?":
		m.mode = ModeHelp
	default:
		// Typing a printable character starts editing with that character,
		// which is what a spreadsheet does and what the fingers expect.
		if k.Type == tea.KeyRunes {
			m.beginEditing()
			m.ed.in.Clear()
			m.ed.in.InsertString(string(k.Runes))
			m.refreshSuggestions()
		}
	}
	return m, nil
}

// beginEditing loads the focused field's current value into the input.
func (m *Model) beginEditing() {
	e := &m.ed
	targets := m.editTargets()
	if len(targets) == 0 {
		return
	}
	f := editFields[e.focus]
	value, mixed := fieldValue(m.cat, targets, f.Field)
	if mixed {
		value = "" // start blank rather than silently proposing one track's value
	}
	e.in.SetValue(value)
	e.editing = true
	e.sug.close()
}

// commitEditingField applies the input to every edit target.
func (m *Model) commitEditingField() {
	e := &m.ed
	if !e.editing {
		return
	}
	f := editFields[e.focus]
	value := strings.TrimSpace(e.in.Value())
	e.editing = false
	e.sug.close()

	n := m.commitField(f.Field, value)
	switch {
	case n == 0:
		m.setStatus(statusInfo, "%s unchanged", f.Label)
	case n == 1:
		m.setStatus(statusOK, "%s set", f.Label)
	default:
		m.setStatus(statusOK, "%s set on %d tracks", f.Label, n)
	}
}

// refreshSuggestions recomputes the autocomplete list for the live input.
func (m *Model) refreshSuggestions() {
	e := &m.ed
	f := editFields[e.focus]
	if !f.Complete {
		e.sug.close()
		return
	}
	items := m.cat.Index().Values(f.Field).Complete(e.in.Value(), maxSuggestions)
	// A single suggestion identical to what is typed tells the user nothing.
	if len(items) == 1 && items[0].Value == e.in.Value() {
		items = nil
	}
	e.sug.items = items
	e.sug.sel = 0
	e.sug.open = len(items) > 0
}

func (m *Model) keyConfirmQuit(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "y", "Y":
		return m, tea.Quit
	case "s", "S":
		m.quitting = true
		m.mode = ModeBrowse
		return m, m.startSave()
	case "ctrl+c":
		return m, tea.Quit
	default:
		m.mode = ModeBrowse
	}
	return m, nil
}

// cycleSort advances to the next sortable column, passing through the
// unsorted (catalogue order) state.
func (m *Model) cycleSort(delta int) {
	for i := 0; i < len(m.cols)+1; i++ {
		m.sortCol += delta
		if m.sortCol >= len(m.cols) {
			m.sortCol = -1
		}
		if m.sortCol < -1 {
			m.sortCol = len(m.cols) - 1
		}
		if m.sortCol == -1 || m.cols[m.sortCol].Title != "" {
			break
		}
	}
	m.sortResults()
	m.clampCursor()
	m.setStatus(statusInfo, "sort %s", m.sortLabel())
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

func (m *Model) doUndo() {
	if len(m.undo) == 0 {
		m.setStatus(statusInfo, "nothing to undo")
		return
	}
	b := m.undo[len(m.undo)-1]
	m.undo = m.undo[:len(m.undo)-1]
	b.revert(m.cat)
	m.redo = append(m.redo, b)
	m.refreshAfterEdit()
	m.setStatus(statusOK, "undid %s on %d tracks", b.label, len(b.edits))
}

func (m *Model) doRedo() {
	if len(m.redo) == 0 {
		m.setStatus(statusInfo, "nothing to redo")
		return
	}
	b := m.redo[len(m.redo)-1]
	m.redo = m.redo[:len(m.redo)-1]
	b.apply(m.cat)
	m.undo = append(m.undo, b)
	m.refreshAfterEdit()
	m.setStatus(statusOK, "redid %s on %d tracks", b.label, len(b.edits))
}

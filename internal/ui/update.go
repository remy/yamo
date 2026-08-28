package ui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/remy/tag-manager/internal/client"
	"github.com/remy/tag-manager/internal/library"
)

// searchTickMsg fires when typing has settled.
type searchTickMsg struct{ gen int }

// Update handles one message. Key handling is split by mode so that each
// context owns its whole binding set rather than sharing one large switch.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		m.clampCursor()
		return m, tea.Batch(append(m.ensureVisible(), m.currentArtCmd())...)

	case pageLoadedMsg:
		if msg.err != nil {
			m.src.apply(msg) // clears the in-flight marker
			m.setStatus(statusError, "could not read the library: %v", msg.err)
			return m, nil
		}
		if !m.src.apply(msg) {
			return m, nil
		}
		m.clampCursor()
		return m, tea.Batch(append(m.ensureVisible(), m.currentArtCmd())...)

	case searchTickMsg:
		// Only the newest keystroke's timer runs the search; the rest are the
		// intermediate states the user typed through.
		if msg.gen != m.searchGen {
			return m, nil
		}
		return m, tea.Batch(m.runSearch()...)

	case artLoadedMsg:
		m.applyArtLoaded(msg)
		return m, nil

	case suggestionsMsg:
		// Ignore a reply for a field or prefix the user has already left.
		if !m.ed.editing || editFields[m.ed.focus].Field != msg.field || m.ed.in.Value() != msg.prefix {
			return m, nil
		}
		items := msg.items
		// A single suggestion identical to what is typed tells the user nothing.
		if len(items) == 1 && items[0].Value == msg.prefix {
			items = nil
		}
		m.ed.sug.items = items
		m.ed.sug.sel = 0
		m.ed.sug.open = len(items) > 0
		return m, nil

	case artCopiedMsg:
		if msg.err != nil {
			m.setStatus(statusError, "could not copy artwork: %v", msg.err)
			return m, nil
		}
		m.setStatus(statusOK, "copied %s", msg.info.Summary)
		return m, nil

	case artPastedMsg:
		return m, m.finishPaste(msg)

	case saveResultMsg:
		return m, m.applySaveResult(msg)

	case saveFinishedMsg:
		cmds := m.finishSave()
		if m.quitting {
			return m, tea.Quit
		}
		return m, tea.Batch(cmds...)

	case library.Event:
		return m, m.applyServerEvent(msg)

	case tea.KeyMsg:
		before := m.cursor
		model, cmd := m.handleKey(msg)
		if m.cursor != before {
			if load := m.currentArtCmd(); load != nil {
				return model, tea.Batch(cmd, load)
			}
		}
		return model, cmd
	}
	return m, nil
}

// finishPaste folds an artwork write back into the view.
func (m *Model) finishPaste(msg artPastedMsg) tea.Cmd {
	m.artWriting = 0
	if msg.err != nil {
		m.setStatus(statusError, "could not write artwork: %v", msg.err)
		return nil
	}
	var res library.BatchResult
	if err := client.DecodeResult(msg.job, &res); err != nil {
		m.setStatus(statusError, "artwork written, but the result could not be read: %v", err)
		return nil
	}
	switch {
	case res.Failed > 0:
		m.setStatus(statusError, "wrote artwork to %d tracks, %d failed", res.Changed, res.Failed)
	case res.Changed == 0:
		m.setStatus(statusInfo, "those tracks already had that image")
	default:
		m.setStatus(statusOK, "wrote artwork to %s tracks", FormatCount(res.Changed))
	}
	// The files changed, so both the rows and any cached covers are stale.
	m.art = map[string]*artInfo{}
	m.src.invalidate()
	return tea.Batch(append(m.ensureVisible(), m.currentArtCmd())...)
}

// applyServerEvent reacts to a change made by another client.
func (m *Model) applyServerEvent(e library.Event) tea.Cmd {
	switch e.Type {
	case library.EventCatalogReplaced:
		m.src.invalidate()
		m.setStatus(statusInfo, "the library was rescanned")
		return tea.Batch(m.ensureVisible()...)
	case library.EventTracksChanged:
		// Ignore the echo of this client's own writes; a save refetches
		// anyway, and refetching mid-save would fight it.
		if m.saving != nil {
			return nil
		}
		for _, id := range e.TrackIDs {
			delete(m.art, id)
		}
		m.src.invalidate()
		return tea.Batch(m.ensureVisible()...)
	}
	return nil
}

func (m *Model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	// A save or an artwork write in flight blocks editing, but must not trap
	// the user.
	if m.saving != nil || m.artWriting > 0 {
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
	m.helpOffset = max(m.helpOffset, 0)
	return m, nil
}

func (m *Model) keyBrowse(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "q":
		if m.src.dirtyCount() > 0 {
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
		case !m.sel.empty():
			m.sel.clear()
			m.setStatus(statusInfo, "selection cleared")
		case !m.search.Empty():
			m.search.Clear()
			m.setStatus(statusInfo, "filter cleared")
			return m, tea.Batch(m.runSearch()...)
		}
		return m, nil

	case "j", "down":
		return m, tea.Batch(m.moveCursor(1)...)
	case "k", "up":
		return m, tea.Batch(m.moveCursor(-1)...)
	case "ctrl+d":
		return m, tea.Batch(m.moveCursor(m.visibleRows() / 2)...)
	case "ctrl+u":
		return m, tea.Batch(m.moveCursor(-m.visibleRows() / 2)...)
	case "pgdown", "ctrl+f":
		return m, tea.Batch(m.moveCursor(m.visibleRows())...)
	case "pgup", "ctrl+b":
		return m, tea.Batch(m.moveCursor(-m.visibleRows())...)
	case "g", "home":
		m.cursor = 0
		m.clampCursor()
		return m, tea.Batch(m.ensureVisible()...)
	case "G", "end":
		m.cursor = m.total() - 1
		m.clampCursor()
		return m, tea.Batch(m.ensureVisible()...)

	case " ":
		m.toggleSelect()
		return m, tea.Batch(m.moveCursor(1)...)
	case "v":
		m.selectRange()
		m.setStatus(statusInfo, "%s selected", FormatCount(m.sel.count(m.total())))
	case "a":
		// Marking by query rather than by identifier: this may be a hundred
		// thousand tracks, and only the query travels.
		m.sel.selectAll(m.search.Value())
		m.setStatus(statusInfo, "%s selected — everything matching", FormatCount(m.total()))
	case "n":
		m.sel.clear()
		m.setStatus(statusInfo, "selection cleared")

	case "e", "enter":
		if m.total() == 0 {
			m.setStatus(statusWarn, "nothing to edit")
			return m, nil
		}
		m.mode = ModeEdit
		m.ed = editor{active: true}
		return m, nil

	case "s":
		return m, tea.Batch(m.cycleSort(1)...)
	case "S":
		m.sortDesc = !m.sortDesc
		m.setStatus(statusInfo, "sort %s", m.sortLabel())
		return m, tea.Batch(m.applySort()...)

	case "ctrl+s":
		return m, m.startSave()

	case "u":
		m.doUndo()
	case "ctrl+r":
		m.doRedo()

	case "i":
		m.showDetail = !m.showDetail
		m.clampCursor()
		return m, m.currentArtCmd()

	case "A":
		m.showArt = !m.showArt
		m.clampCursor()
		return m, m.currentArtCmd()

	case "y":
		return m, m.yankArt()
	case "p":
		return m, m.pasteArt()

	case "R", "ctrl+l":
		return m, tea.Batch(m.refreshFilter()...)

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
		return m, tea.Batch(m.moveCursor(-1)...)
	case "down":
		return m, tea.Batch(m.moveCursor(1)...)
	case "pgup":
		return m, tea.Batch(m.moveCursor(-m.visibleRows())...)
	case "pgdown":
		return m, tea.Batch(m.moveCursor(m.visibleRows())...)
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
	return m, m.debounceSearch()
}

// debounceSearch schedules a search once typing settles.
func (m *Model) debounceSearch() tea.Cmd {
	m.searchGen++
	gen := m.searchGen
	return tea.Tick(searchDebounce, func(time.Time) tea.Msg { return searchTickMsg{gen: gen} })
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
		return m, m.beginEditing()
	case "shift+tab":
		m.commitEditingField()
		e.nextFocus(-1)
		return m, m.beginEditing()
	case "down":
		if e.sug.open {
			e.sug.move(1)
			return m, nil
		}
		m.commitEditingField()
		e.moveFocus(m.editColumns(), 0, 1)
		return m, m.beginEditing()
	case "up":
		if e.sug.open {
			e.sug.move(-1)
			return m, nil
		}
		m.commitEditingField()
		e.moveFocus(m.editColumns(), 0, -1)
		return m, m.beginEditing()
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
	return m, m.refreshSuggestions()
}

func (m *Model) keyEditNavigate(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	e := &m.ed
	switch k.String() {
	case "esc", "q":
		m.mode = ModeBrowse
		e.active = false
	case "enter", "i":
		return m, m.beginEditing()
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
		return m, tea.Batch(m.moveCursor(1)...)
	case "K":
		return m, tea.Batch(m.moveCursor(-1)...)
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
			cmd := m.beginEditing()
			m.ed.in.Clear()
			m.ed.in.InsertString(string(k.Runes))
			return m, tea.Batch(cmd, m.refreshSuggestions())
		}
	}
	return m, nil
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

// beginEditing loads the focused field's current value into the input.
func (m *Model) beginEditing() tea.Cmd {
	e := &m.ed
	targets := m.editTargets()
	if len(targets) == 0 {
		return nil
	}
	f := editFields[e.focus]
	value, mixed := fieldValue(targets, f.Field)
	if mixed {
		value = "" // start blank rather than silently proposing one track's value
	}
	e.in.SetValue(value)
	e.editing = true
	e.sug.close()
	return nil
}

// commitEditingField stages the input against every edit target.
func (m *Model) commitEditingField() {
	e := &m.ed
	if !e.editing {
		return
	}
	f := editFields[e.focus]
	value := strings.TrimSpace(e.in.Value())
	e.editing = false
	e.sug.close()

	n := m.stageField(f.Field, value)
	switch {
	case n == 0:
		m.setStatus(statusInfo, "%s unchanged", f.Label)
	case n == 1:
		m.setStatus(statusOK, "%s set  ·  ^s to write", f.Label)
	default:
		m.setStatus(statusOK, "%s set on %d tracks  ·  ^s to write", f.Label, n)
	}
}

// suggestionsMsg carries autocomplete candidates back from the server.
type suggestionsMsg struct {
	field  string
	prefix string
	items  []client.ValueCount
}

// refreshSuggestions asks the server for values matching what is being typed.
func (m *Model) refreshSuggestions() tea.Cmd {
	e := &m.ed
	f := editFields[e.focus]
	if !f.Complete {
		e.sug.close()
		return nil
	}
	c, field, prefix := m.c, f.Field, e.in.Value()
	return func() tea.Msg {
		ctx, cancel := contextWithTimeout(5 * time.Second)
		defer cancel()
		items, err := c.Values(ctx, field, prefix, maxSuggestions)
		if err != nil {
			return suggestionsMsg{field: field, prefix: prefix}
		}
		return suggestionsMsg{field: field, prefix: prefix, items: items}
	}
}

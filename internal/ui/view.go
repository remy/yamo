package ui

import (
	"fmt"
	"strings"
)

// Fixed chrome: the top rule, the search bar, the two rules bracketing the
// header, the header itself, the rule closing the table, the status line and
// the bottom rule.
const chromeRows = 8

// View renders the whole screen as one string.
//
// The frame is drawn by hand rather than composed from styled boxes, because
// the vertical rules have to line up with the junctions in every horizontal
// rule. Every cell that goes into a line is padded to an exact width first,
// and styles only ever add colour.
func (m *Model) View() string {
	if !m.ready {
		return "" // no size yet; drawing now would flash a wrong layout
	}
	inner := m.width - 2
	if m.width < 34 || m.height < 12 {
		return m.renderTooSmall()
	}

	lay := ComputeLayout(m.cols, inner)
	panelH := m.panelRows()
	panel := m.renderPanel(inner, panelH)
	rows := m.visibleRows()

	var b strings.Builder
	b.Grow(m.width * m.height)

	writeLine := func(s string) { b.WriteString(s); b.WriteByte('\n') }
	border := func(s string) string { return m.theme.Border.Render(s) }
	wrap := func(content string) string { return border(lineV) + content + border(lineV) }

	writeLine(border(titledRule(inner, m.headerTitle())))
	writeLine(wrap(m.renderSearchBar(inner)))

	if m.mode == ModeHelp {
		// Help owns the whole area; drawing the table's column rules behind it
		// would imply a grid that is not there.
		writeLine(border(midRule(inner, nil)))
		writeLine(wrap(" " + m.theme.Title.Render(Pad("keys", inner-2)) + " "))
		writeLine(border(midRule(inner, nil)))
		for _, line := range m.helpWindow(inner, rows) {
			writeLine(wrap(line))
		}
		writeLine(border(midRule(inner, nil)))
	} else {
		writeLine(border(openRule(inner, lay.Seps)))
		writeLine(wrap(m.renderHeaderRow(lay)))
		writeLine(border(midRule(inner, lay.Seps)))
		for i := 0; i < rows; i++ {
			writeLine(wrap(m.renderRow(lay, m.offset+i)))
		}
		writeLine(border(closeRule(inner, lay.Seps)))
	}

	if len(panel) > 0 {
		for _, line := range panel {
			writeLine(wrap(line))
		}
		writeLine(border(midRule(inner, nil)))
	}
	writeLine(wrap(m.renderStatus(inner)))
	b.WriteString(border(botRule(inner, nil)))
	return b.String()
}

func (m *Model) renderTooSmall() string {
	msg := fmt.Sprintf("terminal too small (%dx%d) — needs at least 34x12", m.width, m.height)
	return m.theme.Warn.Render(Truncate(msg, max(m.width, 1)))
}

// minTableRows is the smallest table worth showing. The panel is capped so at
// least this many track rows survive, because an editor with no visible
// context is worse than a slightly clipped panel.
const minTableRows = 2

// visibleRows is how many track rows fit in the current window.
func (m *Model) visibleRows() int {
	p := m.panelRows()
	extra := 0
	if p > 0 {
		extra = p + 1 // the panel plus the rule above the status line
	}
	n := m.height - chromeRows - extra
	if n < 1 {
		return 1
	}
	return n
}

// panelRows is the height granted to the detail or edit panel, excluding its
// rules. It is the smaller of what the panel wants and what the window can
// spare; the renderer then produces exactly this many lines.
func (m *Model) panelRows() int {
	want := m.panelWants()
	if want == 0 {
		return 0
	}
	// The chrome, the rule above the status line, and a minimal table.
	budget := m.height - chromeRows - 1 - minTableRows
	if budget < 1 {
		return 0
	}
	return min(want, budget)
}

// panelWants is the panel's natural height, before the window is considered.
func (m *Model) panelWants() int {
	switch m.mode {
	case ModeEdit:
		n := 1 + m.editFieldRows()
		if m.ed.sug.open {
			n += len(m.ed.sug.items) + 2
		}
		return n
	case ModeHelp:
		return 0
	default:
		if m.showArt && m.height >= 22 {
			return artPanelRows
		}
		if m.showDetail && m.height >= 18 {
			return 4
		}
		return 0
	}
}

// editColumns picks a one- or two-column field grid based on the width
// available. Two narrow columns are worse than one usable one.
func (m *Model) editColumns() int {
	if m.width-2 >= 72 {
		return 2
	}
	return 1
}

func (m *Model) editFieldRows() int {
	if m.editColumns() == 2 {
		return editRows
	}
	return len(editFields)
}

func (m *Model) headerTitle() string {
	if len(m.roots) == 0 {
		return "tagmgr"
	}
	return "tagmgr  " + strings.Join(m.roots, "  ")
}

// renderSearchBar draws the query line with the result counts on the right.
func (m *Model) renderSearchBar(inner int) string {
	th := m.theme
	right := FormatCount(m.total()) + " matching"
	if m.filterStale {
		right = "edited · R to refresh  ·  " + right
	}
	if n := m.sel.count(m.total()); n > 0 {
		right = fmt.Sprintf("%s selected  ·  %s", FormatCount(n), right)
	}
	if n := m.src.dirtyCount(); n > 0 {
		right = th.Dirty.Render(fmt.Sprintf("%d unsaved", n)) + th.Dim.Render("  ·  ") + th.Dim.Render(right)
	} else {
		right = th.Dim.Render(right)
	}
	rightW := DisplayWidth(stripANSI(right))

	prompt := " / "
	promptStyled := th.Dim.Render(prompt)
	if m.mode == ModeSearch {
		promptStyled = th.Accent.Render(prompt)
	}

	fieldW := inner - DisplayWidth(prompt) - rightW - 2
	if fieldW < 4 {
		fieldW = 4
		rightW = 0
		right = ""
	}
	field := m.search.Render(fieldW, m.mode == ModeSearch, th, "search: artist:elvis  year:>1980  -genre:live")
	pad := inner - DisplayWidth(prompt) - fieldW - rightW - 1
	if pad < 0 {
		pad = 0
	}
	return promptStyled + field + strings.Repeat(" ", pad) + right + " "
}

// renderHeaderRow draws the column titles with a sort indicator.
func (m *Model) renderHeaderRow(lay Layout) string {
	th := m.theme
	cells := make([]string, len(lay.Cols))
	for i, c := range lay.Cols {
		title := c.Title
		if i == m.sortCol && title != "" {
			arrow := "▲"
			if m.sortDesc {
				arrow = "▼"
			}
			title = Truncate(title, max(lay.Widths[i]-1, 0)) + arrow
		}
		if c.Right {
			cells[i] = th.Header.Render(PadLeft(title, lay.Widths[i]))
		} else {
			cells[i] = th.Header.Render(Pad(title, lay.Widths[i]))
		}
	}
	return strings.Join(cells, th.Border.Render(lineV))
}

func (m *Model) renderEmptyRow(lay Layout) string {
	return strings.Join(emptyCells(lay), m.theme.Border.Render(lineV))
}

func emptyCells(lay Layout) []string {
	cells := make([]string, len(lay.Cols))
	for i := range lay.Cols {
		cells[i] = strings.Repeat(" ", lay.Widths[i])
	}
	return cells
}

// renderRow draws one track. The row under the cursor is drawn as a single
// styled band, separators included, so the highlight reads as one object.
//
// A row whose page has not arrived yet is drawn empty rather than skipped, so
// the table keeps its shape while the window is being fetched.
func (m *Model) renderRow(lay Layout, row int) string {
	th := m.theme
	track, loaded := m.trackAt(row)
	isCursor := row == m.cursor
	if !loaded {
		if isCursor {
			return th.Cursor.Render(strings.Join(emptyCells(lay), lineV))
		}
		return m.renderEmptyRow(lay)
	}
	t := &track
	marked := m.sel.contains(t.ID)
	dirty := m.src.isDirty(t.ID)

	plain := make([]string, len(lay.Cols))
	for i, c := range lay.Cols {
		w := lay.Widths[i]
		var v string
		if i == 0 {
			v = gutter(marked, dirty)
		} else if c.Render != nil {
			v = c.Render(t)
		}
		if c.Right {
			plain[i] = PadLeft(v, w)
		} else {
			plain[i] = Pad(v, w)
		}
	}

	if isCursor {
		style := th.Cursor
		if marked {
			style = th.CursorSel
		}
		return style.Render(strings.Join(plain, lineV))
	}
	if marked {
		return th.Selected.Render(strings.Join(plain, lineV))
	}

	// Ordinary rows style each cell separately so that empty fields can be
	// dimmed, which makes gaps in the library visible at a glance.
	styled := make([]string, len(plain))
	for i, c := range lay.Cols {
		switch {
		case i == 0:
			styled[i] = th.Dirty.Render(plain[i])
		case c.Render != nil && strings.TrimSpace(c.Render(t)) == "":
			styled[i] = th.Dim.Render(plain[i])
		default:
			styled[i] = th.Row.Render(plain[i])
		}
	}
	return strings.Join(styled, th.Border.Render(lineV))
}

// gutter renders the two-cell marker column: selection then unsaved state.
func gutter(marked, dirty bool) string {
	sel, dot := " ", " "
	if marked {
		sel = "✓"
	}
	if dirty {
		dot = "•"
	}
	return sel + dot
}

// renderPanel draws either the read-only detail lines or the edit grid, and
// guarantees exactly h lines so the frame below it lands where it should.
func (m *Model) renderPanel(inner, h int) []string {
	if h <= 0 {
		return nil
	}
	var lines []string
	switch m.mode {
	case ModeEdit:
		lines = m.renderEditPanel(inner, h)
	case ModeHelp:
		return nil
	default:
		switch {
		case m.showArt && h >= artPanelRows:
			lines = m.renderArtPreview(inner)
		case m.showDetail:
			lines = m.renderDetail(inner)
		}
	}
	blank := " " + strings.Repeat(" ", inner-2) + " "
	for len(lines) < h {
		lines = append(lines, blank)
	}
	return lines[:h]
}

// renderDetail shows the highlighted track's path and technical properties.
func (m *Model) renderDetail(inner int) []string {
	th := m.theme
	w := inner - 2
	blank := []string{" " + Pad("", w) + " ", " " + Pad("", w) + " ",
		" " + Pad("", w) + " ", " " + Pad("", w) + " "}

	track, ok := m.currentTrack()
	if !ok {
		return blank
	}
	t := &track

	// Paths are long and the interesting end is the right one, so elide the
	// front rather than the back.
	line1 := th.Dim.Render(Pad(elideLeft(t.Path, w), w))

	parts := []string{t.Format}
	if t.Bitrate > 0 {
		parts = append(parts, fmt.Sprintf("%d kbps", t.Bitrate))
	}
	if t.SampleRate > 0 {
		parts = append(parts, fmt.Sprintf("%.1f kHz", float64(t.SampleRate)/1000))
	}
	if t.Channels > 0 {
		parts = append(parts, channelName(t.Channels))
	}
	if !t.Writable {
		parts = append(parts, "read-only format")
	}
	parts = append(parts, FormatBytes(t.Size), FormatMillis(t.DurationMS))
	if t.HasArt {
		parts = append(parts, "artwork")
	}
	line2 := th.FieldValue.Render(Pad(strings.Join(parts, " · "), w))

	line3 := th.Dim.Render(Pad(labelled(map[string]string{
		"album artist": t.AlbumArtist,
		"composer":     t.Composer,
		"comment":      t.Comment,
	}, []string{"album artist", "composer", "comment"}), w))

	line4 := m.artLine(w)

	return []string{" " + line1 + " ", " " + line2 + " ", " " + line3 + " ", " " + line4 + " "}
}

func labelled(values map[string]string, order []string) string {
	parts := make([]string, 0, len(order))
	for _, k := range order {
		v := values[k]
		if v == "" {
			v = "—"
		}
		parts = append(parts, k+": "+v)
	}
	return strings.Join(parts, "   ")
}

func channelName(n uint8) string {
	switch n {
	case 1:
		return "mono"
	case 2:
		return "stereo"
	default:
		return fmt.Sprintf("%d ch", n)
	}
}

// elideLeft trims the front of a string, marking the cut, so the tail stays
// readable. Used for paths, where the filename matters most.
func elideLeft(s string, w int) string {
	if w <= 1 || DisplayWidth(s) <= w {
		return s
	}
	r := []rune(s)
	for i := 1; i < len(r); i++ {
		if DisplayWidth(string(r[i:]))+1 <= w {
			return "…" + string(r[i:])
		}
	}
	return Truncate(s, w)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// stripANSI removes escape sequences so a styled string can be measured in
// terminal cells.
//
// It has to understand more than colour codes. An inline image is an OSC or
// APC sequence carrying base64, and base64 contains every letter — so a naive
// scan that stopped at the first 'm' would cut a sequence in half and count
// the remainder as visible text. The frame is laid out in exact cells, so a
// mismeasured line puts the right-hand rule in the wrong column.
func stripANSI(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != 0x1b || i+1 >= len(s) {
			b.WriteByte(s[i])
			i++
			continue
		}
		switch s[i+1] {
		case '[': // CSI: ends at the first byte in @ to ~
			i += 2
			for i < len(s) && (s[i] < '@' || s[i] > '~') {
				i++
			}
			if i < len(s) {
				i++
			}
		case ']', '_', 'P', '^', 'X': // OSC, APC, DCS, PM, SOS: end at BEL or ST
			i += 2
			for i < len(s) {
				if s[i] == 0x07 { // BEL
					i++
					break
				}
				if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' { // ST
					i += 2
					break
				}
				i++
			}
		default: // a two-byte escape
			i += 2
		}
	}
	return b.String()
}

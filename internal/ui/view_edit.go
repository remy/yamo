package ui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/remy/tag-manager/internal/library"
)

// Label column widths inside the edit grid. "Album Artist" is the longest
// label, and giving every label the same width keeps the values aligned into a
// readable second column.
const (
	labelWideW = 13
	labelThinW = 8
	gridGap    = 3
)

// renderEditPanel draws the field grid, plus the suggestion list when one is
// open. It never overlays the table: the panel grows instead, so no line has
// to be composited over another and the frame stays intact.
func (m *Model) renderEditPanel(inner, budget int) []string {
	th := m.theme
	w := inner - 2
	targets := m.editTargets()

	head := "editing " + m.selectionSummary()
	headStyle := th.Title
	if bad := unwritableFormats(targets); len(bad) > 0 {
		head += fmt.Sprintf("  ·  %s cannot be written", describeFormats(bad))
		headStyle = th.Warn
	}
	hint := "enter edit  ·  tab next  ·  esc back  ·  ^s save"
	if m.ed.editing {
		hint = "enter commit  ·  tab accept suggestion  ·  esc cancel"
	}
	// The hint is a convenience, not information. If it will not fit with a
	// clear gap after the header, drop it rather than jamming the two together.
	const minGap = 3
	gap := w - DisplayWidth(head) - DisplayWidth(hint)
	if gap < minGap {
		hint = ""
		gap = max(w-DisplayWidth(head), 0)
	}
	lines := []string{" " + headStyle.Render(head) + strings.Repeat(" ", gap) + th.Dim.Render(hint) +
		strings.Repeat(" ", max(w-DisplayWidth(head)-gap-DisplayWidth(hint), 0)) + " "}

	// Spend the remaining budget on fields first and suggestions second: the
	// field you are editing matters more than the list of things it could be.
	remaining := budget - len(lines)
	cols := m.editColumns()
	var grid []string
	if cols == 2 {
		grid = m.renderGrid2(w, targets)
	} else {
		grid = m.renderGrid1(w, targets)
	}
	grid = windowAroundFocus(grid, m.focusRow(cols), remaining)
	lines = append(lines, grid...)

	if m.ed.sug.open {
		if sug := m.renderSuggestions(w); len(sug) <= budget-len(lines) {
			lines = append(lines, sug...)
		}
	}
	return lines
}

// focusRow is the grid row the focused field occupies.
func (m *Model) focusRow(cols int) int {
	if cols == 2 {
		return m.ed.focus % editRows
	}
	return m.ed.focus
}

// windowAroundFocus trims a list of rows to n, keeping the focused row visible.
// A short terminal loses rows rather than the ability to edit.
func windowAroundFocus(rows []string, focus, n int) []string {
	if n <= 0 {
		return nil
	}
	if len(rows) <= n {
		return rows
	}
	start := focus - n/2
	start = clamp(start, 0, len(rows)-n)
	return rows[start : start+n]
}

// unwritableFormats counts edit targets whose container this build cannot
// write, grouped by format. Reporting it while editing turns what would
// otherwise be a batch of save-time failures into something the user knows
// before they type.
func unwritableFormats(targets []library.Track) map[string]int {
	var out map[string]int
	for i := range targets {
		if targets[i].Writable {
			continue
		}
		if out == nil {
			out = map[string]int{}
		}
		out[targets[i].Format]++
	}
	return out
}

// describeFormats renders a count-by-format map in a stable order.
func describeFormats(m map[string]int) string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		parts = append(parts, fmt.Sprintf("%d %s", m[n], n))
	}
	return strings.Join(parts, ", ")
}

// renderGrid2 lays the fields out in two columns, filled column-major.
func (m *Model) renderGrid2(w int, targets []library.Track) []string {
	leftW := (w-gridGap)*3/5 - labelWideW - 1
	rightW := w - gridGap - (leftW + labelWideW + 1) - labelThinW - 1
	if leftW < 6 {
		leftW = 6
	}
	if rightW < 4 {
		rightW = 4
	}

	lines := make([]string, 0, editRows)
	for row := 0; row < editRows; row++ {
		left := m.renderField(row, labelWideW, leftW, targets)
		right := m.renderField(editRows+row, labelThinW, rightW, targets)
		line := left + strings.Repeat(" ", gridGap) + right
		lines = append(lines, " "+padStyled(line, w)+" ")
	}
	return lines
}

// renderGrid1 stacks every field in a single column for narrow terminals.
func (m *Model) renderGrid1(w int, targets []library.Track) []string {
	valW := w - labelWideW - 1
	if valW < 6 {
		valW = 6
	}
	lines := make([]string, 0, len(editFields))
	for i := range editFields {
		lines = append(lines, " "+padStyled(m.renderField(i, labelWideW, valW, targets), w)+" ")
	}
	return lines
}

// renderField draws one label and value, showing a live input when the field
// is being edited and a marker when a multi-track selection disagrees.
func (m *Model) renderField(i, labelW, valueW int, targets []library.Track) string {
	th := m.theme
	f := editFields[i]
	focused := i == m.ed.focus

	label := Pad(f.Label, labelW)
	if focused {
		label = th.Accent.Render(label)
	} else {
		label = th.FieldName.Render(label)
	}

	if focused && m.ed.editing {
		return label + " " + m.ed.in.Render(valueW, true, th, "")
	}

	value, mixed := fieldValue(targets, f.Field)
	switch {
	case mixed:
		return label + " " + th.Warn.Render(Pad(mixedMarker, valueW))
	case value == "":
		if focused {
			return label + " " + th.Dim.Render(Pad("—", valueW))
		}
		return label + " " + th.Dim.Render(Pad("—", valueW))
	case focused:
		return label + " " + th.Selected.Render(Pad(value, valueW))
	default:
		return label + " " + th.FieldValue.Render(Pad(value, valueW))
	}
}

// renderSuggestions draws the autocomplete list as a small framed box, indented
// to sit under the value it is completing.
func (m *Model) renderSuggestions(w int) []string {
	th := m.theme
	items := m.ed.sug.items

	indent := labelWideW + 1
	if m.editColumns() == 2 && m.ed.focus >= editRows {
		indent = (w-gridGap)*3/5 + gridGap + labelThinW + 1
	}
	if indent > w-12 {
		indent = max(w-12, 0)
	}

	boxW := 0
	for _, it := range items {
		if n := DisplayWidth(it.Value) + 11; n > boxW {
			boxW = n
		}
	}
	boxW = clamp(boxW, 16, w-indent)
	innerW := boxW - 2

	pre := strings.Repeat(" ", indent)
	lines := make([]string, 0, len(items)+2)
	lines = append(lines, " "+padStyled(pre+th.PopupBorder.Render(topRule(innerW, nil)), w)+" ")
	for i, it := range items {
		count := fmt.Sprintf("%d", it.Count)
		nameW := innerW - DisplayWidth(count) - 5
		text := "  " + Pad(it.Value, nameW) + "  " + PadLeft(count, DisplayWidth(count)) + " "
		if i == m.ed.sug.sel {
			text = th.PopupPick.Render(text)
		} else {
			text = th.PopupItem.Render(text)
		}
		body := th.PopupBorder.Render(lineV) + text + th.PopupBorder.Render(lineV)
		lines = append(lines, " "+padStyled(pre+body, w)+" ")
	}
	lines = append(lines, " "+padStyled(pre+th.PopupBorder.Render(botRule(innerW, nil)), w)+" ")
	return lines
}

// padStyled pads an already-styled string to w cells, measuring the visible
// text rather than the bytes.
func padStyled(s string, w int) string {
	n := DisplayWidth(stripANSI(s))
	if n >= w {
		return s
	}
	return s + strings.Repeat(" ", w-n)
}

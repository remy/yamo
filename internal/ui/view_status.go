package ui

import (
	"fmt"
	"strings"
)

// renderStatus draws the bottom line: a transient message on the left and the
// key hints for the current mode on the right, so the reminder of what to
// press is always where the eye already is.
func (m *Model) renderStatus(inner int) string {
	th := m.theme
	w := inner - 2

	msg := m.status
	style := th.Dim
	switch m.statusKind {
	case statusOK:
		style = th.OK
	case statusWarn:
		style = th.Warn
	case statusError:
		style = th.Error
	}

	if s := m.saver; s != nil {
		msg = fmt.Sprintf("saving %d/%d…", s.done, s.total)
		style = th.Accent
	}
	if m.artWriting > 0 {
		msg = fmt.Sprintf("writing artwork to %s tracks…", FormatCount(m.artWriting))
		style = th.Accent
	}
	if m.mode == ModeConfirmQuit {
		msg = fmt.Sprintf("%d tracks have unsaved changes — (s)ave and quit, (y)es quit anyway, (n)o",
			m.cat.DirtyCount())
		style = th.Warn
	}

	hints := m.hintsFor()
	hintText := th.Dim.Render(hints)
	hintW := DisplayWidth(hints)

	msgW := w - hintW - 2
	if msgW < 8 {
		msgW = w
		hintText, hintW = "", 0
	}
	left := style.Render(Pad(Truncate(msg, msgW), msgW))
	gap := w - msgW - hintW
	if gap < 0 {
		gap = 0
	}
	return " " + left + strings.Repeat(" ", gap) + hintText + " "
}

// hintsFor picks the key hints worth showing in the current mode.
func (m *Model) hintsFor() string {
	switch m.mode {
	case ModeSearch:
		return "⏎ done   esc cancel   ↑↓ move"
	case ModeEdit:
		if m.ed.editing {
			return "⏎ commit   tab accept   esc cancel"
		}
		return "⏎ edit   J/K track   esc back   ^s save"
	case ModeHelp:
		return "any key to close"
	case ModeConfirmQuit:
		return ""
	default:
		return "? help   / search   e edit   space mark   y/p art   ^s save   q quit"
	}
}

// helpAll renders the complete key reference. The caller windows it, so the
// list can be longer than the terminal.
func (m *Model) helpAll(inner int) []string {
	th := m.theme
	w := inner - 2

	type entry struct{ keys, desc string }
	sections := []struct {
		title   string
		entries []entry
	}{
		{"moving", []entry{
			{"j / k, ↑ / ↓", "move one track"},
			{"^d / ^u", "half a page"},
			{"pgdn / pgup", "a full page"},
			{"g / G", "first / last track"},
			{"i", "show or hide the detail panel"},
		}},
		{"finding", []entry{
			{"/", "search; results update as you type"},
			{"artist:elvis", "restrict a term to one field"},
			{"artist:\"elvis presley\"", "quote values containing spaces"},
			{"year:1977, year:>1980", "compare numeric fields"},
			{"year:1970-1979", "an inclusive range"},
			{"-genre:live", "exclude matches"},
			{"album:", "find tracks where a field is empty"},
			{"R or ^l", "re-apply the filter after editing"},
			{"esc", "clear the filter"},
		}},
		{"selecting", []entry{
			{"space", "mark a track and move on"},
			{"v", "mark from the last mark to here"},
			{"a / n", "mark all results / clear marks"},
		}},
		{"artwork", []entry{
			{"A", "show the cover, as an image where the terminal can"},
			{"y", "copy this track's cover to the clipboard"},
			{"p", "paste it onto the marked tracks, or this one"},
			{"", "artwork is written straight to disk, not held for ^s"},
		}},
		{"editing", []entry{
			{"e or ⏎", "open the editor for the marks, or this track"},
			{"tab / shift-tab", "next / previous field"},
			{"⏎ or typing", "start editing the focused field"},
			{"tab", "accept the highlighted suggestion"},
			{"⏎", "commit the field to every selected track"},
			{"J / K", "move to the next track without leaving the editor"},
			{"u / ^r", "undo / redo a whole edit"},
			{"^s", "write every change back to disk"},
		}},
		{"sorting", []entry{
			{"s / S", "cycle the sort column / reverse it"},
		}},
	}

	keyW := 24
	var lines []string
	add := func(s string) { lines = append(lines, " "+padStyled(s, w)+" ") }

	for si, sec := range sections {
		if si > 0 {
			add("")
		}
		add(th.Header.Render(sec.title))
		for _, e := range sec.entries {
			add("  " + th.Key.Render(Pad(e.keys, keyW)) + th.Row.Render(Truncate(e.desc, max(w-keyW-2, 0))))
		}
	}
	return lines
}

// helpWindow returns the visible slice of the key reference plus a marker line
// when there is more above or below.
func (m *Model) helpWindow(inner, rows int) []string {
	w := inner - 2
	all := m.helpAll(inner)
	blank := " " + strings.Repeat(" ", w) + " "
	if rows <= 0 {
		return nil
	}

	maxOff := max(len(all)-rows, 0)
	m.helpOffset = clamp(m.helpOffset, 0, maxOff)
	end := min(m.helpOffset+rows, len(all))
	out := append([]string(nil), all[m.helpOffset:end]...)
	for len(out) < rows {
		out = append(out, blank)
	}

	// Replace the last line with a scroll marker when content is cut off, so
	// the truncation is visible rather than silent.
	if maxOff > 0 {
		marker := ""
		if m.helpOffset < maxOff {
			marker = "↓ more — ↑↓ to scroll"
		} else {
			marker = "↑ back to the top — ↑↓ to scroll"
		}
		out[len(out)-1] = " " + padStyled(m.theme.Dim.Render(marker), w) + " "
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

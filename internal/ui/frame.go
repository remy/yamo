package ui

import "strings"

// Box-drawing runes. Light lines read as structure without competing with the
// content for attention, which matters when the content is the point.
const (
	lineH  = "─"
	lineV  = "│"
	cornTL = "┌"
	cornTR = "┐"
	cornBL = "└"
	cornBR = "┘"
	teeL   = "├"
	teeR   = "┤"
	teeT   = "┬"
	teeB   = "┴"
	cross  = "┼"
)

// rule draws a horizontal rule of the given inner width with junctions at the
// listed inner column offsets.
//
// left, right and mid select which junction runes to use, so the same routine
// draws the top edge (┌ ┬ ┐), an interior divider (├ ┼ ┤) and the bottom edge
// (└ ┴ ┘). Getting the junctions right is the difference between a drawn
// interface and a pile of dashes.
func rule(width int, seps []int, left, mid, right string) string {
	if width < 0 {
		width = 0
	}
	cells := make([]string, width)
	for i := range cells {
		cells[i] = lineH
	}
	for _, s := range seps {
		if s >= 0 && s < width {
			cells[s] = mid
		}
	}
	return left + strings.Join(cells, "") + right
}

// topRule draws the upper edge of a box.
func topRule(width int, seps []int) string { return rule(width, seps, cornTL, teeT, cornTR) }

// midRule draws a divider between two rows of a box.
func midRule(width int, seps []int) string { return rule(width, seps, teeL, cross, teeR) }

// botRule draws the lower edge of a box.
func botRule(width int, seps []int) string { return rule(width, seps, cornBL, teeB, cornBR) }

// closeRule draws a divider where columns end and a full-width area begins, so
// the vertical rules terminate in ┴ rather than running on into the panel.
func closeRule(width int, seps []int) string { return rule(width, seps, teeL, teeB, teeR) }

// openRule draws a divider where a full-width area gives way to columns.
func openRule(width int, seps []int) string { return rule(width, seps, teeL, teeT, teeR) }

// titledRule draws a top edge with a label set into it, as in
// ┌─ tagmgr ──────┐. The label is truncated if the box is too narrow for it.
func titledRule(width int, label string) string {
	if width <= 0 {
		return ""
	}
	label = " " + strings.TrimSpace(label) + " "
	lw := DisplayWidth(label)
	if lw+3 > width {
		label = Truncate(label, width-3)
		lw = DisplayWidth(label)
		if lw <= 0 {
			return topRule(width, nil)
		}
	}
	rest := width - lw - 1
	return cornTL + lineH + label + strings.Repeat(lineH, rest) + cornTR
}

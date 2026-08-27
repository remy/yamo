// Package ui renders the terminal interface.
package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"
)

// DisplayWidth returns the number of terminal cells a string occupies. It is
// not len(s) and not len([]rune(s)): CJK characters and emoji are two cells
// wide, and getting this wrong tears the box-drawing layout apart.
func DisplayWidth(s string) int { return runewidth.StringWidth(s) }

// Truncate shortens s to at most w cells, marking the cut with an ellipsis.
func Truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if runewidth.StringWidth(s) <= w {
		return s
	}
	if w == 1 {
		return "…"
	}
	return runewidth.Truncate(s, w, "…")
}

// Pad right-pads s with spaces to exactly w cells, truncating when too long.
// Every cell of the layout goes through here, which is what keeps the vertical
// rules aligned no matter what is in the tags.
func Pad(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = Truncate(s, w)
	if n := w - runewidth.StringWidth(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// PadLeft left-pads s to w cells, for right-aligned numeric columns.
func PadLeft(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = Truncate(s, w)
	if n := w - runewidth.StringWidth(s); n > 0 {
		return strings.Repeat(" ", n) + s
	}
	return s
}

// FormatMillis renders a track length as m:ss, or h:mm:ss past an hour.
func FormatMillis(ms int32) string {
	if ms <= 0 {
		return "--:--"
	}
	total := int(ms / 1000)
	h, m, s := total/3600, (total%3600)/60, total%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

// FormatDuration renders an elapsed time compactly for status lines.
func FormatDuration(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// FormatBytes renders a byte count in binary units.
func FormatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 5; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// FormatTrackNo renders a track number, blank when unset.
func FormatTrackNo(n int32) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf("%d", n)
}

// FormatCount renders large counts with thin separators for readability.
func FormatCount(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	lead := len(s) % 3
	if lead > 0 {
		b.WriteString(s[:lead])
	}
	for i := lead; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

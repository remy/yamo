package ui

import (
	"github.com/remy/yamo/internal/library"
)

// Column describes one column of the track table.
type Column struct {
	Title string

	// Field is the editable metadata field this column shows, by canonical
	// name. Empty for columns that are derived rather than edited.
	Field string

	// SortKey is what the server is asked to sort by. Empty means the column
	// is skipped when cycling the sort.
	SortKey string

	Weight   int // share of the flexible space; 0 for fixed columns
	Min      int // smallest useful width
	Fixed    int // exact width; when set, Weight is ignored
	Right    bool
	Priority int // higher numbers are dropped first on narrow terminals
	Render   func(t *library.Track) string
}

// DefaultColumns is the standard table layout. Title gets the largest share
// because it is the least predictable field; the numeric columns are fixed
// because a ragged right edge on numbers is hard to read.
var DefaultColumns = []Column{
	{
		Title: "", Fixed: 2, Priority: 0,
		Render: func(t *library.Track) string { return "" }, // gutter, drawn by the row renderer
	},
	{
		Title: "Artist", Field: "artist", SortKey: "artist", Weight: 22, Min: 8, Priority: 1,
		Render: func(t *library.Track) string { return t.Artist },
	},
	{
		Title: "Album", Field: "album", SortKey: "album", Weight: 22, Min: 8, Priority: 2,
		Render: func(t *library.Track) string { return t.Album },
	},
	{
		Title: "Title", Field: "title", SortKey: "title", Weight: 26, Min: 10, Priority: 0,
		Render: func(t *library.Track) string { return t.Title },
	},
	{
		Title: "#", Field: "track", SortKey: "track", Fixed: 4, Right: true, Priority: 5,
		Render: func(t *library.Track) string { return FormatTrackNo(t.TrackNo) },
	},
	{
		Title: "Year", Field: "year", SortKey: "year", Fixed: 4, Right: true, Priority: 4,
		Render: func(t *library.Track) string { return FormatTrackNo(t.Year) },
	},
	{
		Title: "Genre", Field: "genre", SortKey: "genre", Weight: 12, Min: 6, Priority: 6,
		Render: func(t *library.Track) string { return t.Genre },
	},
	{
		Title: "Time", SortKey: "duration", Fixed: 6, Right: true, Priority: 3,
		Render: func(t *library.Track) string { return FormatMillis(t.DurationMS) },
	},
	{
		Title: "Fmt", SortKey: "format", Fixed: 4, Priority: 7,
		Render: func(t *library.Track) string { return t.Format },
	},
}

// Layout is a resolved set of column widths for a given terminal width.
type Layout struct {
	Cols   []Column
	Widths []int
	Seps   []int // inner offsets of the vertical rules, for drawing junctions
	Inner  int
}

// ComputeLayout fits columns into inner cells, dropping the lowest-priority
// columns when there is not enough room and distributing what is left over by
// weight. It always returns at least one column, so a very narrow terminal
// degrades to a single Title column rather than to nothing.
func ComputeLayout(cols []Column, inner int) Layout {
	if inner < 1 {
		inner = 1
	}
	active := make([]Column, len(cols))
	copy(active, cols)

	for {
		need := len(active) - 1 // one cell per separator
		for _, c := range active {
			need += c.width()
		}
		if need <= inner || len(active) == 1 {
			break
		}
		active = dropLowestPriority(active)
	}

	widths := distribute(active, inner)
	seps := make([]int, 0, len(active)-1)
	pos := 0
	for i, w := range widths {
		pos += w
		if i < len(widths)-1 {
			seps = append(seps, pos)
			pos++
		}
	}
	return Layout{Cols: active, Widths: widths, Seps: seps, Inner: inner}
}

// width is a column's minimum footprint.
func (c Column) width() int {
	if c.Fixed > 0 {
		return c.Fixed
	}
	if c.Min > 0 {
		return c.Min
	}
	return 4
}

// dropLowestPriority removes the least important remaining column.
func dropLowestPriority(cols []Column) []Column {
	worst, at := -1, -1
	for i, c := range cols {
		if c.Priority > worst {
			worst, at = c.Priority, i
		}
	}
	if at < 0 {
		return cols[:1]
	}
	return append(cols[:at:at], cols[at+1:]...)
}

// distribute assigns actual widths: fixed columns take what they ask for, and
// the flexible ones split the remainder in proportion to their weights.
func distribute(cols []Column, inner int) []int {
	widths := make([]int, len(cols))
	remaining := inner - (len(cols) - 1)
	totalWeight := 0

	for i, c := range cols {
		if c.Fixed > 0 {
			widths[i] = c.Fixed
			remaining -= c.Fixed
			continue
		}
		totalWeight += c.Weight
	}
	if remaining < 0 {
		remaining = 0
	}

	// Reserve each flexible column's minimum before sharing out the surplus,
	// so a narrow window never starves one column to pad another.
	flexible := 0
	for i, c := range cols {
		if c.Fixed > 0 {
			continue
		}
		flexible++
		if remaining >= c.Min {
			widths[i] = c.Min
			remaining -= c.Min
		}
	}
	if flexible == 0 || totalWeight == 0 {
		if len(widths) > 0 && remaining > 0 {
			widths[len(widths)-1] += remaining
		}
		return widths
	}

	surplus := remaining
	given := 0
	last := -1
	for i, c := range cols {
		if c.Fixed > 0 {
			continue
		}
		last = i
		share := surplus * c.Weight / totalWeight
		widths[i] += share
		given += share
	}
	// Rounding leftovers go to the final flexible column so the row always
	// fills the frame exactly.
	if last >= 0 {
		widths[last] += surplus - given
	}
	return widths
}

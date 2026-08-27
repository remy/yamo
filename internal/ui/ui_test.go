package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/remy/tag-manager/internal/catalog"
	"github.com/remy/tag-manager/internal/tags"
)

func testCatalog(n int) *catalog.Catalog {
	artists := []string{"Elvis Presley", "Björk", "坂本龍一", "Miles Davis"}
	albums := []string{"Sun Sessions", "Homogénic", "音楽図鑑", "Kind of Blue"}
	c := catalog.New()
	c.Tracks = make([]catalog.Track, n)
	for i := range c.Tracks {
		c.Tracks[i] = catalog.Track{
			Path:       "/music/x/y/track.mp3",
			Title:      "A Song With A Fairly Long Title " + strings.Repeat("x", i%7),
			Artist:     artists[i%len(artists)],
			Album:      albums[i%len(albums)],
			Genre:      "Rock",
			Year:       int32(1970 + i%40),
			TrackNo:    int32(i%12 + 1),
			DurationMS: 210000,
			Size:       5 << 20,
			Format:     tags.FormatMP3,
			Channels:   2,
			SampleRate: 44100,
			Bitrate:    256,
		}
	}
	c.Roots = []string{"/music"}
	c.ScannedAt = time.Now()
	return c
}

func sized(t *testing.T, m *Model, w, h int) {
	t.Helper()
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
}

// TestFrameGeometry is the load-bearing test for the interface: every line the
// renderer emits must be exactly the terminal width, and there must be exactly
// as many lines as there are rows. If either holds false the box-drawing
// characters stop lining up and the whole layout visibly tears.
func TestFrameGeometry(t *testing.T) {
	m := New(testCatalog(300), "")
	sizes := []struct{ w, h int }{
		{80, 24}, {120, 40}, {200, 60}, {60, 20}, {40, 16}, {34, 12},
		{35, 13}, {100, 30}, {72, 18}, {73, 19}, {160, 50},
	}
	modes := []struct {
		name  string
		setup func()
	}{
		{"browse", func() { m.mode = ModeBrowse }},
		{"search", func() { m.mode = ModeSearch; m.search.SetValue("artist:elvis") }},
		{"edit", func() { m.mode = ModeEdit; m.ed = editor{active: true} }},
		{"edit-typing", func() {
			m.mode = ModeEdit
			m.ed = editor{active: true, focus: 2, editing: true}
			m.ed.in.SetValue("Sun")
			m.refreshSuggestions()
		}},
		{"help", func() { m.mode = ModeHelp }},
		{"confirm-quit", func() { m.mode = ModeConfirmQuit }},
		{"no-detail", func() { m.mode = ModeBrowse; m.showDetail = false }},
		{"empty-results", func() {
			m.mode = ModeBrowse
			m.showDetail = true
			m.search.SetValue("zzzznomatch")
			m.runSearch()
		}},
	}

	for _, mode := range modes {
		for _, s := range sizes {
			mode.setup()
			sized(t, m, s.w, s.h)
			view := m.View()
			lines := strings.Split(view, "\n")

			if len(lines) != s.h {
				t.Errorf("%s at %dx%d: %d lines, want %d", mode.name, s.w, s.h, len(lines), s.h)
				continue
			}
			for i, line := range lines {
				if got := DisplayWidth(stripANSI(line)); got != s.w {
					t.Errorf("%s at %dx%d: line %d is %d cells, want %d\n  %q",
						mode.name, s.w, s.h, i, got, s.w, stripANSI(line))
					break
				}
			}
		}
	}
	// Reset the filter so later assertions in this file are not affected.
	m.search.Clear()
	m.runSearch()
}

// TestTooSmallDoesNotPanic covers sizes below the usable minimum.
func TestTooSmallDoesNotPanic(t *testing.T) {
	m := New(testCatalog(10), "")
	for _, s := range []struct{ w, h int }{{1, 1}, {10, 3}, {33, 11}, {0, 0}, {34, 11}, {33, 12}} {
		sized(t, m, s.w, s.h)
		_ = m.View()
	}
}

func TestComputeLayoutFillsExactly(t *testing.T) {
	for inner := 10; inner < 400; inner++ {
		lay := ComputeLayout(DefaultColumns, inner)
		total := len(lay.Widths) - 1 // separators
		for _, w := range lay.Widths {
			if w < 0 {
				t.Fatalf("inner=%d produced a negative width %v", inner, lay.Widths)
			}
			total += w
		}
		if total != inner {
			t.Fatalf("inner=%d: columns total %d, want %d (widths %v)", inner, total, inner, lay.Widths)
		}
		if len(lay.Cols) == 0 {
			t.Fatalf("inner=%d dropped every column", inner)
		}
	}
}

func TestRuleJunctions(t *testing.T) {
	seps := []int{3, 8}
	got := midRule(12, seps)
	want := "├───┼────┼───┤"
	if got != want {
		t.Errorf("midRule = %q, want %q", got, want)
	}
	if DisplayWidth(topRule(12, seps)) != 14 {
		t.Errorf("topRule width = %d, want 14", DisplayWidth(topRule(12, seps)))
	}
}

func TestPadAndTruncateAreCellExact(t *testing.T) {
	cases := []string{"", "abc", "Björk", "坂本龍一", "日本語テキスト", "a坂b本c", "🎵🎶"}
	for _, s := range cases {
		for w := 0; w < 16; w++ {
			if got := DisplayWidth(Pad(s, w)); got != w {
				t.Errorf("Pad(%q, %d) is %d cells, want %d", s, w, got, w)
			}
			if got := DisplayWidth(PadLeft(s, w)); got != w {
				t.Errorf("PadLeft(%q, %d) is %d cells, want %d", s, w, got, w)
			}
			if got := DisplayWidth(Truncate(s, w)); got > w {
				t.Errorf("Truncate(%q, %d) is %d cells, want at most %d", s, w, got, w)
			}
		}
	}
}

func TestInputRendersExactWidth(t *testing.T) {
	th := NewTheme()
	var in input
	for _, v := range []string{"", "hello", "坂本龍一のアルバム", strings.Repeat("long ", 40)} {
		in.SetValue(v)
		for _, w := range []int{1, 5, 12, 40, 100} {
			for _, focused := range []bool{true, false} {
				got := DisplayWidth(stripANSI(in.Render(w, focused, th, "")))
				if got != w {
					t.Errorf("Render(%q, w=%d, focused=%v) is %d cells, want %d", v, w, focused, got, w)
				}
			}
		}
	}
}

func TestInputEditing(t *testing.T) {
	var in input
	in.SetValue("hello world")
	in.Home()
	in.InsertString("say ")
	if in.Value() != "say hello world" {
		t.Fatalf("after insert: %q", in.Value())
	}
	in.End()
	in.DeleteWordBack()
	if in.Value() != "say hello " {
		t.Fatalf("after DeleteWordBack: %q", in.Value())
	}
	in.DeleteToStart()
	if in.Value() != "" {
		t.Fatalf("after DeleteToStart: %q", in.Value())
	}
	// Cursor movement must stay in range.
	in.SetValue("ab")
	for i := 0; i < 10; i++ {
		in.Left()
	}
	if in.cursor != 0 {
		t.Errorf("cursor ran past the start: %d", in.cursor)
	}
	for i := 0; i < 10; i++ {
		in.Right()
	}
	if in.cursor != 2 {
		t.Errorf("cursor ran past the end: %d", in.cursor)
	}
	in.Backspace()
	in.Backspace()
	in.Backspace()
	if in.Value() != "" {
		t.Errorf("backspacing past the start left %q", in.Value())
	}
}

// TestBulkEditAndUndo exercises the path the interface exists for: filter,
// mark several tracks, change one field on all of them, then take it back.
func TestBulkEditAndUndo(t *testing.T) {
	c := testCatalog(200)
	m := New(c, "")
	sized(t, m, 120, 40)

	m.search.SetValue(`album:"sun sessions"`)
	m.runSearch()
	if len(m.results) != 50 {
		t.Fatalf("filter matched %d, want 50", len(m.results))
	}
	for _, r := range m.results {
		m.selected[r] = struct{}{}
	}

	n := m.commitField(catalog.FieldAlbum, "Complete Masters")
	if n != 50 {
		t.Fatalf("edit touched %d tracks, want 50", n)
	}
	for _, r := range m.results {
		if got := c.Tracks[r].Album; got != "Complete Masters" {
			t.Fatalf("track %d album = %q", r, got)
		}
		if !c.Tracks[r].Dirty() {
			t.Fatalf("track %d is not marked unsaved", r)
		}
	}
	// The rows must stay put: re-filtering here would empty the view.
	if len(m.results) != 50 {
		t.Errorf("result set changed to %d after the edit", len(m.results))
	}
	if !m.filterStale {
		t.Error("the filter should be flagged as stale after an edit")
	}

	m.doUndo()
	for _, r := range m.results {
		if got := c.Tracks[r].Album; got != "Sun Sessions" {
			t.Fatalf("after undo, track %d album = %q", r, got)
		}
	}
	m.doRedo()
	if got := c.Tracks[m.results[0]].Album; got != "Complete Masters" {
		t.Fatalf("after redo, album = %q", got)
	}

	// Refreshing drops the tracks that no longer match and clears their marks.
	m.refreshFilter()
	if len(m.results) != 0 {
		t.Errorf("after refresh, %d tracks still match the old album filter", len(m.results))
	}
	if len(m.selected) != 0 {
		t.Errorf("%d marks survived on rows that are no longer visible", len(m.selected))
	}
}

// TestOnlyChangedFieldsAreWritten guards the promise that a save touches
// nothing the user did not edit.
func TestOnlyChangedFieldsAreWritten(t *testing.T) {
	c := testCatalog(5)
	m := New(c, "")
	sized(t, m, 120, 40)
	m.commitField(catalog.FieldAlbum, "New Album")

	e := buildEdit(&c.Tracks[m.results[0]])
	if e.Album == nil || *e.Album != "New Album" {
		t.Fatalf("album not in the edit: %+v", e.Album)
	}
	if e.Artist != nil || e.Title != nil || e.Genre != nil || e.Year != nil || e.Track != nil {
		t.Error("the edit includes fields that were never changed")
	}
}

func TestSortCycling(t *testing.T) {
	m := New(testCatalog(100), "")
	sized(t, m, 120, 40)
	seen := map[string]bool{}
	for i := 0; i < len(m.cols)+2; i++ {
		m.cycleSort(1)
		seen[m.sortLabel()] = true
		// Sorting must never lose or duplicate rows.
		if len(m.results) != 100 {
			t.Fatalf("sorting changed the result count to %d", len(m.results))
		}
	}
	if len(seen) < 3 {
		t.Errorf("cycling produced only %d distinct sorts", len(seen))
	}

	m.sortCol = 1 // Artist
	m.sortDesc = false
	m.sortResults()
	prev := ""
	for _, r := range m.results {
		cur := catalog.Fold(m.cat.Tracks[r].Artist)
		if prev != "" && cur < prev {
			t.Fatalf("artist sort is not ordered: %q came after %q", cur, prev)
		}
		prev = cur
	}
}

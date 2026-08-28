package ui

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/remy/tag-manager/internal/api"
	"github.com/remy/tag-manager/internal/client"
	"github.com/remy/tag-manager/internal/library"
)

// newTestModel builds a model over a real server holding a small library.
//
// The interface is now a client, so a test against a stub would prove nothing
// about the thing that actually runs. This is slower and worth it.
func newTestModel(t *testing.T, tracks int) *Model {
	t.Helper()
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}

	dir := t.TempDir()
	artists := []string{"Elvis Presley", "Björk", "Miles Davis", "The Clash"}
	albums := []string{"Sun Sessions", "Homogénic", "Kind of Blue", "London Calling"}
	for i := 0; i < tracks; i++ {
		album := filepath.Join(dir, "music", artists[i%4], albums[i%4])
		if err := os.MkdirAll(album, 0o755); err != nil {
			t.Fatal(err)
		}
		out := filepath.Join(album, fmt.Sprintf("%03d track.mp3", i))
		cmd := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-y",
			"-f", "lavfi", "-i", "sine=frequency=440:duration=0.2", "-c:a", "libmp3lame",
			"-metadata", fmt.Sprintf("title=A Song With A Fairly Long Title %d", i),
			"-metadata", "artist="+artists[i%4],
			"-metadata", "album="+albums[i%4],
			"-metadata", "genre=Rock",
			"-metadata", fmt.Sprintf("track=%d", i%12+1),
			"-metadata", fmt.Sprintf("date=%d", 1970+i%40), out)
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg: %v\n%s", err, b)
		}
	}

	svc, err := library.Open(library.Options{
		CatalogPath:  filepath.Join(dir, "catalog.db"),
		SaveInterval: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.New(svc, api.Options{}))
	t.Cleanup(func() {
		srv.Close()
		svc.Close()
	})

	c, err := client.New(srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	job, err := c.Scan(ctx, library.ScanRequest{Roots: []string{filepath.Join(dir, "music")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.WaitJob(ctx, job.ID, nil); err != nil {
		t.Fatal(err)
	}

	m := New(c, []string{filepath.Join(dir, "music")})
	sized(t, m, 120, 40)
	return m
}

// pump runs a command and every message it produces, so a test can drive the
// model without a terminal.
func pump(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	pumpDepth(t, m, cmd, 0)
}

func pumpDepth(t *testing.T, m *Model, cmd tea.Cmd, depth int) {
	t.Helper()
	if cmd == nil || depth > 40 {
		return
	}
	msg := cmd()
	switch v := msg.(type) {
	case nil:
		return
	case tea.BatchMsg:
		for _, c := range v {
			pumpDepth(t, m, c, depth+1)
		}
	default:
		_, next := m.Update(msg)
		pumpDepth(t, m, next, depth+1)
	}
}

// sized delivers a window size and settles the resulting fetches.
func sized(t *testing.T, m *Model, w, h int) {
	t.Helper()
	_, cmd := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	pump(t, m, cmd)
}

// press sends a key and settles whatever it starts.
func press(t *testing.T, m *Model, key string) {
	t.Helper()
	var msg tea.KeyMsg
	switch key {
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEscape}
	case "space":
		msg = tea.KeyMsg{Type: tea.KeySpace}
	case "down":
		msg = tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		msg = tea.KeyMsg{Type: tea.KeyUp}
	case "tab":
		msg = tea.KeyMsg{Type: tea.KeyTab}
	case "ctrl+s":
		msg = tea.KeyMsg{Type: tea.KeyCtrlS}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
	_, cmd := m.Update(msg)
	pump(t, m, cmd)
}

// typeString sends each character separately, as a person would.
func typeString(t *testing.T, m *Model, s string) {
	t.Helper()
	for _, r := range s {
		if r == ' ' {
			press(t, m, "space")
			continue
		}
		press(t, m, string(r))
	}
	// The search runs on enter, not on each keystroke.
	press(t, m, "enter")
}

// TestFrameGeometry is the load-bearing test for the interface: every line the
// renderer emits must be exactly the terminal width, and there must be exactly
// as many lines as there are rows. If either holds false the box-drawing
// characters stop lining up and the whole layout visibly tears.
func TestFrameGeometry(t *testing.T) {
	m := newTestModel(t, 40)
	sizes := []struct{ w, h int }{
		{80, 24}, {120, 40}, {200, 60}, {60, 20}, {40, 16}, {34, 12},
		{35, 13}, {100, 30}, {72, 18}, {73, 19}, {160, 50},
	}
	modes := []struct {
		name  string
		setup func()
	}{
		{"browse", func() { m.mode = ModeBrowse; m.showArt = false }},
		{"search", func() { m.mode = ModeSearch; m.search.SetValue("artist:elvis") }},
		{"edit", func() { m.mode = ModeEdit; m.ed = editor{active: true} }},
		{"edit-typing", func() {
			m.mode = ModeEdit
			m.ed = editor{active: true, focus: 2, editing: true}
			m.ed.in.SetValue("Sun")
			m.ed.sug.items = []client.ValueCount{{Value: "Sun Sessions", Count: 10}}
			m.ed.sug.open = true
		}},
		{"help", func() { m.mode = ModeHelp }},
		{"confirm-quit", func() { m.mode = ModeConfirmQuit }},
		{"no-detail", func() { m.mode = ModeBrowse; m.showDetail = false }},
		{"art-panel", func() {
			m.mode = ModeBrowse
			m.showDetail = true
			m.showArt = true
			m.imgProto = ImageNone
		}},
		{"art-panel-with-image", func() {
			// Force an image protocol on and hand the panel a real cover, so
			// the escape sequence is measured the way a terminal would.
			m.showArt = true
			m.imgProto = ImageITerm2
			if tr, ok := m.currentTrack(); ok {
				m.art = map[string]*artInfo{tr.ID: {data: fakeImageBytes(), mime: "image/jpeg"}}
			}
		}},
		{"unloaded-rows", func() {
			// Mid-fetch, before any page has arrived.
			m.mode = ModeBrowse
			m.showArt = false
			m.src.invalidate()
		}},
	}

	for _, mode := range modes {
		for _, s := range sizes {
			mode.setup()
			m.width, m.height = s.w, s.h
			m.clampCursor()
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
}

func fakeImageBytes() []byte {
	b := make([]byte, 4096)
	for i := range b {
		b[i] = byte(i * 7)
	}
	return b
}

func TestTooSmallDoesNotPanic(t *testing.T) {
	m := newTestModel(t, 4)
	for _, s := range []struct{ w, h int }{{1, 1}, {10, 3}, {33, 11}, {0, 0}, {34, 11}, {33, 12}} {
		m.width, m.height = s.w, s.h
		m.clampCursor()
		_ = m.View()
	}
}

// TestPagingCoversEverything checks the windowed source: the interface no
// longer holds the library, so moving through it must fetch the right windows.
func TestPagingCoversEverything(t *testing.T) {
	m := newTestModel(t, 40)
	if m.total() != 40 {
		t.Fatalf("total = %d, want 40", m.total())
	}

	seen := map[string]bool{}
	for i := 0; i < 40; i++ {
		m.cursor = i
		m.clampCursor()
		pump(t, m, tea.Batch(m.ensureVisible()...))
		tr, ok := m.trackAt(i)
		if !ok {
			t.Fatalf("row %d was never fetched", i)
		}
		if seen[tr.ID] {
			t.Fatalf("row %d repeated track %s", i, tr.ID)
		}
		seen[tr.ID] = true
	}
	if len(seen) != 40 {
		t.Errorf("walked %d distinct tracks, want 40", len(seen))
	}
}

func TestSearchAndSort(t *testing.T) {
	m := newTestModel(t, 40)

	press(t, m, "/")
	typeString(t, m, "artist:elvis")
	if m.total() != 10 {
		t.Fatalf("artist:elvis matched %d, want 10", m.total())
	}
	// Folded matching must survive the round trip.
	m.search.SetValue("artist:bjork")
	settleSearchAfter(t, m)
	if m.total() != 10 {
		t.Errorf("artist:bjork matched %d, want 10", m.total())
	}

	m.search.Clear()
	settleSearchAfter(t, m)
	if m.total() != 40 {
		t.Fatalf("clearing the filter left %d", m.total())
	}

	// Sorting is done by the server; check the order that comes back.
	m.sortCol, m.sortDesc = 5, false // the Year column
	pump(t, m, tea.Batch(m.applySort()...))
	prev := int32(0)
	for i := 0; i < min(20, m.total()); i++ {
		tr, ok := m.trackAt(i)
		if !ok {
			continue
		}
		if tr.Year < prev {
			t.Fatalf("year sort out of order at row %d: %d after %d", i, tr.Year, prev)
		}
		prev = tr.Year
	}
}

// settleSearchAfter runs a search that was set directly rather than typed.
func settleSearchAfter(t *testing.T, m *Model) {
	t.Helper()
	pump(t, m, tea.Batch(m.runSearch()...))
}

// TestSelectionByQueryCostsNothing is the reason selection is not a list of
// identifiers: marking everything must not depend on how much there is.
func TestSelectionByQueryCostsNothing(t *testing.T) {
	m := newTestModel(t, 40)
	m.search.SetValue("artist:elvis")
	settleSearchAfter(t, m)

	press(t, m, "a")
	if !m.sel.all {
		t.Fatal("marking everything did not use a query selection")
	}
	if len(m.sel.ids) != 0 {
		t.Errorf("a query selection materialised %d ids", len(m.sel.ids))
	}
	if got := m.targetCount(); got != 10 {
		t.Errorf("target count = %d, want 10", got)
	}
	sel := m.sel.selector(nil)
	if sel.Query != "artist:elvis" {
		t.Errorf("selector query = %q", sel.Query)
	}

	// Unticking one removes it without expanding the rest.
	m.cursor = 0
	press(t, m, "space")
	if len(m.sel.excluded) != 1 {
		t.Errorf("unticking gave %d exclusions", len(m.sel.excluded))
	}
	if got := m.targetCount(); got != 9 {
		t.Errorf("after unticking one, target count = %d, want 9", got)
	}
}

// TestStageEditThenSave covers the whole editing path: stage locally, show the
// pending state, then write it through and see it on the server.
func TestStageEditThenSave(t *testing.T) {
	m := newTestModel(t, 12)
	m.search.SetValue("artist:elvis")
	settleSearchAfter(t, m)
	if m.total() != 3 {
		t.Fatalf("fixture matched %d elvis tracks, want 3", m.total())
	}

	press(t, m, "a") // mark them all
	m.mode = ModeEdit
	m.ed = editor{active: true, focus: 4} // Composer
	m.ed.editing = true
	m.ed.in.SetValue("Sam Phillips")
	m.commitEditingField()

	if m.src.dirtyCount() != 3 {
		t.Fatalf("staged %d tracks, want 3", m.src.dirtyCount())
	}
	// The staged value must show in the list before it is written.
	tr, _ := m.trackAt(0)
	if tr.Composer != "Sam Phillips" {
		t.Errorf("the list does not show the staged value: %q", tr.Composer)
	}
	if !m.src.isDirty(tr.ID) {
		t.Error("the track is not marked as having unsaved changes")
	}

	// Undo puts it back, still pending.
	m.doUndo()
	tr, _ = m.trackAt(0)
	if tr.Composer != "" {
		t.Errorf("after undo the value is %q", tr.Composer)
	}
	m.doRedo()

	pump(t, m, m.startSave())
	if m.src.dirtyCount() != 0 {
		t.Errorf("%d tracks are still pending after a save", m.src.dirtyCount())
	}

	// And the server agrees.
	page, err := m.c.ListTracks(context.Background(), library.ListParams{Query: `composer:"sam phillips"`})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 {
		t.Errorf("the server has %d tracks with the new composer, want 3", page.Total)
	}
}

// TestStaleEditIsReportedNotOverwritten is the multi-client case: something
// else changed the file while it was being edited here.
func TestStaleEditIsReportedNotOverwritten(t *testing.T) {
	m := newTestModel(t, 4)
	tr, ok := m.trackAt(0)
	if !ok {
		t.Fatal("no track")
	}

	// Stage an edit, then change the same track behind the interface's back.
	m.mode = ModeEdit
	m.ed = editor{active: true, focus: 9, editing: true} // Comment
	m.ed.in.SetValue("from the browser")
	m.commitEditingField()

	if _, err := m.c.PatchTrack(context.Background(), tr.ID,
		library.Changes{"comment": strPtr("from somewhere else")}, ""); err != nil {
		t.Fatal(err)
	}

	pump(t, m, m.startSave())

	if m.statusKind != statusError {
		t.Errorf("a conflicting save was not reported as a problem: %q", m.status)
	}
	if !strings.Contains(m.status, "changed elsewhere") {
		t.Errorf("status = %q, want it to mention the conflict", m.status)
	}
	// The conflicted edit stays pending so it is not silently lost.
	if m.src.dirtyCount() != 1 {
		t.Errorf("the conflicted edit was dropped rather than kept pending")
	}
	// And the other client's write survived.
	got, err := m.c.GetTrack(context.Background(), tr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Comment != "from somewhere else" {
		t.Errorf("the browser overwrote the other write: %q", got.Comment)
	}
}

func strPtr(s string) *string { return &s }

// TestServerEventInvalidates covers staying in step with another client.
func TestServerEventInvalidates(t *testing.T) {
	m := newTestModel(t, 8)
	if _, ok := m.trackAt(0); !ok {
		t.Fatal("the first page never arrived")
	}

	tr, _ := m.trackAt(0)
	if _, err := m.c.PatchTrack(context.Background(), tr.ID,
		library.Changes{"genre": strPtr("Skiffle")}, ""); err != nil {
		t.Fatal(err)
	}

	_, cmd := m.Update(library.Event{Type: library.EventTracksChanged, TrackIDs: []string{tr.ID}})
	pump(t, m, cmd)

	after, ok := m.trackAt(0)
	if !ok {
		t.Fatal("rows did not come back after the event")
	}
	if after.Genre != "Skiffle" {
		t.Errorf("the view did not pick up another client's edit: %q", after.Genre)
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
	if got, want := midRule(12, seps), "├───┼────┼───┤"; got != want {
		t.Errorf("midRule = %q, want %q", got, want)
	}
	if DisplayWidth(topRule(12, seps)) != 14 {
		t.Errorf("topRule width = %d, want 14", DisplayWidth(topRule(12, seps)))
	}
}

func TestPadAndTruncateAreCellExact(t *testing.T) {
	for _, s := range []string{"", "abc", "Björk", "坂本龍一", "日本語テキスト", "a坂b本c", "🎵🎶"} {
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
				if got := DisplayWidth(stripANSI(in.Render(w, focused, th, ""))); got != w {
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
}

func TestStripANSIHandlesImageSequences(t *testing.T) {
	th := NewTheme()
	img := RenderImage(ImageITerm2, fakeImageBytes(), 18, 9)
	if img == "" {
		t.Fatal("no image sequence produced")
	}
	line := th.Row.Render("left") + img + strings.Repeat(" ", 18) + th.Dim.Render("right")
	if got, want := DisplayWidth(stripANSI(line)), 4+18+5; got != want {
		t.Errorf("measured %d cells, want %d", got, want)
	}
	kit := RenderImage(ImageKitty, fakeImageBytes(), 18, 9)
	if got := DisplayWidth(stripANSI(kit)); got != 0 {
		t.Errorf("a kitty image measured %d cells, want 0", got)
	}
	if n := strings.Count(kit, "\x1b_G"); n < 2 {
		t.Errorf("kitty payload was not chunked: %d escapes", n)
	}
}

func TestDetectImageProtocol(t *testing.T) {
	cases := []struct {
		env  map[string]string
		want ImageProtocol
	}{
		{map[string]string{"TERM_PROGRAM": "iTerm.app"}, ImageITerm2},
		{map[string]string{"LC_TERMINAL": "iTerm2"}, ImageITerm2},
		{map[string]string{"TERM": "xterm-kitty"}, ImageKitty},
		{map[string]string{"KITTY_WINDOW_ID": "1"}, ImageKitty},
		{map[string]string{"TERM_PROGRAM": "Apple_Terminal"}, ImageNone},
		{map[string]string{}, ImageNone},
		{map[string]string{"TERM_PROGRAM": "iTerm.app", "TMUX": "/tmp/x"}, ImageNone},
		{map[string]string{"TERM_PROGRAM": "iTerm.app", "TAGMGR_NO_IMAGES": "1"}, ImageNone},
	}
	for _, c := range cases {
		for _, k := range []string{"TERM", "TERM_PROGRAM", "LC_TERMINAL", "KITTY_WINDOW_ID", "TMUX", "TAGMGR_NO_IMAGES"} {
			t.Setenv(k, "")
		}
		for k, v := range c.env {
			t.Setenv(k, v)
		}
		if got := DetectImageProtocol(); got != c.want {
			t.Errorf("env %v gave %v, want %v", c.env, got, c.want)
		}
	}
}

// TestImageSizeReadsHeaders covers the dimension parsing the art panel needs,
// which now works from bytes rather than from the server's measurement.
func TestImageSizeReadsHeaders(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	for _, c := range []struct {
		name string
		w, h int
	}{{"a.jpg", 120, 90}, {"b.png", 64, 200}} {
		out := filepath.Join(dir, c.name)
		if b, err := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-y",
			"-f", "lavfi", "-i", fmt.Sprintf("color=c=red:s=%dx%d", c.w, c.h),
			"-frames:v", "1", out).CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg: %v\n%s", err, b)
		}
		data, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		gotW, gotH := imageSize(data)
		if gotW != c.w || gotH != c.h {
			t.Errorf("%s measured %dx%d, want %dx%d", c.name, gotW, gotH, c.w, c.h)
		}
	}
	if w, h := imageSize([]byte("not an image")); w != 0 || h != 0 {
		t.Errorf("garbage measured %dx%d", w, h)
	}
}

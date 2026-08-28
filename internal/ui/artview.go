package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/remy/tag-manager/internal/catalog"
	"github.com/remy/tag-manager/internal/tags"
)

// artInfo is what the interface knows about one track's cover.
type artInfo struct {
	pic     *tags.Picture
	err     error
	loading bool
}

// artLoadedMsg carries a cover back from the goroutine that read it.
type artLoadedMsg struct {
	path string
	pic  *tags.Picture
	err  error
}

// artCacheLimit bounds how many covers are held at once. Each is a few hundred
// kilobytes, and moving down a long list would otherwise accumulate all of it.
const artCacheLimit = 24

// loadArtCmd reads a track's cover off the main goroutine.
//
// The read is a command rather than an inline call because on a NAS it is a
// network round trip, and doing that on every cursor movement would make the
// list stutter in exactly the situation this program exists for.
func loadArtCmd(path string) tea.Cmd {
	return func() tea.Msg {
		pic, err := tags.ReadCover(path)
		return artLoadedMsg{path: path, pic: pic, err: err}
	}
}

// artCached looks up a cover without starting a read.
//
// The view must not have side effects: it runs on every frame, and a lookup
// that queued work would fire a read per repaint rather than per track.
func (m *Model) artCached(path string) *artInfo {
	if m.art == nil {
		return nil
	}
	return m.art[path]
}

// ensureArt starts reading a track's cover if it is not already cached, and
// returns the command that will deliver it.
func (m *Model) ensureArt(path string, hasArt bool) tea.Cmd {
	if !hasArt || path == "" {
		return nil
	}
	if m.art == nil {
		m.art = map[string]*artInfo{}
	}
	if _, ok := m.art[path]; ok {
		return nil
	}
	if len(m.art) >= artCacheLimit {
		m.art = map[string]*artInfo{} // cheaper than tracking an eviction order
	}
	m.art[path] = &artInfo{loading: true}
	return loadArtCmd(path)
}

// applyArtLoaded folds a finished read into the cache.
func (m *Model) applyArtLoaded(msg artLoadedMsg) {
	if m.art == nil {
		return
	}
	if info, ok := m.art[msg.path]; ok {
		info.pic, info.err, info.loading = msg.pic, msg.err, false
	}
}

// artLine describes the current track's cover in one line of text, including
// how much of the album shares it.
func (m *Model) artLine(width int) string {
	th := m.theme
	idx, ok := m.currentTrack()
	if !ok {
		return th.Dim.Render(Pad("", width))
	}
	t := &m.cat.Tracks[idx]
	if !t.HasArt {
		return th.Dim.Render(Pad("artwork: none", width))
	}

	info := m.artCached(t.Path)
	switch {
	case info == nil, info.loading:
		return th.Dim.Render(Pad("artwork: reading…", width))
	case info.err != nil:
		return th.Warn.Render(Pad("artwork: unreadable", width))
	case info.pic == nil:
		return th.Dim.Render(Pad("artwork: none", width))
	}

	s := "artwork: " + info.pic.Summary()
	if n := m.albumArtCount(t); n > 1 {
		s += fmt.Sprintf("  ·  shared with %d tracks in this album", n)
	}
	return th.FieldValue.Render(Pad(s, width))
}

// albumArtCount counts how many tracks of the same album carry art, which is
// what tells you whether a cover needs pasting across the rest of them.
func (m *Model) albumArtCount(t *catalog.Track) int {
	if t.Album == "" {
		return 0
	}
	n := 0
	for i := range m.cat.Tracks {
		o := &m.cat.Tracks[i]
		if o.Album == t.Album && o.AlbumArtist == t.AlbumArtist && o.HasArt {
			n++
		}
	}
	return n
}

// artPanelRows is the height of the panel when a cover is being previewed.
const artPanelRows = 9

// renderArtPreview draws the cover as an image beside its details.
//
// The escape sequence measures as nothing, so the cells the terminal will
// cover with the picture are padded explicitly. Get that wrong and the frame's
// right-hand rule lands in the middle of the image.
func (m *Model) renderArtPreview(inner int) []string {
	w := inner - 2
	rows := artPanelRows

	// Terminal cells are roughly twice as tall as they are wide, so a square
	// cover needs twice as many columns as rows. Reserve nothing when the
	// terminal cannot draw it, rather than leaving a blank hole.
	imgCols := 0
	if m.imgProto != ImageNone {
		imgCols = rows * 2
		if imgCols > w/2 {
			imgCols = w / 2
		}
	}

	lines := make([]string, 0, rows)
	idx, ok := m.currentTrack()
	if !ok {
		return lines
	}
	t := &m.cat.Tracks[idx]

	var pic *tags.Picture
	if info := m.artCached(t.Path); info != nil {
		pic = info.pic
	}

	gap := 0
	if imgCols > 0 {
		gap = 2
	}
	textW := max(w-imgCols-gap, 8)
	details := m.artDetailLines(t, pic, textW)

	for i := 0; i < rows; i++ {
		left := strings.Repeat(" ", imgCols)
		if i == 0 && pic != nil && imgCols > 0 {
			// The image is emitted once, on the panel's first line; the
			// terminal paints it down and across from there, so the cells it
			// will cover are padded here and the lines beneath left blank.
			left = RenderImage(m.imgProto, pic.Data, imgCols, rows) + left
		}
		right := ""
		if i < len(details) {
			right = details[i]
		}
		lines = append(lines, " "+left+strings.Repeat(" ", gap)+padStyled(right, textW)+" ")
	}
	return lines
}

// artDetailLines describes the cover and the track beside the preview.
func (m *Model) artDetailLines(t *catalog.Track, pic *tags.Picture, w int) []string {
	th := m.theme
	out := []string{
		th.Title.Render(Truncate(t.Album, w)),
		th.Dim.Render(Truncate(t.AlbumArtist+t.Artist, w)),
		"",
	}
	if pic == nil {
		out = append(out, th.Dim.Render("no artwork"))
	} else {
		out = append(out,
			th.FieldValue.Render(Truncate(pic.Summary(), w)),
			th.Dim.Render(Truncate(pic.Kind.String(), w)),
		)
	}
	if n := m.albumArtCount(t); n > 0 {
		out = append(out, th.Dim.Render(Truncate(fmt.Sprintf("%d tracks in this album have art", n), w)))
	}
	if m.imgProto == ImageNone {
		out = append(out, th.Dim.Render(Truncate("this terminal cannot show images", w)))
	}
	out = append(out, "", th.Dim.Render(Truncate("y copy   p paste   A close", w)))
	return out
}

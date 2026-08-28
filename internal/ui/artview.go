package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/remy/tag-manager/internal/client"
	"github.com/remy/tag-manager/internal/library"
)

// artInfo is what the interface knows about one track's cover.
type artInfo struct {
	data    []byte
	mime    string
	err     error
	loading bool
}

// artLoadedMsg carries a cover back from the request that fetched it.
type artLoadedMsg struct {
	id   string
	data []byte
	mime string
	err  error
}

// artCacheLimit bounds how many covers are held at once. Each is a few hundred
// kilobytes, and moving down a long list would otherwise accumulate all of it.
const artCacheLimit = 24

// artTimeout bounds one cover fetch.
const artTimeout = 20 * time.Second

// artCached looks up a cover without starting a request.
//
// The view must not have side effects: it runs on every frame, and a lookup
// that queued work would fetch once per repaint rather than once per track.
func (m *Model) artCached(id string) *artInfo { return m.art[id] }

// ensureArt starts fetching a track's cover if it is not already cached.
func (m *Model) ensureArt(id string, hasArt bool) tea.Cmd {
	if !hasArt || id == "" {
		return nil
	}
	if m.art == nil {
		m.art = map[string]*artInfo{}
	}
	if _, ok := m.art[id]; ok {
		return nil
	}
	if len(m.art) >= artCacheLimit {
		m.art = map[string]*artInfo{} // cheaper than tracking an eviction order
	}
	m.art[id] = &artInfo{loading: true}

	c := m.c
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), artTimeout)
		defer cancel()
		data, mime, err := c.Artwork(ctx, id)
		return artLoadedMsg{id: id, data: data, mime: mime, err: err}
	}
}

// applyArtLoaded folds a finished fetch into the cache.
func (m *Model) applyArtLoaded(msg artLoadedMsg) {
	if info, ok := m.art[msg.id]; ok {
		info.data, info.mime, info.err, info.loading = msg.data, msg.mime, msg.err, false
	}
}

// currentArtCmd starts fetching the cover for the track under the cursor.
func (m *Model) currentArtCmd() tea.Cmd {
	if !m.showDetail && !m.showArt {
		return nil // nothing on screen would use it
	}
	t, ok := m.currentTrack()
	if !ok {
		return nil
	}
	return m.ensureArt(t.ID, t.HasArt)
}

// artLine describes the current track's cover in one line of text.
func (m *Model) artLine(width int) string {
	th := m.theme
	t, ok := m.currentTrack()
	if !ok {
		return th.Dim.Render(Pad("", width))
	}
	if !t.HasArt {
		return th.Dim.Render(Pad("artwork: none", width))
	}
	info := m.artCached(t.ID)
	switch {
	case info == nil, info.loading:
		return th.Dim.Render(Pad("artwork: reading…", width))
	case info.err != nil:
		return th.Warn.Render(Pad("artwork: unreadable", width))
	case len(info.data) == 0:
		return th.Dim.Render(Pad("artwork: none", width))
	}
	return th.FieldValue.Render(Pad("artwork: "+describeImage(info), width))
}

// describeImage renders a cover for display. The server has already measured
// it, but the browser only holds the bytes, so the dimensions come from the
// image header rather than a second request.
func describeImage(info *artInfo) string {
	kind := strings.TrimPrefix(info.mime, "image/")
	if kind == "" {
		kind = "image"
	}
	w, h := imageSize(info.data)
	if w > 0 && h > 0 {
		return fmt.Sprintf("%d×%d %s %s", w, h, kind, FormatBytes(int64(len(info.data))))
	}
	return fmt.Sprintf("%s %s", kind, FormatBytes(int64(len(info.data))))
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
		imgCols = min(rows*2, w/2)
	}

	lines := make([]string, 0, rows)
	t, ok := m.currentTrack()
	if !ok {
		return lines
	}
	var info *artInfo
	if i := m.artCached(t.ID); i != nil && i.err == nil && len(i.data) > 0 {
		info = i
	}

	gap := 0
	if imgCols > 0 {
		gap = 2
	}
	textW := max(w-imgCols-gap, 8)
	details := m.artDetailLines(&t, info, textW)

	for i := 0; i < rows; i++ {
		left := strings.Repeat(" ", imgCols)
		if i == 0 && info != nil && imgCols > 0 {
			// The image is emitted once, on the panel's first line; the
			// terminal paints it down and across from there, so the cells it
			// will cover are padded here and the lines beneath left blank.
			left = RenderImage(m.imgProto, info.data, imgCols, rows) + left
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
func (m *Model) artDetailLines(t *library.Track, info *artInfo, w int) []string {
	th := m.theme
	artist := t.AlbumArtist
	if artist == "" {
		artist = t.Artist
	}
	out := []string{
		th.Title.Render(Truncate(t.Album, w)),
		th.Dim.Render(Truncate(artist, w)),
		"",
	}
	switch {
	case info != nil:
		out = append(out, th.FieldValue.Render(Truncate(describeImage(info), w)))
	case t.HasArt:
		out = append(out, th.Dim.Render(Truncate("reading…", w)))
	default:
		out = append(out, th.Dim.Render(Truncate("no artwork", w)))
	}
	if m.imgProto == ImageNone {
		out = append(out, th.Dim.Render(Truncate("this terminal cannot show images", w)))
	}
	out = append(out, "", th.Dim.Render(Truncate("y copy   p paste   A close", w)))
	return out
}

// imageSize reads the dimensions out of a JPEG or PNG header.
//
// Only the header is parsed: the interface needs two numbers, not a decoded
// bitmap, and decoding a cover on every repaint would be absurd.
func imageSize(b []byte) (int, int) {
	if len(b) >= 24 && string(b[:8]) == "\x89PNG\r\n\x1a\n" && string(b[12:16]) == "IHDR" {
		return int(be32(b[16:20])), int(be32(b[20:24]))
	}
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return 0, 0
	}
	for i := 2; i+9 < len(b); {
		if b[i] != 0xFF {
			i++
			continue
		}
		marker := b[i+1]
		// The start-of-frame markers carry the dimensions; the rest are
		// skipped by their declared length.
		if marker >= 0xC0 && marker <= 0xCF && marker != 0xC4 && marker != 0xC8 && marker != 0xCC {
			return int(b[i+7])<<8 | int(b[i+8]), int(b[i+5])<<8 | int(b[i+6])
		}
		if marker == 0xD8 || (marker >= 0xD0 && marker <= 0xD9) {
			i += 2
			continue
		}
		i += 2 + int(b[i+2])<<8 + int(b[i+3])
	}
	return 0, 0
}

func be32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

// --- copying and pasting ------------------------------------------------

// artCopiedMsg reports the outcome of copying to the clipboard.
type artCopiedMsg struct {
	info *client.PictureInfo
	err  error
}

// artPastedMsg reports the outcome of writing artwork across a selection.
type artPastedMsg struct {
	job *library.Job
	err error
}

// yankArt copies the current track's cover to the server's clipboard, from
// where any client can paste it.
func (m *Model) yankArt() tea.Cmd {
	t, ok := m.currentTrack()
	if !ok {
		return nil
	}
	if !t.HasArt {
		m.setStatus(statusWarn, "this track has no artwork to copy")
		return nil
	}
	c, id := m.c, t.ID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), artTimeout)
		defer cancel()
		info, err := c.CopyArtworkFromTrack(ctx, id)
		return artCopiedMsg{info: info, err: err}
	}
}

// pasteArt writes the clipboard image onto every selected track.
//
// Unlike a tag edit this is not staged for ^s. Holding several hundred covers
// in memory to write later would cost more than the library itself, and there
// is no useful way to show a pending image in a list of text.
func (m *Model) pasteArt() tea.Cmd {
	n := m.targetCount()
	if n == 0 {
		return nil
	}
	sel := m.sel.selector(nil)
	if m.sel.empty() {
		t, ok := m.currentTrack()
		if !ok {
			return nil
		}
		sel = library.Selector{IDs: []string{t.ID}}
	}

	m.artWriting = n
	m.setStatus(statusInfo, "writing artwork to %s tracks…", FormatCount(n))
	c := m.c
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		job, err := c.BatchArtwork(ctx, client.BatchArtworkRequest{
			Selector: sel, Source: "clipboard",
		})
		if err != nil {
			return artPastedMsg{err: err}
		}
		done, err := c.WaitJob(ctx, job.ID, nil)
		return artPastedMsg{job: done, err: err}
	}
}

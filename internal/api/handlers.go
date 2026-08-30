package api

import (
	"encoding/base64"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/remy/tag-manager/internal/discogs"
	"github.com/remy/tag-manager/internal/library"
	"github.com/remy/tag-manager/internal/tags"
)

func (s *Server) listTracks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.List(listParams(r)))
}

func (s *Server) getTrack(w http.ResponseWriter, r *http.Request) {
	t, err := s.svc.Get(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("ETag", strconv.Quote(t.Version))
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) patchTrack(w http.ResponseWriter, r *http.Request) {
	var ch library.Changes
	if err := decodeJSON(r, &ch); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	// The header is quoted per HTTP, but a client that sends the bare version
	// is being reasonable rather than wrong.
	ifMatch := unquote(r.Header.Get("If-Match"))

	t, err := s.svc.Patch(r.PathValue("id"), ch, ifMatch)
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("ETag", strconv.Quote(t.Version))
	writeJSON(w, http.StatusOK, t)
}

func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

func (s *Server) listAlbums(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.Albums(listParams(r)))
}

func (s *Server) listValues(w http.ResponseWriter, r *http.Request) {
	vals, err := s.svc.Values(r.PathValue("field"), r.URL.Query().Get("prefix"), intParam(r, "limit", 20))
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]valueCount, 0, len(vals))
	for _, v := range vals {
		out = append(out, valueCount{Value: v.Value, Count: v.Count})
	}
	writeJSON(w, http.StatusOK, out)
}

type valueCount struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

func (s *Server) getStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.Stats())
}

// --- artwork ------------------------------------------------------------

func (s *Server) getArtwork(w http.ResponseWriter, r *http.Request) {
	pic, err := s.svc.Artwork(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	writeImage(w, pic)
}

// getAudio streams the file itself, so a song can be listened to rather than
// only read about.
//
// http.ServeContent rather than a copy: it answers Range requests, which is
// what makes a player able to seek without pulling the whole file, and it
// handles conditional requests from the modification time for free.
func (s *Server) getAudio(w http.ResponseWriter, r *http.Request) {
	path, mime, err := s.svc.Audio(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		// The catalogue is a snapshot, so a track it lists can have been moved
		// or deleted since the scan. That is a missing resource rather than a
		// server fault, and saying so is what tells a client to rescan.
		if errors.Is(err, fs.ErrNotExist) {
			writeError(w, http.StatusNotFound, "not_found", "the file is no longer where the catalogue says it is")
			return
		}
		fail(w, err)
		return
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		fail(w, err)
		return
	}
	// Set before serving: ServeContent only sniffs a type when the header is
	// empty, and sniffing reads the first bytes of the file to guess what the
	// catalogue already knows.
	w.Header().Set("Content-Type", mime)
	http.ServeContent(w, r, filepath.Base(path), fi.ModTime(), f)
}

func writeImage(w http.ResponseWriter, pic *tags.Picture) {
	mime := pic.MIME
	if mime == "" {
		mime = "application/octet-stream"
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Length", strconv.Itoa(len(pic.Data)))
	w.Header().Set("ETag", strconv.Quote(pictureETag(pic)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pic.Data)
}

// maxImageBytes bounds an uploaded cover. Real artwork runs to a few hundred
// kilobytes; this is generous enough for anything genuine and small enough
// that a stray upload cannot exhaust memory.
const maxImageBytes = 32 << 20

// readImage reads an uploaded image body.
func readImage(r *http.Request) (*tags.Picture, error) {
	data, err := io.ReadAll(io.LimitReader(r.Body, maxImageBytes))
	if err != nil {
		return nil, err
	}
	// The format comes from the content, not the Content-Type header: a client
	// that mislabels a PNG as JPEG should still get a working cover.
	return tags.NewPicture(data)
}

func (s *Server) putArtwork(w http.ResponseWriter, r *http.Request) {
	pic, err := readImage(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	id := r.PathValue("id")
	if err := s.svc.SetArtwork(id, pic); err != nil {
		fail(w, err)
		return
	}
	t, err := s.svc.Get(id)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) deleteArtwork(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.SetArtwork(r.PathValue("id"), nil); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) artworkSummary(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.ArtworkSummary(r.URL.Query().Get("q")))
}

// batchArtworkBody mirrors the schema; the image arrives base64 encoded so the
// whole request stays one JSON document.
type batchArtworkBody struct {
	Selector library.Selector `json:"selector"`
	Source   string           `json:"source"`
	Image    string           `json:"image,omitempty"`
	DryRun   bool             `json:"dryRun,omitempty"`
}

func (s *Server) batchArtwork(w http.ResponseWriter, r *http.Request) {
	var body batchArtworkBody
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	req := library.BatchArtworkRequest{
		Selector: body.Selector,
		Source:   library.ArtworkSource(body.Source),
		DryRun:   body.DryRun,
	}
	if body.Image != "" {
		data, err := base64.StdEncoding.DecodeString(body.Image)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "image is not valid base64")
			return
		}
		req.Image = data
	}
	job, err := s.svc.BatchArtwork(req)
	if err != nil {
		fail(w, err)
		return
	}
	writeJob(w, job)
}

// --- clipboard ----------------------------------------------------------

func (s *Server) getClipboard(w http.ResponseWriter, r *http.Request) {
	held, err := s.svc.Clipboard().Paste()
	if err != nil {
		fail(w, library.ErrNotFound)
		return
	}
	writeImage(w, &held.Picture)
}

func (s *Server) putClipboard(w http.ResponseWriter, r *http.Request) {
	pic, err := readImage(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if err := s.svc.Clipboard().Copy(pic, "upload"); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, describePicture(pic, "upload"))
}

func (s *Server) deleteClipboard(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.Clipboard().Clear(); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) copyArtworkFromTrack(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	pic, err := s.svc.Artwork(id)
	if err != nil {
		fail(w, err)
		return
	}
	path, _ := s.svc.Path(id)
	if err := s.svc.Clipboard().Copy(pic, path); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, describePicture(pic, path))
}

type pictureInfo struct {
	MIME    string `json:"mime,omitempty"`
	Width   int    `json:"width,omitempty"`
	Height  int    `json:"height,omitempty"`
	Bytes   int    `json:"bytes"`
	Kind    string `json:"kind,omitempty"`
	Summary string `json:"summary"`
	Source  string `json:"source,omitempty"`
}

func describePicture(p *tags.Picture, source string) pictureInfo {
	return pictureInfo{
		MIME: p.MIME, Width: p.Width, Height: p.Height, Bytes: len(p.Data),
		Kind: p.Kind.String(), Summary: p.Summary(), Source: source,
	}
}

func pictureETag(p *tags.Picture) string {
	return strconv.Itoa(len(p.Data)) + "-" + strconv.Itoa(p.Width) + "x" + strconv.Itoa(p.Height)
}

// --- batch and maintenance ----------------------------------------------

// splitTracks pulls the artist out of the title across a selection.
func (s *Server) splitTracks(w http.ResponseWriter, r *http.Request) {
	var req library.SplitRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	job, err := s.svc.Split(req)
	if err != nil {
		// A template that cannot be parsed is the caller's mistake, and saying
		// which part of it is wrong is the whole value of the message.
		if errors.Is(err, library.ErrBadTemplate) {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		fail(w, err)
		return
	}
	writeJob(w, job)
}

func (s *Server) batchEditTracks(w http.ResponseWriter, r *http.Request) {
	var req library.BatchSetRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	job, err := s.svc.BatchSet(req)
	if err != nil {
		fail(w, err)
		return
	}
	writeJob(w, job)
}

func (s *Server) stripTags(w http.ResponseWriter, r *http.Request) {
	// A strip defaults to a dry run, so the absence of the field must mean
	// true rather than Go's zero value.
	req := library.StripRequest{DryRun: true}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	job, err := s.svc.Strip(req)
	if err != nil {
		fail(w, err)
		return
	}
	writeJob(w, job)
}

func (s *Server) listBackups(w http.ResponseWriter, r *http.Request) {
	b, err := s.svc.Backups()
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) restoreBackup(w http.ResponseWriter, r *http.Request) {
	var req library.RestoreRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	job, err := s.svc.Restore(req)
	if err != nil {
		fail(w, err)
		return
	}
	writeJob(w, job)
}

func (s *Server) getScanStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.ScanStatus())
}

func (s *Server) startScan(w http.ResponseWriter, r *http.Request) {
	var req library.ScanRequest
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
	}
	job, err := s.svc.Scan(req)
	if err != nil {
		fail(w, err)
		return
	}
	writeJob(w, job)
}

// --- jobs ---------------------------------------------------------------

func writeJob(w http.ResponseWriter, j *library.Job) {
	w.Header().Set("Location", "/v1/jobs/"+j.ID)
	writeJSON(w, http.StatusAccepted, j)
}

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.JobRegistry().List())
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	j, err := s.svc.JobRegistry().Get(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, j)
}

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	err := s.svc.JobRegistry().Cancel(r.PathValue("id"))
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case err == library.ErrNotFound:
		fail(w, err)
	default:
		writeError(w, http.StatusConflict, "conflict", err.Error())
	}
}

// --- discogs ------------------------------------------------------------
//
// The server makes these calls rather than the browser for three reasons, and
// the third is the one that forces it. Discogs wants a User-Agent it can
// attribute. The per-minute budget is per IP, so it can only be paced in one
// place. And the image host sends no CORS header, so a browser can display a
// cover but cannot read its bytes to upload them — the download has to happen
// here or not at all.

func (s *Server) discogsSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	res, err := s.svc.DiscogsSearch(r.Context(), q, limit)
	if err != nil {
		failDiscogs(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) discogsMaster(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "the master id must be a number")
		return
	}
	m, err := s.svc.DiscogsMaster(r.Context(), id)
	if err != nil {
		failDiscogs(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// discogsAlbum looks one album up for the fields the Get Info sheet can fill
// in, which is a different question from finding a cover and a far cheaper
// one: a search already carries the year and the genres.
func (s *Server) discogsAlbum(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if strings.TrimSpace(q.Get("album")) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "an album name is required")
		return
	}
	info, err := s.svc.DiscogsAlbum(r.Context(), q.Get("artist"), q.Get("album"))
	if err != nil {
		failDiscogs(w, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// copyArtworkFromURL puts a Discogs cover on the clipboard, from where the
// existing paste applies it to one track or to a whole album.
func (s *Server) copyArtworkFromURL(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL string `json:"url"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	pic, err := s.svc.CopyArtworkFromURL(r.Context(), body.URL)
	if err != nil {
		failDiscogs(w, err)
		return
	}
	writeJSON(w, http.StatusOK, describePicture(pic, "discogs"))
}

// failDiscogs maps the lookup's own failures before deferring to fail.
//
// A rate limit is 429 with a Retry-After, because the client's right response
// is to wait a stated number of seconds rather than to treat it as an outage.
// A URL off the allowlist is the caller's mistake, not a server fault.
func failDiscogs(w http.ResponseWriter, err error) {
	var limited *discogs.RateLimitError
	switch {
	case errors.Is(err, library.ErrNoDiscogs):
		writeError(w, http.StatusServiceUnavailable, "unavailable", err.Error())
	case errors.As(err, &limited):
		w.Header().Set("Retry-After", strconv.Itoa(int(limited.RetryAfter.Seconds()+0.999)))
		writeError(w, http.StatusTooManyRequests, "rate_limited", limited.Error())
	case errors.Is(err, discogs.ErrNotAllowed):
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
	case errors.Is(err, discogs.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	default:
		fail(w, err)
	}
}

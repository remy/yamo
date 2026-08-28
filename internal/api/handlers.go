package api

import (
	"encoding/base64"
	"io"
	"net/http"
	"strconv"

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

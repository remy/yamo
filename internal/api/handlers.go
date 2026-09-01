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

	"github.com/remy/yamo/internal/discogs"
	"github.com/remy/yamo/internal/library"
	"github.com/remy/yamo/internal/tags"
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
	if notModified(w, r, t.Version) {
		return
	}
	w.Header().Set("ETag", strconv.Quote(t.Version))
	writeJSON(w, http.StatusOK, t)
}

// getRawTags lists what the file actually holds, rather than what the
// catalogue made of it. It is the pre-flight for a strip.
func (s *Server) getRawTags(w http.ResponseWriter, r *http.Request) {
	raw, err := s.svc.RawTags(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, raw)
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

// deleteTrack removes the file itself, not just its tags.
//
// If-Match is honoured for the same reason it is on an edit, and matters more:
// this is the one request that cannot be undone from inside the program.
func (s *Server) deleteTrack(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.Delete(r.PathValue("id"), unquote(r.Header.Get("If-Match"))); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// renameTrack moves the file. A track's id is derived from its path, so the
// response is the track under its new identity and Location points at it.
func (s *Server) renameTrack(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	t, err := s.svc.Rename(r.PathValue("id"), body.Path, unquote(r.Header.Get("If-Match")))
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Location", "/v1/tracks/"+t.ID)
	w.Header().Set("ETag", strconv.Quote(t.Version))
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) listAlbums(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.Albums(listParams(r)))
}

func (s *Server) listArtists(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.Artists(listParams(r)))
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

// getArtwork serves a track's cover, optionally scaled down.
//
// The ETag is the track's version rather than the image's, so a cover that
// changed because the file was rewritten invalidates, and the size is folded
// into it so a grid asking for thumbnails is not served the full-size image
// out of a cache keyed only by the track.
func (s *Server) getArtwork(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	size := intParam(r, "size", 0)

	etag := ""
	if t, err := s.svc.Get(id); err == nil {
		etag = t.Version
		if size > 0 {
			etag += "@" + strconv.Itoa(size)
		}
		if notModified(w, r, etag) {
			return
		}
	}

	var pic *tags.Picture
	var err error
	if size > 0 {
		pic, err = s.svc.Thumbnail(id, size)
	} else {
		pic, err = s.svc.Artwork(id)
	}
	if err != nil {
		fail(w, err)
		return
	}
	writeImageETag(w, pic, etag)
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
	writeImageETag(w, pic, "")
}

// writeImageETag serves an image, preferring a caller-supplied validator.
//
// The picture's own tag — its size and dimensions — is a weak identifier: two
// different covers of the same size would share it. It is used only where
// there is no better one, which is the clipboard.
func writeImageETag(w http.ResponseWriter, pic *tags.Picture, etag string) {
	mime := pic.MIME
	if mime == "" {
		mime = "application/octet-stream"
	}
	if etag == "" {
		etag = pictureETag(pic)
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Length", strconv.Itoa(len(pic.Data)))
	w.Header().Set("ETag", strconv.Quote(etag))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pic.Data)
}

// maxImageBytes bounds an uploaded cover. Real artwork runs to a few hundred
// kilobytes; this is generous enough for anything genuine and small enough
// that a stray upload cannot exhaust memory.
const maxImageBytes = library.MaxImageBytes

// errTooLarge means the upload exceeded the bound.
var errTooLarge = errors.New("the image is larger than this server accepts")

// readImage reads an uploaded image body.
//
// One byte more than the limit is read so that an oversized upload is refused
// rather than truncated. Reading exactly the limit and stopping would embed
// the first 32MB of a larger file as if it were the whole image, which is a
// corrupt cover written into every selected track and no error anywhere.
func readImage(r *http.Request) (*tags.Picture, error) {
	data, err := io.ReadAll(io.LimitReader(r.Body, maxImageBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxImageBytes {
		return nil, errTooLarge
	}
	// The format comes from the content, not the Content-Type header: a client
	// that mislabels a PNG as JPEG should still get a working cover.
	return tags.NewPicture(data)
}

// failImage answers an upload that could not be read. Too large is its own
// status because the client's answer is to send a smaller image, not to fix a
// malformed one.
func failImage(w http.ResponseWriter, err error) {
	if errors.Is(err, errTooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", err.Error())
		return
	}
	writeError(w, http.StatusBadRequest, "bad_request", err.Error())
}

func (s *Server) putArtwork(w http.ResponseWriter, r *http.Request) {
	pic, err := readImage(r)
	if err != nil {
		failImage(w, err)
		return
	}
	id := r.PathValue("id")
	if err := s.svc.SetArtwork(id, pic, unquote(r.Header.Get("If-Match"))); err != nil {
		fail(w, err)
		return
	}
	t, err := s.svc.Get(id)
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("ETag", strconv.Quote(t.Version))
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) deleteArtwork(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.SetArtwork(r.PathValue("id"), nil, unquote(r.Header.Get("If-Match"))); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) artworkSummary(w http.ResponseWriter, r *http.Request) {
	rep, err := s.svc.ArtworkSummary(selectorFromQuery(r))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// exportArtwork writes each selection's embedded cover out beside the music,
// which is the direction the folder source does not go.
func (s *Server) exportArtwork(w http.ResponseWriter, r *http.Request) {
	var req library.ExportArtworkRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	job, err := s.svc.ExportArtwork(req)
	if err != nil {
		fail(w, err)
		return
	}
	writeJob(w, job)
}

// batchArtworkBody mirrors the schema; the image arrives base64 encoded so the
// whole request stays one JSON document.
type batchArtworkBody struct {
	Selector library.Selector `json:"selector"`
	Source   string           `json:"source"`
	Image    string           `json:"image,omitempty"`
	DryRun   bool             `json:"dryRun,omitempty"`
	Backup   bool             `json:"backup,omitempty"`
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
		Backup:   body.Backup,
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
	if notModified(w, r, pictureETag(&held.Picture)) {
		return
	}
	writeImage(w, &held.Picture)
}

func (s *Server) putClipboard(w http.ResponseWriter, r *http.Request) {
	pic, err := readImage(r)
	if err != nil {
		failImage(w, err)
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
	// Journalled unless the client says otherwise, so the absence of the
	// field means true rather than Go's zero value.
	req := library.SplitRequest{Backup: true}
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
	// Journalled unless the client says otherwise. This is the operation the
	// API exists for and the one most likely to be regretted, so the record
	// that makes it reversible has to be there without being asked for.
	req := library.BatchSetRequest{Backup: true}
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

// renameTracks renames a whole selection after the tags each file carries,
// which is the other direction from a split.
func (s *Server) renameTracks(w http.ResponseWriter, r *http.Request) {
	req := library.RenameTracksRequest{Backup: true}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	job, err := s.svc.RenameTracks(req)
	if err != nil {
		if errors.Is(err, library.ErrBadTemplate) {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		fail(w, err)
		return
	}
	writeJob(w, job)
}

// listDuplicates groups tracks that look like the same recording.
func (s *Server) listDuplicates(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	writeJSON(w, http.StatusOK, s.svc.Duplicates(library.DuplicateParams{
		Query:           q.Get("q"),
		By:              listParam(q["by"]),
		DurationSeconds: intParam(r, "durationSeconds", 0),
		Limit:           intParam(r, "limit", library.DefaultLimit),
		Offset:          intParam(r, "offset", 0),
	}))
}

// listFolders lists one level of the directory tree, for browsing a library
// whose tags are not yet good enough to browse by album.
func (s *Server) listFolders(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.Folders(library.FolderParams{
		Path:   r.URL.Query().Get("path"),
		Query:  r.URL.Query().Get("q"),
		Limit:  intParam(r, "limit", library.DefaultLimit),
		Offset: intParam(r, "offset", 0),
	}))
}

// getCapabilities describes the server rather than the library, which is why
// it is the one operation served without a token: a client needs to know
// whether a token is required before it can present one.
func (s *Server) getCapabilities(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.Capabilities(s.opts.Token != "", s.opts.AllowCrossOrigin))
}

// identity is what the server can say about the caller. With a single shared
// token there is no identity to report, so the useful answer is whether the
// credentials work at all — which a client otherwise has to discover by
// making a real request and reading the failure.
type identity struct {
	Authenticated bool     `json:"authenticated"`
	TokenRequired bool     `json:"tokenRequired"`
	Scopes        []string `json:"scopes"`
}

func (s *Server) getMe(w http.ResponseWriter, r *http.Request) {
	// Reaching this handler means the token check passed, so the answer is
	// always yes; the request would have been a 401 otherwise.
	writeJSON(w, http.StatusOK, identity{
		Authenticated: true,
		TokenRequired: s.opts.Token != "",
		// One token, and it grants everything. Named rather than left implicit
		// so that a client written against this keeps working if the answer
		// ever becomes narrower.
		Scopes: []string{"read", "write"},
	})
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
	b, err := s.svc.Backups(intParam(r, "limit", library.DefaultLimit), intParam(r, "offset", 0))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

// getBackup describes what one journal holds, so a client can see what a
// restore would put back before asking for it.
func (s *Server) getBackup(w http.ResponseWriter, r *http.Request) {
	b, err := s.svc.Backup(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

// deleteBackup discards a journal. Nothing expires them, so this is how they
// stop accumulating.
func (s *Server) deleteBackup(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.DeleteBackup(r.PathValue("id")); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
	q := r.URL.Query()
	writeJSON(w, http.StatusOK, s.svc.JobRegistry().Page(library.JobFilter{
		Kind:   q.Get("kind"),
		State:  q.Get("state"),
		Limit:  intParam(r, "limit", library.DefaultLimit),
		Offset: intParam(r, "offset", 0),
	}))
}

// undoJob reverses a job by restoring the journal it wrote.
func (s *Server) undoJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.svc.Undo(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJob(w, job)
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
	setRateLimit(w, res.Limit, res.Remaining)
	writeJSON(w, http.StatusOK, res)
}

// setRateLimit reports the Discogs budget in headers as well as in the body.
//
// The body carries it because it is worth showing a person, and a header
// carries it because the code that has to pace itself is often not the code
// that parses the response — a fetch wrapper, a queue, a retry policy. A 429
// already answers in headers; answering the same way on success is what lets
// a client slow down before it gets there rather than after.
func setRateLimit(w http.ResponseWriter, limit, remaining int) {
	if limit <= 0 {
		return
	}
	h := w.Header()
	h.Set("RateLimit-Limit", strconv.Itoa(limit))
	h.Set("RateLimit-Remaining", strconv.Itoa(max(remaining, 0)))
	// The budget is per minute, so the window a client should wait out is
	// never more than one.
	h.Set("RateLimit-Reset", "60")
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
	setRateLimit(w, info.Limit, info.Remaining)
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

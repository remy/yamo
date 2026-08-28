package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/remy/tag-manager/internal/library"
)

// streamEvents sends every catalogue change as a server-sent event.
//
// This is what lets several interfaces stay in step: an edit made on a phone
// pushes tracks.changed, and the terminal drops those rows from its cache
// rather than polling for them.
func (s *Server) streamEvents(w http.ResponseWriter, r *http.Request) {
	s.stream(w, r, func(e library.Event) bool { return true })
}

// streamJobEvents narrows the stream to one job.
func (s *Server) streamJobEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.svc.JobRegistry().Get(id); err != nil {
		fail(w, err)
		return
	}
	s.stream(w, r, func(e library.Event) bool {
		return e.Job != nil && e.Job.ID == id
	})
}

// sseKeepAlive bounds how long the connection can be silent. Proxies and
// mobile radios drop idle connections, and a comment costs nothing.
const sseKeepAlive = 25 * time.Second

func (s *Server) stream(w http.ResponseWriter, r *http.Request, want func(library.Event) bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal", "streaming is not supported here")
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	// Without this an intermediary may buffer the stream into uselessness.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	events, cancel := s.svc.Events().Subscribe()
	defer cancel()

	ticker := time.NewTicker(sseKeepAlive)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		case e, open := <-events:
			if !open {
				return // the server is shutting down
			}
			if !want(e) {
				continue
			}
			payload, err := json.Marshal(e)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Type, payload)
			flusher.Flush()
		}
	}
}

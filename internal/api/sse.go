package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/remy/yamo/internal/library"
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

	epoch, after := resumeFrom(r)
	events, backlog, cancel := s.svc.Events().SubscribeFrom(epoch, after)
	defer cancel()

	send := func(e library.Event) {
		if !want(e) {
			return
		}
		payload, err := json.Marshal(e)
		if err != nil {
			return
		}
		// The id line is what the browser's EventSource sends back as
		// Last-Event-ID after a dropped connection, which is what makes the
		// resume automatic rather than something every client implements.
		fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", eventID(e), e.Type, payload)
		flusher.Flush()
	}

	// The backlog first, in order, before anything new: a client resuming has
	// to see what it missed before it sees what happened since.
	for _, e := range backlog {
		// A gap is addressed to this subscriber rather than to the stream, so
		// it goes out whatever the filter is: a job's stream that lost events
		// is as much in the dark as the whole library's.
		if e.Type == library.EventGap {
			payload, err := json.Marshal(e)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", eventID(e), e.Type, payload)
			flusher.Flush()
			continue
		}
		send(e)
	}

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
			send(e)
		}
	}
}

// eventID is the identifier a client sends back to resume.
//
// It carries the epoch as well as the sequence number because a sequence
// number alone is ambiguous across a restart: event 12 from this run of the
// server and event 12 from the last one are different events, and resuming
// from the wrong one would silently skip everything between.
func eventID(e library.Event) string {
	return e.Epoch + ":" + strconv.FormatUint(e.Seq, 10)
}

// resumeFrom reads where a client wants to pick up.
//
// The Last-Event-ID header is what a browser's EventSource sends by itself on
// a reconnect. The query parameter is for everything else: a native client
// reconnecting by hand, or a browser opening the stream for the first time
// with an id it stored from a previous session, which EventSource has no way
// to express.
func resumeFrom(r *http.Request) (epoch string, after uint64) {
	raw := r.Header.Get("Last-Event-ID")
	if raw == "" {
		raw = r.URL.Query().Get("lastEventId")
	}
	if raw == "" {
		return "", 0
	}
	epoch, num, ok := strings.Cut(raw, ":")
	if !ok {
		return "", 0
	}
	seq, err := strconv.ParseUint(num, 10, 64)
	if err != nil {
		return "", 0
	}
	return epoch, seq
}

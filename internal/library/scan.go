package library

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/remy/yamo/internal/scan"
)

// ScanRequest asks for a filesystem scan.
type ScanRequest struct {
	// Roots to walk. Empty means refresh whatever the catalogue already
	// covers, which is the usual case.
	Roots []string `json:"roots,omitempty"`

	// Full re-reads every file instead of reusing entries whose size and
	// modification time are unchanged.
	Full bool `json:"full,omitempty"`

	Exclude        []string `json:"exclude,omitempty"`
	IncludeHidden  bool     `json:"includeHidden,omitempty"`
	FollowSymlinks bool     `json:"followSymlinks,omitempty"`
	Workers        int      `json:"workers,omitempty"`
}

// ScanRunningError means a scan is already under way.
//
// A second concurrent scan is never what anyone wants: both walk the same
// tree, both build a whole catalogue, and whichever finishes last silently
// wins. Refusing is better than returning the running job, because a request
// asking for a full re-read would otherwise be quietly answered with an
// incremental one already in progress.
type ScanRunningError struct{ JobID string }

func (e *ScanRunningError) Error() string {
	return "library: a scan is already running (job " + e.JobID + ")"
}

// ScanStatus describes the state of scanning, for a client that wants to know
// whether the library is being rebuilt underneath it.
type ScanStatus struct {
	Running   bool       `json:"running"`
	Job       *Job       `json:"job,omitempty"`  // the scan in progress
	Last      *Job       `json:"last,omitempty"` // the most recent finished scan
	Roots     []string   `json:"roots"`
	Tracks    int        `json:"tracks"`
	ScannedAt *time.Time `json:"scannedAt,omitempty"`
}

// ScanStatus reports whether a scan is running and what the last one did.
func (s *Service) ScanStatus() ScanStatus {
	st := ScanStatus{}
	if j, ok := s.jobs.RunningOfKind(JobScan); ok {
		st.Running, st.Job = true, j
	}
	if j, ok := s.jobs.LastOfKind(JobScan); ok {
		st.Last = j
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	st.Roots = append([]string{}, s.cat.Roots...)
	st.Tracks = len(s.cat.Tracks)
	if !s.cat.ScannedAt.IsZero() {
		at := s.cat.ScannedAt
		st.ScannedAt = &at
	}
	return st
}

// ScanResult reports what a scan found.
type ScanResult struct {
	Tracks    int      `json:"tracks"`
	Roots     []string `json:"roots"`
	Dirs      int64    `json:"directories"`
	Found     int64    `json:"found"`
	Parsed    int64    `json:"read"`
	Reused    int64    `json:"unchanged"`
	Errors    int64    `json:"errors"`
	Removed   int      `json:"removed"`
	ElapsedMS int64    `json:"elapsedMs"`
}

// Scan starts a scan job.
//
// Scanning replaces the catalogue wholesale, so it is the one operation that
// has to swap state under every reader at once. Edits are written through to
// the files as they are made, which means there is never pending work for a
// scan to lose — the reason write-through is worth the round trip.
func (s *Service) Scan(req ScanRequest) (*Job, error) {
	// Checking and starting must be one step, or two requests arriving
	// together would both see no scan running and both start one.
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	if j, ok := s.jobs.RunningOfKind(JobScan); ok {
		return nil, &ScanRunningError{JobID: j.ID}
	}

	roots := req.Roots
	if len(roots) == 0 {
		s.mu.RLock()
		roots = append([]string(nil), s.cat.Roots...)
		s.mu.RUnlock()
	}
	if len(roots) == 0 {
		return nil, errors.New("library: no directories to scan and none recorded in the catalogue")
	}

	return s.jobs.Start(JobScan, func(ctx context.Context, j *Job) (any, error) {
		opts := scan.Options{
			Roots:          roots,
			Workers:        req.Workers,
			Exclude:        req.Exclude,
			IncludeHidden:  req.IncludeHidden,
			FollowSymlinks: req.FollowSymlinks,
		}
		if !req.Full {
			s.mu.RLock()
			opts.Previous = s.cat
			s.mu.RUnlock()
		}

		start := time.Now()
		var last scan.Stats
		next, err := scan.Scan(ctx, opts, func(st scan.Stats) {
			last = st
			j.SetProgress(Progress{
				Done:    st.Parsed + st.Reused,
				Total:   st.Found,
				Message: st.Current,
			})
		})
		if next == nil {
			return nil, err
		}

		s.mu.RLock()
		prevCount := len(s.cat.Tracks)
		s.mu.RUnlock()

		s.replaceCatalog(next)

		res := ScanResult{
			Tracks:    next.Len(),
			Roots:     roots,
			Dirs:      last.Dirs,
			Found:     last.Found,
			Parsed:    last.Parsed,
			Reused:    last.Reused,
			Errors:    last.Errors,
			ElapsedMS: time.Since(start).Milliseconds(),
		}
		if removed := prevCount - next.Len(); removed > 0 {
			res.Removed = removed
		}
		return res, err
	}), nil
}

// RescanSchedule reports the periodic rescan setting: how often the roots are
// rescanned and when the next one is due. A zero interval means the timer is
// off, which is the default — nothing watches the filesystem, so a library
// changed by something other than this server is only noticed when a scan is
// asked for.
func (s *Service) RescanSchedule() (every time.Duration, next time.Time) {
	s.rescanMu.Lock()
	defer s.rescanMu.Unlock()
	return s.opts.RescanInterval, s.nextRescan
}

// rescanLoop rescans the catalogue's roots on a timer.
//
// It asks for exactly what "yamo scan" with no arguments asks for: an
// incremental scan of the roots already in the catalogue. That is cheap on an
// unchanged library — a stat per file rather than a re-read — which is what
// makes running it on a timer reasonable at all.
func (s *Service) rescanLoop() {
	defer close(s.rescanDone)
	t := time.NewTicker(s.opts.RescanInterval)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
			s.rescanMu.Lock()
			s.nextRescan = time.Now().Add(s.opts.RescanInterval)
			s.rescanMu.Unlock()

			// Nothing to scan yet is the ordinary state of a server started
			// with an empty catalogue and no roots, so it is not worth a line
			// on every tick.
			s.mu.RLock()
			roots := len(s.cat.Roots)
			s.mu.RUnlock()
			if roots == 0 {
				continue
			}

			if _, err := s.Scan(ScanRequest{}); err != nil {
				var running *ScanRunningError
				if errors.As(err, &running) {
					continue // the previous one is still going; skip this turn
				}
				fmt.Fprintf(os.Stderr, "yamo: scheduled rescan failed: %v\n", err)
			}
		}
	}
}

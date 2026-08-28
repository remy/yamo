package library

import (
	"context"
	"errors"
	"time"

	"github.com/remy/tag-manager/internal/scan"
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

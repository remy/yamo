package library

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"sync"
	"time"
)

// JobState is where a job has got to.
type JobState string

const (
	JobRunning   JobState = "running"
	JobSucceeded JobState = "succeeded"
	JobFailed    JobState = "failed"
	JobCancelled JobState = "cancelled"
)

// Job kinds.
const (
	JobScan    = "scan"
	JobEdit    = "edit"
	JobArtwork = "artwork"
	JobStrip   = "strip"
	JobRestore = "restore"
)

// Progress is how far a job has got.
type Progress struct {
	Done    int64  `json:"done"`
	Total   int64  `json:"total"`
	Message string `json:"message,omitempty"`
}

// Job is a long-running operation.
//
// Every operation that can touch more than one file returns one of these, even
// when it finishes immediately, so that a client has a single shape to handle
// rather than guessing which calls might block.
type Job struct {
	ID         string     `json:"id"`
	Kind       string     `json:"kind"`
	State      JobState   `json:"state"`
	Progress   Progress   `json:"progress"`
	Result     any        `json:"result,omitempty"`
	Error      string     `json:"error,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`

	mu     sync.Mutex
	cancel context.CancelFunc
	svc    *Service
}

// snapshot returns a copy safe to hand out while the job is still running.
func (j *Job) snapshot() *Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	return &Job{
		ID: j.ID, Kind: j.Kind, State: j.State, Progress: j.Progress,
		Result: j.Result, Error: j.Error,
		CreatedAt: j.CreatedAt, FinishedAt: j.FinishedAt,
	}
}

// SetProgress updates a running job and notifies subscribers.
func (j *Job) SetProgress(p Progress) {
	j.mu.Lock()
	j.Progress = p
	j.mu.Unlock()
	if j.svc != nil {
		j.svc.events.publish(Event{Type: EventJobProgress, Job: j.snapshot()})
	}
}

func (j *Job) finish(result any, err error, cancelled bool) {
	now := time.Now()
	j.mu.Lock()
	switch {
	case cancelled:
		j.State = JobCancelled
	case err != nil:
		j.State = JobFailed
		j.Error = err.Error()
	default:
		j.State = JobSucceeded
		j.Result = result
	}
	j.FinishedAt = &now
	j.mu.Unlock()
	if j.svc != nil {
		j.svc.events.publish(Event{Type: EventJobFinished, Job: j.snapshot()})
	}
}

// Jobs is the registry of running and finished jobs.
type Jobs struct {
	mu   sync.Mutex
	jobs map[string]*Job
	svc  *Service
}

func newJobs(s *Service) *Jobs {
	return &Jobs{jobs: map[string]*Job{}, svc: s}
}

// jobRetention is how long a finished job stays queryable. Long enough for a
// client to poll for its result after a slow round trip, short enough that a
// long-lived server does not accumulate them.
const jobRetention = time.Hour

// Start registers a job and runs fn in the background.
func (r *Jobs) Start(kind string, fn func(ctx context.Context, j *Job) (any, error)) *Job {
	ctx, cancel := context.WithCancel(context.Background())
	j := &Job{
		ID: newJobID(), Kind: kind, State: JobRunning,
		CreatedAt: time.Now(), cancel: cancel, svc: r.svc,
	}

	r.mu.Lock()
	r.jobs[j.ID] = j
	r.sweepLocked()
	r.mu.Unlock()

	go func() {
		defer cancel()
		result, err := fn(ctx, j)
		j.finish(result, err, ctx.Err() != nil && err != nil)
	}()
	return j
}

// Get returns a snapshot of one job.
func (r *Jobs) Get(id string) (*Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	if !ok {
		return nil, ErrNotFound
	}
	return j.snapshot(), nil
}

// List returns every known job, newest first.
func (r *Jobs) List() []*Job {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Job, 0, len(r.jobs))
	for _, j := range r.jobs {
		out = append(out, j.snapshot())
	}
	sort.Slice(out, func(i, k int) bool { return out[i].CreatedAt.After(out[k].CreatedAt) })
	return out
}

// Cancel asks a running job to stop. Work already done is not undone: a scan
// keeps what it found, and a batch keeps the files it has already written.
func (r *Jobs) Cancel(id string) error {
	r.mu.Lock()
	j, ok := r.jobs[id]
	r.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	j.mu.Lock()
	state, cancel := j.State, j.cancel
	j.mu.Unlock()
	if state != JobRunning {
		return errors.New("library: the job has already finished")
	}
	cancel()
	return nil
}

func (r *Jobs) cancelAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, j := range r.jobs {
		j.mu.Lock()
		if j.State == JobRunning && j.cancel != nil {
			j.cancel()
		}
		j.mu.Unlock()
	}
}

// sweepLocked drops jobs that finished long enough ago to be uninteresting.
func (r *Jobs) sweepLocked() {
	cutoff := time.Now().Add(-jobRetention)
	for id, j := range r.jobs {
		j.mu.Lock()
		done := j.FinishedAt != nil && j.FinishedAt.Before(cutoff)
		j.mu.Unlock()
		if done {
			delete(r.jobs, id)
		}
	}
}

func newJobID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail in practice; a timestamp is a workable
		// fallback and still unique enough within one process.
		return hex.EncodeToString([]byte(time.Now().Format(time.RFC3339Nano)))[:16]
	}
	return hex.EncodeToString(b[:])
}

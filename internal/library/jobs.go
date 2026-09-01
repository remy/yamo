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

// Job kinds. A client polling the job list distinguishes operations by these,
// so every operation that starts a job has one of its own: a split reported as
// an edit would be indistinguishable from a batch set, and its result carries
// different fields.
const (
	JobScan    = "scan"
	JobEdit    = "edit"
	JobSplit   = "split"
	JobRename  = "rename"
	JobArtwork = "artwork"
	JobExport  = "export"
	JobStrip   = "strip"
	JobRestore = "restore"
	JobUndo    = "undo"
)

// JobKinds lists every kind a job may have, for the capabilities endpoint.
var JobKinds = []string{
	JobScan, JobEdit, JobSplit, JobRename, JobArtwork, JobExport,
	JobStrip, JobRestore, JobUndo,
}

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

	// BackupID names the journal this job wrote, when it recorded one. It is
	// what makes the job undoable: POST /jobs/{id}/undo finds the journal
	// through this rather than making the client remember it.
	BackupID string `json:"backupId,omitempty"`

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
		Result: j.Result, Error: j.Error, BackupID: j.BackupID,
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

	// running counts jobs in flight so shutdown can wait for them. A job
	// writes to music files; returning from Close while one is mid-write
	// would be a lie.
	running sync.WaitGroup
}

func newJobs(s *Service) *Jobs {
	return &Jobs{jobs: map[string]*Job{}, svc: s}
}

// jobRetention is how long a finished job stays queryable. Long enough for a
// client to poll for its result after a slow round trip, short enough that a
// long-lived server does not accumulate them.
const jobRetention = time.Hour

// Start registers a job, runs fn in the background, and returns a snapshot of
// the job as it was at that moment.
func (r *Jobs) Start(kind string, fn func(ctx context.Context, j *Job) (any, error)) *Job {
	return r.StartWithJournal(kind, "", fn)
}

// StartWithJournal is Start for an operation that recorded an undo journal.
//
// The journal id is fixed before the job runs rather than filled in when it
// finishes, so a client watching the job can offer an undo the moment the work
// starts — and so a job cancelled halfway is still undoable for the files it
// did get to.
func (r *Jobs) StartWithJournal(kind, backupID string, fn func(ctx context.Context, j *Job) (any, error)) *Job {
	ctx, cancel := context.WithCancel(context.Background())
	j := &Job{
		ID: newJobID(), Kind: kind, State: JobRunning, BackupID: backupID,
		CreatedAt: time.Now(), cancel: cancel, svc: r.svc,
	}

	r.mu.Lock()
	r.jobs[j.ID] = j
	r.sweepLocked()
	r.mu.Unlock()

	r.running.Add(1)
	go func() {
		defer r.running.Done()
		defer cancel()
		result, err := fn(ctx, j)
		j.finish(result, err, ctx.Err() != nil && err != nil)
	}()

	// A snapshot, not the live job: the caller is about to serialise this
	// while the goroutine above is already writing progress into it.
	return j.snapshot()
}

// RunningOfKind returns the running job of a kind, if there is one.
func (r *Jobs) RunningOfKind(kind string) (*Job, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, j := range r.jobs {
		j.mu.Lock()
		match := j.Kind == kind && j.State == JobRunning
		j.mu.Unlock()
		if match {
			return j.snapshot(), true
		}
	}
	return nil, false
}

// LastOfKind returns the most recently finished job of a kind.
func (r *Jobs) LastOfKind(kind string) (*Job, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var best *Job
	for _, j := range r.jobs {
		snap := j.snapshot()
		if snap.Kind != kind || snap.FinishedAt == nil {
			continue
		}
		if best == nil || snap.FinishedAt.After(*best.FinishedAt) {
			best = snap
		}
	}
	return best, best != nil
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

// JobFilter narrows a job listing. Empty fields match everything.
type JobFilter struct {
	Kind   string // one job kind
	State  string // one job state
	Limit  int
	Offset int
}

// JobPage is one page of jobs, in the envelope every other listing uses.
type JobPage struct {
	Items  []*Job `json:"items"`
	Total  int    `json:"total"`
	Limit  int    `json:"limit"`
	Offset int    `json:"offset"`

	// Retention is how long a finished job stays queryable, so a client can
	// tell an id that has aged out from one that never existed.
	RetentionMS int64 `json:"retentionMs"`
}

// List returns every known job, newest first.
func (r *Jobs) List() []*Job {
	return r.Page(JobFilter{Limit: -1}).Items
}

// Page returns a filtered, paged listing, newest first.
//
// A long-running server accumulates jobs from every client on it, and a phone
// asking "what is running" wants the two that are, not the four hundred that
// finished. Filtering happens before paging, so total counts the matches.
func (r *Jobs) Page(f JobFilter) JobPage {
	limit := f.Limit
	switch {
	case limit < 0:
		limit = 0 // caller wants everything; applied after the count
	case limit == 0:
		limit = DefaultLimit
	case limit > MaxLimit:
		limit = MaxLimit
	}
	if f.Offset < 0 {
		f.Offset = 0
	}

	r.mu.Lock()
	all := make([]*Job, 0, len(r.jobs))
	for _, j := range r.jobs {
		snap := j.snapshot()
		if f.Kind != "" && snap.Kind != f.Kind {
			continue
		}
		if f.State != "" && string(snap.State) != f.State {
			continue
		}
		all = append(all, snap)
	}
	r.mu.Unlock()

	sort.Slice(all, func(i, k int) bool { return all[i].CreatedAt.After(all[k].CreatedAt) })

	out := JobPage{
		Total: len(all), Limit: limit, Offset: f.Offset, Items: []*Job{},
		RetentionMS: jobRetention.Milliseconds(),
	}
	if f.Limit < 0 {
		out.Items, out.Limit = all, len(all)
		return out
	}
	if f.Offset >= len(all) {
		return out
	}
	out.Items = all[f.Offset:min(f.Offset+limit, len(all))]
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

// cancelAll asks every running job to stop and waits for them.
//
// Jobs check for cancellation between files, so this returns once the file
// currently being written is finished rather than in the middle of one.
func (r *Jobs) cancelAll() {
	r.mu.Lock()
	for _, j := range r.jobs {
		j.mu.Lock()
		if j.State == JobRunning && j.cancel != nil {
			j.cancel()
		}
		j.mu.Unlock()
	}
	r.mu.Unlock()
	r.running.Wait()
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

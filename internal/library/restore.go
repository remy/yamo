package library

import (
	"context"
	"errors"
	"fmt"

	"github.com/remy/yamo/internal/tags"
)

// Putting a journal back.
//
// One restore covers every kind of journal, because the client's question is
// always the same — "undo what that did" — and the difference between re-adding
// a stripped frame, writing back a field, re-embedding a cover and moving a
// file home is the service's business rather than the caller's.

// RestoreRequest puts a journal's contents back.
type RestoreRequest struct {
	BackupID string `json:"backupId"`
	DryRun   bool   `json:"dryRun,omitempty"`
}

// Restore reads a journal and puts back what the operation that wrote it
// changed.
func (s *Service) Restore(req RestoreRequest) (*Job, error) {
	return s.restore(req.BackupID, req.DryRun, JobRestore)
}

// Undo reverses a job by restoring the journal it wrote.
//
// It is the same work as a restore and exists beside it because it is a
// different question: a client watching a batch edit it just started has the
// job id in hand and should not have to go looking through the journal list
// for the one that job happens to have written.
func (s *Service) Undo(jobID string) (*Job, error) {
	j, err := s.jobs.Get(jobID)
	if err != nil {
		return nil, err
	}
	if j.State == JobRunning {
		return nil, fmt.Errorf("%w: the job is still running", ErrConflict)
	}
	if j.BackupID == "" {
		return nil, fmt.Errorf("%w: job %s recorded no journal, so there is nothing to undo it from", ErrNotFound, jobID)
	}
	return s.restore(j.BackupID, false, JobUndo)
}

func (s *Service) restore(backupID string, dryRun bool, kind string) (*Job, error) {
	records, err := s.readJournal(backupID)
	if err != nil {
		return nil, err
	}
	journalKind := s.backupKind(backupID)

	return s.jobs.Start(kind, func(ctx context.Context, j *Job) (any, error) {
		res := BatchResult{Matched: len(records), DryRun: dryRun}
		j.SetProgress(Progress{Total: int64(len(records))})

		var touched []string
		var reread []string
		for n, r := range records {
			if ctx.Err() != nil {
				break
			}
			outcome, ids, err := s.restoreOne(r, journalKind, dryRun)
			switch {
			case err != nil:
				res.fail(TrackID(r.Path), r.Path, err)
			case outcome:
				res.Changed++
				touched = append(touched, ids...)
				if journalKind == JournalStrip {
					// A strip restore re-adds whatever was removed, which the
					// request cannot describe, so the record has to be re-read
					// rather than patched.
					reread = append(reread, ids...)
				}
			default:
				res.Skipped++
			}
			if n%32 == 0 || n == len(records)-1 {
				j.SetProgress(Progress{Done: int64(n + 1), Total: int64(len(records))})
			}
		}

		if len(reread) > 0 {
			s.refreshTracks(reread)
		}
		if len(touched) > 0 {
			s.markDirty()
			s.events.publish(Event{Type: EventTracksChanged, TrackIDs: touched})
		}
		return res, ctx.Err()
	}), nil
}

// restoreOne puts one record back, reporting whether anything changed and
// which track ids were affected. A rename touches two — where the file was and
// where it is now — because a client caching either has to drop it.
func (s *Service) restoreOne(r journalRecord, kind string, dryRun bool) (bool, []string, error) {
	id := TrackID(r.Path)

	switch {
	case kind == JournalRename || r.From != "":
		if r.From == "" {
			return false, nil, errors.New("library: the record does not say where the file came from")
		}
		if dryRun {
			return true, nil, nil
		}
		// Renaming back means naming the old file, which is a bare absolute
		// path rather than anything relative to where the track sits now.
		t, err := s.Rename(id, r.From, "")
		if err != nil {
			return false, nil, err
		}
		return true, []string{id, t.ID}, nil

	case r.Art != nil:
		var pic *tags.Picture
		if len(r.Art.Data) > 0 {
			p, err := tags.NewPicture(r.Art.Data)
			if err != nil {
				return false, nil, err
			}
			pic = p
		}
		changed, err := s.setArtwork(id, pic, dryRun, nil)
		return changed, []string{id}, err

	case len(r.Fields) > 0:
		ch := make(Changes, len(r.Fields))
		for f, v := range r.Fields {
			ch[f] = v
		}
		changed, err := s.applyOne(id, ch, "", dryRun, nil)
		return changed, []string{id}, err

	case len(r.Frames) > 0:
		if dryRun {
			return true, nil, nil
		}
		added, err := tags.RestoreFile(r.Path, r.Frames)
		if err != nil {
			return false, nil, err
		}
		if added > 0 {
			s.bumpRev(r.Path)
		}
		return added > 0, []string{id}, nil
	}
	return false, nil, nil
}

// journalArtFor captures a track's current cover so an artwork operation can
// be undone. A track with no cover records an empty payload rather than no
// record at all: putting that back means taking the new cover off again.
func journalArtFor(path string, hasArt bool) *journalArt {
	if !hasArt {
		return &journalArt{}
	}
	pic, err := tags.ReadCover(path)
	if err != nil || pic == nil {
		return &journalArt{}
	}
	return &journalArt{MIME: pic.MIME, Data: pic.Data}
}

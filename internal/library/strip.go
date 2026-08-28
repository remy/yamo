package library

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/remy/tag-manager/internal/catalog"
	"github.com/remy/tag-manager/internal/tags"
)

// StripRequest removes every tag not on a keep list.
type StripRequest struct {
	Selector Selector `json:"selector"`

	// Keep replaces the default keep list. Names may be canonical
	// ("albumartist") or native to any format ("TPE2", "aART").
	Keep []string `json:"keep,omitempty"`

	// Also adds to the default keep list.
	Also []string `json:"also,omitempty"`

	// DryRun defaults to true: an operation that permanently discards data
	// across a library should have to be asked for twice.
	DryRun bool `json:"dryRun"`

	// Backup records what was removed so it can be restored.
	Backup bool `json:"backup,omitempty"`
}

// StripResult reports what a strip did or would do.
type StripResult struct {
	BatchResult
	BackupID string         `json:"backupId,omitempty"`
	Removed  []StripGroup   `json:"removed"`
	Skipped  map[string]int `json:"skippedFormats,omitempty"`
	Upgraded int            `json:"upgradedFromV22,omitempty"`
	Bytes    int64          `json:"bytesRemoved"`
	Keep     []string       `json:"keep"`
}

// StripGroup counts one kind of removed metadata.
type StripGroup struct {
	Format  string   `json:"format"`
	Key     string   `json:"key"`
	Meaning string   `json:"meaning,omitempty"`
	Tracks  int      `json:"tracks"`
	Bytes   int64    `json:"bytes"`
	Samples []string `json:"samples,omitempty"`
}

// keepSetFrom builds the keep list for a request.
func keepSetFrom(keep, also []string) (tags.KeepSet, []string, error) {
	set := tags.NewKeepSet(tags.DefaultKeepTags)
	if len(keep) > 0 {
		parsed, unknown := tags.ParseKeepSet(keep)
		if len(unknown) > 0 {
			return nil, nil, fmt.Errorf("library: unknown tag %q", unknown[0])
		}
		set = parsed
	}
	if len(also) > 0 {
		extra, unknown := tags.ParseKeepSet(also)
		if len(unknown) > 0 {
			return nil, nil, fmt.Errorf("library: unknown tag %q", unknown[0])
		}
		for t := range extra {
			set[t] = true
		}
	}
	if len(set) == 0 {
		return nil, nil, errors.New("library: the keep list is empty; that would remove every tag")
	}
	names := make([]string, 0, len(set))
	for _, t := range set.Sorted() {
		names = append(names, t.Name())
	}
	return set, names, nil
}

// Strip starts a job that removes every tag not on the keep list.
func (s *Service) Strip(req StripRequest) (*Job, error) {
	keep, keepNames, err := keepSetFrom(req.Keep, req.Also)
	if err != nil {
		return nil, err
	}
	ids, err := s.Resolve(req.Selector)
	if err != nil {
		return nil, err
	}

	var backup *backupWriter
	if req.Backup && !req.DryRun {
		if backup, err = s.newBackup(); err != nil {
			return nil, err
		}
	}

	return s.jobs.Start(JobStrip, func(ctx context.Context, j *Job) (any, error) {
		res := StripResult{
			BatchResult: BatchResult{Matched: len(ids), DryRun: req.DryRun},
			Keep:        keepNames,
			Skipped:     map[string]int{},
			Removed:     []StripGroup{},
		}
		if backup != nil {
			res.BackupID = backup.id
			defer backup.Close()
		}
		j.SetProgress(Progress{Total: int64(len(ids))})

		type key struct{ format, name, meaning string }
		groups := map[key]*StripGroup{}
		var touched []string

		for n, id := range ids {
			if ctx.Err() != nil {
				break
			}
			path, err := s.Path(id)
			if err != nil {
				res.BatchResult.Skipped++
				continue
			}
			rep, err := tags.StripFile(path, keep, !req.DryRun)
			switch {
			case err != nil:
				res.fail(id, path, err)
			case rep.Unsupported:
				res.Skipped[formatName(path)]++
			case !rep.Changed:
				res.BatchResult.Skipped++
			default:
				res.Changed++
				touched = append(touched, id)
				if rep.Upgraded {
					res.Upgraded++
				}
				for _, r := range rep.Removed {
					k := key{r.Format.String(), r.Display(), r.Meaning}
					g := groups[k]
					if g == nil {
						g = &StripGroup{Format: k.format, Key: k.name, Meaning: k.meaning}
						groups[k] = g
					}
					g.Tracks++
					g.Bytes += int64(r.Bytes)
					res.Bytes += int64(r.Bytes)
					if len(g.Samples) < 3 && r.Sample != "" {
						g.Samples = append(g.Samples, r.Sample)
					}
				}
				if backup != nil {
					backup.write(path, rep.Removed)
				}
			}
			if n%32 == 0 || n == len(ids)-1 {
				j.SetProgress(Progress{Done: int64(n + 1), Total: int64(len(ids))})
			}
		}

		for _, g := range groups {
			res.Removed = append(res.Removed, *g)
		}
		sort.Slice(res.Removed, func(i, k int) bool { return res.Removed[i].Tracks > res.Removed[k].Tracks })

		if len(touched) > 0 {
			s.refreshTracks(touched)
			s.events.publish(Event{Type: EventTracksChanged, TrackIDs: touched})
		}
		return res, ctx.Err()
	}), nil
}

// RestoreRequest puts stripped tags back.
type RestoreRequest struct {
	BackupID string `json:"backupId"`
	DryRun   bool   `json:"dryRun,omitempty"`
}

// Restore reads a backup and re-adds the tags it holds.
func (s *Service) Restore(req RestoreRequest) (*Job, error) {
	records, err := s.readBackup(req.BackupID)
	if err != nil {
		return nil, err
	}
	return s.jobs.Start(JobRestore, func(ctx context.Context, j *Job) (any, error) {
		res := BatchResult{Matched: len(records), DryRun: req.DryRun}
		j.SetProgress(Progress{Total: int64(len(records))})

		var touched []string
		for n, r := range records {
			if ctx.Err() != nil {
				break
			}
			if req.DryRun {
				res.Changed++
			} else {
				added, err := tags.RestoreFile(r.Path, r.Frames)
				switch {
				case err != nil:
					res.fail(TrackID(r.Path), r.Path, err)
				case added > 0:
					res.Changed++
					touched = append(touched, TrackID(r.Path))
				default:
					res.Skipped++
				}
			}
			if n%32 == 0 || n == len(records)-1 {
				j.SetProgress(Progress{Done: int64(n + 1), Total: int64(len(records))})
			}
		}
		if len(touched) > 0 {
			s.refreshTracks(touched)
			s.events.publish(Event{Type: EventTracksChanged, TrackIDs: touched})
		}
		return res, ctx.Err()
	}), nil
}

// refreshTracks re-reads files that were rewritten outside the normal edit
// path, where the change cannot be predicted from the request.
//
// A field edit knows exactly what it set, so it updates the record directly. A
// strip does not: it removes whatever was not on the keep list, which may
// include fields the catalogue holds. Without re-reading, a search would go on
// matching a comment that is no longer in the file.
func (s *Service) refreshTracks(ids []string) {
	type update struct {
		id      string
		track   catalog.Track
		present bool
	}
	updates := make([]update, 0, len(ids))
	r := tags.NewReader()

	for _, id := range ids {
		s.mu.RLock()
		i, ok := s.lookupLocked(id)
		var cur catalog.Track
		if ok {
			cur = s.cat.Tracks[i]
		}
		s.mu.RUnlock()
		if !ok {
			continue
		}

		fi, err := os.Stat(cur.Path)
		if err != nil {
			continue
		}
		md, err := r.ReadFile(cur.Path)
		if err != nil && md.Format == tags.FormatUnknown {
			// Unreadable now, but the file is still there; keep the record and
			// only correct what a stat can tell us.
			cur.Size, cur.ModTime = fi.Size(), fi.ModTime().Unix()
			updates = append(updates, update{id, cur, true})
			continue
		}
		next := catalog.Track{Path: cur.Path, Size: fi.Size(), ModTime: fi.ModTime().Unix()}
		next.FromMetadata(&md)
		if next.Format == tags.FormatUnknown {
			next.Format = cur.Format
		}
		updates = append(updates, update{id, next, true})
	}

	s.mu.Lock()
	for _, u := range updates {
		if i, ok := s.lookupLocked(u.id); ok {
			s.cat.Tracks[i] = u.track
			s.cat.Touch(int(i))
		}
	}
	s.mu.Unlock()
	s.markDirty()
}

func formatName(path string) string { return tags.FormatForPath(path).String() }

// --- backups ------------------------------------------------------------

// Backups live server-side and are addressed by id, so that the client that
// restores need not be the one that stripped.

type backupRecord struct {
	Path   string            `json:"path"`
	Frames []tags.RemovedTag `json:"frames"`
}

// Backup describes a stored backup.
type Backup struct {
	ID      string    `json:"id"`
	Created time.Time `json:"created"`
	Tracks  int       `json:"tracks"`
	Bytes   int64     `json:"bytes"`
}

func defaultBackupDir(catalogPath string) string {
	if catalogPath == "" {
		return "backups"
	}
	return filepath.Join(filepath.Dir(catalogPath), "backups")
}

type backupWriter struct {
	id   string
	f    *os.File
	w    *bufio.Writer
	rows int
}

func (s *Service) newBackup() (*backupWriter, error) {
	if s.opts.BackupDir == "" {
		return nil, errors.New("library: no backup directory configured")
	}
	if err := os.MkdirAll(s.opts.BackupDir, 0o755); err != nil {
		return nil, err
	}
	id := newJobID()
	f, err := os.Create(filepath.Join(s.opts.BackupDir, id+".jsonl"))
	if err != nil {
		return nil, err
	}
	return &backupWriter{id: id, f: f, w: bufio.NewWriterSize(f, 1<<20)}, nil
}

func (b *backupWriter) write(path string, frames []tags.RemovedTag) {
	_ = json.NewEncoder(b.w).Encode(backupRecord{Path: path, Frames: frames})
	b.rows++
}

func (b *backupWriter) Close() error {
	if err := b.w.Flush(); err != nil {
		b.f.Close()
		return err
	}
	return b.f.Close()
}

// Backups lists the stored backups, newest first.
func (s *Service) Backups() ([]Backup, error) {
	entries, err := os.ReadDir(s.opts.BackupDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []Backup{}, nil
		}
		return nil, err
	}
	out := []Backup{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Backup{
			ID:      strings.TrimSuffix(e.Name(), ".jsonl"),
			Created: info.ModTime(),
			Bytes:   info.Size(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out, nil
}

// readBackup loads a backup by id.
func (s *Service) readBackup(id string) ([]backupRecord, error) {
	if id == "" || strings.ContainsAny(id, `/\.`) {
		return nil, fmt.Errorf("%w: backup %q", ErrNotFound, id)
	}
	f, err := os.Open(filepath.Join(s.opts.BackupDir, id+".jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	defer f.Close()

	var out []backupRecord
	sc := bufio.NewScanner(f)
	// Records hold whole frame payloads, which for cover art run to megabytes.
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for line := 1; sc.Scan(); line++ {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var r backupRecord
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			return nil, fmt.Errorf("backup %s line %d: %w", id, line, err)
		}
		out = append(out, r)
	}
	return out, sc.Err()
}

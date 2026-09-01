package library

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/remy/yamo/internal/tags"
)

// Undo journals.
//
// A strip has always been recoverable, because tags removed from a file cannot
// be got back from anywhere else. Everything else that writes across a
// selection had the same problem and no answer: a batch edit that set the
// artist on two thousand files wrote them and forgot what they said, so a
// mistake made from a phone was unrecoverable from anywhere.
//
// A journal is the answer, and it is the same mechanism in every case: before
// a file is written, record what it held. The record differs by operation —
// removed frames for a strip, previous values for an edit, the old cover for
// artwork, the old path for a rename — but the file, the addressing and the
// restore are one thing, so a client has one shape to handle and one endpoint
// to call.
//
// Journals live server-side and are addressed by id, so the client that undoes
// need not be the one that made the change.

// Journal kinds. The kind says how a record is put back.
const (
	JournalStrip   = "strip"
	JournalEdit    = "edit"
	JournalArtwork = "artwork"
	JournalRename  = "rename"
)

// journalRecord is one file's previous state. Exactly one of the payloads is
// set, matching the journal's kind.
type journalRecord struct {
	Path string `json:"path"`

	// Frames are the tags a strip removed, in enough detail to re-add them.
	Frames []tags.RemovedTag `json:"frames,omitempty"`

	// Fields are the values an edit overwrote, keyed by canonical field name.
	// A null value means the field was empty before, so putting it back means
	// clearing it — which is why the map holds pointers rather than strings.
	Fields map[string]*string `json:"fields,omitempty"`

	// Art is the cover an artwork operation replaced or removed. A record
	// with Art set and Data empty means the track had no cover, so undoing
	// means taking the new one off again.
	Art *journalArt `json:"art,omitempty"`

	// From is where a rename moved the file from. Path is where it is now,
	// so undoing is the same move in reverse.
	From string `json:"from,omitempty"`
}

// journalArt is a cover as it was before an artwork operation. The bytes go
// out as base64, which is what encoding/json does with a byte slice, and cost
// what the image costs — which is why artwork journals are opt-in.
type journalArt struct {
	MIME string `json:"mime,omitempty"`
	Data []byte `json:"data,omitempty"`
}

// Backup describes a stored journal.
type Backup struct {
	ID      string    `json:"id"`
	Kind    string    `json:"kind"`
	JobID   string    `json:"jobId,omitempty"`
	Created time.Time `json:"created"`
	Tracks  int       `json:"tracks"`
	Bytes   int64     `json:"bytes"`
}

// BackupPage is one page of journals, in the envelope every other listing uses.
type BackupPage struct {
	Items  []Backup `json:"items"`
	Total  int      `json:"total"`
	Limit  int      `json:"limit"`
	Offset int      `json:"offset"`
}

// BackupDetail is what one journal holds, for a client deciding whether to
// restore it. The point is to answer "what would this put back" without
// applying it: a journal is addressed by an opaque id and created by an
// operation the client may not have made.
type BackupDetail struct {
	Backup
	Fields  []string        `json:"fields,omitempty"` // canonical fields an edit journal would restore
	Formats map[string]int  `json:"formats,omitempty"`
	Samples []BackupSample  `json:"samples,omitempty"`
	Groups  []BackupSummary `json:"groups,omitempty"` // per key, for a strip
}

// BackupSample is one file the journal covers, as an example of the rest.
type BackupSample struct {
	Path string `json:"path"`
	ID   string `json:"id"`
	Note string `json:"note,omitempty"`
}

// BackupSummary counts one kind of entry across the journal.
type BackupSummary struct {
	Key    string `json:"key"`
	Tracks int    `json:"tracks"`
}

// maxBackupSamples bounds the examples a detail carries; enough to recognise
// what a journal covers, small enough to read.
const maxBackupSamples = 5

// journal writes records for one operation.
type journal struct {
	id   string
	kind string
	dir  string
	f    *os.File
	w    *bufio.Writer
	rows int
}

func defaultBackupDir(catalogPath string) string {
	if catalogPath == "" {
		return "backups"
	}
	return filepath.Join(filepath.Dir(catalogPath), "backups")
}

// openJournal starts a journal, failing when there is nowhere to put one.
//
// Used where the client asked for a backup outright: silently not recording
// one before an operation that permanently discards data would be the worst
// of both answers.
func (s *Service) openJournal(kind string) (*journal, error) {
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
	return &journal{id: id, kind: kind, dir: s.opts.BackupDir, f: f, w: bufio.NewWriterSize(f, 1<<20)}, nil
}

// tryJournal starts a journal and gives up quietly if it cannot.
//
// Used where recording is the default rather than a request: a service running
// without a catalogue path has nowhere to write one, and refusing every batch
// edit on that account would be worse than doing the edit unrecorded. The job
// carries no backup id in that case, which is how a client finds out.
func (s *Service) tryJournal(kind string) *journal {
	j, err := s.openJournal(kind)
	if err != nil {
		return nil
	}
	return j
}

// id returns the journal's id, or empty for no journal. It takes a nil
// receiver so callers need not branch.
func (j *journal) ID() string {
	if j == nil {
		return ""
	}
	return j.id
}

// write records one file's previous state. A nil journal writes nothing, so
// the operations can call it unconditionally.
func (j *journal) write(rec journalRecord) {
	if j == nil {
		return
	}
	_ = json.NewEncoder(j.w).Encode(rec)
	j.rows++
}

// Close flushes the records and writes the sidecar that describes them.
//
// The sidecar is separate from the records so that listing the journals costs
// one small read each rather than a walk of every record, and so the record
// file stays uniformly one JSON object per line.
func (j *journal) Close(jobID string) error {
	if j == nil {
		return nil
	}
	err := j.w.Flush()
	if cerr := j.f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	// A journal that recorded nothing describes an operation that changed
	// nothing. Leaving it would fill the list with empty entries offering an
	// undo that would do nothing.
	if j.rows == 0 {
		_ = os.Remove(filepath.Join(j.dir, j.id+".jsonl"))
		return nil
	}
	meta := Backup{ID: j.id, Kind: j.kind, JobID: jobID, Created: time.Now(), Tracks: j.rows}
	b, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(j.dir, j.id+".json"), b, 0o644)
}

// Backups lists the stored journals, newest first.
func (s *Service) Backups(limit, offset int) (BackupPage, error) {
	all, err := s.allBackups()
	if err != nil {
		return BackupPage{}, err
	}
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	if offset < 0 {
		offset = 0
	}
	out := BackupPage{Total: len(all), Limit: limit, Offset: offset, Items: []Backup{}}
	if offset >= len(all) {
		return out, nil
	}
	out.Items = all[offset:min(offset+limit, len(all))]
	return out, nil
}

func (s *Service) allBackups() ([]Backup, error) {
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
		id := strings.TrimSuffix(e.Name(), ".jsonl")
		b := Backup{ID: id, Kind: JournalStrip, Created: info.ModTime(), Bytes: info.Size()}
		// The sidecar carries the kind, the job and the row count. A journal
		// written before sidecars existed has none, and every one of those was
		// a strip — which is why that is the fallback rather than "unknown".
		if raw, err := os.ReadFile(filepath.Join(s.opts.BackupDir, id+".json")); err == nil {
			var meta Backup
			if json.Unmarshal(raw, &meta) == nil {
				b.Kind, b.JobID, b.Tracks = meta.Kind, meta.JobID, meta.Tracks
				if !meta.Created.IsZero() {
					b.Created = meta.Created
				}
			}
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out, nil
}

// Backup returns one journal and a description of what it holds.
func (s *Service) Backup(id string) (BackupDetail, error) {
	all, err := s.allBackups()
	if err != nil {
		return BackupDetail{}, err
	}
	var found *Backup
	for i := range all {
		if all[i].ID == id {
			found = &all[i]
			break
		}
	}
	if found == nil {
		return BackupDetail{}, fmt.Errorf("%w: backup %q", ErrNotFound, id)
	}

	records, err := s.readJournal(id)
	if err != nil {
		return BackupDetail{}, err
	}

	detail := BackupDetail{Backup: *found}
	detail.Tracks = len(records)
	fields := map[string]bool{}
	keys := map[string]int{}
	formats := map[string]int{}

	for _, r := range records {
		if len(detail.Samples) < maxBackupSamples {
			detail.Samples = append(detail.Samples, BackupSample{
				Path: r.Path, ID: TrackID(r.Path), Note: r.note(),
			})
		}
		for f := range r.Fields {
			fields[f] = true
		}
		for _, fr := range r.Frames {
			keys[fr.Display()]++
			formats[fr.Format.String()]++
		}
	}

	for f := range fields {
		detail.Fields = append(detail.Fields, f)
	}
	sort.Strings(detail.Fields)
	if len(formats) > 0 {
		detail.Formats = formats
	}
	for k, n := range keys {
		detail.Groups = append(detail.Groups, BackupSummary{Key: k, Tracks: n})
	}
	sort.Slice(detail.Groups, func(i, j int) bool {
		if detail.Groups[i].Tracks != detail.Groups[j].Tracks {
			return detail.Groups[i].Tracks > detail.Groups[j].Tracks
		}
		return detail.Groups[i].Key < detail.Groups[j].Key
	})
	return detail, nil
}

// note describes one record for a sample line.
func (r journalRecord) note() string {
	switch {
	case r.From != "":
		return "moved from " + r.From
	case r.Art != nil && len(r.Art.Data) == 0:
		return "had no cover"
	case r.Art != nil:
		return "cover " + r.Art.MIME
	case len(r.Fields) > 0:
		names := make([]string, 0, len(r.Fields))
		for f := range r.Fields {
			names = append(names, f)
		}
		sort.Strings(names)
		return strings.Join(names, ", ")
	case len(r.Frames) > 0:
		return fmt.Sprintf("%d tags", len(r.Frames))
	}
	return ""
}

// DeleteBackup removes a journal and its sidecar.
//
// Journals are the only thing here that grows without bound: every strip and
// every batch edit leaves one, and an artwork journal holds the covers it
// replaced. Nothing expires them, because expiring the record of a change
// nobody has noticed yet is the wrong default — so they are deleted when
// somebody says so.
func (s *Service) DeleteBackup(id string) error {
	if err := validBackupID(id); err != nil {
		return err
	}
	path := filepath.Join(s.opts.BackupDir, id+".jsonl")
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: backup %q", ErrNotFound, id)
		}
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	// The sidecar may predate sidecars, so a missing one is not a failure.
	if err := os.Remove(filepath.Join(s.opts.BackupDir, id+".json")); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// validBackupID refuses an id that could name a file outside the backup
// directory. Ids are generated here and are hex, so anything else is either a
// mistake or an attempt to walk the filesystem.
func validBackupID(id string) error {
	if id == "" || strings.ContainsAny(id, `/\.`) {
		return fmt.Errorf("%w: backup %q", ErrNotFound, id)
	}
	return nil
}

// readJournal loads a journal by id.
func (s *Service) readJournal(id string) ([]journalRecord, error) {
	if err := validBackupID(id); err != nil {
		return nil, err
	}
	f, err := os.Open(filepath.Join(s.opts.BackupDir, id+".jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	defer f.Close()

	var out []journalRecord
	sc := bufio.NewScanner(f)
	// Records hold whole frame payloads and, for an artwork journal, whole
	// covers — which run to megabytes.
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for line := 1; sc.Scan(); line++ {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var r journalRecord
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			return nil, fmt.Errorf("backup %s line %d: %w", id, line, err)
		}
		out = append(out, r)
	}
	return out, sc.Err()
}

// backupKind reads a journal's kind without reading its records.
func (s *Service) backupKind(id string) string {
	raw, err := os.ReadFile(filepath.Join(s.opts.BackupDir, id+".json"))
	if err != nil {
		return JournalStrip // written before sidecars; every one of those was a strip
	}
	var meta Backup
	if json.Unmarshal(raw, &meta) != nil || meta.Kind == "" {
		return JournalStrip
	}
	return meta.Kind
}

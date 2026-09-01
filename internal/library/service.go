package library

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/remy/yamo/internal/artclip"
	"github.com/remy/yamo/internal/catalog"
	"github.com/remy/yamo/internal/discogs"
)

// Options configures a Service.
type Options struct {
	// CatalogPath is the snapshot the service loads at startup and writes
	// back to. A missing file is not an error: the service starts empty and
	// the first scan fills it.
	CatalogPath string

	// ClipboardDir holds the artwork clipboard. Defaults to a directory
	// beside the catalogue.
	ClipboardDir string

	// BackupDir holds strip backups, which are addressed by id so that any
	// client can restore one.
	BackupDir string

	// SaveInterval bounds how often the catalogue snapshot is rewritten.
	// Zero picks a sensible default.
	SaveInterval time.Duration

	// DiscogsToken authenticates cover lookups. Empty is the normal case and
	// still works: search needs no token. A token only buys a higher rate
	// limit and images in the search response itself, which together make a
	// search cost one request instead of nine.
	DiscogsToken string

	// NoDiscogs turns the cover lookup off entirely, for a server that should
	// make no outbound requests at all.
	NoDiscogs bool

	// RescanInterval rescans the catalogue's own roots on a timer. Zero, the
	// default, never rescans: nothing watches the filesystem, so a library
	// changed by anything other than this server is only noticed when a scan
	// is asked for. A rescan is incremental, so an unchanged library costs a
	// stat per file rather than a re-read.
	RescanInterval time.Duration
}

// Service owns the catalogue and performs every operation on it.
//
// One reader-writer lock guards the catalogue and its index. Searches are
// milliseconds and mutations are rare, so contention is not the concern;
// correctness is. The catalogue's index builds lazily and mutates on read,
// which is safe in a single-threaded command and a data race in a server.
type Service struct {
	mu   sync.RWMutex
	cat  *catalog.Catalog
	byID map[string]int32

	opts    Options
	locks   pathLocks
	clip    *artclip.Store
	discogs *discogs.Client
	thumbs  *thumbCache

	events *eventBus
	jobs   *Jobs

	// scanMu makes checking for a running scan and starting one atomic.
	scanMu sync.Mutex

	saveMu    sync.Mutex
	saveDirty bool

	// revsMu guards revs, the per-path write counter that makes a version
	// change even when the file's size and modification time do not. See
	// version.go.
	revsMu sync.Mutex
	revs   map[string]uint64

	// rescanMu guards nextRescan, which a client reads through Stats while
	// the rescan loop is writing it.
	rescanMu   sync.Mutex
	nextRescan time.Time

	done       chan struct{}
	saveDone   chan struct{}
	rescanDone chan struct{}
	closeOne   sync.Once
}

const defaultSaveInterval = 5 * time.Second

// ErrNotFound is returned for an unknown track or job id.
var ErrNotFound = errors.New("library: not found")

// ErrConflict means the caller's version no longer matches the file on disk.
var ErrConflict = errors.New("library: the file changed since it was read")

// ErrBadRequest means the request itself was wrong rather than the server.
//
// The API layer maps it to 400. It exists so that a service error can say so
// outright rather than leaving the mapping to be inferred from the wording of
// a message, which is a contract nobody can see and every rephrasing breaks.
var ErrBadRequest = errors.New("library: bad request")

// Open loads the catalogue and starts the service.
func Open(opts Options) (*Service, error) {
	if opts.SaveInterval <= 0 {
		opts.SaveInterval = defaultSaveInterval
	}
	if opts.ClipboardDir == "" {
		opts.ClipboardDir = artclip.DefaultDir(opts.CatalogPath)
	}
	if opts.BackupDir == "" && opts.CatalogPath != "" {
		opts.BackupDir = defaultBackupDir(opts.CatalogPath)
	}

	cat, err := catalog.Load(opts.CatalogPath)
	if err != nil {
		if opts.CatalogPath != "" && !os.IsNotExist(err) && !errors.Is(err, catalog.ErrBadSnapshot) {
			return nil, fmt.Errorf("loading catalogue: %w", err)
		}
		cat = catalog.New() // no catalogue yet; the first scan makes one
		// A snapshot this build cannot read still knows where the music is,
		// and that is worth salvaging: a rescan with no roots scans nothing,
		// so dropping them would turn a version bump into a manual recovery.
		cat.Roots = catalog.LoadRoots(opts.CatalogPath)
	}

	s := &Service{
		cat:        cat,
		opts:       opts,
		clip:       artclip.New(opts.ClipboardDir),
		thumbs:     newThumbCache(),
		events:     newEventBus(),
		done:       make(chan struct{}),
		saveDone:   make(chan struct{}),
		rescanDone: make(chan struct{}),
	}
	// The lookup is on unless it is turned off: it needs no credentials and
	// makes no request until someone searches.
	if !opts.NoDiscogs {
		s.discogs = discogs.New(opts.DiscogsToken)
	}
	s.jobs = newJobs(s)
	s.reindexLocked()

	go s.saveLoop()
	if opts.RescanInterval > 0 {
		s.nextRescan = time.Now().Add(opts.RescanInterval)
		go s.rescanLoop()
	} else {
		close(s.rescanDone) // nothing to wait for in Close
	}
	return s, nil
}

// Close stops background work and flushes the snapshot.
//
// The order matters and the waiting is the point. Jobs and the save loop both
// write files, so returning while either is mid-write leaves the caller
// believing the service has stopped when it has not — which shows up as a
// directory that refuses to delete, and on a real system as a snapshot written
// after shutdown.
func (s *Service) Close() error {
	var err error
	s.closeOne.Do(func() {
		close(s.done)      // stop the background loops
		<-s.rescanDone     // before cancelling jobs, or it could start another
		s.jobs.cancelAll() // cancels running jobs and waits for them
		<-s.saveDone       // and wait for any write the save loop had started
		s.events.close()
		err = s.saveIfDirty() // one final snapshot
	})
	return err
}

// reindexLocked rebuilds the identity map and the search index. The caller
// must hold the write lock.
//
// The index is built here rather than left to build on first search, because
// the catalogue builds it lazily and mutates itself doing so — which under a
// read lock would be a race.
func (s *Service) reindexLocked() {
	s.cat.Index()
	s.byID = make(map[string]int32, len(s.cat.Tracks))
	for i := range s.cat.Tracks {
		id := TrackID(s.cat.Tracks[i].Path)
		if prev, dup := s.byID[id]; dup {
			// Astronomically unlikely, but silently serving the wrong file
			// would be worse than a log line.
			fmt.Fprintf(os.Stderr, "yamo: track id collision %s: %q and %q\n",
				id, s.cat.Tracks[prev].Path, s.cat.Tracks[i].Path)
		}
		s.byID[id] = int32(i)
	}
}

// read runs fn under the read lock.
func (s *Service) read(fn func(c *catalog.Catalog)) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	fn(s.cat)
}

// write runs fn under the write lock.
func (s *Service) write(fn func(c *catalog.Catalog)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s.cat)
}

// lookupLocked resolves a track id to its index. The caller holds a lock.
func (s *Service) lookupLocked(id string) (int32, bool) {
	i, ok := s.byID[id]
	if !ok || int(i) >= len(s.cat.Tracks) {
		return 0, false
	}
	return i, true
}

// Get returns one track.
func (s *Service) Get(id string) (Track, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	i, ok := s.lookupLocked(id)
	if !ok {
		return Track{}, ErrNotFound
	}
	return s.toTrack(&s.cat.Tracks[i]), nil
}

// Path returns the file path for a track id, which several operations need
// without the rest of the record.
// Audio returns the file to play for a track and the media type to serve it
// as. Separate from Path because the caller streaming a file needs both, and
// the format is known here without a second read.
func (s *Service) Audio(id string) (path, mime string, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	i, ok := s.lookupLocked(id)
	if !ok {
		return "", "", ErrNotFound
	}
	t := &s.cat.Tracks[i]
	return t.Path, t.Format.MIME(), nil
}

func (s *Service) Path(id string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	i, ok := s.lookupLocked(id)
	if !ok {
		return "", ErrNotFound
	}
	return s.cat.Tracks[i].Path, nil
}

// Clipboard exposes the artwork clipboard, which is server-side so that a
// cover copied on one client can be pasted from another.
func (s *Service) Clipboard() *artclip.Store { return s.clip }

// Jobs exposes the job registry.
func (s *Service) JobRegistry() *Jobs { return s.jobs }

// Events exposes the change notification bus.
func (s *Service) Events() *eventBus { return s.events }

// replaceCatalog swaps in a freshly scanned catalogue.
//
// The swap is atomic under the write lock: a client mid-request sees either
// the old catalogue or the new one, never a half-rebuilt index.
func (s *Service) replaceCatalog(next *catalog.Catalog) {
	s.mu.Lock()
	s.cat = next
	s.reindexLocked()
	s.mu.Unlock()

	s.markDirty()
	s.events.publish(Event{Type: EventCatalogReplaced})
}

// markDirty records that the snapshot needs rewriting.
//
// Writing it inline would be absurd: the snapshot is a few megabytes and a
// batch edit changes thousands of tracks. The save loop coalesces them.
func (s *Service) markDirty() {
	s.saveMu.Lock()
	s.saveDirty = true
	s.saveMu.Unlock()
}

func (s *Service) saveLoop() {
	defer close(s.saveDone)
	t := time.NewTicker(s.opts.SaveInterval)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
			if err := s.saveIfDirty(); err != nil {
				fmt.Fprintf(os.Stderr, "yamo: could not save the catalogue: %v\n", err)
			}
		}
	}
}

// saveIfDirty writes the snapshot when something has changed since the last one.
func (s *Service) saveIfDirty() error {
	s.saveMu.Lock()
	dirty := s.saveDirty
	s.saveDirty = false
	s.saveMu.Unlock()

	if !dirty || s.opts.CatalogPath == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := catalog.Save(s.opts.CatalogPath, s.cat); err != nil {
		s.markDirty() // try again on the next tick
		return err
	}
	return nil
}

// Flush writes the snapshot now, for callers that need it durable.
func (s *Service) Flush() error { return s.saveIfDirty() }

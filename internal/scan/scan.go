// Package scan builds a catalogue by walking directory trees and extracting
// tags in parallel.
//
// The work splits in two: a pool of goroutines walking directories, and a pool
// reading tags. Both are needed. Directory traversal on network or spinning
// storage is latency-bound, so a single walker starves the readers; tag
// extraction is one or two reads per file, so readers spend most of their time
// waiting on IO and there should be many more of them than there are cores.
package scan

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/remy/yamo/internal/catalog"
	"github.com/remy/yamo/internal/tags"
)

// Options configures a scan.
type Options struct {
	// Roots are the directories to walk. Relative paths are resolved against
	// the working directory.
	Roots []string

	// Workers is the number of tag-reading goroutines. Zero picks a default
	// tuned for IO-bound work rather than CPU count.
	Workers int

	// DirWorkers is the number of concurrent directory reads. Zero picks a
	// default.
	DirWorkers int

	// Exclude holds glob patterns tested against each entry's base name and
	// full path. Matching entries are skipped, directories included.
	Exclude []string

	// IncludeHidden walks dot-directories, which are skipped by default.
	IncludeHidden bool

	// FollowSymlinks resolves symlinked directories. Off by default because a
	// single cyclic link turns a scan into an infinite one.
	FollowSymlinks bool

	// Previous, when set, enables incremental scanning: a file whose size and
	// modification time are unchanged is carried over without being opened.
	Previous *catalog.Catalog
}

// Stats is a snapshot of scan progress. All fields are read atomically, so a
// progress callback can be invoked concurrently with the scan itself.
type Stats struct {
	Dirs      int64
	Found     int64 // audio files discovered
	Parsed    int64 // files opened and read
	Reused    int64 // carried over from the previous catalogue unchanged
	Errors    int64
	Bytes     int64 // total size of catalogued files
	Elapsed   time.Duration
	Current   string // a recently processed path, for display
	Finished  bool
	LastError string
}

// defaultSkipDirs are directories that never contain user music but do contain
// thousands of files. Skipping them by name saves real time on NAS volumes.
var defaultSkipDirs = map[string]struct{}{
	"@eaDir":                    {}, // Synology thumbnails
	"#recycle":                  {},
	"#snapshot":                 {},
	".AppleDouble":              {},
	".AppleDB":                  {},
	".Spotlight-V100":           {},
	".TemporaryItems":           {},
	".Trashes":                  {},
	"$RECYCLE.BIN":              {},
	"System Volume Information": {},
	"lost+found":                {},
	"node_modules":              {},
	".git":                      {},
}

// DefaultWorkers returns the tag-reader concurrency used when Options.Workers
// is zero. Readers block on IO far more than they compute, so oversubscribing
// the CPU is the point.
func DefaultWorkers() int {
	n := runtime.NumCPU() * 4
	if n < 8 {
		n = 8
	}
	if n > 128 {
		n = 128
	}
	return n
}

// DefaultDirWorkers returns the directory-walk concurrency default.
func DefaultDirWorkers() int {
	n := runtime.NumCPU() * 2
	if n < 4 {
		n = 4
	}
	if n > 32 {
		n = 32
	}
	return n
}

type scanner struct {
	opts    Options
	ctx     context.Context
	files   chan fileJob
	dirSem  chan struct{}
	dirWG   sync.WaitGroup
	stats   statsCounters
	prev    map[string]*catalog.Track
	seenMu  sync.Mutex
	seen    map[string]struct{} // resolved dirs, only used when following links
	current atomic.Pointer[string]
}

type statsCounters struct {
	dirs, found, parsed, reused, errors, bytes atomic.Int64
	lastErr                                    atomic.Pointer[string]
}

type fileJob struct {
	path    string
	size    int64
	modTime int64
}

// Scan walks the configured roots and returns a fresh catalogue. onProgress,
// if non-nil, is called roughly every 100ms from a separate goroutine.
func Scan(ctx context.Context, opts Options, onProgress func(Stats)) (*catalog.Catalog, error) {
	if len(opts.Roots) == 0 {
		return nil, errors.New("scan: no roots given")
	}
	if opts.Workers <= 0 {
		opts.Workers = DefaultWorkers()
	}
	if opts.DirWorkers <= 0 {
		opts.DirWorkers = DefaultDirWorkers()
	}

	s := &scanner{
		opts:   opts,
		ctx:    ctx,
		files:  make(chan fileJob, opts.Workers*8),
		dirSem: make(chan struct{}, opts.DirWorkers),
	}
	if opts.FollowSymlinks {
		s.seen = make(map[string]struct{})
	}
	s.buildPrevIndex()

	start := time.Now()
	stop := s.startProgress(start, onProgress)

	// Tag readers. Each keeps a private slice so the hot path never contends
	// on a shared collector; the slices are concatenated once at the end.
	results := make([][]catalog.Track, opts.Workers)
	var readWG sync.WaitGroup
	for i := 0; i < opts.Workers; i++ {
		readWG.Add(1)
		go func(i int) {
			defer readWG.Done()
			results[i] = s.readLoop()
		}(i)
	}

	for _, root := range opts.Roots {
		abs, err := filepath.Abs(root)
		if err != nil {
			abs = root
		}
		s.dirWG.Add(1)
		go s.walkDir(abs)
	}

	s.dirWG.Wait()
	close(s.files)
	readWG.Wait()
	stop()

	total := 0
	for _, r := range results {
		total += len(r)
	}
	c := catalog.New()
	c.Tracks = make([]catalog.Track, 0, total)
	for _, r := range results {
		c.Tracks = append(c.Tracks, r...)
	}
	c.Roots = append(c.Roots, opts.Roots...)
	c.ScannedAt = time.Now()
	c.SortByPath()

	if onProgress != nil {
		st := s.snapshot(start)
		st.Finished = true
		onProgress(st)
	}
	return c, ctx.Err()
}

// buildPrevIndex indexes the previous catalogue by path for incremental reuse.
func (s *scanner) buildPrevIndex() {
	if s.opts.Previous == nil {
		return
	}
	prev := s.opts.Previous
	s.prev = make(map[string]*catalog.Track, len(prev.Tracks))
	for i := range prev.Tracks {
		s.prev[prev.Tracks[i].Path] = &prev.Tracks[i]
	}
}

// startProgress runs the progress ticker and returns a function that stops it.
func (s *scanner) startProgress(start time.Time, onProgress func(Stats)) func() {
	if onProgress == nil {
		return func() {}
	}
	done := make(chan struct{})
	var once sync.Once
	go func() {
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				onProgress(s.snapshot(start))
			}
		}
	}()
	return func() { once.Do(func() { close(done) }) }
}

func (s *scanner) snapshot(start time.Time) Stats {
	st := Stats{
		Dirs:    s.stats.dirs.Load(),
		Found:   s.stats.found.Load(),
		Parsed:  s.stats.parsed.Load(),
		Reused:  s.stats.reused.Load(),
		Errors:  s.stats.errors.Load(),
		Bytes:   s.stats.bytes.Load(),
		Elapsed: time.Since(start),
	}
	if p := s.current.Load(); p != nil {
		st.Current = *p
	}
	if e := s.stats.lastErr.Load(); e != nil {
		st.LastError = *e
	}
	return st
}

// walkDir reads one directory, queueing subdirectories and audio files. It
// hands subdirectories to the pool when a slot is free and recurses inline
// when it is not, so the walk never blocks on its own concurrency limit.
func (s *scanner) walkDir(dir string) {
	defer s.dirWG.Done()
	if s.ctx.Err() != nil {
		return
	}
	if s.opts.FollowSymlinks && !s.markSeen(dir) {
		return
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		s.recordError(dir, err)
		return
	}
	s.stats.dirs.Add(1)

	for _, e := range entries {
		name := e.Name()
		full := filepath.Join(dir, name)

		isDir := e.IsDir()
		if !isDir && s.opts.FollowSymlinks && e.Type()&fs.ModeSymlink != 0 {
			if fi, err := os.Stat(full); err == nil {
				isDir = fi.IsDir()
			}
		}

		if isDir {
			if s.skipDir(name, full) {
				continue
			}
			s.dirWG.Add(1)
			select {
			case s.dirSem <- struct{}{}:
				go func(p string) {
					defer func() { <-s.dirSem }()
					s.walkDir(p)
				}(full)
			default:
				s.walkDir(full) // pool is busy; stay on this goroutine
			}
			continue
		}

		if !s.wantFile(name, full) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			s.recordError(full, err)
			continue
		}
		if info.Size() <= 0 {
			continue
		}
		s.stats.found.Add(1)
		select {
		case s.files <- fileJob{path: full, size: info.Size(), modTime: info.ModTime().Unix()}:
		case <-s.ctx.Done():
			return
		}
	}
}

// markSeen records a resolved directory and reports whether it is new. Only
// used when following symlinks, where a cycle would otherwise loop forever.
func (s *scanner) markSeen(dir string) bool {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		real = dir
	}
	s.seenMu.Lock()
	defer s.seenMu.Unlock()
	if _, ok := s.seen[real]; ok {
		return false
	}
	s.seen[real] = struct{}{}
	return true
}

func (s *scanner) skipDir(name, full string) bool {
	if _, ok := defaultSkipDirs[name]; ok {
		return true
	}
	if !s.opts.IncludeHidden && strings.HasPrefix(name, ".") {
		return true
	}
	return s.excluded(name, full)
}

func (s *scanner) wantFile(name, full string) bool {
	// AppleDouble sidecars mirror real filenames, extension included, and are
	// never playable audio.
	if strings.HasPrefix(name, "._") {
		return false
	}
	if !s.opts.IncludeHidden && strings.HasPrefix(name, ".") {
		return false
	}
	if !tags.IsAudioPath(name) {
		return false
	}
	return !s.excluded(name, full)
}

func (s *scanner) excluded(name, full string) bool {
	for _, pat := range s.opts.Exclude {
		if ok, _ := filepath.Match(pat, name); ok {
			return true
		}
		if ok, _ := filepath.Match(pat, full); ok {
			return true
		}
		if strings.Contains(full, pat) {
			return true
		}
	}
	return false
}

// readLoop is one tag-reader worker.
func (s *scanner) readLoop() []catalog.Track {
	r := tags.NewReader()
	out := make([]catalog.Track, 0, 1024)
	for job := range s.files {
		if s.ctx.Err() != nil {
			return out
		}
		t, ok := s.readOne(r, job)
		if ok {
			out = append(out, t)
			s.stats.bytes.Add(job.size)
		}
	}
	return out
}

func (s *scanner) readOne(r *tags.Reader, job fileJob) (catalog.Track, bool) {
	// Incremental reuse: an unchanged file does not need to be opened at all.
	// On a rescan this turns almost the entire library into a map lookup.
	if prev, ok := s.prev[job.path]; ok && prev.Size == job.size && prev.ModTime == job.modTime {
		s.stats.reused.Add(1)
		t := *prev
		t.Changed = 0
		return t, true
	}

	md, err := r.ReadFile(job.path)
	if err != nil && md.Format == tags.FormatUnknown {
		s.recordError(job.path, err)
		// Keep the file in the catalogue anyway: an unreadable tag is exactly
		// the kind of thing the user opened this program to fix.
	}
	s.stats.parsed.Add(1)
	s.current.Store(&job.path)

	t := catalog.Track{Path: job.path, Size: job.size, ModTime: job.modTime}
	t.FromMetadata(&md)
	if t.Format == tags.FormatUnknown {
		t.Format = tags.FormatForPath(job.path)
	}
	return t, true
}

func (s *scanner) recordError(path string, err error) {
	s.stats.errors.Add(1)
	msg := path + ": " + err.Error()
	s.stats.lastErr.Store(&msg)
}

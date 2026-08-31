package library

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/remy/yamo/internal/catalog"
)

// openTimed opens a service over an empty catalogue whose root exists, which
// is all the rescan timer needs: it scans the roots the catalogue records.
func openTimed(t *testing.T, every time.Duration) *Service {
	t.Helper()
	dir := t.TempDir()
	music := filepath.Join(dir, "music")
	if err := os.MkdirAll(music, 0o755); err != nil {
		t.Fatal(err)
	}
	c := catalog.New()
	c.Roots = []string{music}

	path := filepath.Join(dir, "catalog.db")
	if err := catalog.Save(path, c); err != nil {
		t.Fatal(err)
	}
	s, err := Open(Options{
		CatalogPath:    path,
		SaveInterval:   50 * time.Millisecond,
		RescanInterval: every,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestRescanTimerRuns(t *testing.T) {
	s := openTimed(t, 50*time.Millisecond)

	deadline := time.Now().Add(10 * time.Second)
	for s.ScanStatus().Last == nil {
		if time.Now().After(deadline) {
			t.Fatal("the rescan timer never started a scan")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if last := s.ScanStatus().Last; last.State != JobSucceeded {
		t.Fatalf("the scheduled rescan %s: %s", last.State, last.Error)
	}

	// The schedule is part of the summary, because it is what says how
	// current the rest of the numbers are.
	st := s.Stats()
	if st.RescanEveryMS != 50 {
		t.Errorf("stats reported rescanEveryMs = %d, want 50", st.RescanEveryMS)
	}
	if st.NextRescanAt == nil {
		t.Fatal("stats reported no next rescan while the timer is running")
	}
	if !st.NextRescanAt.After(time.Now().Add(-time.Second)) {
		t.Errorf("the next rescan is %s, which is not ahead of now", st.NextRescanAt)
	}
}

// TestRescanOffByDefault is the important half: nothing watches the
// filesystem and nothing scans on its own unless it was asked to.
func TestRescanOffByDefault(t *testing.T) {
	s := openTimed(t, 0)

	st := s.Stats()
	if st.RescanEveryMS != 0 || st.NextRescanAt != nil {
		t.Errorf("stats advertised a rescan schedule with the timer off: %d, %v",
			st.RescanEveryMS, st.NextRescanAt)
	}

	time.Sleep(200 * time.Millisecond)
	if now := s.ScanStatus(); now.Running || now.Last != nil {
		t.Error("a scan ran with no timer and nobody asking")
	}
}

// TestRescanWithNoRoots covers the state a container starts in: a server with
// the timer on and an empty catalogue has nothing to scan, and must not turn
// that into an error on every tick.
func TestRescanWithNoRoots(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(Options{
		CatalogPath:    filepath.Join(dir, "catalog.db"),
		SaveInterval:   50 * time.Millisecond,
		RescanInterval: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	time.Sleep(200 * time.Millisecond)
	if now := s.ScanStatus(); now.Running || now.Last != nil {
		t.Error("a scan ran with no roots to scan")
	}
}

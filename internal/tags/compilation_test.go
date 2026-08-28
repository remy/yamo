package tags

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCompilationRoundTrip covers the Various Artists flag through every
// container that can be written. It is the one field with no text form: ID3
// spells it "1" in TCMP, MP4 stores a raw byte in cpil, and Vorbis uses a
// COMPILATION comment — so "reads back as what was written" is the only
// assertion that means anything across all three.
func TestCompilationRoundTrip(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"song.mp3", []string{"-c:a", "libmp3lame"}},
		{"song.m4a", []string{"-c:a", "aac"}},
		{"song.flac", nil},
		{"song.ogg", []string{"-c:a", "libvorbis"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := genFile(t, dir, tc.name, tc.args...)
			r := NewReader()

			set := func(on bool) {
				t.Helper()
				e := &Edit{Compilation: &on}
				if err := Write(path, e); err != nil {
					t.Fatalf("write %v: %v", on, err)
				}
			}
			read := func() Metadata {
				t.Helper()
				md, err := r.ReadFile(path)
				if err != nil && err != ErrNoTags {
					t.Fatalf("read: %v", err)
				}
				return md
			}

			if md := read(); md.Compilation {
				t.Fatal("an encoder-written file already claims to be a compilation")
			}
			set(true)
			md := read()
			if !md.Compilation {
				t.Error("the flag did not survive the write")
			}
			// Setting one field must not disturb the others.
			if md.Title != "Original Title" || md.Artist != "Elvis Presley" {
				t.Errorf("setting the flag changed other fields: %+v", md)
			}
			decodes(t, path)

			set(false)
			if read().Compilation {
				t.Error("the flag could not be cleared")
			}
			decodes(t, path)
		})
	}
}

// A nil Compilation must leave the file's own answer alone, which is what
// separates "do not touch this" from "set it to false" — an edit to one field
// must not silently clear the flag on a compilation.
func TestCompilationUntouchedByOtherEdits(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"keep.mp3", "keep.m4a", "keep.flac"} {
		t.Run(name, func(t *testing.T) {
			path := genFile(t, dir, name)
			on := true
			if err := Write(path, &Edit{Compilation: &on}); err != nil {
				t.Fatal(err)
			}
			if err := Write(path, &Edit{Album: str("Something Else")}); err != nil {
				t.Fatal(err)
			}
			md, _ := NewReader().ReadFile(path)
			if !md.Compilation {
				t.Error("an unrelated edit cleared the compilation flag")
			}
			if md.Album != "Something Else" {
				t.Errorf("album = %q", md.Album)
			}
		})
	}
}

// ffmpeg writes the flag as TXXX:TCMP rather than as the frame the
// specification provides, which is how it arrives on a real library.
func TestCompilationFromUserTextFrame(t *testing.T) {
	dir := t.TempDir()
	path := genFile(t, dir, "ffmpeg.mp3", "-c:a", "libmp3lame", "-metadata", "compilation=1")
	md, err := NewReader().ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !md.Compilation {
		raw, _ := os.ReadFile(path)
		t.Fatalf("not read as a compilation; head: %q", raw[:min(len(raw), 300)])
	}
	// And writing it moves the value into the frame that owns it.
	off := false
	if err := Write(path, &Edit{Compilation: &off}); err != nil {
		t.Fatal(err)
	}
	if md, _ := NewReader().ReadFile(path); md.Compilation {
		t.Error("clearing left the TXXX spelling behind, so it still reads as one")
	}
	_ = filepath.Base(path)
}

// A flag written by ffmpeg as TXXX:TCMP is found, and reported as living
// somewhere other than where this library writes it, so a clean-up moves it
// into the frame that owns it.
func TestCompilationInUserTextIsNonCanonical(t *testing.T) {
	dir := t.TempDir()
	path := genFile(t, dir, "ffmpeg2.mp3", "-c:a", "libmp3lame", "-metadata", "compilation=1")
	rep, err := StripFile(path, NewKeepSet(DefaultKeepTags), false)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, tag := range rep.NonCanonical {
		if tag == TagCompilation {
			found = true
		}
	}
	if !found {
		t.Fatalf("TXXX:TCMP not reported as non-canonical; got %v", rep.NonCanonical)
	}
}

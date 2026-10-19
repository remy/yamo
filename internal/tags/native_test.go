package tags

import (
	"bytes"
	"errors"
	"os"
	"testing"
)

// nativeListing is what RawTags is built from: everything in the file, by the
// key EditNative takes.
func nativeListing(t *testing.T, path string) map[string]RemovedTag {
	t.Helper()
	rep, err := StripFile(path, KeepSet{}, false)
	if err != nil {
		t.Fatalf("listing %s: %v", path, err)
	}
	out := map[string]RemovedTag{}
	for _, r := range rep.Removed {
		out[r.Display()] = r
	}
	return out
}

// keyWithText finds the entry holding a value, since each container files the
// same ffmpeg metadata under its own key.
func keyWithText(t *testing.T, list map[string]RemovedTag, text string) string {
	t.Helper()
	for k, r := range list {
		if r.Text == text {
			return k
		}
	}
	t.Fatalf("no entry holds %q among %v", text, list)
	return ""
}

// The point of the native editor is the tags nothing else can reach, so it is
// exercised on one of those in every writable container: set, read back
// through the listing a client edits from, then removed — and the audio has
// to survive both.
func TestEditNativeAcrossFormats(t *testing.T) {
	cases := []struct {
		name string
		file string
		args []string
	}{
		{"mp3", "a.mp3", []string{"-c:a", "libmp3lame"}},
		{"mp3-v24", "b.mp3", []string{"-c:a", "libmp3lame", "-id3v2_version", "4"}},
		{"flac", "c.flac", []string{"-c:a", "flac"}},
		{"m4a", "d.m4a", []string{"-c:a", "aac"}},
		{"ogg", "e.ogg", []string{"-c:a", "libvorbis"}},
		{"opus", "f.opus", []string{"-c:a", "libopus"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := genTagged(t, t.TempDir(), tc.file, tc.args...)
			before := nativeListing(t, path)
			key := keyWithText(t, before, "1956 RCA") // the copyright
			if !before[key].Editable {
				t.Fatalf("%s is text but is not offered as editable", key)
			}

			// Beyond Latin-1, so an ID3v2.3 frame has to change encoding.
			want := "℗ 1957 Sun Records"
			changed, err := EditNative(path, NativeEdits{key: str(want)})
			if err != nil || !changed {
				t.Fatalf("EditNative = %v, %v", changed, err)
			}
			decodes(t, path)
			after := nativeListing(t, path)
			if got := after[key].Text; got != want {
				t.Errorf("%s reads %q after the edit, want %q", key, got, want)
			}
			if len(after) != len(before) {
				t.Errorf("the edit changed the number of entries: %d, was %d", len(after), len(before))
			}
			md, err := NewReader().ReadFile(path)
			if err != nil || md.Artist != "Elvis Presley" || md.Title != "Hound Dog" {
				t.Errorf("the other fields did not survive: %+v, %v", md, err)
			}

			// The same value again is not a write.
			if changed, err := EditNative(path, NativeEdits{key: str(want)}); err != nil || changed {
				t.Errorf("repeating the edit = %v, %v; want no write", changed, err)
			}

			if changed, err := EditNative(path, NativeEdits{key: nil}); err != nil || !changed {
				t.Fatalf("removing = %v, %v", changed, err)
			}
			decodes(t, path)
			gone := nativeListing(t, path)
			if _, ok := gone[key]; ok {
				t.Errorf("%s is still in the file after removing it", key)
			}
			if len(gone) != len(before)-1 {
				t.Errorf("removing one key left %d entries, want %d", len(gone), len(before)-1)
			}
		})
	}
}

// The extensible frames carry their description in the key, so an edit has to
// keep it — and the comment's language with it — or the tag would come back
// under a different name, or as the visible comment.
func TestEditNativeKeepsDescriptions(t *testing.T) {
	path := kitchenSink(t, t.TempDir())

	edits := NativeEdits{
		"TXXX:MusicBrainz Album Id": str("0d15ea5e-0000-4000-8000-000000000000"),
		"COMM:iTunSMPB":             str(" 00000000 00000840 000003C0"),
		"TPUB":                      str("Sun Records — Memphis"),
	}
	if _, err := EditNative(path, edits); err != nil {
		t.Fatal(err)
	}
	decodes(t, path)
	list := nativeListing(t, path)
	for k, v := range edits {
		r, ok := list[k]
		if !ok {
			t.Errorf("%s is gone; the edit changed its key", k)
			continue
		}
		if r.Text != *v && r.Text != trimmed(*v) {
			t.Errorf("%s = %q, want %q", k, r.Text, *v)
		}
	}
	// Gapless data is not the comment, before or after.
	md, _ := NewReader().ReadFile(path)
	if md.Comment != "a real comment" {
		t.Errorf("comment = %q; editing iTunSMPB disturbed the real one", md.Comment)
	}
}

func trimmed(s string) string { return string(bytes.TrimSpace([]byte(s))) }

// A key the file does not hold, or a string for something that is not text, is
// refused before anything is written: a client acting on a stale listing has to
// hear about it, not see "saved".
func TestEditNativeRefuses(t *testing.T) {
	path := kitchenSink(t, t.TempDir())
	orig, _ := os.ReadFile(path)

	for name, edits := range map[string]NativeEdits{
		"missing key":    {"TXXX:NOT THERE": str("x")},
		"binary as text": {"APIC": str("a cover")},
		"one bad of two": {"TPUB": str("fine"), "TXXX:NOT THERE": nil},
	} {
		_, err := EditNative(path, edits)
		if !errors.Is(err, ErrNativeKey) {
			t.Errorf("%s: err = %v, want ErrNativeKey", name, err)
		}
		if now, _ := os.ReadFile(path); !bytes.Equal(now, orig) {
			t.Fatalf("%s: the file was written despite the refusal", name)
		}
	}

	// Binary can still go.
	if changed, err := EditNative(path, NativeEdits{"APIC": nil, "PRIV": nil}); err != nil || !changed {
		t.Fatalf("removing binary frames = %v, %v", changed, err)
	}
	list := nativeListing(t, path)
	if _, ok := list["APIC"]; ok {
		t.Error("APIC survived its removal")
	}
	if list["APIC"].Editable || list["TPUB"].Editable == false {
		t.Error("editable flags are wrong")
	}
	decodes(t, path)
}

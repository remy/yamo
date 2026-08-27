package tags

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestFrameCensus reports which ID3v2 frames actually occur in a library, and
// how much space each one takes. It is an inspection aid, not an assertion:
// deciding which frames are worth keeping needs data about the files in hand,
// not a list of what the specification permits.
//
//	go test ./internal/tags/ -run TestFrameCensus -v -args        (set CENSUS_ROOT)
func TestFrameCensus(t *testing.T) {
	root := os.Getenv("CENSUS_ROOT")
	if root == "" {
		t.Skip("set CENSUS_ROOT to a directory of audio files")
	}

	type stat struct {
		files  int
		bytes  int
		sample string
	}
	counts := map[string]*stat{}
	versions := map[byte]int{}
	total := 0

	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || FormatForPath(p) != FormatMP3 {
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		size := id3v2Size(raw)
		if size == 0 || size > len(raw) {
			return nil
		}
		tag, err := parseID3v2(raw)
		if err != nil {
			return nil
		}
		total++
		versions[tag.major]++

		seen := map[string]bool{}
		for _, f := range tag.frames {
			s := counts[f.id]
			if s == nil {
				s = &stat{}
				counts[f.id] = s
			}
			if !seen[f.id] {
				s.files++
				seen[f.id] = true
			}
			s.bytes += len(f.payload) + 10
			if s.sample == "" && strings.HasPrefix(f.id, "T") {
				if v := frameText(f.payload); v != "" {
					s.sample = v
				}
			}
			if s.sample == "" && f.id == "TXXX" {
				d, v := userText(f.payload)
				s.sample = d + " = " + v
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if total == 0 {
		t.Skip("no MP3s with an ID3v2 tag found")
	}

	ids := make([]string, 0, len(counts))
	for id := range counts {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return counts[ids[i]].files > counts[ids[j]].files })

	t.Logf("%d tagged MP3s under %s", total, root)
	for v, n := range versions {
		t.Logf("  ID3v2.%d: %d files", v, n)
	}
	t.Log("frame    files   total bytes  meaning / sample")
	for _, id := range ids {
		s := counts[id]
		t.Logf("%-6s  %4d  %10s  %s | %s", id, s.files, fmt.Sprint(s.bytes),
			frameMeaning(id), truncSample(s.sample))
	}
}

func truncSample(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 48 {
		return s[:48] + "…"
	}
	return s
}

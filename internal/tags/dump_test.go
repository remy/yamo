package tags

import (
	"os"
	"testing"
)

// TestDumpFiles is a manual inspection aid: point TAGDUMP at files or run it
// with -run TestDumpFiles to eyeball real-world parses.
func TestDumpFiles(t *testing.T) {
	paths := os.Getenv("TAGDUMP")
	if paths == "" {
		t.Skip("set TAGDUMP to a newline-separated list of files")
	}
	r := NewReader()
	for _, p := range splitLines(paths) {
		md, err := r.ReadFile(p)
		if err != nil {
			t.Logf("%s\n  ERROR: %v", p, err)
			continue
		}
		t.Logf("%s\n  fmt=%s artist=%q albumartist=%q album=%q title=%q\n  genre=%q year=%d track=%d/%d disc=%d/%d dur=%dms br=%d sr=%d ch=%d art=%v",
			p, md.Format, md.Artist, md.AlbumArtist, md.Album, md.Title,
			md.Genre, md.Year, md.Track, md.TrackTotal, md.Disc, md.DiscTotal,
			md.DurationMS, md.Bitrate, md.SampleRate, md.Channels, md.HasArt)
	}
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '\n' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	return out
}

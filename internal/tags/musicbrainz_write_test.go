package tags

import "testing"

const (
	mbGenesis  = "8e3fcd7d-bda1-4ca0-b987-b8528d2ad8fa"
	mbCollins  = "1e9a1a5d-5d0f-4b35-a0ff-49ba0b6e0e86"
	mbBanks    = "9d2b8a4a-ca7d-4e0c-a0b3-25ee42e9c5f2"
	mbOldStale = "00000000-1111-2222-3333-444444444444"
)

// The ids are written in Picard's spelling and read back from it, and an
// existing spelling of the same key — ffmpeg's TXXX:MUSICBRAINZ_ARTISTID in
// an MP3 — is replaced rather than left alongside. The reader keeps the first
// value it finds, so a stale frame left in place would make the edit look as
// though it never happened.
func TestMBIDRoundTrip(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"v24.mp3", []string{"-c:a", "libmp3lame", "-id3v2_version", "4"}},
		{"v23.mp3", []string{"-c:a", "libmp3lame", "-id3v2_version", "3"}},
		{"song.m4a", []string{"-c:a", "aac"}},
		{"song.flac", nil},
		{"song.ogg", []string{"-c:a", "libvorbis"}},
		{"song.opus", []string{"-c:a", "libopus"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"-metadata", "MUSICBRAINZ_ARTISTID=" + mbOldStale}, tc.args...)
			path := genFile(t, dir, tc.name, args...)
			r := NewReader()

			err := Write(path, &Edit{
				MBArtistID:      str(mbCollins + "; " + mbBanks),
				MBAlbumArtistID: str(mbGenesis),
			})
			if err != nil {
				t.Fatalf("write: %v", err)
			}
			md, err := r.ReadFile(path)
			if err != nil && err != ErrNoTags {
				t.Fatalf("read: %v", err)
			}
			if want := mbCollins + "; " + mbBanks; md.MusicBrainzArtistID != want {
				t.Errorf("artist ids = %q, want %q", md.MusicBrainzArtistID, want)
			}
			if md.MusicBrainzAlbumArtistID != mbGenesis {
				t.Errorf("album artist id = %q, want %q", md.MusicBrainzAlbumArtistID, mbGenesis)
			}
			if md.Title != "Original Title" || md.Artist != "Elvis Presley" {
				t.Errorf("writing ids changed the display fields: %+v", md)
			}
			decodes(t, path)

			// A second write replaces rather than adds.
			if err := Write(path, &Edit{MBArtistID: str(mbGenesis)}); err != nil {
				t.Fatalf("rewrite: %v", err)
			}
			md, _ = r.ReadFile(path)
			if md.MusicBrainzArtistID != mbGenesis {
				t.Errorf("artist ids after rewrite = %q, want only %q", md.MusicBrainzArtistID, mbGenesis)
			}

			// Clearing one leaves the other, and an unrelated edit leaves both.
			if err := Write(path, &Edit{MBArtistID: str("")}); err != nil {
				t.Fatalf("clear: %v", err)
			}
			if err := Write(path, &Edit{Genre: str("Prog")}); err != nil {
				t.Fatalf("genre: %v", err)
			}
			md, _ = r.ReadFile(path)
			if md.MusicBrainzArtistID != "" {
				t.Errorf("artist ids = %q after clearing", md.MusicBrainzArtistID)
			}
			if md.MusicBrainzAlbumArtistID != mbGenesis {
				t.Errorf("clearing the artist ids disturbed the album artist's: %q", md.MusicBrainzAlbumArtistID)
			}
			decodes(t, path)
		})
	}
}

func TestNormaliseMBIDs(t *testing.T) {
	upper := "8E3FCD7D-BDA1-4CA0-B987-B8528D2AD8FA"
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{"", "", true},
		{"  ", "", true},
		{mbGenesis, mbGenesis, true},
		{upper, mbGenesis, true},
		{mbCollins + "/" + mbBanks, mbCollins + "; " + mbBanks, true},
		{mbCollins + ", " + mbBanks + "; " + mbCollins, mbCollins + "; " + mbBanks, true},
		{"genesis", "", false},
		{mbGenesis + "; not-an-id", "", false},
		{mbGenesis[:35], "", false},
	} {
		got, ok := NormaliseMBIDs(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("NormaliseMBIDs(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

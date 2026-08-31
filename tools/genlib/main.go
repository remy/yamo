// Command genlib builds a synthetic music library for benchmarking.
//
// It exists because the interesting properties of this program — scan
// throughput, search latency, memory footprint — only show up at a scale that
// is impractical to keep in a repository. Point it at a scratch directory and
// it produces a tree with realistic shape: many artists, several albums each,
// a dozen tracks per album, with tags written by the same code path the editor
// uses.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/remy/yamo/internal/tags"
)

func main() {
	out := flag.String("out", "", "directory to generate into (required)")
	count := flag.Int("n", 100000, "number of tracks")
	perAlbum := flag.Int("per-album", 12, "tracks per album")
	albums := flag.Int("albums", 5, "albums per artist")
	audio := flag.String("audio", "", "source audio file whose stream is reused (required)")
	flag.Parse()

	if *out == "" || *audio == "" {
		fmt.Fprintln(os.Stderr, "usage: genlib -out DIR -audio FILE.mp3 [-n N]")
		os.Exit(2)
	}
	if err := run(*out, *audio, *count, *perAlbum, *albums); err != nil {
		fmt.Fprintln(os.Stderr, "genlib:", err)
		os.Exit(1)
	}
}

func run(out, audio string, count, perAlbum, albumsPer int) error {
	stream, err := os.ReadFile(audio)
	if err != nil {
		return err
	}
	// Strip any leading ID3v2 tag so each generated file gets a fresh one.
	stream = stripID3(stream)

	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}

	jobs := make(chan int, 1024)
	var wg sync.WaitGroup
	var done atomic.Int64
	workers := runtime.NumCPU()

	start := time.Now()
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if err := writeOne(out, stream, i, perAlbum, albumsPer); err != nil {
					fmt.Fprintln(os.Stderr, "genlib:", err)
				}
				if n := done.Add(1); n%5000 == 0 {
					fmt.Fprintf(os.Stderr, "\r  %d/%d  %s", n, count, time.Since(start).Round(time.Second))
				}
			}
		}()
	}
	for i := 0; i < count; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	fmt.Fprintf(os.Stderr, "\r  %d tracks in %s\n", count, time.Since(start).Round(time.Second))
	return nil
}

func writeOne(root string, stream []byte, i, perAlbum, albumsPer int) error {
	albumIdx := i / perAlbum
	trackNo := i%perAlbum + 1
	artistIdx := albumIdx / albumsPer
	albumOfArtist := albumIdx%albumsPer + 1

	artist := artistNames[artistIdx%len(artistNames)]
	if artistIdx >= len(artistNames) {
		artist = fmt.Sprintf("%s %d", artist, artistIdx/len(artistNames)+1)
	}
	album := fmt.Sprintf("%s, Vol. %d", albumTitles[albumIdx%len(albumTitles)], albumOfArtist)
	title := fmt.Sprintf("%s %d", songTitles[i%len(songTitles)], i%97+1)
	genre := genres[artistIdx%len(genres)]
	year := int32(1960 + artistIdx%64)

	dir := filepath.Join(root, sanitise(artist), sanitise(album))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, fmt.Sprintf("%02d %s.mp3", trackNo, sanitise(title)))
	if err := os.WriteFile(path, stream, 0o644); err != nil {
		return err
	}

	tt := int32(perAlbum)
	return tags.Write(path, &tags.Edit{
		Title: &title, Artist: &artist, Album: &album, Genre: &genre,
		AlbumArtist: &artist,
		Year:        &year,
		Track:       ptr(int32(trackNo)), TrackTotal: &tt,
		Disc: ptr(int32(1)), DiscTotal: ptr(int32(1)),
	})
}

func ptr[T any](v T) *T { return &v }

func stripID3(b []byte) []byte {
	if len(b) < 10 || string(b[:3]) != "ID3" {
		return b
	}
	size := 10 + int(b[6]&0x7F)<<21 | int(b[7]&0x7F)<<14 | int(b[8]&0x7F)<<7 | int(b[9]&0x7F)
	if size > 0 && size < len(b) {
		return b[size:]
	}
	return b
}

func sanitise(s string) string {
	out := []rune(s)
	for i, r := range out {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			out[i] = '-'
		}
	}
	return string(out)
}

var artistNames = []string{
	"Elvis Presley", "The Beatles", "Björk", "Miles Davis", "Aretha Franklin",
	"Radiohead", "Nina Simone", "David Bowie", "Kraftwerk", "Joni Mitchell",
	"Fela Kuti", "The Clash", "Portishead", "Sufjan Stevens", "Ella Fitzgerald",
	"Massive Attack", "Beyoncé", "Talking Heads", "Aphex Twin", "Sigur Rós",
	"Stevie Wonder", "The Levellers", "Vangelis", "Burial", "Bonobo",
	"Four Tet", "Caribou", "The Orb", "Orbital", "Underworld",
	"Public Enemy", "A Tribe Called Quest", "Kendrick Lamar", "Solange",
	"Thom Yorke", "Jon Hopkins", "Nils Frahm", "Ólafur Arnalds",
	"Kaytranada", "Little Dragon", "Khruangbin", "Hiatus Kaiyote",
}

var albumTitles = []string{
	"Sun Sessions", "Blue Lines", "Kind of Blue", "Hounds of Love",
	"Homogenic", "OK Computer", "Low", "Dummy", "Illinois", "Selected Works",
	"Remain in Light", "London Calling", "Innervisions", "Hope Street",
	"Untrue", "Black Sands", "Rounds", "Swim", "Adventures Beyond",
	"Dubnobasswithmyheadman", "It Takes a Nation", "Midnight Marauders",
}

var songTitles = []string{
	"Hound Dog", "Blue Moon", "Teardrop", "So What", "Running Up That Hill",
	"Jóga", "Paranoid Android", "Heroes", "Glory Box", "Chicago",
	"Once in a Lifetime", "Train in Vain", "Higher Ground", "Miles Away",
	"Archangel", "Kiara", "Gong", "Odessa", "Little Fluffy Clouds",
	"Born Slippy", "Fight the Power", "Electric Relaxation", "Alright",
	"Cranes in the Sky", "Hope Street", "Leave This Town", "Chariots of Fire",
}

var genres = []string{
	"Rock", "Electronic", "Jazz", "Soul", "Hip-Hop", "Classical", "Folk",
	"Ambient", "Punk", "Trip-Hop", "Alternative", "Funk",
}

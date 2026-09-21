package library

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/remy/yamo/internal/tags"
)

// Transcoding, for a device that cannot play the original.
//
// The case this exists for is an iPod: it plays AAC and MP3 and nothing a
// library assembled from FLAC is made of. A client syncing one asks for a track
// as AAC and gets a finished .m4a — encoded, tagged and with its cover — that it
// can copy across as it stands.
//
// Nothing is cached, and that is the point: the alternative is a second copy
// of the library in another format, which is exactly what a person keeping one
// library does not want. A sync reads each track once, start to finish, so
// encoding on request costs a few seconds a track and nothing afterwards. The
// thing that stops the same track being encoded twice is the client, which
// knows what is already on the device and holds each track's version to tell
// whether it has changed since.
//
// Each request encodes into a temporary file rather than into the response.
// Two reasons, and both are load-bearing:
//
//   - An MP4 cannot be written to a pipe. Its index, the moov atom, holds the
//     offsets of the audio and is only known once the audio has been written,
//     so the muxer seeks back to put it in place. Over a pipe ffmpeg can only
//     produce fragmented MP4, which Apple's own import paths handle badly.
//   - The tags are written by this library's own writer, not by ffmpeg.
//     ffmpeg's metadata mapping loses what this project is careful to keep —
//     the compilation flag, the sort fields — so the encode is told to carry
//     no metadata at all and the file is tagged afterwards, from the source,
//     through the same code every other edit goes through.
//
// A real file also means the response has a length and answers a Range
// request like any other, through http.ServeContent. The file is unlinked as
// soon as the response is done; see Transcoded.Close.

// ErrNoTranscode means the server was started without a usable ffmpeg.
var ErrNoTranscode = errors.New("library: transcoding is not enabled; the server has no ffmpeg with an AAC encoder")

// ErrUntranscodable means ffmpeg could not make sense of the source. It is a
// property of the file rather than a server fault, so retrying will not help.
var ErrUntranscodable = errors.New("library: the file could not be transcoded")

// The formats a track can be transcoded to. AAC is the only one so far; it is
// what an iPod plays best, and a list rather than a constant so that adding
// MP3 is an entry here rather than a change of shape.
const TranscodeAAC = "aac"

// TranscodeFormats lists every format Transcode accepts.
var TranscodeFormats = []string{TranscodeAAC}

// The AAC bitrate bounds, in kbps. 256 is what the iTunes Store sold and is
// transparent for nearly everyone; 128 is the floor anyone would choose on
// purpose, for a small device, and nothing is gained above 320.
const (
	DefaultAACBitrate = 256
	MinAACBitrate     = 96
	MaxAACBitrate     = 320
)

// TranscodeRequest says what to make of a track.
type TranscodeRequest struct {
	// As is the target format; see TranscodeFormats.
	As string

	// Bitrate is in kbps. Zero means DefaultAACBitrate.
	Bitrate int
}

// Transcoded is one encoded track, held open in a temporary file.
type Transcoded struct {
	File *os.File

	// Name is a filename for the result: the source's, with the new extension.
	Name string
	MIME string

	// ETag identifies this rendition; see transcodeETag.
	ETag string
}

// Close releases the file and removes it. It must be called: the temporary
// file is the only copy and nothing else will clean it up.
func (t *Transcoded) Close() error {
	err := t.File.Close()
	if rerr := os.Remove(t.File.Name()); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) && err == nil {
		err = rerr
	}
	return err
}

// transcodeSlots bounds how many encodes run at once.
//
// Each is one ffmpeg process pinning a core, and a sync that fires requests
// in parallel would otherwise start as many as it liked on a NAS that also has
// to answer searches. Half the cores leaves the rest of the API responsive; a
// request past the bound waits for a slot rather than being refused, since
// the client would only retry.
func transcodeSlots() int {
	return max(1, runtime.NumCPU()/2)
}

// Transcode encodes a track into the requested format and tags the result
// from the source.
//
// The caller owns the returned file and must Close it. Cancelling ctx — a
// client disconnecting — kills the encoder and removes what it had written.
func (s *Service) Transcode(ctx context.Context, id string, req TranscodeRequest) (*Transcoded, error) {
	as, bitrate, err := s.checkTranscode(req)
	if err != nil {
		return nil, err
	}

	s.mu.RLock()
	i, ok := s.lookupLocked(id)
	if !ok {
		s.mu.RUnlock()
		return nil, ErrNotFound
	}
	t := &s.cat.Tracks[i]
	path, version := t.Path, s.version(t)
	s.mu.RUnlock()

	select {
	case s.transcodes <- struct{}{}:
		defer func() { <-s.transcodes }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	// The tags are read from the file rather than from the catalogue, which
	// is a snapshot and does not hold the artwork anyway.
	md, err := tags.NewReader().ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: the file is no longer where the catalogue says it is", ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: reading its tags: %v", ErrUntranscodable, err)
	}
	cover, err := tags.ReadCover(path)
	if err != nil && !errors.Is(err, tags.ErrNoPicture) {
		return nil, fmt.Errorf("%w: reading its artwork: %v", ErrUntranscodable, err)
	}

	tmp, err := os.CreateTemp("", "yamo-transcode-*.m4a")
	if err != nil {
		return nil, err
	}
	out := tmp.Name()
	tmp.Close()
	keep := false
	defer func() {
		if !keep {
			os.Remove(out)
		}
	}()

	if err := s.encodeAAC(ctx, path, out, bitrate, &md); err != nil {
		return nil, err
	}
	if err := tags.Write(out, editFromMetadata(&md, cover)); err != nil {
		return nil, fmt.Errorf("tagging the transcoded file: %w", err)
	}

	f, err := os.Open(out)
	if err != nil {
		return nil, err
	}
	keep = true
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)) + ".m4a"
	return &Transcoded{
		File: f,
		Name: name,
		MIME: "audio/mp4",
		ETag: transcodeETag(version, as, bitrate),
	}, nil
}

// TranscodeETag is the ETag Transcode would give a rendition, worked out
// without encoding anything. It is what lets a client that already holds a
// track be answered 304 before an encoder is started, rather than after.
func (s *Service) TranscodeETag(id string, req TranscodeRequest) (string, error) {
	as, bitrate, err := s.checkTranscode(req)
	if err != nil {
		return "", err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	i, ok := s.lookupLocked(id)
	if !ok {
		return "", ErrNotFound
	}
	return transcodeETag(s.version(&s.cat.Tracks[i]), as, bitrate), nil
}

// transcodeETag identifies a rendition: the source's version and the settings,
// since the same track at another bitrate is a different file.
func transcodeETag(version, as string, bitrate int) string {
	return version + "-" + as + strconv.Itoa(bitrate)
}

// checkTranscode validates a request and fills in its defaults.
func (s *Service) checkTranscode(req TranscodeRequest) (string, int, error) {
	if s.opts.FFmpeg == "" {
		return "", 0, ErrNoTranscode
	}
	as := strings.ToLower(strings.TrimSpace(req.As))
	if as != TranscodeAAC {
		return "", 0, fmt.Errorf("%w: cannot transcode to %q; the formats are %s",
			ErrBadRequest, req.As, strings.Join(TranscodeFormats, ", "))
	}
	bitrate := req.Bitrate
	if bitrate == 0 {
		bitrate = DefaultAACBitrate
	}
	if bitrate < MinAACBitrate || bitrate > MaxAACBitrate {
		return "", 0, fmt.Errorf("%w: bitrate %d is outside %d–%d kbps",
			ErrBadRequest, bitrate, MinAACBitrate, MaxAACBitrate)
	}
	return as, bitrate, nil
}

// encodeAAC runs ffmpeg over one file.
func (s *Service) encodeAAC(ctx context.Context, in, out string, kbps int, md *tags.Metadata) error {
	args := []string{
		"-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-i", in,
		// The first audio stream only: a FLAC's cover arrives as a video
		// stream, and the cover is added by the tag writer instead.
		"-map", "0:a:0",
		// No metadata from ffmpeg at all; see the top of this file.
		"-map_metadata", "-1", "-map_chapters", "-1",
		"-c:a", "aac", "-b:a", strconv.Itoa(kbps) + "k",
	}
	// An iPod plays up to 48kHz and two channels. Hi-res and surround sources
	// are brought down to what it can play rather than left to fail on the
	// device, where the only symptom is a track that skips.
	if md.SampleRate > 48000 {
		args = append(args, "-ar", "44100")
	}
	if md.Channels > 2 {
		args = append(args, "-ac", "2")
	}
	args = append(args,
		// bitexact stops ffmpeg writing its own encoder tag, which it does
		// even with -map_metadata -1.
		"-flags", "+bitexact", "-fflags", "+bitexact",
		// The index at the front, so a player can start before it has the
		// whole file. The ipod muxer is MP4 with the brand iTunes writes.
		"-movflags", "+faststart", "-f", "ipod", out)

	cmd := exec.CommandContext(ctx, s.opts.FFmpeg, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 500 {
			msg = msg[:500] + "…"
		}
		return fmt.Errorf("%w: ffmpeg: %v: %s", ErrUntranscodable, err, msg)
	}
	return nil
}

// editFromMetadata turns everything a file says about itself into an edit for
// a file that says nothing yet.
//
// Only fields with a value are set, since the target is freshly encoded and
// has nothing to clear. The compilation flag is the exception and is always
// written: that is what iTunes does, and a reader that tells an absent flag
// from a false one would otherwise disagree with the rest of the library.
func editFromMetadata(md *tags.Metadata, cover *tags.Picture) *tags.Edit {
	e := &tags.Edit{}
	str := func(dst **string, v string) {
		if v != "" {
			*dst = &v
		}
	}
	num := func(dst **int32, v int32) {
		if v != 0 {
			*dst = &v
		}
	}
	str(&e.Title, md.Title)
	str(&e.Artist, md.Artist)
	str(&e.AlbumArtist, md.AlbumArtist)
	str(&e.Album, md.Album)
	str(&e.Genre, md.Genre)
	str(&e.Composer, md.Composer)
	str(&e.Comment, md.Comment)
	str(&e.TitleSort, md.TitleSort)
	str(&e.ArtistSort, md.ArtistSort)
	str(&e.AlbumSort, md.AlbumSort)
	str(&e.AlbumArtistSort, md.AlbumArtistSort)
	str(&e.ComposerSort, md.ComposerSort)
	num(&e.Year, md.Year)
	num(&e.Track, md.Track)
	num(&e.TrackTotal, md.TrackTotal)
	num(&e.Disc, md.Disc)
	num(&e.DiscTotal, md.DiscTotal)
	comp := md.Compilation
	e.Compilation = &comp
	if cover != nil {
		e.SetArtwork([]tags.Picture{*cover})
	}
	return e
}

// CheckFFmpeg finds an ffmpeg that can do what Transcode asks of it and
// returns its path.
//
// Finding the binary is not enough. Builds differ — a NAS vendor's ffmpeg is
// routinely built without the encoders a desktop one has — and a missing
// encoder would otherwise surface as a failure on every single request. So
// the encoder list is asked for once, at startup, where the answer can be
// reported to a person rather than to a phone.
func CheckFFmpeg(path string) (string, error) {
	if path == "" {
		path = "ffmpeg"
	}
	resolved, err := exec.LookPath(path)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, resolved, "-hide_banner", "-encoders").Output()
	if err != nil {
		return "", fmt.Errorf("%s -encoders: %w", resolved, err)
	}
	if !hasEncoder(b, "aac") {
		return "", fmt.Errorf("%s has no aac encoder", resolved)
	}
	return resolved, nil
}

// hasEncoder reads `ffmpeg -encoders` output, whose lines are a flags column,
// the encoder's name and a description.
func hasEncoder(list []byte, name string) bool {
	for _, line := range strings.Split(string(list), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && strings.HasPrefix(f[0], "A") && f[1] == name {
			return true
		}
	}
	return false
}

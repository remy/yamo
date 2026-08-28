package catalog

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/remy/tag-manager/internal/tags"
)

// Snapshot format. Strings are interned into a single table and referenced by
// index, which is what makes the file small and the load fast: the repeated
// artist, album and directory strings of a real library collapse to one copy
// each, and decoding is a linear walk with no parsing or allocation per field.
const (
	snapshotMagic = "TAGMGRDB"

	// Version 2 added the sort fields and the compilation flag.
	//
	// Compilation arrived first without a bump, on the reasoning that the
	// flags byte had spare bits and an older snapshot would decode with the
	// bit clear until the next scan said otherwise. The next scan never says
	// otherwise: scanning is incremental, and a file whose size and
	// modification time are unchanged is carried over from the previous
	// catalogue without ever being opened. So the bit stayed clear for every
	// file in an existing library, permanently, and no rescan short of
	// deleting the snapshot could fill it in.
	//
	// The lesson generalises: a field added to a snapshot is only
	// backward-compatible if something re-reads the files, and incremental
	// scanning guarantees nothing does. Rejecting an older snapshot outright
	// costs one full scan and is the only thing that makes a new field appear.
	snapshotVersion = 2
)

const (
	flagHasArt      = 1 << 0
	flagCompilation = 1 << 1
)

// ErrBadSnapshot means the file is not a snapshot this build can read.
var ErrBadSnapshot = errors.New("catalog: unrecognised snapshot")

// interner assigns stable indices to strings during encoding. Index 0 is
// always the empty string, so absent fields cost a single zero byte.
type interner struct {
	idx  map[string]uint64
	list []string
}

func newInterner() *interner {
	return &interner{idx: map[string]uint64{"": 0}, list: []string{""}}
}

func (in *interner) id(s string) uint64 {
	if i, ok := in.idx[s]; ok {
		return i
	}
	i := uint64(len(in.list))
	in.idx[s] = i
	in.list = append(in.list, s)
	return i
}

// Encode serialises the catalogue.
func Encode(c *Catalog) []byte {
	in := newInterner()

	// Intern first so the table is complete before records are written, and
	// so the buffer can be sized from real totals rather than a guess.
	type rec struct {
		dir, base, title, artist, albumArtist, album, genre, composer, comment uint64
		titleSort, artistSort, albumSort, albumArtistSort, composerSort        uint64
	}
	recs := make([]rec, len(c.Tracks))
	for i := range c.Tracks {
		t := &c.Tracks[i]
		dir, base := filepath.Split(t.Path)
		recs[i] = rec{
			dir: in.id(dir), base: in.id(base),
			title: in.id(t.Title), artist: in.id(t.Artist),
			albumArtist: in.id(t.AlbumArtist), album: in.id(t.Album),
			genre: in.id(t.Genre), composer: in.id(t.Composer),
			comment: in.id(t.Comment),
			// The sort fields intern especially well: a whole library's worth
			// of them collapses to one copy per distinct artist, and a track
			// without any costs five zero bytes.
			titleSort: in.id(t.TitleSort), artistSort: in.id(t.ArtistSort),
			albumSort: in.id(t.AlbumSort), albumArtistSort: in.id(t.AlbumArtistSort),
			composerSort: in.id(t.ComposerSort),
		}
	}

	buf := make([]byte, 0, 64+len(in.list)*24+len(c.Tracks)*40)
	buf = append(buf, snapshotMagic...)
	buf = binary.LittleEndian.AppendUint16(buf, snapshotVersion)
	buf = binary.LittleEndian.AppendUint16(buf, 0) // reserved flags
	buf = binary.LittleEndian.AppendUint64(buf, uint64(c.ScannedAt.Unix()))

	buf = binary.AppendUvarint(buf, uint64(len(c.Roots)))
	for _, r := range c.Roots {
		buf = binary.AppendUvarint(buf, uint64(len(r)))
		buf = append(buf, r...)
	}

	buf = binary.AppendUvarint(buf, uint64(len(in.list)))
	for _, s := range in.list {
		buf = binary.AppendUvarint(buf, uint64(len(s)))
		buf = append(buf, s...)
	}

	buf = binary.AppendUvarint(buf, uint64(len(c.Tracks)))
	for i := range c.Tracks {
		t := &c.Tracks[i]
		r := recs[i]
		buf = binary.AppendUvarint(buf, r.dir)
		buf = binary.AppendUvarint(buf, r.base)
		buf = binary.AppendUvarint(buf, r.title)
		buf = binary.AppendUvarint(buf, r.artist)
		buf = binary.AppendUvarint(buf, r.albumArtist)
		buf = binary.AppendUvarint(buf, r.album)
		buf = binary.AppendUvarint(buf, r.genre)
		buf = binary.AppendUvarint(buf, r.composer)
		buf = binary.AppendUvarint(buf, r.comment)
		buf = binary.AppendUvarint(buf, r.titleSort)
		buf = binary.AppendUvarint(buf, r.artistSort)
		buf = binary.AppendUvarint(buf, r.albumSort)
		buf = binary.AppendUvarint(buf, r.albumArtistSort)
		buf = binary.AppendUvarint(buf, r.composerSort)
		buf = binary.AppendUvarint(buf, uint64(nonNeg(t.Size)))
		buf = binary.AppendUvarint(buf, uint64(nonNeg(t.ModTime)))
		buf = binary.AppendUvarint(buf, uint64(nonNeg32(t.Year)))
		buf = binary.AppendUvarint(buf, uint64(nonNeg32(t.TrackNo)))
		buf = binary.AppendUvarint(buf, uint64(nonNeg32(t.TrackTotal)))
		buf = binary.AppendUvarint(buf, uint64(nonNeg32(t.Disc)))
		buf = binary.AppendUvarint(buf, uint64(nonNeg32(t.DiscTotal)))
		buf = binary.AppendUvarint(buf, uint64(nonNeg32(t.DurationMS)))
		buf = binary.AppendUvarint(buf, uint64(nonNeg32(t.Bitrate)))
		buf = binary.AppendUvarint(buf, uint64(nonNeg32(t.SampleRate)))
		var flags byte
		if t.HasArt {
			flags |= flagHasArt
		}
		if t.Compilation {
			flags |= flagCompilation
		}
		buf = append(buf, t.Channels, byte(t.Format), flags)
	}
	return buf
}

// Decode parses a snapshot produced by Encode.
func Decode(buf []byte) (*Catalog, error) {
	d := &decoder{b: buf}
	if len(buf) < 20 || string(buf[:8]) != snapshotMagic {
		return nil, ErrBadSnapshot
	}
	d.p = 8
	version := binary.LittleEndian.Uint16(buf[d.p:])
	if version != snapshotVersion {
		return nil, fmt.Errorf("%w: version %d, want %d", ErrBadSnapshot, version, snapshotVersion)
	}
	d.p += 4 // version and reserved flags
	scannedAt := int64(binary.LittleEndian.Uint64(buf[d.p:]))
	d.p += 8

	c := &Catalog{ScannedAt: time.Unix(scannedAt, 0)}

	nRoots := d.uvarint()
	if d.err != nil || nRoots > 1<<16 {
		return nil, ErrBadSnapshot
	}
	c.Roots = make([]string, 0, nRoots)
	for i := uint64(0); i < nRoots; i++ {
		c.Roots = append(c.Roots, d.str())
	}

	nStrings := d.uvarint()
	if d.err != nil || nStrings > uint64(len(buf)) {
		return nil, ErrBadSnapshot
	}
	table := make([]string, 0, nStrings)
	for i := uint64(0); i < nStrings; i++ {
		table = append(table, d.str())
	}
	if d.err != nil {
		return nil, d.err
	}

	nTracks := d.uvarint()
	if d.err != nil || nTracks > uint64(len(buf)) {
		return nil, ErrBadSnapshot
	}
	get := func(i uint64) string {
		if i < uint64(len(table)) {
			return table[i]
		}
		d.err = ErrBadSnapshot
		return ""
	}

	c.Tracks = make([]Track, nTracks)
	for i := range c.Tracks {
		t := &c.Tracks[i]
		dir, base := get(d.uvarint()), get(d.uvarint())
		t.Path = dir + base
		t.Title = get(d.uvarint())
		t.Artist = get(d.uvarint())
		t.AlbumArtist = get(d.uvarint())
		t.Album = get(d.uvarint())
		t.Genre = get(d.uvarint())
		t.Composer = get(d.uvarint())
		t.Comment = get(d.uvarint())
		t.TitleSort = get(d.uvarint())
		t.ArtistSort = get(d.uvarint())
		t.AlbumSort = get(d.uvarint())
		t.AlbumArtistSort = get(d.uvarint())
		t.ComposerSort = get(d.uvarint())
		t.Size = int64(d.uvarint())
		t.ModTime = int64(d.uvarint())
		t.Year = int32(d.uvarint())
		t.TrackNo = int32(d.uvarint())
		t.TrackTotal = int32(d.uvarint())
		t.Disc = int32(d.uvarint())
		t.DiscTotal = int32(d.uvarint())
		t.DurationMS = int32(d.uvarint())
		t.Bitrate = int32(d.uvarint())
		t.SampleRate = int32(d.uvarint())
		if d.p+3 > len(d.b) {
			return nil, ErrBadSnapshot
		}
		t.Channels = d.b[d.p]
		t.Format = tags.Format(d.b[d.p+1])
		t.HasArt = d.b[d.p+2]&flagHasArt != 0
		t.Compilation = d.b[d.p+2]&flagCompilation != 0
		d.p += 3
		if d.err != nil {
			return nil, d.err
		}
	}
	return c, nil
}

type decoder struct {
	b   []byte
	p   int
	err error
}

func (d *decoder) uvarint() uint64 {
	if d.err != nil || d.p >= len(d.b) {
		d.err = ErrBadSnapshot
		return 0
	}
	v, n := binary.Uvarint(d.b[d.p:])
	if n <= 0 {
		d.err = ErrBadSnapshot
		return 0
	}
	d.p += n
	return v
}

func (d *decoder) str() string {
	n := d.uvarint()
	if d.err != nil || d.p+int(n) > len(d.b) {
		d.err = ErrBadSnapshot
		return ""
	}
	s := string(d.b[d.p : d.p+int(n)])
	d.p += int(n)
	return s
}

// Save writes the snapshot atomically: a temporary file in the destination
// directory, fsynced, then renamed over the target. A crash mid-write leaves
// the previous catalogue intact rather than a truncated one.
func Save(path string, c *Catalog) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tagmgr-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds

	if _, err := tmp.Write(Encode(c)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// Load reads a snapshot from disk.
func Load(path string) (*Catalog, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Decode(b)
}

// LoadRoots reads only the library roots from a snapshot, whatever version it
// is, and returns nil if it cannot.
//
// This is the recovery path for a snapshot a version bump has just rejected.
// The tracks in it are unusable — that is the whole point of rejecting it —
// but the roots are still the right answer to "where is the music", and they
// are the one thing a caller cannot reconstruct. Without them a version bump
// would leave the server not merely with an empty catalogue but with nothing
// to scan, so the rescan that fixes everything could not be asked for.
//
// The header has held the same shape since version 1: magic, version,
// reserved flags, scan time, then the root list. Only the track records have
// changed, and those are not read here.
func LoadRoots(path string) []string {
	b, err := os.ReadFile(path)
	if err != nil || len(b) < 20 || string(b[:8]) != snapshotMagic {
		return nil
	}
	d := &decoder{b: b, p: 20} // magic, version, reserved flags, scan time
	n := d.uvarint()
	if d.err != nil || n > 1<<16 {
		return nil
	}
	roots := make([]string, 0, n)
	for i := uint64(0); i < n; i++ {
		r := d.str()
		if d.err != nil {
			return nil
		}
		roots = append(roots, r)
	}
	return roots
}

func nonNeg(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

func nonNeg32(v int32) int32 {
	if v < 0 {
		return 0
	}
	return v
}

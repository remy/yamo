# tagmgr

A terminal music metadata manager. It catalogues a large music library fast,
searches it instantly, and edits tags in place — including across hundreds of
tracks at once.

Built for a NAS: one static binary, no runtime dependencies, no database
server, no GUI.

```
┌─ tagmgr  /volume1/music ─────────────────────────────────────────────────────┐
│ / artist:elvis album:"sun sessions"                     12 selected · 12 / 98,412 │
├──┬───────────────┬──────────────────┬─────────────────────┬────┬────┬────────┤
│  │Artist         │Album             │Title                │   #│Year│  Time  │
├──┼───────────────┼──────────────────┼─────────────────────┼────┼────┼────────┤
│✓•│Elvis Presley  │The Sun Sessions  │Blue Moon of Kentucky│   1│1956│  2:04  │
│✓•│Elvis Presley  │The Sun Sessions  │I Don't Care         │   2│1956│  2:41  │
└──┴───────────────┴──────────────────┴─────────────────────┴────┴────┴────────┘
```

## Why Go

The work here is almost entirely file IO: open a hundred thousand files, read a
few kilobytes from each, close them. Go gives goroutine-per-file concurrency
that saturates the IO queue without any async plumbing, cross-compiles to a
single static binary for the NAS with one environment variable, and has the
strongest terminal-UI ecosystem going. A faster language would not help,
because nothing here is CPU-bound.

## Install

```sh
make nas                       # builds dist/tagmgr-linux-{amd64,arm64}
scp dist/tagmgr-linux-amd64 nas:/usr/local/bin/tagmgr
```

UGREEN NASync boxes are x86-64, so `tagmgr-linux-amd64` is the one you want.
Both binaries are static and depend on nothing on the target.

For local use: `make install` or `go install ./cmd/tagmgr`.

## Use

```sh
tagmgr scan /volume1/music     # build the catalogue
tagmgr                         # browse and edit
tagmgr find artist:elvis       # query from a script
tagmgr info                    # what is in the catalogue
tagmgr help                    # usage; `tagmgr help scan` for one command
```

Every command takes `-h`, and `tagmgr help <command>` prints the same thing.
Usage goes to stdout so it pipes into a pager; errors go to stderr.

The catalogue lives in your cache directory (`tagmgr info` prints the path).
Override it with `-catalog PATH` or `TAGMGR_CATALOG`.

### Scanning

`tagmgr scan` walks the given directories and extracts tags. Re-running it
without `-full` reuses every entry whose size and modification time are
unchanged, so a refresh costs a stat per file rather than a read.

```sh
tagmgr scan /volume1/music              # first run, or refresh
tagmgr scan                             # refresh whatever the catalogue covers
tagmgr scan -full /volume1/music        # ignore the cache, re-read everything
tagmgr scan -exclude Podcasts /volume1/music
```

Deleted files disappear from the catalogue on the next scan. Directories that
never hold music (`@eaDir`, `#recycle`, `lost+found`, dot-directories) are
skipped, as are AppleDouble `._` sidecars.

### Searching

The same query language works in the search bar and on the command line.

| Query | Matches |
| --- | --- |
| `elvis` | any text field contains "elvis" |
| `artist:elvis` | just the artist field |
| `artist:"elvis presley"` | quoted values may contain spaces |
| `year:1977` | exact year |
| `year:>1980`, `year:<=1969` | comparisons |
| `year:1970-1979` | an inclusive range |
| `-genre:christmas` | exclude matches |
| `album:` | tracks where the field is empty |
| `artist:elvis year:>1960` | terms are ANDed |

Matching is case- and accent-insensitive in both directions: `bjork` finds
Björk, and `Beyoncé` finds Beyoncé. Unqualified terms search the tag fields but
not the file path; use `path:` for that.

On the command line a query starting with `-` needs `--` first, so the flag
parser leaves it alone:

```sh
tagmgr find -- -genre:live artist:elvis
```

### Editing

Press `?` in the browser for the full key list. The short version:

- `/` search, updating as you type
- `space` mark a track, `v` mark a range, `a` mark everything matching
- `e` open the editor for the marked tracks, or for the one under the cursor
- `tab` move between fields, `⏎` edit the focused one
- typing offers completions drawn from values already in your library; `tab`
  accepts the highlighted one
- `⏎` commits the field to **every** marked track at once
- `u` / `^r` undo and redo, one step per edit no matter how many tracks it hit
- `^s` write every change back to disk
- `R` re-apply the filter after editing

Nothing touches disk until `^s`. Changed tracks carry a `•` until then.

Edits are applied per field: saving a track writes only the fields you actually
changed and leaves every other tag in the file exactly as it was. Where a
format reserves padding for this (ID3v2, FLAC, MP4 `free` atoms) the tag is
rewritten in place without moving the audio, so correcting a title in a 40 MB
FLAC writes a few kilobytes rather than 40 MB. When the tag has to grow beyond
its padding the file is rebuilt alongside the original and renamed over it, so
an interrupted write cannot leave a damaged track.

### Stripping

`tagmgr strip` removes every ID3v2 frame that is not on a fixed keep list,
leaving a uniform tag across the library.

```sh
tagmgr strip                                    # dry run over everything
tagmgr strip artist:elvis                       # dry run over a subset
tagmgr strip -list                              # print the keep list
tagmgr strip -backup ~/strip.jsonl -apply       # do it, reversibly
tagmgr restore -backup ~/strip.jsonl -apply     # put it all back
```

It is a **dry run unless `-apply` is given**, and the dry run reports exactly
what would go, grouped by frame, with sample values:

```
frame     files       bytes  meaning
COMM         22     2.7 KiB  comment  ·  iTunSMPB: 00000000 00000210 0000… / iTunNORM:…
TENC         11       253 B  encoded by  ·  iTunes v4.9 / iTunes v4.2
```

The default keep list is sixteen frames — the ones that identify a song, plus
the ones a library needs to group and sort correctly:

| | |
| --- | --- |
| Identity | `TIT2` `TPE1` `TALB` `TPE2` `TRCK` `TPOS` `TCON` `TDRC` `TYER` |
| Behaviour | `TCMP` `TSOT` `TSOP` `TSOA` `TSO2` `TCOM` `APIC` |

`TCMP` is the compilation flag; without it a Various Artists album fragments
into one album per track. The `TSO*` frames are sort orders, which put "The
Beatles" under B. Change the list with `-keep` or extend it with `-also`.

Everything else goes: encoder signatures, comments, private blobs, URLs,
ratings, and external identifiers such as MusicBrainz IDs. Two consequences
are worth knowing before running it:

- **`COMM` carries iTunes internals as well as real comments.** `iTunSMPB` is
  gapless-playback data; removing it breaks gapless on live albums and DJ
  mixes. Keep it with `-also COMM`, which keeps every comment frame — the keep
  list works on frame identifiers, not on comment descriptions.
- **MusicBrainz and AcoustID identifiers are not regenerable** from the audio
  alone. They live in `TXXX` and `UFID` frames and are removed by default.

Because the tag only ever shrinks, the rewrite happens inside the existing
padding: the file size does not change and no audio is moved. Stripping the
whole library is bounded by the cost of one small write per file.

ID3v2.2 tags are rewritten as v2.3, but only for files that actually lose a
frame. The frames are translated first, so a v2.2 `TP1` is recognised as an
artist and kept.

## Format support

| Format | Read | Write |
| --- | --- | --- |
| MP3 (ID3v2.2/2.3/2.4, ID3v1) | yes | yes |
| FLAC (Vorbis comments) | yes | yes |
| MP4 / M4A / M4B (iTunes atoms) | yes | yes |
| Ogg Vorbis | yes | yes |
| Opus | yes | yes |
| WMA (ASF) | yes | no |
| WAV (RIFF INFO, ID3 chunk) | yes | no |
| AIFF | yes | no |

The editor warns before you type when the selection contains a format it cannot
write back.

Everything is parsed by this package directly rather than through a tag
library, so reads touch only the bytes the metadata occupies: a 40 MB FLAC and
a 3 MB MP3 cost the same to catalogue. Duration and bitrate come from the
stream header, including Xing/VBRI frame counts for variable-bitrate MP3s.

## Performance

Measured on 100,000 MP3 files (a synthetic library from `tools/genlib`), on an
M-series Mac with the files in page cache. On a NAS the scan will be bound by
disk and network rather than by this code, but the per-file work is the same.

| Operation | Time |
| --- | --- |
| Full scan, 100,000 files | 2.6 s (~38,600 files/sec) |
| Incremental rescan, nothing changed | 0.30 s |
| Load catalogue (4.6 MiB) | 13 ms |
| Build search index | 49 ms, once at startup |
| Search | 0.9–3.6 ms |
| Save 12 edited files | 30 ms |

Search is a linear pass over pre-folded, contiguous strings rather than an
inverted index. At this size that is both faster and more useful, because it
supports mid-word matches that a token index cannot.

The catalogue is a single file with every repeated string interned once, which
is why 100,000 tracks fit in 4.6 MiB and load in 13 ms.

## Layout

```
cmd/tagmgr/        command line: scan, find, info, browse
internal/tags/     format parsers and writers; no third-party tag library
internal/catalog/  in-memory library, binary snapshot, search index
internal/scan/     parallel directory walk and tag extraction
internal/ui/       the terminal interface
tools/genlib/      synthetic library generator, for benchmarking
tools/tuidrive/    drives the interface in a pty, for testing the rendering
```

## Development

```sh
make test          # unit tests, plus round-trips against real ffmpeg output
make bench         # search and catalogue benchmarks
make vet fmt
```

The tag round-trip tests encode real files with ffmpeg, write tags, and check
both that the tags read back and that the audio still decodes — a tag writer
that corrupts the stream can still produce perfectly readable tags. They skip
if ffmpeg is not installed.

The interface is tested by asserting that every rendered line is exactly the
terminal width, across eleven window sizes and eight modes. That invariant is
what keeps the box-drawing junctions aligned; when it breaks, the layout
visibly tears.

To look at the interface without a terminal:

```sh
python3 -m venv .venv && ./.venv/bin/pip install pyte
./.venv/bin/python tools/tuidrive/drive.py "./dist/tagmgr" 120x30 '/artist:elvis<enter>' 'e'
```

## Limitations

- WMA, WAV and AIFF are read but not written.
- `strip` and `restore` handle MP3 only; other formats in the catalogue are
  counted and skipped.
- Cover art is detected but not displayed or edited.
- The catalogue is not watched; run `tagmgr scan` after adding music.
- One catalogue at a time; use `-catalog` to keep several.

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

## The server

`tagmgr serve` runs the backend that owns the catalogue and the music files.
It exposes an HTTP API described by an OpenAPI 3.1 schema, so a mobile web
interface — or anything else — can be built on the same operations the terminal
uses.

```sh
tagmgr serve                                 # loopback, no token needed
cd webapp && tagmgr serve                    # ...and serve the browser front end
tagmgr serve -listen 0.0.0.0:8467            # reachable on the network
tagmgr serve -listen unix:///tmp/tagmgr.sock

curl -O http://127.0.0.1:8467/openapi.yaml   # download the contract
open http://127.0.0.1:8467/docs              # browse it
```

The schema is served by the running binary, so it is always the contract that
build actually implements. `/docs` is a self-contained page with no outbound
network access required, because a NAS may not have any.

### The shape of it

The library runs to six figures and the work is gradual, so three things
follow. Everything is paged — there is no endpoint that returns the whole
library. Operations select by **query** rather than by identifier, so setting a
field on two thousand tracks does not mean uploading two thousand ids. And
anything that can touch more than one file returns a **job**, even when it
finishes at once, so a client has one shape to handle.

```sh
# the case this exists for: fix a misspelled artist across a whole search
curl -X POST localhost:8467/v1/tracks/batch -H 'Content-Type: application/json' -d '{
  "selector": {"query": "artist:presly", "expectCount": 42},
  "set": {"artist": "Elvis Presley"}
}'
```

`expectCount` is a safety rail: the client states how many matches it showed
someone, and the server refuses if the selection has moved since.

Every track carries a `version`. Send it back as `If-Match` on a `PATCH` and a
mismatch returns `409`, which is what stops an edit made on a phone silently
overwriting one made in the terminal a moment earlier. `GET /v1/events` streams
changes, so several interfaces stay in step without polling.

Edits write through: a `PATCH` writes the tag to the file and returns. There is
no pending state to commit, and therefore none for a background scan to lose.

### Access

Loopback by default, where no token is needed. Binding anywhere else requires
one — generated on first run, printed once, kept beside the catalogue.

Cross-origin browser requests are only permitted when a token is set. A server
on loopback with permissive headers could be driven by any web page you
happened to visit, and this API rewrites music files.

## Use

```sh
tagmgr serve                   # the backend; everything else is a client
tagmgr scan /volume1/music     # build the catalogue
tagmgr                         # browse and edit
tagmgr find artist:elvis       # query from a script
tagmgr info                    # what is in the library
tagmgr help                    # usage; `tagmgr help scan` for one command
```

The server owns the catalogue and the music files; nothing else opens them.
Start it before anything else, or from a systemd unit or launchd plist so it
comes up with the machine.

Every command takes `-h`, and `tagmgr help <command>` prints the same thing.
Usage goes to stdout so it pipes into a pager; errors go to stderr.

The catalogue lives in your cache directory (`tagmgr info` prints the path).
Override it with `-catalog PATH` or `TAGMGR_CATALOG`.

### Scanning

`tagmgr scan` walks the given directories and extracts tags. Re-running it
without `-full` reuses every entry whose size and modification time are
unchanged, so a refresh costs a stat per file rather than a read.

```sh
tagmgr scan -status                     # is one running?
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
| `compilation:1` | the Various Artists flag is set (`comp:`, `va:`) |
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

The browser is a client of the server, so it can run on your laptop against
the NAS rather than only over SSH. It holds a window of the library rather than
all of it, fetches pages as you scroll, and search is debounced so a keystroke
does not mean a round trip.

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
- `R` refresh after editing has made the view stale

Nothing touches disk until `^s`. Changed tracks carry a `•` until then, and
marking everything with `a` selects by query rather than by listing tracks, so
it costs the same whether it matches ten or a hundred thousand.

Each save sends the version the track was read at. If something else changed
the same file in between — a phone, another terminal — that edit is reported
and kept pending rather than overwriting the other one.

Edits are applied per field: saving a track writes only the fields you actually
changed and leaves every other tag in the file exactly as it was. Where a
format reserves padding for this (ID3v2, FLAC, MP4 `free` atoms) the tag is
rewritten in place without moving the audio, so correcting a title in a 40 MB
FLAC writes a few kilobytes rather than 40 MB. When the tag has to grow beyond
its padding the file is rebuilt alongside the original and renamed over it, so
an interrupted write cannot leave a damaged track.

### Cover art

Art is moved around with a clipboard: copy one image, then paste it onto as
many tracks as you like. The clipboard is on disk, so a cover copied in the
browser can be pasted from the command line and the other way round.

```sh
tagmgr art                                   # what art the library has
tagmgr art -copy cover.jpg                   # put an image on the clipboard
tagmgr art -copy "01 track.flac"             # ...or lift one off a track
tagmgr art -paste artist:elvis -apply        # write it to matching tracks
tagmgr art -from-folder -apply               # embed the folder.jpg beside each track
tagmgr art -export ~/covers                  # write covers out as files
tagmgr art -remove 'album:demos' -apply      # take art off
```

In the browser: `y` copies the cover under the cursor, `p` pastes it onto the
marked tracks, and `A` opens the art panel — which draws the cover as an actual
image in iTerm2, Kitty, WezTerm and Ghostty, and falls back to text everywhere
else. Unlike tag edits, artwork is written straight to disk rather than held
for `^s`: holding several hundred covers in memory to write later would cost
more than the library itself.

`tagmgr art` with no other flag reports what is there, grouped by image:

```
  tracks  image                           embedded  example album
      12  600×600 jpeg 125 KB              1.5 MiB  Sun Sessions
```

That grouping is the useful part. Measured on a real library, **85% of
embedded artwork is duplicate bytes** — the same cover repeated once per track.

Two things to know:

- **Embedding art rewrites the file.** A cover is far larger than the padding
  any format reserves, so unlike every other edit the audio has to move. Tracks
  whose art already matches are skipped, which makes a repeated run free.
- **`-from-folder` is usually what you want.** It looks beside each track for
  `cover.jpg`, `folder.jpg`, `front.jpg` and the like — how a downloaded
  library normally stores art, and the usual reason none of it appears on a
  phone. The directory is scanned once per album, not once per track.

### Stripping

`tagmgr strip` removes every tag that is not on a keep list, leaving a uniform
set of metadata across the library.

```sh
tagmgr strip                                    # dry run over everything
tagmgr strip artist:elvis                       # dry run over a subset
tagmgr strip -list                              # print the keep list
tagmgr strip -backup ~/strip.jsonl -apply       # do it, reversibly
tagmgr restore -backup ~/strip.jsonl -apply     # put it all back
```

It is a **dry run unless `-apply` is given**, and the dry run reports exactly
what would go, grouped by format and key, with sample values:

```
fmt   key                       files       bytes  meaning
flac  DESCRIPTION                   1        24 B  free-text comment  ·  strip me
mp3   TSSE                          1        24 B  encoder name and settings  ·  Lavf61.7.100
mp3   TXXX:comment                  1        28 B  free-text comment  ·  comment=strip me
mp4   ©cmt                          1        32 B  free-text comment  ·  strip me
```

#### One list, every format

The keep list is written in canonical names rather than in the identifiers any
one container uses, so the same list applies everywhere. An album artist is
kept whether the file spells it `TPE2`, `ALBUMARTIST` or `aART`:

| tag | mp3 | flac / ogg / opus | mp4 |
| --- | --- | --- | --- |
| `title` | TIT2 | TITLE | ©nam |
| `artist` | TPE1 | ARTIST PERFORMER | ©ART |
| `album` | TALB | ALBUM | ©alb |
| `albumartist` | TPE2 | ALBUMARTIST, ALBUM ARTIST | aART |
| `track` | TRCK | TRACKNUMBER, TOTALTRACKS | trkn |
| `disc` | TPOS | DISCNUMBER, TOTALDISCS | disk |
| `genre` | TCON | GENRE | ©gen, gnre |
| `date` | TDRC, TYER, TDRL | DATE, YEAR | ©day |
| `compilation` | TCMP | COMPILATION | cpil |
| `composer` | TCOM | COMPOSER | ©wrt |
| `titlesort` | TSOT | TITLESORT | sonm |
| `artistsort` | TSOP | ARTISTSORT | soar |
| `albumsort` | TSOA | ALBUMSORT | soal |
| `albumartistsort` | TSO2 | ALBUMARTISTSORT | soaa |
| `artwork` | APIC | METADATA_BLOCK_PICTURE | covr |
| `gapless` | COMM:iTunSMPB | — | pgap, iTunSMPB |
| `soundcheck` | COMM:iTunNORM | — | iTunNORM |
| `itunes` | — | — | stik, apID, purd, cnID, atID, plID, … |

`compilation` is the flag that stops a Various Artists album fragmenting into
one album per track. The sort tags are what put "The Beatles" under B.
`gapless` is iTunes' own, and kept because nothing can reconstruct it: it is
the only record of where the encoder padding starts.

`gapless`, `soundcheck` and `itunes` cover everything iTunes wrote, so a
library ripped and bought through it comes out of a strip still describing
itself the way iTunes described it. `itunes` covers Apple's own atoms — the
media kind, the advisory flag, the store and purchase identifiers, the account
that bought the file — along with any freeform item in the `com.apple.iTunes`
namespace whose name is not recognised as something else. The namespace alone
cannot decide, because Picard writes MusicBrainz tags there too.

Note that `apID` holds the Apple ID that bought the file, which is an email
address. Drop it on its own with `-keep` minus `itunes`, or keep the rest and
accept it.

The date is written to `TDRL` as well as the year frame. ID3 separates when a
recording was made from when it was released and MP4 does not, so an MP3
carrying only a year frame reports no release date at all while an M4A of the
same song reports one. Writing both makes the two formats agree.

Change the list with `-keep`, extend it with `-also`. Names may be canonical
(`albumartist`) or native to any format (`TPE2`, `ALBUMARTIST`, `aART`) — you
should not have to translate a list you already have. `tagmgr strip -list`
prints the full vocabulary.

#### Putting values where they belong

`-normalize` additionally moves kept fields a file holds under an older name
into the one this tool writes: an ID3v2.2 frame, a genre stored as `(19)`, an
MP4 `gnre` atom, a Vorbis `PERFORMER`, a year with no `TDRL` beside it. The
values do not change, only where they are kept — which is what decides whether
the next tool along finds them. A dry run counts them without writing.

#### What goes, and what that costs

Everything else: encoder signatures, comments, private blobs, URLs, ratings,
and external identifiers. Two consequences are worth knowing beforehand:

- **Comments are not one thing.** iTunes hides private data among them, so
  comments are resolved by description rather than by container key: the
  gapless data in `COMM:iTunSMPB` is kept while an ordinary comment beside it,
  under the same frame id, goes. Volume normalisation (`iTunNORM`) is dropped —
  ReplayGain has superseded it and it can be recomputed. Keep it with
  `-also soundcheck`.
- **MusicBrainz and AcoustID identifiers are not regenerable** from the audio.
  `-also musicbrainz,acoustid` keeps them.

Because the metadata only ever shrinks, the rewrite happens inside the padding
each format reserves: verified on real files, tag and file sizes come out
byte-identical afterwards and no audio is moved. Stripping a whole library is
bounded by one small write per file.

ID3v2.2 tags are rewritten as v2.3, but only for files that actually lose
something. The frames are translated first, so a v2.2 `TP1` is recognised as an
artist and kept.

WMA, WAV and AIFF are read but not written, so they are counted and skipped
rather than silently ignored.

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
api/               the OpenAPI contract, embedded into the binary
cmd/tagmgr/        command line: serve, scan, find, info, art, strip, browse
internal/tags/     format parsers and writers; no third-party tag library
internal/catalog/  in-memory library, binary snapshot, search index
internal/scan/     parallel directory walk and tag extraction
internal/library/  the service layer: owns the catalogue, all operations
internal/api/      HTTP handlers over the service
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
- `strip` and `restore` cover MP3, FLAC, MP4, Ogg Vorbis and Opus. WMA, WAV
  and AIFF are read but not written, so they are reported and skipped.
- The catalogue is not watched; run `tagmgr scan` after adding music.
- One catalogue at a time; use `-catalog` to keep several.

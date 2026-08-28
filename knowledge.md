# tagmgr — project knowledge

Written for whoever (or whatever) picks this up next. It covers what the
program is, how it is split, why the awkward decisions were made that way, and
the traps that have already been fallen into so they are not fallen into again.

The README is the user-facing document. This one is for someone changing the
code.

---

## 1. What it is

A music metadata manager for a large library — the target is around 100,000
files on a NAS. It catalogues them, searches them, and edits the tags in the
files themselves.

Written in Go. It compiles to one static binary with no runtime dependencies
and cross-compiles to the NAS with an environment variable. The work is
overwhelmingly file IO rather than computation, so goroutine-per-file
concurrency is the whole performance story; a faster language would not help.

**Current state: complete and working.** Server, HTTP API with an OpenAPI
contract, terminal browser, and command line. 102 tests, all clean under
`-race`.

---

## 2. Running it

```sh
make build          # dist/tagmgr for this machine
make nas            # dist/tagmgr-linux-{amd64,arm64}, static, no cgo
make test           # go test ./...
make bench          # search and catalogue benchmarks
```

The server owns everything; every other command is a client.

```sh
tagmgr serve                                  # loopback, no token
tagmgr serve -listen 0.0.0.0:8467 -token XXX  # reachable on the network
tagmgr scan /volume1/music                    # asks the server to scan
tagmgr                                        # the browser
tagmgr find artist:elvis
tagmgr help <command>                         # every command has real usage
```

Clients take `-server` / `TAGMGR_SERVER` and `-token` / `TAGMGR_TOKEN`.

Tests need `ffmpeg` on the path; they skip without it rather than failing. Real
encoder output is used deliberately — hand-built fixtures would only prove the
code agrees with itself.

### Releasing

```sh
make release        # builds and copies to /Volumes/Media/tagmgr
```

The NAS is **x86-64**, so `tagmgr-linux-amd64` is the build that ships. The
share is mounted on the Mac over SMB, so the binary is copied rather than
scp'd; `make release` chmods it and then checks the execute bit actually
stuck, because SMB mounts do not always honour it and a binary that arrives
without it fails on the NAS as "permission denied", which looks like something
worse than it is.

`make release` writes a temporary name and renames over the target, because
overwriting a binary that is currently running fails outright. Renaming swaps
the directory entry, so a running server keeps the file it started from and
the next start picks up the new one — deploying does not require stopping the
server, but restarting it does apply the new build.

Paths differ between the two machines: the Mac sees `/Volumes/Media/music`,
the NAS sees `/volume1/Media/music`. Scans run on the server, so the roots
given to `tagmgr scan` must be the **NAS's** paths.

### Where the catalogue lives

`$TAGMGR_CATALOG`, else `-catalog`, else the user cache directory
(`~/.cache/tagmgr/catalog.db` on Linux). The server prints the resolved
absolute path at startup.

With neither `HOME` nor `XDG_CACHE_HOME` — the normal case under systemd — the
server **refuses to start** and asks for `-catalog`. It used to fall back to a
relative `tagmgr-catalog.db`, which put the catalogue in whatever the working
directory happened to be, or failed to write and quietly rescanned on every
restart.

---

## 3. The split, and why it is this way

**The server is the only process that opens the catalogue or a music file.**
Everything else talks to it over HTTP. This was a deliberate choice by the
project owner, taken over two alternatives (keeping the terminal in-process, or
supporting both). The reasoning recorded at the time:

- one code path, so a bug in batch editing is one bug rather than two;
- several interfaces stay consistent, rather than each holding its own copy of
  the catalogue and racing to write it back;
- the terminal is the most demanding client imaginable — search on every
  keystroke, autocomplete, bulk edit, artwork — so an API that satisfies it
  will satisfy a phone.

The accepted costs: `tagmgr` needs a running server, and remote use needs
debouncing to stay responsive. There is deliberately **no auto-spawn**.

```
                    ┌──────────────────┐
   terminal ───────▶│                  │
   command line ───▶│   tagmgr serve   │──▶ catalogue snapshot
   browser/phone ──▶│  (internal/api)  │──▶ music files
                    └──────────────────┘
                             │
                    internal/library
                    (owns the catalogue)
```

---

## 4. Package map

| Path | Lines | Responsibility |
| --- | --- | --- |
| `api/` | 42 | `openapi.yaml` (966 lines) plus the Go embed. **The contract.** |
| `internal/tags/` | 7,536 | Format parsers and writers. No third-party tag library. |
| `internal/catalog/` | 1,410 | In-memory library, binary snapshot, search index, query language. |
| `internal/scan/` | 694 | Parallel directory walk and tag extraction. |
| `internal/library/` | 3,136 | **The service.** Owns the catalogue, all operations, jobs, events. |
| `internal/api/` | 1,492 | HTTP handlers over the service, SSE, docs page. |
| `internal/client/` | 889 | Go client for the API. |
| `internal/ui/` | 4,296 | The terminal browser, now a client. |
| `internal/artclip/` | 144 | The server-side artwork clipboard. |
| `cmd/tagmgr/` | 1,431 | `serve` plus the client commands. |
| `tools/genlib/` | 173 | Synthetic library generator, for benchmarking. |
| `webapp/` | ~900 | Browser front end. A sample: vanilla ES modules, no build. |
| `tools/tuidrive/` | — | Python: drives the terminal in a pty. Not shipped. |

Direct dependencies are only `bubbletea`, `lipgloss`, `go-runewidth` and
`yaml.v3`. Everything else — tag parsing, the search index, the HTTP layer,
the API client — is written here.

---

## 5. Decisions that will look strange without the reasoning

### Tag parsing is hand-rolled

`internal/tags/` implements ID3v2.2/2.3/2.4, ID3v1, FLAC, MP4/iTunes atoms,
Ogg Vorbis, Opus, ASF/WMA, RIFF/WAV and AIFF from scratch. Two reasons: reads
touch **only** the bytes the metadata occupies, so a 40 MB FLAC and a 3 MB MP3
cost the same to catalogue; and no Go tag library writes all these formats.

### Writes land inside existing padding where they can

ID3v2, FLAC and MP4 all reserve slack for this. A tag edit that fits rewrites
only the head of the file and the audio never moves — verified on real files as
byte-identical sizes. Only when a tag must grow is the file rebuilt beside the
original and renamed over it, so an interrupted write cannot damage a track.

**Artwork is the exception.** A cover is far larger than any format's padding,
so embedding one always rewrites the file. This is why artwork operations skip
tracks whose art already matches — otherwise a repeated run churns the library.

### Edits are expressed as field-level `Edit` values, never as whole metadata

Rewriting every field would silently normalise tags nobody asked to change — a
genre stored as `(17)` would become `Rock`. Across 100,000 files those
unrequested edits are impossible to review. See `internal/tags/edit.go`.

### The search index is a linear scan, not an inverted index

`internal/catalog/index.go` holds one folded, NUL-separated string per track
containing every searchable field. A search is `strings.Contains` over
contiguous memory. At this size it beats an inverted index once query parsing
and posting-list merges are paid for, **and** it supports mid-word matching,
which a token index cannot.

### The strip keep list is canonical, not per-format

15 canonical tags (`internal/tags/tagkind.go`), each mapping to the native keys
of every container. An album artist is kept whether the file spells it `TPE2`,
`ALBUMARTIST` or `aART`. Expressing the list four times would mean getting it
wrong once.

Resolution reads **descriptions**, not just keys. ID3's `TXXX`/`COMM` and MP4's
`----` freeform atoms are where iTunes hides gapless data among comments and
where MusicBrainz and ReplayGain live at all. `-also gapless` keeps `iTunSMPB`
while ordinary comments still go.

### Track identity is a hash of the path

`library.TrackID` — FNV-1a 64-bit hex. Slice indices shift on every rescan, so
they cannot be an API identity. Deriving from the path means any client can
compute one without asking, and moving a file correctly changes its identity.

### Selectors carry a query, not a list of ids

`library.Selector` — setting a field on 2,400 tracks must not mean a phone
uploading 2,400 identifiers. `expectCount` is a safety rail: the client states
how many matches it showed someone, and the server refuses if the selection has
moved since. An empty selector matches nothing; `all` must be set explicitly.

### Only one scan runs at a time

`POST /v1/scans` while one is running returns `409` with `code: scan_running`
and the running job's id — it does **not** return the running job as if it had
started a new one. Two scans would each walk the tree, each build a whole
catalogue, and whichever finished last would silently win; and returning the
running job would quietly answer a `full: true` request with an incremental
scan already in progress.

`GET /v1/scans` reports whether one is running, the last finished one, and the
catalogue's own `scannedAt`. The check and the start are under one mutex
(`Service.scanMu`), or two simultaneous requests would both see nothing
running.

### Everything long-running returns a job

Even when it finishes at once, so a client has one shape to handle rather than
guessing which calls block. Progress streams over SSE.

### The browser stages edits despite a write-through API

The API writes through on `PATCH`. The terminal deliberately does not: it holds
edits until `^s`, because that is where undo lives and where a batch of related
corrections gets assembled. Each save carries the version the track was read
at, so a conflict is reported and **kept pending**, never silently dropped.

Artwork does not stage — see above.

### Handlers are hand-written, not generated

OpenAPI 3.1 is what the intended clients want, but the Go generators still
trail it. `internal/api/conformance_test.go` walks the YAML and asserts every
operation has a route and every route has an operation. **It has been verified
to fail in both directions** — a test that cannot fail is worthless.

---

## 6. The API

Contract: `api/openapi.yaml`, embedded and served at `/openapi.yaml`,
`/openapi.json` and browsable at `/docs` (self-contained, no CDN, because a NAS
may have no outbound access). 25 operations.

Reading: `GET /v1/tracks` (q, sort, limit, offset), `/tracks/{id}`, `/albums`,
`/values/{field}`, `/stats`, `/tracks/{id}/artwork`.
Writing: `PATCH /v1/tracks/{id}`, `PUT`/`DELETE` artwork, the clipboard.
Batch: `/tracks/batch`, `/artwork/batch`, `/strip`, `/restore`, `/scans`.
Jobs: `/jobs`, `/jobs/{id}`, `/jobs/{id}/events`, `/events`.

**Optimistic concurrency.** Every track carries a `version` covering its state
on disk. Send it as `If-Match`; a mismatch is `409`. This is what makes editing
from a phone and a terminal at the same time safe.

**Auth.** Loopback needs no token. Any other bind requires one — auto-generated
and persisted beside the catalogue if not supplied.

**CORS is only sent when a token is set.** This is deliberate and load-bearing:
a loopback server with permissive headers could be driven by any web page the
user visits, and this API rewrites music files.

### `/albums` filters tracks, then groups

`?q=cat` returns albums containing *tracks* that match, so an album can come
back whose own title has nothing to do with the term — a composer called "Jamie
Catto" is enough. This is the useful semantic (find the album with that one song
on it) but it surprises people. Scope the term with `album:` to search titles.

Grouping happens over every matching track on each request: about 39 ms for an
unfiltered `/albums` on 100,000 tracks, versus 2–6 ms for `/tracks`. Filtered
queries are fast. If an album-first client pages through the unfiltered list,
memoising the grouping per catalogue generation would be the fix.

### Known limitation for browser-only clients

`<img src>` and `EventSource` **cannot send an `Authorization` header**, so both
return 401 against a tokened server. A client-side app must use `fetch()` →
`blob()` → `createObjectURL` for covers, and `fetch()` with a `ReadableStream`
reader instead of `EventSource`. Both work in current browsers.

If that is unacceptable, the options discussed were a `?token=` query parameter
on those two endpoints (leaks into logs and history) or cookie auth set by a
small login endpoint (works natively with both). **Neither is implemented.**

---

## 7. Format support

| Format | Read | Write |
| --- | --- | --- |
| MP3 (ID3v2.2/2.3/2.4, ID3v1) | yes | yes |
| FLAC | yes | yes |
| MP4 / M4A / M4B | yes | yes |
| Ogg Vorbis | yes | yes |
| Opus | yes | yes |
| WMA (ASF) | yes | **no** |
| WAV (RIFF INFO + ID3 chunk) | yes | **no** |
| AIFF | yes | **no** |

Unwritable formats are counted and reported, never silently skipped. The editor
warns before you type rather than failing at save time.

Duration and bitrate come from the stream header, including Xing/VBRI frame
counts for VBR MP3s. Verified to match `afinfo` and `ffprobe` exactly.

---

## 8. Measured performance

On 100,000 synthetic MP3s (`tools/genlib`), M-series Mac, files in page cache:

| | |
| --- | --- |
| Full scan | 2.6 s (~38,600 files/sec) |
| Incremental rescan, nothing changed | 0.30 s |
| Catalogue load (4.6 MiB snapshot) | 13 ms |
| Search index build | 49 ms, once at startup |
| Search | 0.9–3.6 ms |
| Snapshot encode / decode | 22 ms / 10 ms |

On a NAS the scan will be bound by disk and network, but the per-file work is
the same. The snapshot is small because every repeated string is interned once.

---

## 9. Testing strategy — what each suite actually guards

- **`internal/tags`** — round-trips real ffmpeg output through the writers and
  then checks **the audio still decodes**. A tag writer that corrupts the
  stream can still produce perfectly readable tags, so this is the assertion
  that matters. Covers all five writable formats, unicode, tag creation from
  scratch, growth, shrink, and ID3v2.2 upgrade.
- **`internal/ui`** — asserts **every rendered line is exactly the terminal
  width** across 11 window sizes and 10 modes. That invariant is what keeps the
  box-drawing junctions aligned; when it breaks the layout visibly tears. Runs
  against a real server.
- **`internal/api`** — conformance (routes vs schema, both directions) plus
  integration over a real service and real files.
- **`internal/library`** — concurrency under `-race`, which none of this code
  had to be clean under before the server existed.
- **`tools/tuidrive/drive.py`** — drives the terminal in a pty and renders the
  screen through a terminal emulator, so layout can be eyeballed and diffed.

Everything runs under `go test -race ./...`.

---

## 10. Bugs already found and fixed — do not reintroduce

Recorded because several were invisible to the obvious test:

1. **ID3v2.2 frames dropped on write.** iTunes-era MP3s use 3-character frame
   ids; the serialiser skipped them, so editing one field destroyed artist,
   title, album and artwork. Found on the owner's real files, not in tests.
2. **`TXXX:TCMP` stripped despite `compilation` being kept.** ffmpeg writes the
   compilation flag as a user-defined text frame; description resolution now
   also tries ID3 frame names.
3. **`TXXX:comment` never read.** ffmpeg writes MP3 comments there; the reader
   ignored it entirely.
4. **MP4 atom names corrupted through JSON.** Apple's names start with a raw
   `0xA9` byte, which is not valid UTF-8, and Go's encoder substitutes rather
   than failing — so a strip backup restored `cprt` but silently dropped `©cmt`
   and `©too`. Names are held in printable form throughout now.
5. **`stripANSI` only understood colour codes.** An inline image is an OSC/APC
   sequence carrying base64, and base64 contains every letter, so a scan
   stopping at the first `m` cut the sequence in half and mismeasured the line.
6. **Flags after a positional were silently dropped.** Go's flag package stops
   at the first non-flag argument, so `tagmgr strip artist:elvis -apply` did
   nothing. `reorderArgs` in `cmd/tagmgr/main.go` fixes it for every command.
7. **`Jobs.Start` returned the live job**, which a handler then serialised while
   the worker wrote progress into it. A real data race.
8. **Strip left the catalogue holding removed values**, so a search matched
   tags no longer on disk. Unlike a field edit, a strip cannot predict what it
   removed, so affected tracks are re-read.
9. **`Service.Close` did not wait** for the save loop or running jobs, so a
   write could land after shutdown.
10. **iTunes internals shown as the comment.** The reader took the first `COMM`
    frame whatever its description said, so `iTunSMPB` gapless data appeared as
    the comment on 12 of 35 tracks in the owner's real library. `COMM` must be
    resolved by description — `tagForID3Frame` already did this for stripping.

---

## 11. Not done

- **TLS.** The token crosses the network in plaintext. Fine on a trusted LAN;
  put it behind Tailscale or a TLS proxy for anything wider.
- **Service units.** No systemd unit or launchd plist yet, so the server does
  not come up with the machine. This was planned and not written.
- **Writing WMA, WAV, AIFF.** WAV and AIFF would be straightforward (both take
  an ID3 chunk); ASF is a larger job.
- **Cover art normalising.** No resize or re-encode. Measured on a real
  library, **85% of embedded artwork is duplicate bytes** — the same cover once
  per track — so dedup or resizing is where the space is, not tag stripping.
- **A TypeScript client.** Explicitly dropped. `webapp/` is a working browser
  client written directly against the schema instead.
- **`internal/artclip` and `cmd/tagmgr` have no tests of their own.** They are
  covered indirectly through the API and client suites.

---

## 12. Traps for the next person

- **`ffmpeg` writes its own `TSSE` frame even with `-map_metadata -1`.** A
  "bare" fixture is not bare unless you strip the ID3 tag yourself.
- **ffmpeg exposes Ogg/Opus comments as *stream* tags, not format tags.**
  `ffprobe -show_entries format_tags` on an Ogg shows nothing even when the
  tags are fine. Use `stream_tags`.
- **The shell here is zsh**, which does *not* word-split unquoted parameters.
  `for spec in "a b"; do set -- $spec` behaves differently than in bash.
- **Bubble Tea coalesces printable bytes** arriving in one read into a single
  `KeyRunes` message. A test that writes a whole string at once does not
  exercise the path a person typing does.
- **`t.TempDir` cleanup races background writers.** If a service writes after
  its `Close` returns, the temp directory refuses to delete. That is a real
  shutdown bug, not a flaky test.
- **The view must never have side effects.** It runs every frame; a lookup that
  queues work fires once per repaint rather than once per track. Art loading
  and page fetching are driven from `Update`, never from `View`.
- **Styles set colour only.** Widths are computed in cells by hand; a style
  that changed a string's width would break every vertical rule on screen.

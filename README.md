# YAMO

![YAMO — the green music organiser](yamo.jpg)

**An HTTP API for a large music library.** It catalogues a library fast,
searches it instantly, and edits the tags in the files themselves — including
across hundreds of thousands of tracks at once. Every one of those operations
is an endpoint, described by an OpenAPI 3.1 schema the server serves itself.

The API is the program. The terminal browser and the `scan`/`find`/`art`/
`strip` commands ship in the same binary and are worth having, but they are
**samples**: each one is a client of the API below, written against the same
endpoints anything else can call, and given no access the API does not offer.
A phone, a web page, a shell script and a cron job are all as legitimate a
client as the terminal is, and none of them is working around a subset.

Built for a NAS: one static binary, no runtime dependencies, no database
server, no GUI required.

```sh
yamo serve                                   # loopback, no token needed
curl -O http://127.0.0.1:8467/openapi.yaml   # the contract
open http://127.0.0.1:8467/docs              # browse it
```

The schema is served by the running binary, so it is always the contract that
build actually implements — a test walks the YAML and fails if any operation
lacks a route or any route is missing from the schema. `/docs` is
self-contained and needs no outbound network access, because a NAS may not
have any.

---

## The API

45 operations. Everything below is reachable with `curl`, and everything the
terminal browser can do is one of these calls.

### The shape of it

The library runs to six figures and the work is gradual — find a few wrong
things, fix them, carry on. Four things follow from that, and they are the
whole design.

**Everything is paged.** `GET /v1/tracks` returns a window and a total. There
is no endpoint that returns the whole library. `total` counts matches before
paging, and `limit` and `offset` come back as applied — a `limit` above the
maximum is capped rather than rejected, so read the response rather than
assuming the request was honoured.

**Operations select by query, not by identifier.** Setting the artist on every
Elvis track should not mean uploading two thousand ids, so a `Selector`
carries the same query language the search bar uses.

```sh
# the case this exists for: fix a misspelled artist across a whole search
curl -X POST localhost:8467/v1/tracks/batch -H 'Content-Type: application/json' -d '{
  "selector": {"query": "artist:presly", "expectCount": 42},
  "set": {"artist": "Elvis Presley"}
}'
```

`expectCount` is a safety rail: the client states how many matches it showed
someone, and the server refuses if the selection has moved since. A selector
that names nothing is rejected, and `all` must be set explicitly — so
"everything" can never be reached by accident.

**Anything that can touch more than one file returns a job**, even when it
finishes immediately, so there is one shape to handle rather than a guess about
which calls block. `GET /v1/jobs/{id}` polls it; `GET /v1/jobs/{id}/events`
streams its progress.

**Anything that writes across a selection can be undone.** Batch edits, splits
and renames record what they overwrite without being asked; strips and artwork
pastes do it on request. See [Undo](#undo).

### Finding things

The query language is the same in every endpoint that takes a `q` or a
`Selector`, in the search bar, and on the command line.

| Query | Matches |
| --- | --- |
| `elvis` | any text field contains "elvis" |
| `artist:elvis` | just the artist field |
| `artist:"elvis presley"` | quoted values may contain spaces |
| `artist:^elvis` | the field begins with it |
| `artist:presley$` | the field ends with it |
| `artist:"^elvis presley$"` | the whole field, exactly |
| `artist:~presly` | fuzzy: near misses count, and are scored |
| `year:1977` | exact year |
| `year:>1980`, `year:<=1969` | comparisons |
| `year:1970-1979` | an inclusive range |
| `-genre:christmas` | exclude matches |
| `album:` | tracks where the field is empty |
| `compilation:1` | the Various Artists flag is set (`comp:`, `va:`) |
| `albumartistsort:various` | the sort fields, by name only (`aas:`, `as:`, …) |
| `artist:elvis year:>1960` | terms are ANDed |

Matching is case- and accent-insensitive in both directions: `bjork` finds
Björk, and `Beyoncé` finds Beyoncé. Unqualified terms search the display tag
fields but not the file path or the sort fields; use `path:`, `artistsort:` and
the rest for those. Searching for `presley` should not also return every track
that merely files itself under "Presley, Elvis".

#### Fuzzy terms

`~` loosens one term. It matches the value literally, or spread through the
field in order — `~elvpres` finds Elvis Presley — or within a bounded number of
typos: an insertion, a deletion, a substitution, or two letters swapped, so
both `~presly` and `~prelsey` land. Each result carries a score between 0 and
1, and a query containing a `~` comes back ranked by it, best first, unless a
`sort` of your own says otherwise.

It is opt-in rather than automatic, and only the terms that carry it are
loosened:

```
artist:~presly year:>1960 -genre:live
```

is strict about the year and the genre and forgiving only about the artist —
which is what lets a query still be trustworthy as the definition of a set.
`~` and the anchors combine, in that order: `artist:~^presly` is "starts with
something close to this".

#### Four ways in

A query returns tracks, but a track is rarely the unit of work. Four endpoints
group them, and which one is useful depends on how good the tags already are.

```sh
curl 'localhost:8467/v1/albums?q=elvis&sort=-year'
curl 'localhost:8467/v1/artists?sort=-tracks'
curl 'localhost:8467/v1/folders?path=/volume1/music/Elvis%20Presley'
curl 'localhost:8467/v1/duplicates?by=artist,title'
```

**`/v1/albums` and `/v1/artists`** group the tracks a query matched — the query
filters tracks, and the matching tracks are then grouped. That is deliberate
and worth reading twice: `?q=cat` returns an album whose composer is "Jamie
Catto", because a bare term matches every text field of every track. It is how
you find the album with that one song on it. Scope the term (`?q=album:cat`) to
search titles alone. Both take a `sort` over the group's own values — an
album's `year` is the earliest of its tracks', its `duration` their total — and
both carry a `sampleTrackId`, so a grid can fetch a cover without first listing
an album's tracks to find one.

**`/v1/folders`** is the one for a library whose tags are not good enough yet.
An album grid built from broken tags shows "Unknown Album" four thousand times
and throws away the one thing still correct: that these forty files sit in a
directory together, and whoever ripped them meant them as a record. This lists
one level of the tree at a time, the way a file browser does. `tracks` counts
what is directly in a folder and `descendants` what is below it too, so one
album and one artist says the folder is a record and several says it is a
shelf — or that the tags are wrong, which is why you are looking.

**`/v1/duplicates`** finds the same recording more than once. A library merged
from a few sources has them, and they are invisible from a search: the copies
sort next to each other, look identical, and nothing counts them. What counts
as the same recording is yours to say, because it depends on what went wrong —
two rips of one album duplicate on artist and title, a compilation that also
appears as its own album duplicates on those but not on album, a file copied
twice duplicates on `size` too — so `by` is a field list rather than a rule.
Nothing is deleted: one copy is usually the better rip, so this offers the
grouping and the evidence, and `wasted` says what keeping one of each would
free.

Every group from every one of these carries a `query` that reselects exactly
it, so going from a result to an operation on it is one more call rather than a
list of ids.

`GET /v1/values/{field}` is the exception to paging: a capped list of the
most-used values of one field, for autocomplete, most-used first.
`GET /v1/stats` reports the totals and — the useful part for maintenance — how
many tracks are missing each field, which is where the work is.

### Editing

A `PATCH` is sparse: fields absent from the body are left alone, and fields
present with `null` are cleared. Setting a field to the value it already has
does not rewrite the file, so a repeated request is free rather than churning
the library.

```sh
curl -X PATCH localhost:8467/v1/tracks/$ID \
  -H 'If-Match: 5518755346f5ca46' -H 'Content-Type: application/json' \
  -d '{"artist": "Elvis Presley", "comment": null}'
```

Edits write through: a `PATCH` writes the tag to the file and returns. There is
no pending state to commit, and therefore none for a background scan to lose.

Edits are applied per field, so saving a track writes only the fields that
actually changed and leaves every other tag in the file exactly as it was.
Where a format reserves padding for this (ID3v2, FLAC, MP4 `free` atoms) the
tag is rewritten in place without moving the audio, so correcting a title in a
40 MB FLAC writes a few kilobytes rather than 40 MB. When the tag has to grow
beyond its padding the file is rebuilt alongside the original and renamed over
it, so an interrupted write cannot leave a damaged track.

#### Editing safely from more than one place

Every track carries a `version`. Send it back as `If-Match` and a mismatch
returns `409`, which is what stops an edit made on a phone silently
overwriting one made in the terminal a moment earlier. It is honoured on
`PATCH`, on `DELETE`, on a rename, and on the artwork writes — a cover is a
write to the file like any other.

The version has to be a real one for that to mean anything, and file size and
modification time alone are not. Every tag format reserves padding, so
replacing a value with one of a different length — or a 7 KB cover with a
1.5 KB one — routinely leaves the file exactly as long as it was, and the
modification time is recorded in whole seconds. Two writes inside one second
that leave the same length are indistinguishable from those two facts. So the
version also carries a count of the writes this server has made to that path,
which covers the case the guarantee is about: both clients are talking to this
server. A file changed by another program on the machine is still only visible
through size and time, and a scan is what reconciles that.

The same version is the `ETag`, so a conditional `GET` works on a track and on
its cover:

```sh
curl -H 'If-None-Match: "5518755346f5ca46"' localhost:8467/v1/tracks/$ID  # 304
```

#### What is actually in the file

`GET /v1/tracks/{id}/tags` lists a file's raw metadata rather than the
catalogue's reading of it: the native keys, what each one means, its size, and
whether a strip would keep it. It is the pre-flight for `POST /v1/strip` — the
question there is not what the track says but what is *in* it, which is where
the iTunes purchase account and the 300 KB of ratings a ripper left behind
turn up.

### Pulling the artist out of a title

A compilation says Various Artists in the artist tag, because that is what the
album is, and then has nowhere to put the performer except the title:
`Michael Jackson - Beat It`. Every player shows the artist twice over and none
of them can group by it.

`POST /v1/tracks/split` takes a selector and a template describing the shape
the title is in — `$artist - $title` — and writes each half into the field it
names. There is no rule for finding an artist in a title, which is why it is
described rather than guessed: `" - "` separates the two on one compilation and
`" / "` on the next, and plenty of titles contain a dash of their own.

The separator is matched literally and every capture but the last stops at the
first one that fits, so `Jay-Z - 99 Problems` splits after the surname and
`Faithless - Insomnia - Monster Mix` keeps its remix in the title. A title the
template does not fit is counted as `unmatched` and left alone.

Run it with `dryRun` first. The result carries worked examples and the count
that did not fit, which together say whether the template is right. It journals
by default, so a run that turns out wrong is one `POST /v1/jobs/{id}/undo`
away.

### Renaming files from their tags

`POST /v1/tracks/rename` is the other half of the split. A split reads a
filename's worth of information out of a title; this writes the tags back out
into the name, and it is what a library looks like once the tags are right:
`01 Blue Suede Shoes.mp3` under `Elvis Presley/The Sun Sessions`, rather than
`track01.mp3` under `unsorted`.

```sh
curl -X POST localhost:8467/v1/tracks/rename -H 'Content-Type: application/json' -d '{
  "selector": {"all": true},
  "template": "$albumartist/$album/$track $title",
  "dryRun": true
}'
```

The template is the destination path, resolved against the **library root** the
track sits under rather than the track's own directory — so a template that
files by artist and album can rescue a track from the wrong folder rather than
nesting the right one inside it. Missing directories are created. The extension
is never part of it: a rename may not change a file's container, so the one the
file has is appended to whatever the template produces.

Three things happen automatically, because a template that had to express them
would be unreadable:

- **`$track` and `$disc` are padded to two digits.** Without that a directory
  listing puts track 10 before track 2, and putting the number first stops
  meaning anything.
- **A separator inside a value is replaced.** An artist of "AC/DC" under
  `$artist/$album` would otherwise file the album under "DC" inside a folder
  called "AC".
- **The characters Windows refuses are replaced too.** A library on a NAS is
  read over SMB as often as not, and a name it cannot represent is a file that
  does not appear.

`$albumartist` falls back to the artist, because that is the key `/v1/albums`,
`/v1/artists` and `/v1/folders` all group on — filing by album artist is that
grouping written to disk, and in a library where most files never had one, a
stricter reading would call most of it incomplete.

A track missing a field the template names is counted in `incomplete` and left
alone: that is the number that says whether the tags are ready. Two tracks
wanting the same destination are counted in `collisions`, whose answer is a
better template — usually one carrying `$disc` or `$track` — rather than a
retry.

**Run it with `dryRun` first.** `samples` shows what it would do to real files,
which is the only way to tell a good template from one that moves everything
somewhere wrong. It journals by default, so a bad run can be undone — but a
rename undone is a second pass over the library, which is why the dry run
matters more here than anywhere else.

### Undo

A strip has always been recoverable, because tags removed from a file cannot be
got back from anywhere else. Everything else that wrote across a selection had
the same problem and no answer: a batch edit that set the artist on two
thousand files wrote them and forgot what they said, so a mistake made from a
phone was unrecoverable from anywhere.

A **journal** is the answer, and it is one mechanism in every case: before a
file is written, record what it held. What is recorded differs — removed frames
for a strip, previous values for an edit, the old cover for an artwork paste,
the old path for a rename — but the file, the addressing and the restore are
one thing.

```sh
JOB=$(curl -sX POST localhost:8467/v1/tracks/batch -H 'Content-Type: application/json' \
  -d '{"selector":{"all":true},"set":{"genre":"Rockabilly"}}' | jq -r .id)

curl -X POST localhost:8467/v1/jobs/$JOB/undo     # put it all back
```

| Operation | Journals |
| --- | --- |
| `POST /v1/tracks/batch` | by default |
| `POST /v1/tracks/split` | by default |
| `POST /v1/tracks/rename` | by default |
| `POST /v1/strip` | on `"backup": true` |
| `POST /v1/artwork/batch` | on `"backup": true` |

The two defaults differ because the costs do. An edit's journal is a line of
text per changed file, and a batch edit is the ordinary way to work here, so
the recovery has to be there without being asked for. An artwork journal holds
the images it replaced, so undoing a paste across ten thousand tracks is worth
the space and doing it every time is not.

A job that journalled carries a `backupId`, and its presence is what says the
job can be undone. What was already done is what gets undone: a job cancelled
halfway is still undoable for the files it reached. `GET /v1/backups` lists the
journals, `GET /v1/backups/{id}` describes what one holds without applying it,
and `DELETE /v1/backups/{id}` discards it — nothing expires them, because
expiring the record of a change nobody has noticed yet is the wrong default.

This is not a stack. Undoing an undo is possible, since the undo is a restore
rather than a journalled edit, which is also why redo is not offered. What it
is is the answer to "that was the wrong two thousand files".

### Cover art

Art is moved around with a **server-side clipboard**: copy one image, then
paste it onto as many tracks as you like. It lives on disk rather than in a
client, which is the point — a cover copied in the terminal can be pasted from
a phone and the other way round.

```sh
curl -X PUT --data-binary @cover.jpg localhost:8467/v1/clipboard/artwork
curl -X PUT localhost:8467/v1/clipboard/artwork/from-track/$ID

curl -X POST localhost:8467/v1/artwork/batch -H 'Content-Type: application/json' -d '{
  "selector": {"query": "album:\"sun sessions\""},
  "source": "clipboard"
}'
```

`source` is `clipboard`, `upload`, `folder` or `remove`. **`folder` is usually
the one you want**: it embeds the `cover.jpg` or `folder.jpg` sitting beside
each track, which is how a downloaded library normally stores art and the usual
reason none of it appears on a phone. Each directory is read once, not once per
track.

`POST /v1/artwork/export` goes the other way, writing the embedded covers back
out beside the music — for the media server poster, the file browser thumbnail
and everything else that reads a directory rather than a tag. One image per
directory, since an album's tracks carry the same cover, and the extension
follows the image rather than the request: a PNG asked for as `cover.jpg` is
written as `cover.png`.

`GET /v1/artwork/summary` reports what is there, grouped by image:

```
  tracks  image                           embedded  example album
      12  600×600 jpeg 125 KB              1.5 MiB  Sun Sessions
```

That grouping is the useful part. Measured on a real library, **85% of
embedded artwork is duplicate bytes** — the same cover repeated once per track.
It takes a full selector rather than only a query, because it is the report you
read before deciding what to change, and the thing you then change is named by
a selector.

`GET /v1/tracks/{id}/artwork?size=N` returns a scaled copy. An album grid is
what it is for: embedded covers run to 1500×1500 and half a megabyte, so forty
tiles is twenty megabytes to draw forty postage stamps.

Two things to know:

- **Embedding art rewrites the file.** A cover is far larger than the padding
  any format reserves, so unlike every other edit the audio has to move. Tracks
  whose art already matches are skipped, which makes a repeated run free.
- **Artwork writes straight through**, and is not held pending the way a
  client may hold field edits: several hundred covers kept in memory to write
  later would cost more than the library itself.

#### Finding covers on Discogs

When a library has no art to move around, `GET /v1/discogs/search` finds some.
It searches **masters** — a master is the album, one entry
covering every pressing and reissue, so a release search would offer the same
sleeve twenty times. `GET /v1/discogs/masters/{id}` returns every
picture on one, so a release with more than one reaches the back cover, the
inner sleeve and the disc.

No account is needed. Three facts about the public API shape the whole thing,
and they are worth knowing before changing any of it:

- **Search returns no images.** The `thumb` and `cover_image` fields come back
  as empty strings unless the request is authenticated, so each candidate has
  to be fetched by id to discover its cover. A search is one request plus one
  per candidate.
- **The rate limit is 25 a minute, per IP.** With the point above, one search
  spends nine of them. That is why candidates are capped, why masters are
  cached, and why `rateRemaining` — in the body and in a `RateLimit-Remaining`
  header — is reported rather than letting a client fail silently a minute
  later. `-discogs-token` raises it to 60 and puts covers in the search
  response itself, making a search cost one request.
- **The image host sends no CORS header.** A browser can display one of those
  URLs in an `<img>` but cannot read its bytes, so the download happens on the
  server. It only fetches from Discogs' own image hosts — the URL comes from
  the client, and without that allowlist the endpoint would fetch anything the
  server can reach.

The download lands on the same artwork clipboard as everything else, so
applying it to one track or to an album reuses the paste that already exists.
`-no-discogs` turns the lookup off, leaving the server making no outbound
requests at all.

`GET /v1/discogs/album` asks a cheaper question: the year and the genre of an
album, from the leading match. Only images are missing from an unauthenticated
search, so this costs one request rather than nine, and it reads the master for
a second only when the hit came back with neither field. It returns them rather than
writing them; applying them is an ordinary batch edit.

### Stripping

`POST /v1/strip` removes every tag that is not on a keep list, leaving a
uniform set of metadata across the library.

```sh
curl -X POST localhost:8467/v1/strip -H 'Content-Type: application/json' -d '{
  "selector": {"all": true},
  "dryRun": false,
  "backup": true
}'
```

It **defaults to a dry run**: an operation that permanently discards data
across a library should have to be asked for twice. The dry run reports exactly
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
| `composersort` | TSOC | COMPOSERSORT | soco |
| `artwork` | APIC | METADATA_BLOCK_PICTURE | covr |
| `gapless` | COMM:iTunSMPB | — | pgap, iTunSMPB |
| `soundcheck` | COMM:iTunNORM | — | iTunNORM |
| `itunes` | — | — | stik, apID, purd, cnID, atID, plID, … |

`compilation` is the flag that stops a Various Artists album fragmenting into
one album per track. The sort tags are what put "The Beatles" under B, and they
are read, written and searchable in their own right: on an iTunes compilation
`albumartistsort` is routinely the only tag that names Various Artists at all,
because iTunes writes the sort tag and leaves `albumartist` empty.
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
address. Drop it on its own with a `keep` list minus `itunes`, or keep the rest
and accept it.

The date is written to `TDRL` as well as the year frame. ID3 separates when a
recording was made from when it was released and MP4 does not, so an MP3
carrying only a year frame reports no release date at all while an M4A of the
same song reports one. Writing both makes the two formats agree.

Replace the list with `keep`, extend it with `also`. Names may be canonical
(`albumartist`) or native to any format (`TPE2`, `ALBUMARTIST`, `aART`) — you
should not have to translate a list you already have.
`capabilities.defaultKeepTags` is the starting point, and
`GET /v1/tracks/{id}/tags` shows what one file actually carries and which of it
the default list would keep.

#### Putting values where they belong

`normalize` additionally rewrites kept fields a file does not hold the way
this tool writes them: an ID3v2.2 frame, a genre stored as `(19)`, an MP4
`gnre` atom, a Vorbis `PERFORMER`, a year with no `TDRL` beside it, a date
carrying more than the year. The values do not change, only the form they are
kept in — which is what decides whether the next tool along finds them, and
whether editing one of them does anything.

That last case is the one worth knowing about. A purchased file often dates
itself `2011-08-29`, or with a full iTunes timestamp, and the year is parsed
out of it on the way in — so the track reads as 2011, setting the year to 2011
matches what is already there, and the edit is dropped as a no-op. Normalising
writes the bare year the rest of the library uses, which is also what makes the
field editable again. The month and day go with it.

#### What goes, and what that costs

Everything else: encoder signatures, comments, private blobs, URLs, ratings,
and external identifiers. Two consequences are worth knowing beforehand:

- **Comments are not one thing.** iTunes hides private data among them, so
  comments are resolved by description rather than by container key: the
  gapless data in `COMM:iTunSMPB` is kept while an ordinary comment beside it,
  under the same frame id, goes. Volume normalisation (`iTunNORM`) is dropped —
  ReplayGain has superseded it and it can be recomputed. Keep it with
  `"also": ["soundcheck"]`.
- **MusicBrainz and AcoustID identifiers are not regenerable** from the audio.
  `"also": ["musicbrainz", "acoustid"]` keeps them.

Because the metadata only ever shrinks, the rewrite happens inside the padding
each format reserves: verified on real files, tag and file sizes come out
byte-identical afterwards and no audio is moved. Stripping a whole library is
bounded by one small write per file.

ID3v2.2 tags are rewritten as v2.3, but only for files that actually lose
something. The frames are translated first, so a v2.2 `TP1` is recognised as an
artist and kept.

WMA, WAV and AIFF are read but not written, so they are counted and skipped
rather than silently ignored.

### Keeping several clients in step

`GET /v1/events` is a `text/event-stream` of every change. An edit made on a
phone pushes `tracks.changed`, and the terminal drops those rows from its cache
instead of polling for them.

```sh
curl -N localhost:8467/v1/events
```

Every event carries an `id:` line of `<epoch>:<seq>`, which a browser's
`EventSource` sends back as `Last-Event-ID` when it reconnects by itself.
Anything else can send that header by hand, or pass `?lastEventId=`. A resumed
stream replays what was missed before anything new; when it cannot — the events
have aged out of the window, or the server has restarted — the first event is a
`stream.gap` instead, and a client that gets one should refetch rather than
trust its cache. The epoch is why the id is not a bare number: a sequence
resets when the process does, so event 12 from this run and event 12 from the
last are different events.

A subscriber that falls behind *while connected* still loses events rather than
blocking the write that produced one. Resuming covers a dropped connection, not
a client that cannot keep up.

### Access and discovery

Loopback by default, where no token is needed. Binding anywhere else requires
one — generated on first run, printed once, kept beside the catalogue.

`GET /v1/capabilities` is the one operation served **without** a token, and
`authRequired` is why: a client needs to know whether credentials are required
before it has any to present. Nothing in it describes the library — no roots,
no counts, nothing that says what music is on the machine — which is what makes
that safe.

```sh
curl -s localhost:8467/v1/capabilities | jq '{version, authRequired, features, limits}'
```

It answers three things nothing else can. **Which formats this build writes** —
`Track.writable` says a particular file cannot be written, but only once you
have found it, so warning before an edit rather than after it needs this.
**Whether the Discogs lookup is configured**, which otherwise takes a search
and a `503` to establish. **What `limit` is capped to**, which the paging rules
otherwise leave you to discover by reading a response back. It also carries the
field, sort and job-kind vocabularies, so a client builds its pickers from the
server rather than from a copy of this document that will fall behind.

`GET /v1/me` says whether the token you are holding works — a question a client
otherwise has to provoke a real failure to ask.

Cross-origin browser requests are only permitted when a token is set. A server
on loopback with permissive headers could be driven by any web page you
happened to visit, and this API rewrites music files.

### Errors

Every failure is a JSON body with a `code` a client can branch on, and the
codes are distinct where the right response differs. A rename onto an existing
name is `exists` rather than `conflict`, because the answer is "choose another
name" rather than "re-read and retry". A `count_mismatch` carries `expected`
and `actual`. A `scan_running` names the job already going.

| Status | When |
| --- | --- |
| `400` | The request was malformed, or named an unknown field |
| `401` | No bearer token, or the wrong one |
| `404` | No such track, job, backup or resource |
| `409` | `conflict`, `exists`, `count_mismatch` or `scan_running` |
| `413` | An uploaded cover above `limits.maxImageBytes` — refused, never truncated |
| `422` | `unwritable`: this build reads the format but cannot write it |
| `429` | The Discogs per-minute budget is spent; `Retry-After` says how long |
| `503` | The Discogs lookup is turned off on this server |

### Endpoint reference

The schema at `/openapi.yaml` is authoritative and carries the full description
of every parameter. This is the map.

| | |
| --- | --- |
| **Server** | |
| `GET /v1/capabilities` | What this build can do. No token required |
| `GET /v1/me` | Whether the token works |
| **Tracks** | |
| `GET /v1/tracks` | Search, sort and page |
| `GET /v1/tracks/{id}` | One track. `ETag`, `If-None-Match` |
| `PATCH /v1/tracks/{id}` | Edit fields. `If-Match` |
| `DELETE /v1/tracks/{id}` | Delete the file. `If-Match` |
| `GET /v1/tracks/{id}/tags` | The file's raw metadata |
| `GET /v1/tracks/{id}/audio` | The audio itself, ranges and all |
| `POST /v1/tracks/{id}/rename` | Move one file |
| **Browse** | |
| `GET /v1/albums` | Albums, sorted and paged |
| `GET /v1/artists` | Artists, sorted and paged |
| `GET /v1/folders` | One level of the directory tree |
| `GET /v1/duplicates` | The same recording more than once |
| `GET /v1/values/{field}` | Distinct values, for autocomplete |
| `GET /v1/stats` | Counts, totals, and what is missing |
| **Batch** | |
| `POST /v1/tracks/batch` | One set of changes across a selection |
| `POST /v1/tracks/split` | Pull the fields a title carries into their own tags |
| `POST /v1/tracks/rename` | Rename a selection after its tags |
| `POST /v1/strip` | Remove every tag not on a keep list |
| `GET /v1/backups` | The undo journals |
| `GET /v1/backups/{id}` | What one journal holds |
| `DELETE /v1/backups/{id}` | Discard a journal |
| `POST /v1/restore` | Put a journal back |
| **Artwork** | |
| `GET /v1/tracks/{id}/artwork` | The cover, optionally scaled with `?size=` |
| `PUT /v1/tracks/{id}/artwork` | Replace it. `If-Match` |
| `DELETE /v1/tracks/{id}/artwork` | Remove it. `If-Match` |
| `POST /v1/artwork/batch` | Set or clear art across a selection |
| `POST /v1/artwork/export` | Write embedded covers out as `cover.jpg` |
| `GET /v1/artwork/summary` | Group identical covers across a selection |
| `GET·PUT·DELETE /v1/clipboard/artwork` | The server-side clipboard |
| `PUT /v1/clipboard/artwork/from-track/{id}` | Copy a track's cover to it |
| `PUT /v1/clipboard/artwork/from-url` | Copy a Discogs cover to it |
| **Discogs** | |
| `GET /v1/discogs/search` | Find album covers |
| `GET /v1/discogs/masters/{id}` | Every image on a master |
| `GET /v1/discogs/album` | Look an album up for its year and genre |
| **Jobs and scanning** | |
| `GET /v1/jobs` | Filtered, paged |
| `GET /v1/jobs/{id}` | One job |
| `DELETE /v1/jobs/{id}` | Cancel it |
| `POST /v1/jobs/{id}/undo` | Reverse it |
| `GET /v1/jobs/{id}/events` | Stream its progress |
| `GET /v1/events` | Stream every change. Resumable |
| `GET /v1/scans` | Whether a scan is running |
| `POST /v1/scans` | Bring the catalogue up to date |

Outside `/v1`, and outside the schema: `GET /healthz`, `GET /openapi.yaml`,
`GET /openapi.json` and `GET /docs`, none of which require a token and none of
which say anything about the library. `POST /mcp` is outside it too, and does
require one — it is JSON-RPC rather than REST, and it is off unless the server
was started with `-mcp`. See [Connecting an assistant](#connecting-an-assistant-mcp).

## Running the server

`yamo serve` owns the catalogue and the music files; nothing else opens them.

```sh
yamo serve                                 # loopback, no token needed
yamo serve -listen 0.0.0.0:8467            # reachable on the network
yamo serve -listen unix:///tmp/yamo.sock
yamo serve -root /volume1/music -rescan-every 1h
```

### Keeping up with the files

Nothing watches the filesystem. Music added or edited by anything other than
this server — an album copied over SMB, tags changed in another program — is
invisible until a scan is asked for. `POST /v1/scans` asks for one; a scan
reuses every entry whose size and modification time are unchanged, so a refresh
costs a stat per file rather than a read.

`-rescan-every` puts that on a timer. It runs the same incremental scan, so an
unchanged library costs a stat per file; an hour is a sensible starting point,
and a minute is the shortest accepted. A tick arriving while a scan is still
running is skipped rather than queued, and the roots it scans are the
catalogue's own.

`GET /v1/stats` reports the interval and when the next one is due, so a client
can say how current its numbers are; `capabilities.features.rescan` says
whether the timer is on at all. Unset — the default — nothing is scanned unless
asked, which is the behaviour to assume unless the stats say otherwise.

Posting a second scan while one runs fails with `409` and `scan_running` rather
than returning the running job: two concurrent scans would each walk the tree,
each build a whole catalogue, and whichever finished last would silently win.
The running job's id is in the error.

Deleted files disappear from the catalogue on the next scan. Directories that
never hold music (`@eaDir`, `#recycle`, `lost+found`, dot-directories) are
skipped, as are AppleDouble `._` sidecars.

### Install

```sh
make nas                       # builds dist/yamo-linux-{amd64,arm64}
scp dist/yamo-linux-amd64 nas:/usr/local/bin/yamo
```

UGREEN NASync boxes are x86-64, so `yamo-linux-amd64` is the one you want.
Both binaries are static and depend on nothing on the target.

For local use: `make install` or `go install ./cmd/yamo`.

### Docker

The image is `ghcr.io/remy/yamo`, built by
[`docker.yml`](.github/workflows/docker.yml) for `linux/amd64` and
`linux/arm64` on every push to `main` (`:latest`) and every `vX.Y.Z` tag
(`:X.Y.Z`, `:X.Y`, `:X`). It runs `yamo serve`; the terminal and the rest of
the client commands are still in the binary if you `docker compose exec` in,
but the container's job is to be the API server.

```sh
cp .env.example .env      # fill in YAMO_TOKEN and MUSIC_DIR
docker compose up -d
docker compose logs -f    # first run: confirms the initial scan under -root
```

That's [`docker-compose.yml`](docker-compose.yml) as committed: it binds the
container's `/data` (the catalogue) and `/music` (the library, read-write —
the API edits tags in place) to the host, and passes `YAMO_ROOT=/music` so
the server scans on every start — see `YAMO_ROOT` in the table below for
what that costs on a restart where nothing changed.

The `user:` line is not decoration. The image is distroless `:nonroot`, so
its default is uid 65532 — an account that exists inside the container and
owns nothing on your host — and a bind-mounted `./data` owned by anyone else
gives that uid no write permission. The symptom is the server starting fine
and then failing on every save:

```
yamo: could not save the catalogue: open /data/.yamo-3876691773.tmp: permission denied
```

Set `PUID`/`PGID` in `.env` to the account that owns the bind-mounted
directories (`id -u` and `id -g`), which is also the account that has to own
the music files for tag edits to land. The same uid needs write access to
`/music`, not just `/data`.

A token is not optional here the way it is for `yamo serve` on a bare
machine: binding `0.0.0.0` — the only address reachable through Docker's
port mapping — trips the same "not loopback" check either way, so the
compose file refuses to start without `YAMO_TOKEN` set (`docker compose up`
fails fast with a clear error rather than the container looping on a
generated token nothing outside it can read). Generate one with
`openssl rand -hex 24`.

#### Environment variables

The flags most worth setting without typing them are the ones with an
environment variable fallback, because a container's configuration is
environment variables and volumes, not command-line flags typed by a
person. `YAMO_ROOT` is the one worth calling out for Docker specifically:
with no one around to run `yamo scan` by hand after `docker compose up`, it
scans on every start instead — a no-op cost (a stat per file, not a
re-read) once the library is caught up, which is what makes it safe to
leave set rather than something to remember to run once.

| Variable | Flag | Used by | Meaning |
| --- | --- | --- | --- |
| `YAMO_CATALOG` | `-catalog` | `serve` | Catalogue file path. Defaults to the user cache directory outside Docker; the image sets it to `/data/catalog.db`. |
| `YAMO_ROOT` | `-root` (repeatable) | `serve` | Comma-separated directories to scan on startup, in the background, without blocking the server from accepting requests. Unset means an empty new catalogue stays empty until something scans it. |
| `YAMO_TOKEN` | `-token` | `serve`, and every client command | On `serve`: the bearer token required once it's bound to anything but loopback. On a client (`scan`, `find`, `art`, `strip`, `info`, the browser): the token it sends back. |
| `YAMO_RESCAN_EVERY` | `-rescan-every` | `serve` | Optional. Rescans the catalogue's roots on this interval (`1h`, `30m`) — the same incremental scan, so a stat per file on an unchanged library. Unset, nothing is scanned unless asked: nothing watches the filesystem. A minute is the shortest accepted. |
| `YAMO_DISCOGS_TOKEN` | `-discogs-token` | `serve` | Optional. Raises the Discogs cover-lookup rate limit from 25 to 60 requests/minute. Unset still works, just slower. |
| `YAMO_SERVER` | `-server` | every client command | Server address to connect to. Defaults to `http://127.0.0.1:8467`, so `docker compose exec yamo /yamo find …` needs neither this nor `-token` — the default address is already the container's own loopback, and `YAMO_TOKEN` is already in its environment. Only needed to reach a server elsewhere. |
| `YAMO_NO_IMAGES` | — (no flag) | the terminal browser | Set to disable cover-art preview detection, for a terminal that mishandles the Kitty/iTerm2 image escape sequences rather than ignoring them. Not relevant to `serve` or the other client commands. |

## Sample clients

Everything from here down is a **client of the API above**. None of it is
privileged: the terminal browser opens no files and touches no catalogue, and
neither does `yamo find`. They exist to show the API being used, and because
they are genuinely the fastest way to work on a library over SSH.

```sh
yamo serve                   # the API server; everything else is a client
yamo scan /volume1/music     # build the catalogue
yamo                         # browse and edit, in the terminal
yamo find artist:elvis       # query from a script
yamo info                    # what is in the library
yamo help                    # usage; `yamo help scan` for one command
```

Start the server first, or from a systemd unit or launchd plist so it comes up
with the machine. Every command takes `-h`, and `yamo help <command>` prints
the same thing. Usage goes to stdout so it pipes into a pager; errors go to
stderr.

### The terminal browser

```
┌─ yamo  /volume1/music ─────────────────────────────────────────────────────┐
│ / artist:elvis album:"sun sessions"                     12 selected · 12 / 98,412 │
├──┬───────────────┬──────────────────┬─────────────────────┬────┬────┬────────┤
│  │Artist         │Album             │Title                │   #│Year│  Time  │
├──┼───────────────┼──────────────────┼─────────────────────┼────┼────┼────────┤
│✓•│Elvis Presley  │The Sun Sessions  │Blue Moon of Kentucky│   1│1956│  2:04  │
│✓•│Elvis Presley  │The Sun Sessions  │I Don't Care         │   2│1956│  2:41  │
└──┴───────────────┴──────────────────┴─────────────────────┴────┴────┴────────┘
```

Because it is an HTTP client it runs on your laptop against the NAS rather than
only over SSH. It holds a window of the library rather than all of it, fetching
pages as you scroll, and search is debounced so a keystroke does not mean a
round trip.

Press `?` for the full key list. The short version:

- `/` search, updating as you type
- `space` mark a track, `v` mark a range, `a` mark everything matching
- `e` open the editor for the marked tracks, or for the one under the cursor
- `tab` move between fields, `⏎` edit the focused one
- typing offers completions from `GET /v1/values/{field}` — values already in
  your library; `tab` accepts the highlighted one
- `⏎` commits the field to **every** marked track at once
- `u` / `^r` undo and redo, one step per edit no matter how many tracks it hit
- `^s` write every change back to disk
- `y` copy the cover under the cursor, `p` paste it onto the marked tracks
- `A` the art panel, which draws the cover as an actual image in iTerm2, Kitty,
  WezTerm and Ghostty and falls back to text elsewhere
- `R` refresh after editing has made the view stale

Nothing touches disk until `^s`. Changed tracks carry a `•` until then, and
marking everything with `a` selects by query rather than by listing tracks — so
it costs the same whether it matches ten tracks or a hundred thousand, for
exactly the reason a `Selector` does.

Each save sends the version the track was read at, so if something else changed
the same file in between — a phone, another terminal — that edit is reported and
kept pending rather than overwriting the other one. The browser's `u` is its
own in-memory stack, which is a different thing from `POST /v1/jobs/{id}/undo`:
one is a client convenience for edits it made itself, the other is on the
server and works from anywhere.

The **Split** button beside the title appears only when the artist reads
Various Artists. For one song it fills the fields in and waits for you to save;
for a selection it runs as a job. The **Artwork** tab searches Discogs, and
**Populate from Discogs** beside the year fills in the year and genre.

### The command line

```sh
yamo scan -status                     # is one running?
yamo scan /volume1/music              # first run, or refresh
yamo scan                             # refresh whatever the catalogue covers
yamo scan -full /volume1/music        # ignore the cache, re-read everything
yamo scan -exclude Podcasts /volume1/music

yamo find artist:elvis                # query from a script
yamo find -format path artist:elvis   # ...as a playlist
yamo find -- -genre:live artist:elvis # a query starting with - needs --

yamo art                              # what art the library has
yamo art -copy cover.jpg              # put an image on the clipboard
yamo art -paste artist:elvis -apply   # write it to matching tracks
yamo art -from-folder -apply          # embed the folder.jpg beside each track
yamo art -export ~/covers             # write covers out as files

yamo strip                                    # dry run over everything
yamo strip -backup ~/strip.jsonl -apply       # do it, reversibly
yamo restore -backup ~/strip.jsonl -apply     # put it all back
```

The catalogue lives in your cache directory (`yamo info` prints the path).
Override it with `-catalog PATH` or `YAMO_CATALOG`.

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
internal/api/      HTTP handlers over the service — the server itself
internal/library/  the service layer: owns the catalogue, all operations
internal/tags/     format parsers and writers; no third-party tag library
internal/catalog/  in-memory library, binary snapshot, search index
internal/scan/     parallel directory walk and tag extraction
internal/client/   Go client for the API, used by everything below
internal/mcp/      the MCP endpoint: twenty tools over the same service
internal/ui/       the terminal browser, a client like any other
cmd/yamo/          serve, plus the client commands: scan, find, info, art, strip, browse
tools/genlib/      synthetic library generator, for benchmarking
tools/tuidrive/    drives the interface in a pty, for testing the rendering
```

`internal/library` is where the operations live, one file per idea:
`journal.go` and `restore.go` are the undo mechanism, `renametmpl.go` the
filename templates, `duplicates.go` and `folders.go` the two browse views,
`rawtags.go` the pre-strip listing, `thumb.go` the scaler, `capabilities.go`
what the build can do, and `version.go` the note on why a version is more than
a size and a timestamp.

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
./.venv/bin/python tools/tuidrive/drive.py "./dist/yamo" 120x30 '/artist:elvis<enter>' 'e'
```

## Why Go

The work here is almost entirely file IO: open a hundred thousand files, read a
few kilobytes from each, close them. Go gives goroutine-per-file concurrency
that saturates the IO queue without any async plumbing, cross-compiles to a
single static binary for the NAS with one environment variable, and has the
strongest terminal-UI ecosystem going. A faster language would not help,
because nothing here is CPU-bound.

## Limitations

- WMA, WAV and AIFF are read but not written.
- `strip` and `restore` cover MP3, FLAC, MP4, Ogg Vorbis and Opus. WMA, WAV
  and AIFF are read but not written, so they are reported and skipped.
- The catalogue is not watched — nothing detects file changes as they
  happen. Run `yamo scan` after adding music, or start the server with
  `-rescan-every` to have it rescan on a timer.
- One catalogue at a time; use `-catalog` to keep several.
- The MCP tools cannot upload an image, read audio, or use the artwork
  clipboard; those need the HTTP API.

## Connecting an assistant (MCP)

`yamo serve -mcp` mounts a Model Context Protocol endpoint at `/mcp`, so an
assistant can search the library and correct it — "find every artist spelled
two ways", "these forty tracks have no album art, is there a cover.jpg beside
them" — using the same service the terminal browser and the HTTP API use.

It is off by default. It is behind the same token as the rest of the API, and
for the same reason: these tools rewrite music files.

```sh
yamo serve -mcp                       # loopback, no token needed
YAMO_MCP=1 yamo serve                 # the form a container's environment holds
```

The endpoint is Streamable HTTP: one JSON-RPC message per `POST`, answered with
one JSON response. There is no session id and no server-initiated stream,
because no tool here needs to push — a long operation reports itself through the
job it returns, exactly as it does over HTTP.

```sh
curl -s localhost:8467/mcp -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' | head -c 400
```

### Connecting

From Claude Code, on the machine running the server:

```bash
claude mcp add --transport http yamo http://127.0.0.1:8467/mcp
```

Over the network the server requires a token, and it goes in a header:

```bash
claude mcp add --transport http yamo http://nas.local:8467/mcp --header "Authorization: Bearer $YAMO_TOKEN"
```

Clients configured by file — Claude Desktop, Cursor, and most of the rest —
take the same three things:

```json
{
  "mcpServers": {
    "yamo": {
      "type": "http",
      "url": "http://127.0.0.1:8467/mcp",
      "headers": { "Authorization": "Bearer <token>" }
    }
  }
}
```

Drop the `headers` entry on loopback, where no token is required. A client that
speaks only stdio can be bridged with `npx -y mcp-remote http://127.0.0.1:8467/mcp`
as its command.

`GET /v1/capabilities` reports `features.mcp`, which is how a client finds out
the endpoint is there: it is JSON-RPC rather than REST, so it is not in the
OpenAPI schema and cannot be discovered from it.

### The tools

Twenty, over forty-five endpoints, and the arithmetic is the design. A model
choosing between forty-five near-identical operations chooses badly, and the
endpoints that move bytes it cannot read are no use to it at all.

| Tool | |
| --- | --- |
| **Reading** | |
| `search_tracks` | Search, sort and page. `total` counts every match, not the page |
| `get_track` | One track, including the version that identifies it on disk |
| `get_raw_tags` | Every tag actually in the file, rather than the fields the catalogue keeps |
| `list_albums` | Albums with their track and artwork counts |
| `list_artists` | Artists with their track and album counts |
| `list_values` | Distinct values of a field, with counts — the misspelling finder |
| `find_duplicates` | The same recording more than once, and what it wastes |
| `artwork_summary` | Distinct covers across a selection, grouped, and how many have none |
| `library_stats` | Counts, formats, missing fields, when it was scanned, what this build can do |
| `lookup_album` | Year and genre from Discogs. The only tool that leaves the machine |
| **Writing** | |
| `edit_tracks` | One set of field values across a selection. `null` clears a field |
| `split_titles` | Pull `$artist - $title` out of a title into its own tags |
| `rename_files` | Move files to a path built from their own tags |
| `strip_tags` | Remove everything but a keep list |
| `set_artwork` | Embed the `cover.jpg` beside each track, paste the clipboard, or clear it |
| **Jobs and recovery** | |
| `scan_library` | Bring the catalogue up to date |
| `get_job` | Poll a job, optionally waiting for it |
| `list_backups` | The undo journals, which outlive the hour a job stays queryable |
| `undo_job` | Reverse what a job did |
| `restore_backup` | Put one journal back, for a job older than that hour |

Four rules hold across all of them, and they are the four the API itself is
built on.

**Selections, not files.** Every writing tool takes `query`, or `ids`, or
`all: true` — never a path. `all` must be set explicitly, so an empty selection
can never be read as "everything". `expectCount` carries the number the
assistant told you it was about to change, and the server refuses if the
selection has moved since; it is the difference between a model with a stale
count being obeyed and being corrected.

**Writes are dry runs by default.** This is the one place the MCP surface
deliberately differs from the HTTP API, where only `strip` defaults that way.
An assistant should look before it leaps, so `dryRun` has to be passed as
`false` to write anything at all. A dry run reports what it matched, what it
would change, and for a split or a rename the worked examples that say whether
the template is right.

**Jobs are waited for.** Everything that can touch more than one file returns a
job, and these tools wait up to fifteen seconds for it rather than handing back
an id to poll — a batch edit over a few hundred tracks finishes in well under a
second, and making a model poll for a result that is already there wastes a
round trip and invites it to report work it has not seen finish. A scan is the
exception: it is expected to outlive the call, and `get_job` takes a
`waitSeconds` for that.

**Everything is undoable.** Edits, splits and renames record a journal without
being asked; strips and artwork pastes do it on request. `undo_job` takes back
what a job did, and `list_backups` finds the older ones once the job itself has
aged out.

Not offered here, and deliberately: uploading an image, reading audio, the
artwork clipboard, the event streams, and the per-track write endpoints. The
first three move bytes a model cannot read, the fourth exists so a client can
build its own concurrency, and the last is `edit_tracks` with one id in it.

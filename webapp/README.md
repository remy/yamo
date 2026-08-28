# tagmgr web

A browser front end for the tagmgr API. Finding, filtering and editing — no
playback.

It is a sample: **no build step, no dependencies, no backend of its own.** Four
files of vanilla ES modules.

Serve the directory however you like — `serve`, `python3 -m http.server`,
anything — and point it at a running API:

```sh
cd webapp && serve                                  # the app, on :8000 say
tagmgr serve -listen 127.0.0.1:8467 -token secret   # the API, elsewhere
```

The app and the API are then on different origins, so the **server must have
been started with a token**. Cross-origin requests are only permitted when one
is set: a server with permissive CORS and no token could be driven by any page
you happened to visit, and this API rewrites music files.

### Or let the API serve it

```sh
cd webapp && tagmgr serve
# then http://localhost:8467 — nothing else to run, nothing to type
```

`tagmgr serve` serves the front end when the directory it is run from contains
an `index.html`. Same origin means no CORS is involved at all, so this works
with no token and the page connects without asking. Useful on a NAS, where a
static file server may be one more thing to install. `-web DIR` points
elsewhere; `-web ""` turns it off.

On startup the page tries the origin it was served from, then anything
remembered in `localStorage`, and only shows the connection form if neither
answers.

## What it does

- **Songs** — a recycled table over the whole library, sorted by any column
  server-side. Click a heading to sort, click again to reverse. A hundred
  thousand tracks is around thirty DOM rows, reused as you scroll.
- **Albums** — a grid of covers, lazily fetched. Clicking one searches for
  exactly that album, using the `query` each album carries.
- **URLs** — the view, search, facet and sort live in the hash, so back and
  forward work, a view can be bookmarked, and a reload lands where you were.
  `#/albums`, `#/songs?q=artist%3Aelvis&sort=-year`. Going back to a list
  returns to the place in it you left.
- **Search** — the full query language: `artist:elvis`, `year:>1980`,
  `-genre:live`, `album:"sun sessions"`. Runs on ⏎, not as you type: a request
  per keystroke against a large library is not free, and a half-typed `year:>`
  is never sent. Escape clears it.
- **Genre and artist facets** in the sidebar, from `/v1/values/{field}`.
- **Keyboard** — arrows move the focused row, shift-arrow extends the
  selection, Home/End/PageUp/PageDown jump, ⏎ opens Get Info, Escape closes it
  or clears the selection, ⌘A selects everything matching.
- **Get Info** — modelled on the dialog in Apple Music, restricted to the
  fields this API can write. ⏎ saves from anywhere in the sheet, Escape
  discards. Click a row and press ⏎ or ⌘I, or double-click. The ‹ › buttons
  step through whatever is currently listed without closing the sheet, and
  ⇧-clicking OK (or ⇧⏎) saves and moves to the next one. Stepping commits what
  you typed, as the Apple Music window does: moving on is never a way to lose
  an edit.
- **Autocomplete** on artist, album, album artist and genre, from
  `GET /v1/values/{field}` — prefix matches first, ranked by how many tracks
  use each value, with the count shown. Arrows to choose, ⏎ or Tab to accept.
- **A copy button** beside the location in the File tab, which copies the path
  shell-quoted when it contains anything a shell would treat specially. The
  path itself stays plain selectable text, and the button ticks for a moment
  rather than the text being swapped for the word "Copied" — which hid the
  thing you were still reading.
- **Clean up** in the Get Info footer — removes every tag outside the standard
  set (iTunes gapless data, volume normalisation, purchase identifiers, your
  Apple ID) and moves kept values held under an older name into the field they
  belong in: an ID3v2.2 frame, a genre stored as `(19)`, an MP4 `gnre` atom.
  Two calls to `POST /v1/strip` — a dry run to show what will go, then the real
  one — and a backup is always taken, so `tagmgr restore -backup ID -apply`
  undoes it.
- **Multi-select** — shift-click and ⌘-click, or ⌘A for everything matching.
  Fields that differ show *Mixed* and are left alone unless typed into, which
  is what Apple Music does.

## The three things worth reading the code for

**Artwork cannot be an `<img src>`.** An image element cannot send an
`Authorization` header, so covers are fetched and handed to the DOM as blob
URLs. `Api._cacheArt` bounds the cache and revokes evicted entries — an object
URL holds its blob in memory until it is revoked.

**`EventSource` cannot send one either**, so `/v1/events` is read with `fetch`
and a stream reader. `Api.events` is an async generator; the SSE format is
simple enough that parsing it by hand is a few lines. An edit made in the
terminal or on another device drops the affected rows here without polling.

**Rows are recycled, not rebuilt.** A pool of row elements is created once and
moved with `transform`, and only the text that actually changed is rewritten.
Rebuilding the visible rows each frame — which is what `replaceChildren` does —
means a few hundred element allocations per frame for a list that only ever
shows thirty.

Two CSS traps sit underneath that, both the same one: a flex *or grid* item
defaults to `min-height: auto` and refuses to shrink below its content, so
without `min-height: 0` on both `.main` and `.scroller` the pane sizes itself
to the full list, `overflow` never engages, and the window scrolls instead. The
symptom is every row in the DOM and a `scrollTop` stuck at zero.

**Two browser layout traps are worth knowing about.** A modal `<dialog>` draws
in the top layer, so the suggestion list has to live *inside* it — anywhere
else and it renders behind the backdrop however high its `z-index`. And a
`<form method="dialog">` closes on ⏎ *without submitting*, so pressing enter in
a field silently discarded the edit; the form is submitted explicitly instead.

**Selecting everything does not send every id.** ⌘A sets a flag; the save then
sends the *query* as the selector, so editing ten tracks and editing a hundred
thousand cost the same to request. `expectCount` is available for guarding
against the selection having moved, and is deliberately not used here — this
is a sample, and the guard belongs where a mistake is expensive.

## What it deliberately does not do

- No playback.
- No artwork editing. The API supports it (`PUT /v1/tracks/{id}/artwork` and
  the clipboard); the sheet only displays.
- No scanning. Run `tagmgr scan` from a terminal.
- `grouping`, `compilation`, `rating`, `bpm` and `play count` appear in Apple's
  dialog but have no equivalent in this API, so they are absent rather than
  shown and ignored.
- No offline handling or retry. A dropped connection shows an error and waits
  to be reloaded.

## Files

| | |
| --- | --- |
| `index.html` | structure, including the Get Info sheet |
| `app.css` | an Apple Music-ish shell; adapts to light and dark |
| `api.js` | the API client — blob artwork, streamed events, typed errors |
| `app.js` | state, virtual scrolling, the sheet |

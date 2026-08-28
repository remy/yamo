import { Api, ApiError } from './api.js';

const $ = sel => document.querySelector(sel);
const el = (tag, cls, text) => {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text != null) n.textContent = text;
  return n;
};

// The columns of the track table. `sort` is what the server is asked for.
const COLUMNS = [
  { key: 'track',  label: '#',      sort: 'track',  width: '54px', num: true, dim: true },
  { key: 'title',  label: 'Title',  sort: 'title',  width: '2.2fr' },
  { key: 'artist', label: 'Artist', sort: 'artist', width: '1.6fr' },
  { key: 'album',  label: 'Album',  sort: 'album',  width: '1.6fr' },
  { key: 'genre',  label: 'Genre',  sort: 'genre',  width: '.9fr', dim: true },
  { key: 'year',   label: 'Year',   sort: 'year',   width: '60px', num: true, dim: true },
  { key: 'time',   label: 'Time',   sort: 'duration', width: '62px', num: true, dim: true },
];

// The Get Info fields, in the order Apple Music shows them. `field` is the
// canonical name the API takes; a pair is the "n of m" numeric layout.
const INFO_FIELDS = [
  { field: 'title',       label: 'title' },
  { field: 'artist',      label: 'artist' },
  { field: 'album',       label: 'album' },
  { field: 'albumartist', label: 'album artist' },
  { field: 'composer',    label: 'composer' },
  { field: 'genre',       label: 'genre' },
  { field: 'year',        label: 'year', short: true },
  { field: 'track',       label: 'track', pair: 'tracktotal' },
  { field: 'disc',        label: 'disc number', pair: 'disctotal' },
  { field: 'compilation', label: 'compilation', check: true,
    hint: 'Album is a compilation of songs by various artists' },
  { field: 'comment',     label: 'comments' },
];

// The Sorting tab. Apple Music gives these a panel of their own and so does
// this: they are five more text fields that look exactly like the ones on
// Details, and mixing them in would bury the fields people actually came for.
const SORT_FIELDS = [
  { field: 'titlesort',       label: 'sort title' },
  { field: 'artistsort',      label: 'sort artist' },
  { field: 'albumsort',       label: 'sort album' },
  { field: 'albumartistsort', label: 'sort album artist' },
  { field: 'composersort',    label: 'sort composer' },
];

const ROW_H = 34;
const PAGE = 200;
const DEFAULT_SORT = 'artist,album,track';

const state = {
  api: null,
  view: 'songs',
  query: '',        // what the user typed
  facet: '',        // an extra term from a sidebar click
  sort: DEFAULT_SORT,
  total: 0,
  pages: new Map(), // page index -> array of tracks
  loading: new Set(),
  gen: 0,           // bumped when the query changes; stale replies are dropped
  selected: new Set(),
  cursor: -1,        // the focused row, moved by the arrow keys
  anchor: -1,        // where a shift-extended selection started
  selectAll: false,
  editing: [],      // tracks currently in the Get Info sheet
  editIndex: -1,    // its row, so the sheet can step through the results
  facetValues: null,// the sidebar lists, kept so a route change can repaint them
  eventsAbort: null,
};

// --- connection -------------------------------------------------------

function saveConn(server, token) {
  localStorage.setItem('tagmgr.server', server);
  localStorage.setItem('tagmgr.token', token);
}

async function connect(server, token) {
  const api = new Api(server, token);
  await api.stats();       // fails loudly if the server, token or CORS is wrong
  state.api = api;
  saveConn(server, token);
  $('#connect').hidden = true;
  $('#app').hidden = false;
  await loadFacets();
  // Normalise a bare URL so the first history entry is addressable too.
  if (location.hash !== routeToHash(parseHash(location.hash))) {
    history.replaceState(null, '', routeToHash(parseHash(location.hash)));
  }
  await applyRoute();
  listenForChanges();
}

$('#connect-form').addEventListener('submit', async e => {
  e.preventDefault();
  const err = $('#connect-error');
  err.hidden = true;
  try {
    await connect($('#server').value.trim(), $('#token').value.trim());
  } catch (e) {
    err.hidden = false;
    err.textContent = e instanceof ApiError && e.isAuth
      ? 'The server rejected that token.'
      : `Could not reach the server. ${e.message}. If it is not on this machine it must be started with -token, or the browser will refuse the request.`;
  }
});

$('#disconnect').addEventListener('click', () => {
  localStorage.removeItem('tagmgr.token');
  location.reload();
});

// --- routing ----------------------------------------------------------
//
// What is on screen is four values — view, query, facet, sort — so the URL is
// those four values. Every navigation writes the hash and nothing else; a
// single hashchange listener reads it back and applies it. Going back, going
// forward and loading the page cold therefore all take the same path, and
// there is no second copy of "make the screen match" to keep in step.

function routeToHash(r) {
  const p = new URLSearchParams();
  if (r.query) p.set('q', r.query);
  if (r.facet) p.set('facet', r.facet);
  if (r.sort && r.sort !== DEFAULT_SORT) p.set('sort', r.sort);
  const qs = p.toString();
  return `#/${r.view}${qs ? `?${qs}` : ''}`;
}

function parseHash(hash) {
  const m = /^#\/?([^?]*)(?:\?(.*))?$/.exec(hash || '');
  const p = new URLSearchParams(m?.[2] || '');
  return {
    view: m?.[1] === 'albums' ? 'albums' : 'songs',
    query: p.get('q') || '',
    facet: p.get('facet') || '',
    sort: p.get('sort') || DEFAULT_SORT,
  };
}

const currentRoute = () => ({
  view: state.view, query: state.query, facet: state.facet, sort: state.sort,
});

// applied is the route on screen, or null before the first one lands.
let applied = null;

// navigate records where we are going. Assigning to location.hash pushes a
// history entry and fires hashchange, which does the actual work; when the URL
// would not change there is no event, so the route is applied directly.
function navigate(patch) {
  const next = routeToHash({ ...currentRoute(), ...patch });
  if (location.hash === next) {
    if (!applied) applyRoute();
    return;
  }
  location.hash = next;
}

// Where each URL was scrolled to, so going back to a list returns to the place
// in it you left. Recorded as you scroll rather than as you leave, since the
// back button gives no warning.
const scrollMemory = new Map();

async function applyRoute() {
  const r = parseHash(location.hash);
  const was = applied;
  applied = r;
  Object.assign(state, r);
  // Read before anything can scroll: refresh() resets the pane to the top.
  const restore = scrollMemory.get(location.hash) || 0;

  $('#search').value = r.query;
  paintSearchClear();
  document.querySelectorAll('.nav').forEach(b =>
    b.setAttribute('aria-current', String(b.dataset.view === r.view)));
  $('#songs-view').hidden = r.view !== 'songs';
  $('#albums-view').hidden = r.view !== 'albums';
  paintFacets();
  renderHead();

  // A selection belongs to a result set. Re-sorting keeps the same tracks, so
  // it keeps the selection; changing what is listed does not.
  if (!was || was.query !== r.query || was.facet !== r.facet || was.view !== r.view) {
    state.selected.clear();
    state.selectAll = false;
    state.cursor = -1;
    state.anchor = -1;
  }
  if ($('#info').open) $('#info').close();

  await refresh(true);
  if (restore) {
    $('#scroller').scrollTop = restore;
    render();
    ensurePages();
  }
}

window.addEventListener('hashchange', applyRoute);

// --- fetching ---------------------------------------------------------

// fullQuery combines what was typed with any sidebar facet.
function fullQuery() {
  return [state.query, state.facet].filter(Boolean).join(' ');
}

// refresh discards the cache and fetches the first window.
async function refresh(resetScroll) {
  state.gen++;
  state.pages.clear();
  state.loading.clear();
  if (resetScroll) $('#scroller').scrollTop = 0;
  if (state.view === 'albums') return renderAlbums();
  await ensurePages();
  render();
}

// ensurePages fetches whatever the visible window needs, plus a screen either
// side so scrolling does not stall at every boundary.
async function ensurePages() {
  const sc = $('#scroller');
  const first = Math.max(0, Math.floor(sc.scrollTop / ROW_H) - 20);
  const last = first + Math.ceil(sc.clientHeight / ROW_H) + 40;
  const from = Math.floor(first / PAGE);
  const to = Math.floor(last / PAGE);

  const wanted = [];
  for (let p = from; p <= to; p++) {
    if (p < 0) continue;
    if (state.pages.has(p) || state.loading.has(p)) continue;
    if (p > 0 && state.total && p * PAGE >= state.total) continue;
    wanted.push(p);
  }
  await Promise.all(wanted.map(p => fetchPage(p)));
}

async function fetchPage(page) {
  const gen = state.gen;
  state.loading.add(page);
  try {
    const res = await state.api.listTracks({
      q: fullQuery(), sort: state.sort, limit: PAGE, offset: page * PAGE,
    });
    if (gen !== state.gen) return;   // the query moved on while this was away
    state.pages.set(page, res.items);
    state.total = res.total;
    render();
  } catch (e) {
    if (gen === state.gen) status(e.message, 'err');
  } finally {
    state.loading.delete(page);
  }
}

function trackAt(i) {
  const p = state.pages.get(Math.floor(i / PAGE));
  return p ? p[i % PAGE] : null;
}

// --- rendering --------------------------------------------------------

function colTemplate() { return COLUMNS.map(c => c.width).join(' '); }

function renderHead() {
  const head = $('#thead');
  head.style.setProperty('--cols', colTemplate());
  $('#rows').style.setProperty('--cols', colTemplate());
  head.replaceChildren(...COLUMNS.map(c => {
    const b = el('button', c.num ? 'num' : '', c.label);
    const [field, desc] = state.sort.startsWith('-')
      ? [state.sort.slice(1).split(',')[0], true]
      : [state.sort.split(',')[0], false];
    if (c.sort === field) {
      b.dataset.active = '1';
      b.textContent = `${c.label} ${desc ? '\u25be' : '\u25b4'}`;
    }
    b.addEventListener('click', () =>
      navigate({ sort: (field === c.sort && !desc) ? `-${c.sort}` : c.sort }));
    return b;
  }));
}

// The row pool.
//
// Rows are created once and reused. Rebuilding them on every scroll — which is
// what replaceChildren does — means creating a few hundred elements per frame,
// and at sixty frames a second that is tens of thousands of allocations for a
// list that only ever shows thirty of them. A recycled row is moved with a
// transform and has its text rewritten, which costs nothing by comparison.
const pool = [];

function poolRow(k) {
  while (pool.length <= k) {
    const row = el('div', 'row');
    row.cells = COLUMNS.map(c => {
      const span = el('span', [c.num ? 'num' : '', c.dim ? 'dim' : ''].filter(Boolean).join(' '));
      row.append(span);
      return span;
    });
    row.shown = null;      // what this row currently displays, for cheap diffing
    row.shownTrack = null;
    $('#rows').append(row);
    pool.push(row);
  }
  return pool[k];
}

function render() {
  const sc = $('#scroller');
  $('#sizer').style.height = `${state.total * ROW_H}px`;

  const first = Math.max(0, Math.floor(sc.scrollTop / ROW_H) - 4);
  const visible = Math.ceil(sc.clientHeight / ROW_H) + 8;
  const last = Math.min(first + visible, state.total);

  for (let k = 0; k < Math.max(visible, pool.length); k++) {
    const row = poolRow(k);
    const i = first + k;
    if (i >= last) {                       // surplus rows park rather than die
      if (!row.hidden) { row.hidden = true; row.shown = null; row.shownTrack = null; }
      continue;
    }
    row.hidden = false;
    // Transform rather than top: moving a row this way does not invalidate
    // layout, only compositing.
    row.style.transform = `translateY(${i * ROW_H}px)`;
    paintRow(row, i);
  }

  $('#count').textContent = `${state.total.toLocaleString()} songs` +
    (state.selected.size ? ` \u00b7 ${state.selected.size.toLocaleString()} selected` : '');
  $('#edit-btn').disabled = state.selected.size === 0;
}

// paintRow writes a track into a pooled row, touching only what changed.
function paintRow(row, i) {
  const t = trackAt(i);
  const selected = t ? state.selected.has(t.id) : false;
  const cursor = i === state.cursor;
  const key = `${i}|${selected ? 1 : 0}|${cursor ? 1 : 0}`;
  // The track object is compared by identity rather than by its id, and that
  // is the whole point: an edit gives a track new values under the same id, so
  // a key built from the id alone says "nothing changed" and leaves the row
  // showing what the file used to say. A refetch always yields fresh objects —
  // fetchPage replaces the page array with newly parsed JSON — while scrolling
  // hands back the same ones, so identity is exactly the question being asked.
  if (row.shown === key && row.shownTrack === t) return;
  row.shown = key;
  row.shownTrack = t;

  row.dataset.index = i;
  row.classList.toggle('alt', i % 2 === 1);
  row.classList.toggle('cursor', cursor);
  row.classList.toggle('pending', !t);
  if (t) {
    row.dataset.id = t.id;
    row.setAttribute('aria-selected', String(selected));
    for (let c = 0; c < COLUMNS.length; c++) {
      const text = cellText(t, COLUMNS[c].key);
      if (row.cells[c].textContent !== text) row.cells[c].textContent = text;
    }
  } else {
    delete row.dataset.id;
    row.removeAttribute('aria-selected');
    for (const cell of row.cells) if (cell.textContent !== '') cell.textContent = '';
  }
}

function cellText(t, key) {
  switch (key) {
    case 'time': return formatTime(t.durationMs);
    case 'year': return t.year || '';
    // A track number is shown alone unless the file also says how many there
    // are, which is the form the tag itself uses.
    case 'track': return t.track ? (t.trackTotal ? `${t.track}/${t.trackTotal}` : String(t.track)) : '';
    default: return t[key] || '';
  }
}

const formatTime = ms => {
  if (!ms) return '';
  const s = Math.round(ms / 1000);
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`;
};

let frameQueued = false;
function onScroll() {
  if (frameQueued) return;
  frameQueued = true;
  requestAnimationFrame(() => {
    frameQueued = false;
    scrollMemory.set(location.hash, $('#scroller').scrollTop);
    render();
    ensurePages();
  });
}
$('#scroller').addEventListener('scroll', onScroll, { passive: true });
window.addEventListener('resize', onScroll);

// --- selection --------------------------------------------------------

$('#rows').addEventListener('click', e => {
  const row = e.target.closest('.row');
  if (!row?.dataset.id) return;
  const i = Number(row.dataset.index);
  const id = row.dataset.id;

  state.cursor = i;
  if (e.shiftKey && state.anchor >= 0) {
    selectRange(state.anchor, i);
  } else if (e.metaKey || e.ctrlKey) {
    state.selected.has(id) ? state.selected.delete(id) : state.selected.add(id);
    state.anchor = i;
  } else {
    state.selected.clear();
    state.selectAll = false;
    state.selected.add(id);
    state.anchor = i;
  }
  $('#scroller').focus({ preventScroll: true });
  render();
});

$('#rows').addEventListener('dblclick', e => {
  if (e.target.closest('.row')?.dataset.id) openInfo();
});

document.addEventListener('keydown', e => {
  if ($('#info').open) return;
  // Typing in a field means the keys belong to the field.
  const inField = /^(INPUT|TEXTAREA|SELECT)$/.test(document.activeElement?.tagName || '');

  if ((e.metaKey || e.ctrlKey) && e.key === 'i') { e.preventDefault(); openInfo(); return; }
  if ((e.metaKey || e.ctrlKey) && e.key === 'a' && state.view === 'songs' && !inField) {
    e.preventDefault();
    selectAllMatching();
    return;
  }
  if (inField || state.view !== 'songs') return;

  const page = Math.max(1, Math.floor($('#scroller').clientHeight / ROW_H) - 1);
  switch (e.key) {
    case 'ArrowDown':  e.preventDefault(); moveCursor(1, e.shiftKey); break;
    case 'ArrowUp':    e.preventDefault(); moveCursor(-1, e.shiftKey); break;
    case 'PageDown':   e.preventDefault(); moveCursor(page, e.shiftKey); break;
    case 'PageUp':     e.preventDefault(); moveCursor(-page, e.shiftKey); break;
    case 'Home':       e.preventDefault(); setCursor(0, e.shiftKey); break;
    case 'End':        e.preventDefault(); setCursor(state.total - 1, e.shiftKey); break;
    case 'Enter':      e.preventDefault(); openInfo(); break;
    case 'Escape':     state.selected.clear(); state.selectAll = false; render(); break;
  }
});

// moveCursor shifts the focused row, extending the selection when shift is
// held and replacing it otherwise — the behaviour of every list on the system.
function moveCursor(delta, extend) {
  const from = state.cursor < 0 ? 0 : state.cursor;
  setCursor(Math.min(Math.max(from + delta, 0), state.total - 1), extend);
}

function setCursor(index, extend) {
  if (state.total === 0) return;
  index = Math.min(Math.max(index, 0), state.total - 1);
  state.cursor = index;

  if (extend) {
    if (state.anchor < 0) state.anchor = index;
    selectRange(state.anchor, index);
  } else {
    state.anchor = index;
    state.selected.clear();
    state.selectAll = false;
    const t = trackAt(index);
    if (t) state.selected.add(t.id);
  }
  scrollCursorIntoView();
  render();
  ensurePages().then(() => {
    // A row selected before its page arrived has no id yet; claim it once it does.
    if (!extend) {
      const t = trackAt(state.cursor);
      if (t && !state.selected.has(t.id)) {
        state.selected.clear();
        state.selected.add(t.id);
        render();
      }
    }
  });
}

function selectRange(a, b) {
  const [lo, hi] = a <= b ? [a, b] : [b, a];
  state.selected.clear();
  state.selectAll = false;
  for (let i = lo; i <= hi; i++) {
    const t = trackAt(i);
    if (t) state.selected.add(t.id);
  }
}

// scrollCursorIntoView nudges the pane by the least amount that reveals the
// focused row, so held arrow keys creep rather than jump.
function scrollCursorIntoView() {
  const sc = $('#scroller');
  const top = state.cursor * ROW_H;
  const bottom = top + ROW_H;
  if (top < sc.scrollTop) sc.scrollTop = top;
  else if (bottom > sc.scrollTop + sc.clientHeight) sc.scrollTop = bottom - sc.clientHeight;
}

// selectAllMatching marks everything the query matches. Only the loaded ids
// are held; the edit itself is sent as a query selector, so this costs the
// same whether it matches ten tracks or a hundred thousand.
function selectAllMatching() {
  state.selected.clear();
  for (const page of state.pages.values()) for (const t of page) state.selected.add(t.id);
  state.selectAll = true;
  render();
  status(`${state.total.toLocaleString()} tracks selected — edits will apply to all of them`);
}

// --- search and facets ------------------------------------------------

// Search runs on Enter, not as you type.
//
// Debouncing keystrokes still means a query per pause, and a query over a
// network against a large library is not free. Waiting for Enter also means a
// half-typed expression like `year:>` is never sent.
$('#search').addEventListener('keydown', e => {
  // The suggestion list gets first refusal on the keys it uses. The sheet has
  // a list of its own, which is not this field's business — hence the second
  // test rather than merely "is a list open".
  if (suggestOpen() && suggest.input === e.target) {
    switch (e.key) {
      case 'ArrowDown': e.preventDefault(); highlightSuggest(1); return;
      case 'ArrowUp':   e.preventDefault(); highlightSuggest(-1); return;
      case 'Tab':       if (acceptSuggest()) e.preventDefault(); return;
      case 'Escape':    e.preventDefault(); closeSuggest(); return;
      case 'Enter':
        // Taking a suggestion is not also running the search: the next Enter
        // does that, once what will be searched for is on screen to be read.
        if (suggest.index >= 0) { e.preventDefault(); acceptSuggest(); return; }
        closeSuggest();
        break;
    }
  }
  if (e.key === 'Enter') {
    e.preventDefault();
    runSearch(e.target.value);
    // Give the keyboard back to the list, so the arrows work straight away.
    $('#scroller').focus({ preventScroll: true });
  } else if (e.key === 'Escape' && e.target.value !== '') {
    e.preventDefault();
    e.target.value = '';
    paintSearchClear();
    runSearch('');
  }
});

// The clear button is only there when there is something to clear.
function paintSearchClear() {
  $('#search-clear').hidden = $('#search').value === '';
}

$('#search').addEventListener('input', paintSearchClear);

// Clearing runs the empty search rather than merely emptying the box, which is
// what Escape already does and what the button being inside the field implies.
$('#search-clear').addEventListener('click', () => {
  closeSuggest();
  $('#search').value = '';
  paintSearchClear();
  // The keyboard goes back to the field: the button is about to vanish from
  // under the pointer, and clearing a search is usually the start of another.
  $('#search').focus();
  runSearch('');
});

function runSearch(value) {
  navigate({ query: value });
}

async function loadFacets() {
  const [genres, artists] = await Promise.all([
    state.api.values('genre', '', 20),
    state.api.values('artist', '', 40),
  ]);
  state.facetValues = { genre: genres, artist: artists };
  paintFacets();

  const s = await state.api.stats();
  $('#library-stats').textContent =
    `${s.tracks.toLocaleString()} songs · ${s.albums.toLocaleString()} albums`;
}

// facetTerm is the query a sidebar entry stands for, and the value held in the
// URL — so a link to a facet survives a reload.
const facetTerm = (field, value) => `${field}:"${value.replace(/"/g, '')}"`;

// paintFacets redraws the sidebar from the values already fetched. A route
// change only moves which button is pressed, and that is not worth a request.
function paintFacets() {
  if (!state.facetValues) return;
  for (const [field, host] of [['genre', '#genres'], ['artist', '#artists']]) {
    $(host).replaceChildren(...state.facetValues[field].map(v => {
      const b = el('button', 'facet');
      b.append(el('span', 'name', v.value), el('span', 'n', v.count.toLocaleString()));
      const term = facetTerm(field, v.value);
      b.setAttribute('aria-pressed', String(state.facet === term));
      b.addEventListener('click', () =>
        navigate({ facet: state.facet === term ? '' : term }));
      return b;
    }));
  }
}

// --- views ------------------------------------------------------------

document.querySelectorAll('.nav').forEach(b =>
  b.addEventListener('click', () => navigate({ view: b.dataset.view })));

async function renderAlbums() {
  const grid = $('#album-grid');
  grid.replaceChildren(el('div', 'muted', 'Loading…'));
  const res = await state.api.listAlbums({ q: fullQuery(), limit: 500 });
  $('#count').textContent = `${res.total.toLocaleString()} albums`;

  grid.replaceChildren(...res.items.map(a => {
    const b = el('button', 'album');
    const img = el('img', 'cover');
    img.alt = '';
    b.append(img, el('div', 't', a.album || 'Unknown album'),
             el('div', 'a', a.albumArtist || ''));
    // Each album carries a query that reselects exactly it. Going there is a
    // navigation, so the back button returns to the grid.
    b.addEventListener('click', () => navigate({ view: 'songs', query: a.query }));
    // Covers load lazily; each is a separate authenticated fetch.
    observer.observe(img);
    img.dataset.albumQuery = a.query;
    return b;
  }));
}

// Album covers are fetched only once scrolled into view: each one is a
// request, and a library of several thousand albums would otherwise issue
// several thousand of them at once.
const observer = new IntersectionObserver(async entries => {
  for (const entry of entries) {
    if (!entry.isIntersecting) continue;
    const img = entry.target;
    observer.unobserve(img);
    try {
      const res = await state.api.listTracks({ q: img.dataset.albumQuery, limit: 1 });
      const t = res.items.find(x => x.hasArt) || res.items[0];
      if (t?.hasArt) {
        const url = await state.api.artwork(t.id);
        if (url) img.src = url;
      }
    } catch { /* leave the placeholder */ }
  }
}, { rootMargin: '200px' });

// --- autocomplete -----------------------------------------------------

// The fields worth completing: ones whose values repeat across a library.
// A title is unique to a track and completing it would only get in the way.
const COMPLETES = new Set(['artist', 'album', 'albumartist', 'genre']);

// There are two lists in the page, not one: a closed <dialog> is display:none
// and takes its children with it, so the sheet's list cannot serve the search
// bar. Only one is ever up, and `box` is whichever that is.
const suggest = {
  box: null,        // the floating list in use, or null when none is
  input: null,      // the field it belongs to
  items: [],
  index: -1,
  timer: null,
  seq: 0,           // replies for a prefix already typed past are discarded
};

const suggestOpen = () => suggest.box !== null;

function closeSuggest() {
  if (suggest.box) {
    suggest.box.hidden = true;
    // Empty it as well as hiding it, so nothing stale can flash on reopen.
    suggest.box.replaceChildren();
  }
  suggest.box = null;
  suggest.input = null;
  suggest.items = [];
  suggest.index = -1;
  clearTimeout(suggest.timer);
}

// An item is a value and its count, as the API returns them. A search term
// suggestion carries a `label` to show instead, a `hint` where the count would
// be, and an `apply` that puts it in the field, since a term is spliced into
// what is already typed rather than being the whole of it.
function openSuggest(box, input, items) {
  if (!items.length) { closeSuggest(); return; }
  closeSuggest();                 // in case the other list is the one up
  suggest.box = box;
  suggest.input = input;
  suggest.items = items;
  suggest.index = -1;

  box.replaceChildren(...items.map((v, i) => {
    const row = el('div', 'suggest-item');
    row.setAttribute('role', 'option');
    row.append(el('span', '', v.label ?? v.value),
               el('span', 'n', v.count != null ? v.count.toLocaleString() : (v.hint ?? '')));
    // mousedown, not click: the field must not lose focus before the value
    // is taken, or the blur handler closes the list first.
    row.addEventListener('mousedown', e => { e.preventDefault(); acceptSuggest(i); });
    box.append(row);
    return row;
  }));

  const r = input.getBoundingClientRect();
  box.style.left = `${r.left}px`;
  box.style.top = `${r.bottom + 2}px`;
  box.style.width = `${r.width}px`;
  box.hidden = false;
}

// highlightSuggest cycles through the items and back to nothing selected, so
// arrowing past the end returns to what was typed rather than sticking.
//
// The wheel has n+1 positions: -1 for "nothing", then 0..n-1. Shifting by one
// makes it a plain modulus, and the second modulus keeps a negative step
// positive, since JavaScript's % takes the sign of the left operand.
function highlightSuggest(delta) {
  if (!suggest.items.length) return;
  const n = suggest.items.length;
  suggest.index = (((suggest.index + 1 + delta) % (n + 1)) + (n + 1)) % (n + 1) - 1;
  [...suggest.box.children].forEach((row, i) =>
    row.setAttribute('aria-selected', String(i === suggest.index)));
  if (suggest.index >= 0) suggest.box.children[suggest.index]?.scrollIntoView({ block: 'nearest' });
}

function acceptSuggest(i = suggest.index) {
  if (!suggest.input || i < 0 || i >= suggest.items.length) return false;
  const input = suggest.input, item = suggest.items[i];
  closeSuggest();
  if (item.apply) item.apply(); else input.value = item.value;
  return true;
}

// wireSuggest attaches completion to one field.
function wireSuggest(input, field) {
  input.setAttribute('autocomplete', 'off');
  // Focusing a different field must not leave the previous field's list up.
  input.addEventListener('focus', () => { if (suggest.input !== input) closeSuggest(); });
  input.addEventListener('input', () => {
    clearTimeout(suggest.timer);
    const prefix = input.value.trim();
    if (!prefix) { closeSuggest(); return; }
    // Claim the box now, so a reply for the field just left cannot land here.
    suggest.input = input;
    // A short debounce only: this is a local index lookup, not a search over
    // the library, and it is measured in a fraction of a millisecond.
    suggest.timer = setTimeout(async () => {
      const seq = ++suggest.seq;
      try {
        const items = await state.api.values(field, prefix, 8);
        // A reply is only usable if it is still the newest, still for this
        // field, and the field is still where the keyboard is. Sequence alone
        // is not enough: moving between fields leaves an older reply able to
        // land in the box a different field has since opened.
        if (seq !== suggest.seq || suggest.input !== input) return;
        if (document.activeElement !== input) return;
        if (input.value.trim() !== prefix) return;   // typed on while waiting
        // Offering exactly what is already typed is noise.
        openSuggest($('#suggest'), input, items.filter(v => v.value !== input.value));
      } catch { closeSuggest(); }
    }, 120);
  });
  // Closing on blur has to be deferred, or clicking a suggestion would lose
  // the field before the value is taken. But by the time it runs the keyboard
  // may be in another field that has already scheduled its own lookup — and
  // closing clears the pending timer, which silently cancelled it. Only close
  // if the list still belongs to this input.
  input.addEventListener('blur', () => setTimeout(() => {
    if (suggest.input === input) closeSuggest();
  }, 120));
  input.addEventListener('keydown', e => {
    if (!suggestOpen() || suggest.input !== input) return;
    switch (e.key) {
      case 'ArrowDown': e.preventDefault(); highlightSuggest(1); break;
      case 'ArrowUp':   e.preventDefault(); highlightSuggest(-1); break;
      case 'Escape':    e.preventDefault(); e.stopPropagation(); closeSuggest(); break;
      case 'Tab':       if (acceptSuggest()) e.preventDefault(); break;
      case 'Enter':
        // Taking a suggestion must not also submit the sheet.
        if (suggest.index >= 0) { e.preventDefault(); e.stopPropagation(); acceptSuggest(); }
        else closeSuggest();
        break;
    }
  });
}

// --- search autocomplete ----------------------------------------------
//
// The search bar takes a query language rather than a value, so completion
// works on the term under the caret and not on the whole field. In
// `-artist:elv presley` the term being typed is `-artist:elv`; what is worth
// offering is artists beginning "elv"; and taking one has to go back in that
// term's place, leaving the rest of the line alone.

// The field prefixes the language accepts, in the order they are offered, with
// the aliases each also answers to. It mirrors the server's table: a name the
// server would not resolve is not a prefix there, and must not look like one
// here. `hint` is shown where a count would be, for the fields whose syntax is
// more than a value.
const QUERY_FIELDS = [
  { name: 'artist',      aliases: ['a', 'ar'] },
  { name: 'album',       aliases: ['al', 'b'] },
  { name: 'albumartist', aliases: ['aa', 'band'] },
  { name: 'title',       aliases: ['t', 'name'] },
  { name: 'genre',       aliases: ['g'] },
  { name: 'composer',    aliases: ['c'] },
  { name: 'comment',     aliases: [] },
  { name: 'year',        aliases: ['y', 'date'], hint: '1977, >1980, 1970-1979' },
  { name: 'track',       aliases: ['trackno', 'n'], hint: '5, >5' },
  { name: 'disc',        aliases: ['d'], hint: '1, >1' },
  { name: 'path',        aliases: ['p', 'file'] },
];

const FIELD_NAMES = new Map(QUERY_FIELDS.flatMap(
  f => [f.name, ...f.aliases].map(alias => [alias, f.name])));

// searchTokens splits a query the way the server does — on whitespace, with
// double quotes holding a value together — and keeps each token's span, so the
// one under the caret can be replaced without disturbing its neighbours.
function searchTokens(s) {
  const out = [];
  let start = -1, inQuote = false;
  for (let i = 0; i <= s.length; i++) {
    const c = s[i];
    if (i === s.length || (!inQuote && (c === ' ' || c === '\t'))) {
      if (start >= 0) out.push({ start, end: i, text: s.slice(start, i) });
      start = -1;
    } else {
      if (start < 0) start = i;
      if (c === '"') inQuote = !inQuote;
    }
  }
  return out;
}

// termAt is the term the caret sits in, parsed into the parts completion needs.
// A caret in whitespace is an empty term starting there, which is what typing
// on would begin.
function termAt(input) {
  const caret = input.selectionStart ?? input.value.length;
  const tok = searchTokens(input.value).find(t => caret >= t.start && caret <= t.end)
    || { start: caret, end: caret, text: '' };
  // Quotes come out before anything is read off the text, as they do on the
  // server: `"artist:elvis presley"` is an artist term there and so here.
  let s = tok.text.replace(/"/g, ''), neg = false;
  if (s.length > 1 && s.startsWith('-')) { neg = true; s = s.slice(1); }
  // A prefix only counts when the name resolves, so `AC:DC` and a time like
  // `3:04` stay literal text — again, as they do on the server.
  const colon = s.indexOf(':');
  const field = colon > 0 ? FIELD_NAMES.get(s.slice(0, colon).toLowerCase()) : undefined;
  return { ...tok, neg, field: field || '', value: field ? s.slice(colon + 1) : s };
}

// replaceTerm splices text over the term under the caret and leaves the caret
// after it. The term is found again here rather than remembered from when the
// list was opened, since typing may have moved it since.
function replaceTerm(input, text) {
  const term = termAt(input);
  const before = input.value.slice(0, term.start);
  input.value = before + text + input.value.slice(term.end);
  const at = before.length + text.length;
  input.setSelectionRange(at, at);
  paintSearchClear();
}

// A value is quoted only when it has to be: a space would otherwise end the
// term and start another.
const quoteValue = v => /[\s"]/.test(v) ? `"${v.replace(/"/g, '')}"` : v;

// searchSuggest offers whatever fits the term under the caret: values once it
// names a field, and otherwise the prefixes what has been typed could still
// become — so `art` offers `artist:`, and `artist:elv` offers artists.
function searchSuggest(input) {
  clearTimeout(suggest.timer);
  suggest.seq++;                     // discard anything already in flight
  const term = termAt(input);
  const sign = term.neg ? '-' : '';

  if (!term.field) {
    const typed = term.value.toLowerCase();
    const fields = typed
      ? QUERY_FIELDS.filter(f => f.name.startsWith(typed) || f.aliases.includes(typed))
      : [];
    openSuggest($('#search-suggest'), input, fields.map(f => ({
      label: `${sign}${f.name}:`,
      hint: f.hint ?? '',
      // A prefix is not an answer, so taking one fills in the prefix only and
      // leaves the caret after the colon, ready for the value.
      apply: () => replaceTerm(input, `${sign}${f.name}:`),
    })));
    return;
  }
  // The fields whose values do not repeat are not worth a list: a title or a
  // path is all but unique, and a numeric field takes `>1980` or `1970-1979`
  // as readily as a value, so years would sit in front of what is being typed
  // rather than help it. What is left is the set the sheet completes.
  if (!COMPLETES.has(term.field)) { closeSuggest(); return; }

  // Claim the box now, so a reply meant for a sheet field cannot land here.
  suggest.input = input;
  suggest.timer = setTimeout(async () => {
    const seq = ++suggest.seq;
    try {
      const items = await state.api.values(term.field, term.value, 8);
      if (seq !== suggest.seq || suggest.input !== input) return;
      if (document.activeElement !== input) return;
      // Typed on, or the caret moved to another term, while waiting.
      const now = termAt(input);
      if (now.field !== term.field || now.value !== term.value) return;
      // Offering exactly what is already typed is noise.
      openSuggest($('#search-suggest'), input, items
        .filter(v => v.value !== term.value)
        .map(v => ({ ...v, apply: () => replaceTerm(input, `${sign}${term.field}:${quoteValue(v.value)}`) })));
    } catch { closeSuggest(); }
  }, 120);
}

// The keys belong to the search field's own keydown listener, which has to see
// them before the search does; only the rest is wired here.
$('#search').addEventListener('input', () => searchSuggest($('#search')));
$('#search').setAttribute('autocomplete', 'off');
$('#search').addEventListener('blur', () => setTimeout(() => {
  if (suggest.input === $('#search')) closeSuggest();
}, 120));

// --- get info ---------------------------------------------------------

async function openInfo() {
  const ids = [...state.selected];
  if (!ids.length) return;
  state.editIndex = state.cursor;
  // Only the first few are fetched: the sheet needs to know whether values
  // agree, not to hold every track.
  const sample = await Promise.all(ids.slice(0, 50).map(id => state.api.getTrack(id).catch(() => null)));
  state.editing = sample.filter(Boolean);
  if (!state.editing.length) return;

  const multi = ids.length > 1;
  const first = state.editing[0];
  $('#info-title').textContent = multi ? `${ids.length} songs` : (first.title || '—');
  $('#info-artist').textContent = multi ? '' : (first.artist || '');
  $('#info-album').textContent = multi ? '' : (first.album || '');
  $('#info-note').textContent = multi
    ? 'Fields left blank are not changed.'
    : (first.writable ? '' : `${first.format} files cannot be written`);

  closeSuggest();
  buildFields('#details-grid', INFO_FIELDS);
  buildFields('#sorting-grid', SORT_FIELDS);
  buildFile(first, multi);
  loadInfoArt(first);
  paintStepper();
  // showModal on an already-open dialog throws; stepping and refreshing after
  // an edit both reuse this one. The tab only resets when the sheet is opened
  // fresh: changing a cover and being thrown back to Details, or stepping to
  // the next song and losing the artwork you were looking at, is the sheet
  // second-guessing what you were doing.
  if (!$('#info').open) {
    showTab('details');
    $('#info').showModal();
  }
}

// --- stepping through the results -------------------------------------
//
// The sheet walks whatever is currently listed, in the order it is listed, so
// the buttons follow the search and the sort rather than a copy of them.

function paintStepper() {
  const single = state.selected.size === 1 && state.editIndex >= 0;
  $('#info-prev').disabled = !single || state.editIndex <= 0;
  $('#info-next').disabled = !single || state.editIndex >= state.total - 1;
}

// stepInfo commits whatever was typed before moving, which is what the Get
// Info window in Apple Music does: moving on is not a way to lose an edit.
async function stepInfo(delta) {
  await saveInfo({ keepOpen: true });
  const to = state.editIndex + delta;
  if (to < 0 || to >= state.total) { $('#info').close(); return; }

  setCursor(to, false);
  await ensurePages();
  if (!trackAt(to)) return;          // the page never arrived; stay put
  await openInfo();
}

$('#info-prev').addEventListener('click', () => stepInfo(-1));
$('#info-next').addEventListener('click', () => stepInfo(1));

// --- clean up ---------------------------------------------------------
//
// Two calls to /v1/strip: the first is a dry run, so what is about to go can
// be read before it goes, and the second applies it. A backup is always taken,
// so a surprise is recoverable with `tagmgr restore -backup ID -apply`.

// infoSelector is the selection as the batch endpoints want it: a query when
// everything matching is selected, a list of ids otherwise.
function infoSelector() {
  return state.selectAll
    ? { query: fullQuery() || undefined, all: !fullQuery() }
    : { ids: [...state.selected] };
}

async function runStrip(dryRun) {
  const job = await state.api.strip(infoSelector(), { dryRun });
  const done = await state.api.waitJob(job.id, j =>
    status(`${dryRun ? 'Examining' : 'Cleaning'} ${j.progress.done} of ${j.progress.total}…`));
  if (done.state !== 'succeeded') throw new Error(done.error || 'the job failed');
  return done.result || {};
}

async function cleanUp() {
  const btn = $('#info-clean');
  btn.disabled = true;
  try {
    const plan = await runStrip(true);
    const lines = (plan.removed || [])
      .map(g => `  ${g.key}${g.meaning ? ` — ${g.meaning}` : ''}  (${g.tracks} ${g.tracks === 1 ? 'track' : 'tracks'})`);
    if (plan.normalized) {
      lines.push(`  ${plan.normalizeFields.join(', ')} — stored under an older name, will be moved` +
                 ` (${plan.normalized} ${plan.normalized === 1 ? 'track' : 'tracks'})`);
    }
    if (!lines.length) {
      status('Nothing to clean up — these tags are already the standard set', 'ok');
      return;
    }
    const n = plan.matched === 1 ? 'this song' : `${plan.matched} songs`;
    if (!confirm(`Clean up ${n}?\n\n${lines.join('\n')}\n\nA backup is kept, so this can be undone.`)) return;

    const res = await runStrip(false);
    const removed = res.bytesRemoved ? `, ${formatBytes(res.bytesRemoved)} removed` : '';
    status(`Cleaned ${res.changed} of ${res.matched}${removed} · backup ${res.backupId}`, 'ok');
    for (const id of state.selected) state.api.forgetArtwork(id);
    await refresh(false);
    if ($('#info').open) await openInfo();     // show the tidied values
  } catch (e) {
    status(e.message, 'err');
  } finally {
    btn.disabled = false;
  }
}

const formatBytes = n =>
  n < 1024 ? `${n} B` : n < 1048576 ? `${(n / 1024).toFixed(1)} kB` : `${(n / 1048576).toFixed(1)} MB`;

$('#info-clean').addEventListener('click', cleanUp);

// common returns the shared value of a field, and whether the tracks differ.
function common(field) {
  const vals = new Set(state.editing.map(t => String(valueOf(t, field) ?? '')));
  return vals.size === 1 ? [[...vals][0], false] : ['', true];
}

function valueOf(t, field) {
  switch (field) {
    // A flag is compared as the string it is sent as, so "unchanged" means the
    // same thing here as it does on the wire.
    case 'compilation': return t.compilation ? '1' : '0';
    case 'albumartist': return t.albumArtist;
    // The API spells these in camel case; the field name is what the edit
    // endpoint takes, so the two have to be bridged somewhere.
    case 'titlesort': return t.titleSort;
    case 'artistsort': return t.artistSort;
    case 'albumsort': return t.albumSort;
    case 'albumartistsort': return t.albumArtistSort;
    case 'composersort': return t.composerSort;
    case 'track': return t.track;
    case 'tracktotal': return t.trackTotal;
    case 'disc': return t.disc;
    case 'disctotal': return t.discTotal;
    default: return t[field];
  }
}

// buildFields fills one grid from a field list. Details and Sorting are the
// same widget twice over, so they share the builder and the save path finds
// both by looking for the inputs rather than by knowing where they live.
function buildFields(gridSel, fields) {
  const grid = $(gridSel);
  grid.replaceChildren();
  const writable = state.editing.every(t => t.writable);

  for (const f of fields) {
    grid.append(el('label', '', f.label));
    const [val, mixed] = common(f.field);

    if (f.check) {
      // A checkbox has a third state that a text field does not need: across a
      // mixed selection it is indeterminate, and saving leaves each track's own
      // answer alone unless it is actually clicked.
      const box = el('input');
      box.type = 'checkbox';
      box.dataset.field = f.field;
      box.checked = val === '1';
      box.indeterminate = mixed;
      box.disabled = !writable;
      const wrap = el('div', 'check');
      wrap.append(box, el('span', 'muted small', mixed ? 'Mixed' : f.hint));
      box.addEventListener('change', () => {
        wrap.querySelector('span').textContent = f.hint;
      });
      grid.append(wrap);
      continue;
    }

    const input = el('input');
    input.dataset.field = f.field;
    input.value = mixed ? '' : (val || '');
    input.disabled = !writable;
    if (mixed) { input.placeholder = 'Mixed'; input.classList.add('mixed'); }

    if (COMPLETES.has(f.field)) wireSuggest(input, f.field);

    if (f.pair) {
      const [pv, pm] = common(f.pair);
      const pair = el('div', 'pair');
      const of = el('input');
      of.dataset.field = f.pair;
      of.value = pm ? '' : (pv || '');
      of.disabled = !writable;
      if (pm) { of.placeholder = 'Mixed'; of.classList.add('mixed'); }
      pair.append(input, el('span', 'muted', 'of'), of);
      grid.append(pair);
    } else if (f.short) {
      const wrap = el('div', 'pair');
      input.style.width = '90px';
      wrap.append(input);
      grid.append(wrap);
    } else {
      grid.append(input);
    }
  }
}

function buildFile(t, multi) {
  const grid = $('#file-grid');
  grid.replaceChildren();
  const rows = multi
    ? [['songs', String(state.editing.length)]]
    : [
        ['kind', `${t.format.toUpperCase()} audio`],
        ['size', `${(t.size / 1048576).toFixed(1)} MB`],
        ['bit rate', t.bitrate ? `${t.bitrate} kbps` : '—'],
        ['sample rate', t.sampleRate ? `${(t.sampleRate / 1000).toFixed(1)} kHz` : '—'],
        ['channels', t.channels === 1 ? 'mono' : t.channels === 2 ? 'stereo' : String(t.channels || '—')],
        ['duration', formatTime(t.durationMs) || '—'],
        ['writable', t.writable ? 'yes' : `no — ${t.format} is read-only`],
        ['location', t.path],
      ];
  for (const [k, v] of rows) {
    grid.append(el('label', '', k), k === 'location' ? pathCell(v) : el('div', 'val', v));
  }
}

// Two 14px glyphs: the usual overlapping sheets, and the tick that replaces it
// for a moment after a copy. Inline so the page still has no second request.
const ICON_COPY =
  '<svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.4">' +
  '<rect x="5.2" y="5.2" width="8.3" height="8.3" rx="1.6"/>' +
  '<path d="M10.8 5.2V3.9A1.6 1.6 0 0 0 9.2 2.3H3.9A1.6 1.6 0 0 0 2.3 3.9v5.3a1.6 1.6 0 0 0 1.6 1.6h1.3"/></svg>';
const ICON_TICK =
  '<svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" ' +
  'stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">' +
  '<path d="M3 8.6l3.2 3.2L13 5"/></svg>';

// pathCell shows the location with a copy button beside it.
function pathCell(path) {
  const cell = el('div', 'val path');
  const btn = el('button', 'copy-btn');
  btn.type = 'button';
  btn.title = 'Copy path';
  btn.setAttribute('aria-label', 'Copy path');
  btn.innerHTML = ICON_COPY;
  btn.addEventListener('click', () => copyPath(path, btn));
  cell.append(el('span', 'path-text', path), btn);
  return cell;
}

// shellQuote wraps a path so it can be pasted into a shell.
//
// Single quotes rather than double: inside single quotes a shell expands
// nothing, so a path containing $, backticks or a backslash survives intact.
// The only character that needs care is the single quote itself, which is
// closed, escaped and reopened.
function shellQuote(path) {
  if (/^[A-Za-z0-9_@%+=:,.\/-]+$/.test(path)) return path;   // nothing to quote
  return `'${path.replace(/'/g, `'\\''`)}'`;
}

// copyPath puts the location on the clipboard, quoted when it needs to be.
//
// The confirmation happens in the button. Replacing the path itself with the
// word "Copied" moved the eye away from the thing just copied and hid it while
// you were still reading it.
async function copyPath(path, btn) {
  const text = shellQuote(path);
  if (!await writeClipboard(text)) {
    status('Could not copy — select the path and copy it yourself', 'err');
    return;
  }
  btn.classList.add('copied');
  btn.innerHTML = ICON_TICK;
  clearTimeout(btn.revert);
  btn.revert = setTimeout(() => {
    btn.classList.remove('copied');
    btn.innerHTML = ICON_COPY;
  }, 1200);
  status(`Copied ${text}`, 'ok');
}

// writeClipboard uses the async API where it is available and falls back
// otherwise.
//
// navigator.clipboard only exists in a secure context, and a server reached at
// http://nas.local is not one — which is exactly how this is meant to be used.
// The old execCommand path is deprecated and still the only thing that works
// there.
//
// The scratch element has to go inside the open dialog. A modal dialog makes
// the rest of the document inert, so a textarea appended to the body cannot be
// focused or selected, and execCommand then copies an empty selection and
// reports failure — the same top-layer trap that put the suggestion list
// behind the backdrop.
async function writeClipboard(text) {
  if (navigator.clipboard?.writeText) {
    try { await navigator.clipboard.writeText(text); return true; } catch { /* fall through */ }
  }
  const ta = document.createElement('textarea');
  ta.value = text;
  ta.setAttribute('readonly', '');
  ta.style.cssText =
    'position:fixed;top:0;left:0;width:1px;height:1px;padding:0;border:0;' +
    'opacity:0;user-select:text;-webkit-user-select:text';
  (document.querySelector('dialog[open]') || document.body).append(ta);
  ta.focus({ preventScroll: true });
  ta.setSelectionRange(0, ta.value.length);
  let ok = false;
  try { ok = document.execCommand('copy'); } catch { ok = false; }
  ta.remove();
  return ok;
}

async function loadInfoArt(t) {
  const small = $('#info-art');
  const large = $('#artwork-large');
  small.removeAttribute('src');
  large.removeAttribute('src');
  large.hidden = true;
  paintArtActions();

  const many = state.selected.size > 1;
  $('#artwork-hint').textContent = t.hasArt
    ? '' : 'Drop an image, paste one, or click to choose';
  $('#artwork-meta').textContent = t.hasArt ? 'Loading…' : 'No artwork';
  $('#artwork-note').textContent = many
    // Worth saying before the click, not after: this is the one edit that
    // moves the audio, because a cover does not fit in a tag's padding.
    ? `Changes apply to all ${state.selected.size.toLocaleString()} selected songs, and rewrite each file.`
    : 'Embedding a cover rewrites the file: it is far larger than the padding a tag reserves.';
  if (!t.hasArt) return;

  const url = await state.api.artwork(t.id);
  if (!url) { $('#artwork-meta').textContent = 'Artwork could not be read'; return; }
  small.src = url;
  large.src = url;
  large.hidden = false;
  $('#artwork-hint').textContent = '';
  large.onload = () => {
    $('#artwork-meta').textContent = `${large.naturalWidth} × ${large.naturalHeight}`;
  };
}

// --- artwork editing --------------------------------------------------
//
// One track goes through PUT and DELETE, which answer directly — a file that
// cannot be written comes back 422 rather than as a job that reports one
// failure. A selection goes through /v1/artwork/batch, whose selector is a
// query, so "every track on this album" costs the same to send as one.

const artDrop = $('#artwork-drop');

function paintArtActions() {
  if (!state.editing.length) return;
  const writable = state.editing.every(t => t.writable);
  const has = state.editing.some(t => t.hasArt);
  for (const id of ['#art-copy', '#art-paste', '#art-folder', '#art-remove']) {
    $(id).disabled = !writable;
  }
  // Copy reads rather than writes, and needs exactly one track to read from.
  $('#art-copy').disabled = state.editing.length !== 1 || !state.editing[0].hasArt;
  $('#art-remove').disabled = !writable || !has;
  artDrop.classList.toggle('busy', !writable);
}

// artApply runs one artwork change and puts the result on screen.
async function artApply(what, run) {
  artDrop.classList.add('busy');
  try {
    await run();
    for (const id of state.selected) state.api.forgetArtwork(id);
    status(what, 'ok');
    await refresh(false);
    if ($('#info').open) await openInfo();
  } catch (e) {
    status(e instanceof ApiError && e.status === 422
      ? `${e.message} — the artwork was not changed` : e.message, 'err');
  } finally {
    artDrop.classList.remove('busy');
  }
}

// setArtwork embeds an image file or pasted blob.
function setArtwork(file) {
  if (!file || !/^image\//.test(file.type)) {
    status('That is not an image', 'err');
    return;
  }
  const n = state.selected.size;
  if (n > 1 && !confirm(`Set this cover on ${n.toLocaleString()} songs?\n\n` +
      'Each file is rewritten, since a cover does not fit in the space a tag reserves.')) {
    return;
  }
  return artApply(n > 1 ? `Set the cover on ${n.toLocaleString()} songs` : 'Cover updated', async () => {
    if (n === 1) {
      await state.api.putArtwork(state.editing[0].id, file);
      return;
    }
    // The batch endpoint takes the image as base64 in JSON rather than as a
    // body of its own, since the selector has to travel with it.
    await waitArtJob(await state.api.batchArtwork(infoSelector(), 'upload', await toBase64(file)));
  });
}

// toBase64 reads a blob without blowing the stack on a large cover: spreading
// a megabyte-long byte array into String.fromCharCode passes a million
// arguments, which throws. FileReader hands back a data URL already encoded.
const toBase64 = file => new Promise((resolve, reject) => {
  const r = new FileReader();
  r.onload = () => resolve(String(r.result).split(',', 2)[1]);
  r.onerror = () => reject(new Error('the image could not be read'));
  r.readAsDataURL(file);
});

async function waitArtJob(job) {
  const done = await state.api.waitJob(job.id, j =>
    status(`Writing ${j.progress.done} of ${j.progress.total}…`));
  if (done.state !== 'succeeded') throw new Error(done.error || 'the job failed');
  const r = done.result || {};
  if (r.failed) throw new Error(`${r.failed} of ${r.matched} could not be written`);
  return r;
}

$('#artwork-file').addEventListener('change', e => {
  const file = e.target.files?.[0];
  e.target.value = '';                      // so the same file can be picked twice
  setArtwork(file);
});

// The drop target. Both dragover and dragenter must be cancelled or the
// browser navigates to the dropped file instead.
for (const type of ['dragenter', 'dragover']) {
  artDrop.addEventListener(type, e => {
    e.preventDefault();
    artDrop.classList.add('over');
  });
}
artDrop.addEventListener('dragleave', () => artDrop.classList.remove('over'));
artDrop.addEventListener('drop', e => {
  e.preventDefault();
  artDrop.classList.remove('over');
  setArtwork(e.dataTransfer?.files?.[0]);
});

// ⌘V with an image on the system clipboard, anywhere in the sheet.
$('#info').addEventListener('paste', e => {
  const file = [...(e.clipboardData?.files || [])].find(f => /^image\//.test(f.type));
  if (!file) return;
  e.preventDefault();
  showTab('artwork');
  setArtwork(file);
});

$('#art-copy').addEventListener('click', () => artApply('Cover copied to the clipboard', () =>
  state.api.copyArtworkFromTrack(state.editing[0].id)));

$('#art-paste').addEventListener('click', () => {
  const n = state.selected.size;
  if (n > 1 && !confirm(`Paste the clipboard cover onto ${n.toLocaleString()} songs?`)) return;
  artApply(`Pasted onto ${n.toLocaleString()} ${n === 1 ? 'song' : 'songs'}`, () =>
    state.api.batchArtwork(infoSelector(), 'clipboard').then(waitArtJob));
});

$('#art-folder').addEventListener('click', () => {
  const n = state.selected.size;
  if (!confirm(`Embed the cover image sitting beside ${n === 1 ? 'this song' : `these ${n.toLocaleString()} songs`}?\n\n` +
      'Looks for cover.jpg or folder.jpg in each track\u2019s own directory.')) return;
  artApply('Folder image embedded', () =>
    state.api.batchArtwork(infoSelector(), 'folder').then(waitArtJob));
});

$('#art-remove').addEventListener('click', () => {
  const n = state.selected.size;
  if (!confirm(`Remove the artwork from ${n === 1 ? 'this song' : `${n.toLocaleString()} songs`}?`)) return;
  artApply('Artwork removed', async () => {
    if (n === 1) {
      await state.api.deleteArtwork(state.editing[0].id);
      return;
    }
    await waitArtJob(await state.api.batchArtwork(infoSelector(), 'remove'));
  });
});

document.querySelectorAll('.tab').forEach(b =>
  b.addEventListener('click', () => showTab(b.dataset.tab)));

function showTab(name) {
  document.querySelectorAll('.tab').forEach(b =>
    b.setAttribute('aria-selected', String(b.dataset.tab === name)));
  document.querySelectorAll('.panel').forEach(p => { p.hidden = p.dataset.panel !== name; });
}

$('#info-cancel').addEventListener('click', () => $('#info').close());

// Holding shift while saving means "and on to the next one", for working
// down a list of songs without reaching for the mouse between each. The submit
// event carries no modifier state, so it is taken from whatever caused it.
let saveStep = 0;
$('#info-ok').addEventListener('click', e => { saveStep = e.shiftKey ? 1 : 0; });
$('#info-form').addEventListener('keydown', e => {
  if (e.key === 'Enter') saveStep = e.shiftKey ? 1 : 0;
});

// Enter saves, from anywhere in the sheet.
//
// The form previously used method="dialog", which meant enter in a text field
// closed the sheet and threw the edit away without a word. Submitting is now
// intercepted so that the keyboard and the OK button do the same thing.
$('#info-form').addEventListener('submit', e => {
  e.preventDefault();
  const step = saveStep;
  saveStep = 0;
  if (step) stepInfo(step);
  else saveInfo();
});

// Escape closes without saving, which is what a dialog's own cancel means.
$('#info').addEventListener('cancel', () => { /* default close is right */ });

// Restore the keyboard to the list once the sheet is gone.
$('#info').addEventListener('close', () => {
  if (state.view === 'songs') $('#scroller').focus({ preventScroll: true });
});

// saveInfo writes the changed fields.
//
// One track goes through PATCH with its version, so an edit made elsewhere
// since is refused rather than overwritten. Several go through the batch
// endpoint, which takes a selector rather than a list of values per track.
async function saveInfo({ keepOpen = false } = {}) {
  const changes = {};
  const inputs = document.querySelectorAll('#details-grid input, #sorting-grid input');
  for (const input of inputs) {
    const field = input.dataset.field;
    const [val, mixed] = common(field);

    if (input.type === 'checkbox') {
      // Still indeterminate means it was never touched, so every track keeps
      // its own answer. A flag is cleared by sending "0" rather than null:
      // null means "remove the tag", and readers treat a missing flag and a
      // false one alike, so "0" is the honest way to say no.
      if (input.indeterminate) continue;
      const now = input.checked ? '1' : '0';
      if (!mixed && now === String(val ?? '')) continue;
      changes[field] = now;
      continue;
    }

    const typed = input.value.trim();
    if (mixed && typed === '') continue;        // left alone across a mixed set
    if (!mixed && typed === String(val ?? '')) continue;  // unchanged
    changes[field] = typed === '' ? null : typed;
  }

  if (!Object.keys(changes).length) {
    if (!keepOpen) $('#info').close();
    return;
  }
  if (!keepOpen) $('#info').close();

  try {
    if (state.selected.size === 1 && state.editing.length === 1) {
      const t = state.editing[0];
      await state.api.patchTrack(t.id, changes, t.version);
      status('Saved', 'ok');
    } else {
      const job = await state.api.batchEdit(infoSelector(), changes);
      const done = await state.api.waitJob(job.id, j =>
        status(`Saving ${j.progress.done} of ${j.progress.total}…`));
      const r = done.result || {};
      if (done.state !== 'succeeded') throw new Error(done.error || 'the job failed');
      status(`Saved ${r.changed ?? 0} of ${r.matched ?? 0} songs` +
             (r.failed ? ` · ${r.failed} failed` : ''), r.failed ? 'err' : 'ok');
    }
    for (const id of state.selected) state.api.forgetArtwork(id);
    await refresh(false);
  } catch (e) {
    if (e instanceof ApiError && e.isConflict) {
      status('That song changed somewhere else since you opened it. Nothing was written — reopen it to see the current values.', 'err');
      await refresh(false);
    } else {
      status(e.message, 'err');
    }
  }
}

// --- live updates -----------------------------------------------------

// Another client editing the same library pushes an event; the cache is
// dropped rather than reconciled, since refetching a window is cheap.
async function listenForChanges() {
  state.eventsAbort?.abort();
  const ctrl = new AbortController();
  state.eventsAbort = ctrl;
  try {
    for await (const ev of state.api.events(ctrl.signal)) {
      if (ev.type === 'tracks.changed') {
        for (const id of ev.trackIds || []) state.api.forgetArtwork(id);
        if (!$('#info').open) await refresh(false);
      } else if (ev.type === 'catalog.replaced') {
        status('The library was rescanned');
        await Promise.all([loadFacets(), refresh(false)]);
      }
    }
  } catch { /* the stream ended or was aborted; nothing to recover */ }
}

// --- misc -------------------------------------------------------------

let statusTimer;
function status(msg, kind = '') {
  const bar = $('#status');
  bar.textContent = msg;
  bar.className = `statusbar ${kind}`;
  clearTimeout(statusTimer);
  if (kind !== 'err') statusTimer = setTimeout(() => { bar.textContent = ''; }, 4000);
}

$('#edit-btn').addEventListener('click', openInfo);

// Starting up.
//
// When this page is served by tagmgr itself the API is on the same origin, so
// the browser needs no CORS and the server needs no token — try that first and
// only ask if it does not answer.
renderHead();
boot();

async function boot() {
  const savedServer = localStorage.getItem('tagmgr.server');
  const savedToken = localStorage.getItem('tagmgr.token');
  const sameOrigin = location.protocol.startsWith('http') ? location.origin : '';

  const attempts = [];
  if (savedServer) attempts.push([savedServer, savedToken || '']);
  if (sameOrigin && sameOrigin !== savedServer) attempts.push([sameOrigin, savedToken || '']);

  for (const [server, token] of attempts) {
    try {
      await connect(server, token);
      return;
    } catch { /* try the next, then fall through to the form */ }
  }
  // Neither answered. Guess the API rather than the origin this page came
  // from: if the same origin had been the server, we would already be in.
  // Serving the app from a static file server on another port is the normal
  // reason to be here, so the same host on the default port is the better bet.
  $('#server').value = savedServer ||
    (sameOrigin ? `${location.protocol}//${location.hostname}:8467` : 'http://localhost:8467');
  $('#token').value = savedToken || '';
}

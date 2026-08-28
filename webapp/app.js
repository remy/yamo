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
  { field: 'comment',     label: 'comments' },
];

const ROW_H = 34;
const PAGE = 200;

const state = {
  api: null,
  view: 'songs',
  query: '',        // what the user typed
  facet: '',        // an extra term from a sidebar click
  sort: 'artist,album,track',
  total: 0,
  pages: new Map(), // page index -> array of tracks
  loading: new Set(),
  gen: 0,           // bumped when the query changes; stale replies are dropped
  selected: new Set(),
  lastIndex: -1,
  editing: [],      // tracks currently in the Get Info sheet
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
  await Promise.all([loadFacets(), refresh(true)]);
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

function renderHead() {
  const head = $('#thead');
  head.style.setProperty('--cols', COLUMNS.map(c => c.width).join(' '));
  $('#rows').style.setProperty('--cols', COLUMNS.map(c => c.width).join(' '));
  head.replaceChildren(...COLUMNS.map(c => {
    const b = el('button', c.num ? 'num' : '', c.label);
    const [field, desc] = state.sort.startsWith('-')
      ? [state.sort.slice(1).split(',')[0], true]
      : [state.sort.split(',')[0], false];
    if (c.sort === field) {
      b.dataset.active = '1';
      b.textContent = `${c.label} ${desc ? '▾' : '▴'}`;
    }
    b.addEventListener('click', () => {
      state.sort = (field === c.sort && !desc) ? `-${c.sort}` : c.sort;
      renderHead();
      refresh(true);
    });
    return b;
  }));
}

function render() {
  const sc = $('#scroller');
  $('#sizer').style.height = `${state.total * ROW_H}px`;
  const first = Math.max(0, Math.floor(sc.scrollTop / ROW_H) - 5);
  const count = Math.ceil(sc.clientHeight / ROW_H) + 10;
  const frag = document.createDocumentFragment();

  for (let i = first; i < Math.min(first + count, state.total); i++) {
    const t = trackAt(i);
    const row = el('div', 'row');
    row.style.top = `${i * ROW_H}px`;
    row.style.setProperty('--cols', COLUMNS.map(c => c.width).join(' '));
    if (!t) {
      row.classList.add('pending');
      row.append(...COLUMNS.map(() => el('span', '', '')));
    } else {
      row.dataset.id = t.id;
      row.dataset.index = i;
      if (state.selected.has(t.id)) row.setAttribute('aria-selected', 'true');
      for (const c of COLUMNS) {
        const cls = [c.num ? 'num' : '', c.dim ? 'dim' : ''].filter(Boolean).join(' ');
        row.append(el('span', cls, cellText(t, c.key)));
      }
    }
    frag.append(row);
  }
  $('#rows').replaceChildren(frag);
  $('#count').textContent = `${state.total.toLocaleString()} songs` +
    (state.selected.size ? ` · ${state.selected.size} selected` : '');
  $('#edit-btn').disabled = state.selected.size === 0;
}

function cellText(t, key) {
  switch (key) {
    case 'time': return formatTime(t.durationMs);
    case 'year': return t.year || '';
    default: return t[key] || '';
  }
}

const formatTime = ms => {
  if (!ms) return '';
  const s = Math.round(ms / 1000);
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`;
};

$('#scroller').addEventListener('scroll', () => { render(); ensurePages(); }, { passive: true });
window.addEventListener('resize', () => { render(); ensurePages(); });

// --- selection --------------------------------------------------------

$('#rows').addEventListener('click', e => {
  const row = e.target.closest('.row');
  if (!row?.dataset.id) return;
  const i = Number(row.dataset.index);
  const id = row.dataset.id;

  if (e.shiftKey && state.lastIndex >= 0) {
    const [a, b] = [state.lastIndex, i].sort((x, y) => x - y);
    for (let k = a; k <= b; k++) {
      const t = trackAt(k);
      if (t) state.selected.add(t.id);
    }
  } else if (e.metaKey || e.ctrlKey) {
    state.selected.has(id) ? state.selected.delete(id) : state.selected.add(id);
    state.lastIndex = i;
  } else {
    state.selected.clear();
    state.selected.add(id);
    state.lastIndex = i;
  }
  render();
});

$('#rows').addEventListener('dblclick', e => {
  if (e.target.closest('.row')?.dataset.id) openInfo();
});

document.addEventListener('keydown', e => {
  if ($('#info').open) return;
  if ((e.metaKey || e.ctrlKey) && e.key === 'i') { e.preventDefault(); openInfo(); }
  if ((e.metaKey || e.ctrlKey) && e.key === 'a' && state.view === 'songs') {
    e.preventDefault();
    selectAllMatching();
  }
});

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

let searchTimer;
$('#search').addEventListener('input', e => {
  clearTimeout(searchTimer);
  const v = e.target.value;
  // Debounced: over a network each keystroke is a round trip.
  searchTimer = setTimeout(() => {
    state.query = v;
    state.selectAll = false;
    state.selected.clear();
    refresh(true);
  }, 180);
});

async function loadFacets() {
  const render = (host, values, field) => {
    host.replaceChildren(...values.map(v => {
      const b = el('button', 'facet');
      b.append(el('span', 'name', v.value), el('span', 'n', v.count.toLocaleString()));
      const term = `${field}:"${v.value.replace(/"/g, '')}"`;
      b.setAttribute('aria-pressed', String(state.facet === term));
      b.addEventListener('click', () => {
        state.facet = state.facet === term ? '' : term;
        state.selected.clear();
        loadFacets();
        refresh(true);
      });
      return b;
    }));
  };
  const [genres, artists] = await Promise.all([
    state.api.values('genre', '', 20),
    state.api.values('artist', '', 40),
  ]);
  render($('#genres'), genres, 'genre');
  render($('#artists'), artists, 'artist');

  const s = await state.api.stats();
  $('#library-stats').textContent =
    `${s.tracks.toLocaleString()} songs · ${s.albums.toLocaleString()} albums`;
}

// --- views ------------------------------------------------------------

document.querySelectorAll('.nav').forEach(b => b.addEventListener('click', () => {
  document.querySelectorAll('.nav').forEach(x => x.setAttribute('aria-current', String(x === b)));
  state.view = b.dataset.view;
  $('#songs-view').hidden = state.view !== 'songs';
  $('#albums-view').hidden = state.view !== 'albums';
  refresh(true);
}));

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
    b.addEventListener('click', () => {
      // Each album carries a query that reselects exactly it.
      $('#search').value = a.query;
      state.query = a.query;
      state.view = 'songs';
      document.querySelectorAll('.nav').forEach(x => x.setAttribute('aria-current', String(x.dataset.view === 'songs')));
      $('#songs-view').hidden = false;
      $('#albums-view').hidden = true;
      refresh(true);
    });
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

// --- get info ---------------------------------------------------------

async function openInfo() {
  const ids = [...state.selected];
  if (!ids.length) return;
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

  buildDetails(multi);
  buildFile(first, multi);
  loadInfoArt(first);
  showTab('details');
  $('#info').showModal();
}

// common returns the shared value of a field, and whether the tracks differ.
function common(field) {
  const vals = new Set(state.editing.map(t => String(valueOf(t, field) ?? '')));
  return vals.size === 1 ? [[...vals][0], false] : ['', true];
}

function valueOf(t, field) {
  switch (field) {
    case 'albumartist': return t.albumArtist;
    case 'track': return t.track;
    case 'tracktotal': return t.trackTotal;
    case 'disc': return t.disc;
    case 'disctotal': return t.discTotal;
    default: return t[field];
  }
}

function buildDetails(multi) {
  const grid = $('#details-grid');
  grid.replaceChildren();
  const writable = state.editing.every(t => t.writable);

  for (const f of INFO_FIELDS) {
    grid.append(el('label', '', f.label));
    const [val, mixed] = common(f.field);
    const input = el('input');
    input.dataset.field = f.field;
    input.value = mixed ? '' : (val || '');
    input.disabled = !writable;
    if (mixed) { input.placeholder = 'Mixed'; input.classList.add('mixed'); }

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
    grid.append(el('label', '', k), el('div', 'val', v));
  }
}

async function loadInfoArt(t) {
  const small = $('#info-art');
  const large = $('#artwork-large');
  small.removeAttribute('src');
  large.removeAttribute('src');
  $('#artwork-meta').textContent = t.hasArt ? 'Loading…' : 'No artwork';
  if (!t.hasArt) return;
  const url = await state.api.artwork(t.id);
  if (!url) { $('#artwork-meta').textContent = 'Artwork could not be read'; return; }
  small.src = url;
  large.src = url;
  large.onload = () => {
    $('#artwork-meta').textContent = `${large.naturalWidth} × ${large.naturalHeight}`;
  };
}

document.querySelectorAll('.tab').forEach(b =>
  b.addEventListener('click', () => showTab(b.dataset.tab)));

function showTab(name) {
  document.querySelectorAll('.tab').forEach(b =>
    b.setAttribute('aria-selected', String(b.dataset.tab === name)));
  document.querySelectorAll('.panel').forEach(p => { p.hidden = p.dataset.panel !== name; });
}

$('#info-cancel').addEventListener('click', () => $('#info').close());
$('#info-ok').addEventListener('click', saveInfo);

// saveInfo writes the changed fields.
//
// One track goes through PATCH with its version, so an edit made elsewhere
// since is refused rather than overwritten. Several go through the batch
// endpoint, which takes a selector rather than a list of values per track.
async function saveInfo() {
  const changes = {};
  for (const input of $('#details-grid').querySelectorAll('input')) {
    const field = input.dataset.field;
    const [val, mixed] = common(field);
    const typed = input.value.trim();
    if (mixed && typed === '') continue;        // left alone across a mixed set
    if (!mixed && typed === String(val ?? '')) continue;  // unchanged
    changes[field] = typed === '' ? null : typed;
  }

  if (!Object.keys(changes).length) { $('#info').close(); return; }
  $('#info').close();

  try {
    if (state.selected.size === 1 && state.editing.length === 1) {
      const t = state.editing[0];
      await state.api.patchTrack(t.id, changes, t.version);
      status('Saved', 'ok');
    } else {
      const selector = state.selectAll
        ? { query: fullQuery() || undefined, all: !fullQuery() }
        : { ids: [...state.selected] };
      const job = await state.api.batchEdit(selector, changes);
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

// Reconnect on load if the details are remembered.
renderHead();
const savedServer = localStorage.getItem('tagmgr.server');
const savedToken = localStorage.getItem('tagmgr.token');
$('#server').value = savedServer || 'http://localhost:8467';
$('#token').value = savedToken || '';
if (savedServer) {
  connect(savedServer, savedToken || '').catch(() => { /* fall through to the form */ });
}

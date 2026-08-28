// A client for the tagmgr API.
//
// Two things about this API are awkward from a browser and are handled here so
// the rest of the app need not think about them:
//
//   1. Every request needs an Authorization header, which means artwork cannot
//      be an <img src> — it has to be fetched and turned into a blob URL.
//   2. For the same reason EventSource cannot be used for /v1/events; a fetch
//      with a stream reader is used instead.

export class ApiError extends Error {
  constructor(status, payload, fallback) {
    super(payload?.error || fallback || `HTTP ${status}`);
    this.status = status;
    this.code = payload?.code || '';
    this.jobId = payload?.jobId || '';
    this.expected = payload?.expected;
    this.actual = payload?.actual;
  }
  get isConflict() { return this.status === 409; }
  get isAuth() { return this.status === 401; }
  get isNotFound() { return this.status === 404; }
}

export class Api {
  constructor(base, token) {
    this.base = String(base || '').replace(/\/+$/, '');
    this.token = token || '';
    this._art = new Map();      // track id -> blob URL
    this._artOrder = [];        // insertion order, for eviction
  }

  get headers() {
    const h = {};
    if (this.token) h.Authorization = `Bearer ${this.token}`;
    return h;
  }

  async request(method, path, { body, headers = {}, signal } = {}) {
    const init = { method, headers: { ...this.headers, ...headers }, signal };
    if (body !== undefined) {
      init.body = typeof body === 'string' ? body : JSON.stringify(body);
      init.headers['Content-Type'] = init.headers['Content-Type'] || 'application/json';
    }
    const res = await fetch(this.base + path, init);
    if (!res.ok) {
      let payload = null;
      try { payload = await res.json(); } catch { /* not JSON; the status will do */ }
      throw new ApiError(res.status, payload, res.statusText);
    }
    if (res.status === 204) return null;
    const type = res.headers.get('Content-Type') || '';
    return type.includes('json') ? res.json() : res.text();
  }

  // --- reading ---------------------------------------------------------

  listTracks({ q = '', sort = '', limit = 100, offset = 0, signal } = {}) {
    const p = new URLSearchParams();
    if (q) p.set('q', q);
    if (sort) p.set('sort', sort);
    p.set('limit', limit);
    p.set('offset', offset);
    return this.request('GET', `/v1/tracks?${p}`, { signal });
  }

  getTrack(id) { return this.request('GET', `/v1/tracks/${encodeURIComponent(id)}`); }

  listAlbums({ q = '', limit = 200, offset = 0, signal } = {}) {
    const p = new URLSearchParams();
    if (q) p.set('q', q);
    p.set('limit', limit);
    p.set('offset', offset);
    return this.request('GET', `/v1/albums?${p}`, { signal });
  }

  values(field, prefix = '', limit = 12) {
    const p = new URLSearchParams();
    if (prefix) p.set('prefix', prefix);
    p.set('limit', limit);
    return this.request('GET', `/v1/values/${encodeURIComponent(field)}?${p}`);
  }

  stats() { return this.request('GET', '/v1/stats'); }
  scanStatus() { return this.request('GET', '/v1/scans'); }

  // --- writing ---------------------------------------------------------

  // patchTrack sends the version the client last saw. The server refuses if
  // the file changed underneath, which is what stops two people editing the
  // same track from silently overwriting one another.
  patchTrack(id, changes, version) {
    const headers = version ? { 'If-Match': `"${version}"` } : {};
    return this.request('PATCH', `/v1/tracks/${encodeURIComponent(id)}`, { body: changes, headers });
  }

  // batchEdit applies one set of changes to a selection. The selection may be
  // a query rather than a list of ids, so "every Elvis track" costs the same
  // to send whether it matches ten or ten thousand.
  batchEdit(selector, set) {
    return this.request('POST', '/v1/tracks/batch', { body: { selector, set } });
  }

  // strip removes every tag outside the keep list, and with normalize moves
  // kept values held under an older name into the standard one. dryRun reports
  // what would go without touching anything.
  strip(selector, { dryRun = true, normalize = true, backup = true } = {}) {
    return this.request('POST', '/v1/strip', { body: { selector, dryRun, normalize, backup } });
  }

  job(id) { return this.request('GET', `/v1/jobs/${encodeURIComponent(id)}`); }

  // waitJob polls until a job finishes.
  async waitJob(id, onProgress) {
    for (;;) {
      const j = await this.job(id);
      onProgress?.(j);
      if (j.state !== 'running') return j;
      await new Promise(r => setTimeout(r, 150));
    }
  }

  // --- artwork ---------------------------------------------------------

  // artwork returns a blob URL for a track's cover, or null when it has none.
  //
  // A blob is unavoidable: <img src> cannot carry the Authorization header, so
  // the bytes have to be fetched and handed to the DOM as an object URL.
  async artwork(id) {
    if (this._art.has(id)) return this._art.get(id);
    let url = null;
    try {
      const res = await fetch(`${this.base}/v1/tracks/${encodeURIComponent(id)}/artwork`, { headers: this.headers });
      if (res.ok) url = URL.createObjectURL(await res.blob());
    } catch { /* offline or refused; treated as no artwork */ }
    this._cacheArt(id, url);
    return url;
  }

  // putArtwork replaces one track's cover with raw image bytes.
  //
  // The body is the image itself rather than a multipart form: the server
  // sniffs the format from the content, so there is nothing else to send.
  async putArtwork(id, blob) {
    const res = await fetch(`${this.base}/v1/tracks/${encodeURIComponent(id)}/artwork`, {
      method: 'PUT',
      headers: { ...this.headers, 'Content-Type': blob.type || 'application/octet-stream' },
      body: blob,
    });
    if (!res.ok) {
      let payload = null;
      try { payload = await res.json(); } catch { /* the status will do */ }
      throw new ApiError(res.status, payload, res.statusText);
    }
    this.forgetArtwork(id);
    return res.json();
  }

  async deleteArtwork(id) {
    await this.request('DELETE', `/v1/tracks/${encodeURIComponent(id)}/artwork`);
    this.forgetArtwork(id);
  }

  // batchArtwork sets or clears artwork across a selection and returns a job.
  // source is clipboard, upload, folder or remove; upload carries the image.
  batchArtwork(selector, source, imageBase64) {
    const body = { selector, source };
    if (imageBase64) body.image = imageBase64;
    return this.request('POST', '/v1/artwork/batch', { body });
  }

  // The clipboard is the server's, not the browser's, which is the point: a
  // cover copied in the terminal can be pasted here and the other way round.
  copyArtworkFromTrack(id) {
    return this.request('PUT', `/v1/clipboard/artwork/from-track/${encodeURIComponent(id)}`);
  }

  async clipboardArtwork() {
    try {
      const res = await fetch(`${this.base}/v1/clipboard/artwork`, { headers: this.headers });
      if (!res.ok) return null;
      return URL.createObjectURL(await res.blob());
    } catch {
      return null;
    }
  }

  _cacheArt(id, url) {
    this._art.set(id, url);
    this._artOrder.push(id);
    // Object URLs hold their blob in memory until revoked, so the cache is
    // bounded and evicted entries are released rather than merely dropped.
    while (this._artOrder.length > 120) {
      const old = this._artOrder.shift();
      const u = this._art.get(old);
      if (u) URL.revokeObjectURL(u);
      this._art.delete(old);
    }
  }

  forgetArtwork(id) {
    const u = this._art.get(id);
    if (u) URL.revokeObjectURL(u);
    this._art.delete(id);
  }

  // --- events ----------------------------------------------------------

  // events streams catalogue changes, so an edit made in the terminal or on
  // another device shows up here without polling.
  //
  // EventSource cannot send an Authorization header, so this reads the stream
  // by hand. The format is simple enough that parsing it is a few lines.
  async *events(signal) {
    const res = await fetch(`${this.base}/v1/events`, { headers: this.headers, signal });
    if (!res.ok || !res.body) throw new ApiError(res.status, null, 'event stream refused');
    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buf = '';
    for (;;) {
      const { value, done } = await reader.read();
      if (done) return;
      buf += decoder.decode(value, { stream: true });
      let cut;
      while ((cut = buf.indexOf('\n\n')) >= 0) {
        const block = buf.slice(0, cut);
        buf = buf.slice(cut + 2);
        for (const line of block.split('\n')) {
          if (line.startsWith('data: ')) {
            try { yield JSON.parse(line.slice(6)); } catch { /* keep-alive or partial */ }
          }
        }
      }
    }
  }
}

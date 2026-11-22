'use strict';
// The cliamp-web page, ported from the MiSTer Subsonic web remote: same
// shape (pure testable core, one EventSource with backoff and a watchdog,
// hash routing, imperative rows). The data layer is the cliamp bridge:
// snapshot state, {op, params} commands, library lists.

if (typeof history !== 'undefined' && location.search) history.replaceState(null, '', location.pathname);

// ---- pure ----

function initial() {
  return { state: null, at: 0, queue: { tracks: [], play_next: [], playlist_revision: 0, count: 0 }, connected: false };
}

// reduce applies one event to the page state and returns the new state.
// Events: {type:'state', data, now} full snapshot; {type:'tick', data}
// partial (position pulses); {type:'queue', data}; {type:'conn', up}.
function forgetRevision(s) {
  return s.state ? { state: Object.assign({}, s.state, { revision: 0 }) } : {};
}

function reduce(s, ev) {
  // never let an older snapshot replace a newer one (a command reply is taken
  // before playback starts and can arrive after the SSE state event)
  if ((ev.type === 'state' || ev.type === 'tick') && ev.data && s.state &&
      Number(ev.data.revision) > 0 && Number(ev.data.revision) < Number(s.state.revision)) return s;
  if (ev.type === 'state') return Object.assign({}, s, { state: ev.data, at: ev.now });
  if (ev.type === 'tick') return Object.assign({}, s, { state: Object.assign({}, s.state, ev.data), at: ev.now });
  if (ev.type === 'queue') return Object.assign({}, s, { queue: ev.data });
  if (ev.type === 'conn') return Object.assign({}, s, { connected: ev.up },
    ev.up ? forgetRevision(s) : {});
  // the player (re)connected or the bridge resynced: cliamp may have
  // restarted and counts revisions from 1 again, so older numbers are fine
  if (ev.type === 'reset') return Object.assign({}, s, forgetRevision(s));
  return s;
}

// cliamp omits zero values from its snapshot: the first track has no `index`
// and 0 dB has no `volume`. Absent therefore means 0, not unknown.
function curIndex(st) {
  if (!st) return -1;
  if (st.index != null) return Number(st.index);
  return st.track ? 0 : -1;
}
function curVolume(st) { return st && st.volume != null ? Number(st.volume) : 0; }

// ---- art ----
// v changes whenever what the bridge answers for a URL changes (art is cached for
// a day): v=2 retired the album covers once served as discs; v=3: albums without a disc now get a generated one.
const ART_V = 3;
const artUrl = (kind, path) => '/api/art/' + kind + '?v=' + ART_V + '&path=' + encodeURIComponent(path);

// The disc is a platter driven by a motor, so its speed is not set, it is
// approached. spinStep moves {w: deg/s, a: deg/s^2} toward `target` as a
// critically damped system (no overshoot, no jolt: acceleration starts and ends
// at zero), solved exactly so the result does not depend on the frame rate.
function spinStep(m, target, k, dt) {
  const e0 = m.w - target, c = m.a + k * e0, f = Math.exp(-k * dt);
  return { w: Math.max(0, target + (e0 + c * dt) * f), a: (m.a - k * c * dt) * f };
}
const SPIN_DEG_PER_S = 200; // 33 1/3 rpm
// spinPlan: starting is a firm motor push, pausing a brake, stopping a coast.
function spinPlan(state, reduced) {
  if (state === 'playing') return { target: reduced ? 0 : SPIN_DEG_PER_S, k: 4 };
  if (state === 'paused') return { target: 0, k: 5 };
  return { target: 0, k: 2.2 };
}

// ---- fullscreen ----
// nextTrack is the row that will play after the current one, or null when it
// is not knowable: shuffle hides the real order from us, repeat-one replays
// the same song. Play-next entries always go first.
function nextTrack(st, q) {
  if (!st || !q) return null;
  const pn = q.play_next || [];
  if (pn.length) return pn[0];
  if (!st.track || st.shuffle || String(st.repeat || 'Off') === 'One') return null;
  const rows = q.tracks || [], i = curIndex(st);
  if (i < 0 || !rows.length) return null;
  if (i + 1 < rows.length) return rows[i + 1];
  return String(st.repeat) === 'All' ? rows[0] : null;
}

// techLine is 'FLAC • 1161 kbps • 24 bit • 44.1 kHz', leaving out what is unknown.
function techLine(info) {
  if (!info) return '';
  const kHz = (hz) => (hz / 1000).toFixed(1).replace(/\.0$/, '') + ' kHz';
  const ch = info.channels === 1 ? 'mono' : info.channels > 2 ? info.channels + ' ch' : '';
  return [info.format, info.bitrate_kbps && info.bitrate_kbps + ' kbps', info.bit_depth && info.bit_depth + ' bit',
    info.sample_rate && kHz(info.sample_rate), ch].filter(Boolean).join(' • ');
}

// ---- radio ----
// radioNow: what to show for a playing station. cliamp puts the stream's own
// "Artist - Title" (or "Title by Artist") in the artist field and the station
// name in the title. Returns null for anything that is not a station.
function radioNow(song) {
  const pm = (song && song.provider_meta) || {};
  const station = (song && (song.station || pm['radio.name'])) || '';
  if (!song || !song.stream || !station) return null;
  const raw = String(song.artist || '').trim();
  let title = raw, artist = '', m;
  if ((m = raw.match(/^(.+?)\s+[-–]\s+(.+)$/))) { artist = m[1]; title = m[2]; }
  else if ((m = raw.match(/^(.+)\s+by\s+(.+)$/i))) { title = m[1]; artist = m[2]; }
  const place = [pm['radio.country'], pm['radio.state']].filter(Boolean).join(', ');
  if (!raw) return { title: station, artist: '', album: place };
  return { title, artist: artist || station, album: [artist ? station : '', place].filter(Boolean).join(' · ') };
}
// streamInfo: format and bitrate cliamp already knows about a station
function streamInfo(song) {
  const pm = (song && song.provider_meta) || {};
  const o = {};
  if (pm['radio.codec']) o.format = String(pm['radio.codec']).toUpperCase();
  const br = parseInt(pm['radio.bitrate'], 10);
  if (br > 0) o.bitrate_kbps = br;
  return o;
}
// stationSub: the line under a station tile, from the directory's details
const stationSub = (m) => (m ? [m.codec, m.bitrate > 0 && m.bitrate + 'k', m.country, m.found !== false && !m.online && 'offline'].filter(Boolean).join(' · ') : '');
// facetValues: the distinct values of one facet over the items, most common first
function facetValues(items, get) {
  const n = new Map();
  for (const o of items) for (const v of get(o) || []) if (v) n.set(v, (n.get(v) || 0) + 1);
  return [...n].sort((a, b) => b[1] - a[1] || String(a[0]).localeCompare(String(b[0]))).map(([v, c]) => ({ v, n: c }));
}
// Sort and filter presets of the collection pages. Rows carry `attrs` (artist, genre,
// decade, year, tracks, albums, section) that the sorts and facets read.
const nameCmp = (a, b) => String(a.title || '').localeCompare(String(b.title || ''), undefined, { numeric: true, sensitivity: 'base' });
const numCmp = (key, desc) => (a, b) => {
  const x = a.attrs && a.attrs[key], y = b.attrs && b.attrs[key];
  if (!x && !y) return 0;
  if (!x) return 1; // unknown goes last either way
  if (!y) return -1;
  return desc ? y - x : x - y;
};
const textCmp = (key) => (a, b) => String((a.attrs && a.attrs[key]) || '').localeCompare(String((b.attrs && b.attrs[key]) || ''), undefined, { sensitivity: 'base' });
const attrFacet = (id, label) => ({ id, label, get: (o) => [o.attrs && o.attrs[id]] });
const SORT_NAME = { id: 'name', label: 'Name', cmp: nameCmp };
const LIST_VIEWS = {
  // loadAll: these lists are small enough to load completely, so the drop-downs list every artist and genre
  albums: { search: true, loadAll: true, sorts: [SORT_NAME, { id: 'artist', label: 'Artist', cmp: (a, b) => textCmp('artist')(a, b) || nameCmp(a, b) },
    { id: 'new', label: 'Release year, newest', cmp: numCmp('year', true) }, { id: 'old', label: 'Release year, oldest', cmp: numCmp('year', false) },
    { id: 'tracks', label: 'Most tracks', cmp: numCmp('tracks', true) }],
    facets: [attrFacet('artist', 'Artist'), attrFacet('genre', 'Genre'), attrFacet('decade', 'Decade'), attrFacet('folder', 'Folder')] },
  artists: { search: true, loadAll: true, sorts: [SORT_NAME, { id: 'albums', label: 'Most albums', cmp: numCmp('albums', true) }], facets: [attrFacet('folder', 'Folder')] },
  playlists: { search: true, loadAll: true, sorts: [SORT_NAME, { id: 'tracks', label: 'Most tracks', cmp: numCmp('tracks', true) }], facets: [attrFacet('section', 'Section')] },
  'artist-albums': { search: true, loadAll: true, sorts: [{ id: 'new', label: 'Newest first', cmp: numCmp('year', true) }, { id: 'old', label: 'Oldest first', cmp: numCmp('year', false) }, SORT_NAME] },
  catalog: { search: true, sorts: [SORT_NAME] },
  favorites: { search: true, sorts: [SORT_NAME] },
};
// the provider's own album orders (it sorts the whole library, not just what is loaded)
const ALBUM_ORDERS = [['', 'All albums'], ['newest', 'Newest added'], ['recent', 'Recently played'], ['frequent', 'Most played'], ['starred', 'Starred']];

// viewRows: the items to show under a sort, facet choices and "hide offline"
function viewRows(all, view, sorts, facets) {
  let rows = all.filter((o) => {
    if (view.hideDead && o.dead) return false;
    if (view.q && !(String(o.title || '') + ' ' + String(o.sub || '')).toLowerCase().includes(view.q.toLowerCase())) return false;
    for (const f of facets || []) { const want = view.facets[f.id]; if (want && !(f.get(o) || []).includes(want)) return false; }
    return true;
  });
  const sort = (sorts || []).find((x) => x.id === view.sort && x.cmp);
  if (sort) rows = rows.map((o, i) => [o, i]).sort((a, b) => sort.cmp(a[0], b[0]) || a[1] - b[1]).map((x) => x[0]);
  return rows;
}

// fsLayout: with room for two squares side by side show both art faces.
function fsLayout(w, h) { return w >= 700 && h > 0 && w / h >= 1.25 ? 'both' : 'single'; }

// ---- settings ----
// deviceLabel: a readable name for a PulseAudio/PipeWire sink such as
// "alsa_output.usb-Topping_E30-00.HiFi__Headphones__sink" -> "Topping E30 · HiFi Headphones"
function deviceLabel(name) {
  let n = String(name || '').replace(/^alsa_output\./, '').replace(/^alsa_card\./, '');
  const parts = n.split('.');
  const cut = (x) => x.replace(/^(usb|pci|platform)-/, '').replace(/-\d+$/, '').replace(/__+/g, ' ').replace(/[_-]+/g, ' ').replace(/\bsink\b/i, '').replace(/\s+/g, ' ').trim();
  const out = parts.map(cut).filter(Boolean);
  return out.length ? out.join(' · ') : String(name || '');
}
const EQ_FREQS = ['70', '180', '320', '600', '1k', '3k', '6k', '12k', '14k', '16k']; // Hz
const EQ_PRESETS = ['Flat', 'Rock', 'Pop', 'Jazz', 'Classical', 'Bass Boost', 'Treble Boost', 'Vocal', 'Electronic', 'Acoustic', 'Hip-Hop', 'R&B', 'Loudness', 'Late Night', 'Podcast', 'Small Speakers'];
const SPEEDS = [0.5, 0.75, 1, 1.25, 1.5, 2];
const speedLabel = (v) => (Number(v) || 1) + '×';
const fmtBytes = (n) => { n = Number(n) || 0; return n < 1024 ? n + ' B' : n < 1048576 ? (n / 1024).toFixed(0) + ' KB' : n < 1073741824 ? (n / 1048576).toFixed(1) + ' MB' : (n / 1073741824).toFixed(2) + ' GB'; };
function fmtUptime(secs) {
  const s = Math.max(0, Math.floor(Number(secs) || 0)), d = Math.floor(s / 86400), h = Math.floor((s % 86400) / 3600), m = Math.floor((s % 3600) / 60);
  return d ? d + ' d ' + h + ' h' : h ? h + ' h ' + m + ' min' : m ? m + ' min' : s + ' s';
}
// the choices of this browser (the bridge keeps no per-user settings)
const PREF_VIEW = 'pref:view', PREF_FSIDLE = 'pref:fsidle';
const getPref = (k, def) => { try { const v = localStorage.getItem(k); return v === null ? def : v; } catch (e) { return def; } };
const setPref = (k, v) => { try { localStorage.setItem(k, v); } catch (e) { /* storage may be off */ } };

// ---- tile view ----
const viewModeKey = (...parts) => 'view:' + parts.join(':');
// placeholderStyle gives a tile without a picture its own colour, the same one every time.
function placeholderStyle(name) {
  let h = 5381;
  for (const ch of String(name || '')) h = ((h * 33) ^ ch.codePointAt(0)) >>> 0;
  return { hue: h % 360, sat: 32, light: 26 };
}
const itemArtUrl = (kind, provider, id) => (provider === 'navidrome' && id && ['album', 'artist', 'playlist'].includes(kind)
  ? '/api/art/item?provider=navidrome&kind=' + kind + '&id=' + encodeURIComponent(id) : '');
// a radio station's logo, found by the bridge from its (catalog) name
const stationArtUrl = (name) => (name ? '/api/art/station?name=' + encodeURIComponent(name) : '');
const folderArtUrl = (rel) => '/api/files/art?path=' + encodeURIComponent(rel);
// albumFacts: what an album page says about itself, from its tracks (year and genre of
// the first track that has one; length is the sum of the known durations)
const albumFacts = (tracks) => {
  const ts = tracks || [], first = (k) => { const t = ts.find((x) => x && x[k]); return t ? t[k] : ''; };
  const secs = ts.reduce((a, t) => a + (Number(t && t.duration_secs) || 0), 0);
  return { name: first('album'), artist: first('artist'), year: first('year') || '', genre: first('genre'), count: ts.length, secs };
};
// albumFactsLine: 'Metal · 1987 · 5 tracks · 21 min'
const albumFactsLine = (f) => [f.genre, f.year, f.count ? plural(f.count, 'track') : '', f.secs ? (f.secs >= 3600 ? Math.floor(f.secs / 3600) + ' h ' + Math.round((f.secs % 3600) / 60) + ' min' : Math.max(1, Math.round(f.secs / 60)) + ' min') : ''].filter(Boolean).join(' · ');
// nextChunk: how many more rows of an already loaded list to draw
const nextChunk = (total, shown, size) => Math.max(0, Math.min(size, total - shown));

// ---- file browser ----
// Paths are relative to the music root; a folder's place is one encoded hash segment.
const filesRel = (parent, name) => (parent ? parent + '/' + name : name);
const filesHash = (rel) => (rel ? '#browse/files/' + encodeURIComponent(rel) : '#browse/files');
const filesParent = (rel) => (rel === '' ? null : rel.includes('/') ? rel.slice(0, rel.lastIndexOf('/')) : '');
function filterEntries(entries, q) {
  const needle = String(q || '').trim().toLowerCase();
  return (entries || []).filter((e) => !needle || e.name.toLowerCase().includes(needle));
}

// ---- queue view ----
// queueView turns cliamp's two lists into the one the page shows, in play
// order. The live playlist holds every track; the up-next list holds references
// into it (by `index`) that play before the rest. So: played tracks first, the
// current one, the up-next ones in their order, then whatever follows. A queued
// row is NOT played just because it sits before the current index: appended
// tracks are played from the end of the list while earlier ones still wait.
// Entries: {t, title, index (live), status: played|current|next|later, pn (position in up-next, -1 if not)}.
function queueView(st, q) {
  const rows = (q && q.tracks) || [];
  if (!rows.length) return [];
  const cur = st && st.track ? curIndex(st) : -1;
  const queuedAt = new Map();
  ((q && q.play_next) || []).forEach((e, p) => {
    const i = e.index == null ? 0 : Number(e.index);
    if (i >= 0 && i < rows.length && i !== cur && !queuedAt.has(i)) queuedAt.set(i, p);
  });
  const entry = (i, status) => ({ t: rows[i], title: rows[i].title, index: i, status, pn: queuedAt.has(i) ? queuedAt.get(i) : -1 });
  const out = [], used = new Set();
  const add = (i, status) => { used.add(i); out.push(entry(i, status)); };

  if (st && st.shuffle && Array.isArray(q.played)) {
    // Shuffle plays in an order the list does not show, so "before the current
    // index" means nothing. The bridge records what really played, oldest first.
    for (const path of q.played) {
      const i = rows.findIndex((r, j) => r.path === path && j !== cur && !queuedAt.has(j) && !used.has(j));
      if (i >= 0) add(i, 'played');
    }
  } else {
    for (let i = 0; i < rows.length && i < cur; i++) if (!queuedAt.has(i)) add(i, 'played');
  }
  if (cur >= 0 && cur < rows.length) add(cur, 'current');
  Array.from(queuedAt.entries()).sort((a, b) => a[1] - b[1]).forEach(([i]) => add(i, 'next'));
  for (let i = 0; i < rows.length; i++) if (!used.has(i)) add(i, 'later');
  return out;
}

// ---- item menu ----
// rowActions is the menu of one item: Play (clear the queue, add, play) and
// Add to queue everywhere they make sense, plus the extras of each kind.
function rowActions(spec) {
  const A = (id, label, icon, more) => Object.assign({ id, label, icon }, more);
  const fav = spec.fav === undefined ? [] : [A('fav', spec.fav ? 'Remove from favorites' : 'Favorite', spec.fav ? 'heartfill' : 'heart')];
  switch (spec.kind) {
    case 'track': return [A('play', 'Play', 'play'), A('add', 'Add to queue', 'add'), ...fav];
    case 'album': case 'playlist': case 'artist': return [A('play', 'Play', 'play'), A('add', 'Add to queue', 'add'), ...fav];
    case 'radio': return [A('play', 'Play', 'play'), ...fav];
    case 'file': case 'folder': return [A('play', 'Play', 'play'), A('add', 'Add to queue', 'add')];
    case 'eq': return ['Custom', ...EQ_PRESETS].filter((n) => n !== 'Custom' || spec.current === 'Custom' || !EQ_PRESETS.includes(spec.current))
      .map((n) => A('eq:' + n, n, n === spec.current ? 'check' : 'blank', { disabled: n === 'Custom' }));
    case 'queue': {
      const st = spec.status, acts = [];
      if (st !== 'current') acts.push(A('playfrom', 'Play from here', 'play'));
      if (st === 'played' || st === 'later') acts.push(A('next', 'Play next', 'next'));
      if (st === 'next' || st === 'later') acts.push(A('up', 'Move up', 'up', { disabled: !spec.canUp }), A('down', 'Move down', 'down', { disabled: !spec.canDown }));
      acts.push(A('remove', 'Remove', 'trash', { danger: true }));
      return acts;
    }
  }
  return [];
}
// narrow or touch screens get a bottom sheet, wide pointer screens a popover
const menuMode = (width, coarse) => (coarse || width < 600 ? 'sheet' : 'popover');
// menuPlacement puts a popover under its button, right edges aligned, flipping
// upward near the bottom and never leaving the window.
function menuPlacement(a, size, view) {
  const edge = 8, gap = 4;
  const left = Math.max(edge, Math.min(a.left + a.width - size.width, view.width - size.width - edge));
  let top = a.top + a.height + gap, flipped = false;
  if (top + size.height > view.height - edge) { top = a.top - size.height - gap; flipped = true; }
  return { left, top: Math.max(edge, top), flipped };
}
// menuKey moves the focus through n items (wrapping, skipping disabled ones).
function menuKey(i, n, key, disabled) {
  if (n <= 0) return -1;
  const off = (j) => !!(disabled && disabled[j]);
  const step = (from, dir) => { for (let k = 1; k <= n; k++) { const j = (((from + dir * k) % n) + n) % n; if (!off(j)) return j; } return from; };
  switch (key) {
    case 'ArrowDown': return step(i, 1);
    case 'ArrowUp': return i < 0 ? step(0, -1) : step(i, -1);
    case 'Home': return off(0) ? step(0, 1) : 0;
    case 'End': return off(n - 1) ? step(n - 1, -1) : n - 1;
  }
  return i;
}
// progressText is the toast for a background Play / Add to queue.
function progressText(ev, mode) {
  const f = (n) => Number(n || 0).toLocaleString('en-US');
  switch (ev.state) {
    case 'running': return ev.total ? 'Adding ' + f(ev.done) + ' / ' + f(ev.total) + '…' : 'Adding…';
    case 'done': return mode === 'play' ? 'Playing' : 'Added ' + f(ev.total) + (ev.total === 1 ? ' track' : ' tracks');
    case 'cancelled': return 'Stopped at ' + f(ev.done) + ' of ' + f(ev.total);
    default: return 'Could not ' + (mode === 'play' ? 'play' : 'add') + ': ' + (ev.error || 'unknown error');
  }
}

// ---- favorites ----
const favKey = (kind, provider, id) => kind + ':' + provider + ':' + id;
// A track is identified by its provider's id, else (a local file) by its path.
function trackFavKey(t) {
  if (!t) return '';
  const meta = t.provider_meta || {};
  for (const k of Object.keys(meta)) if (k.endsWith('.id') && meta[k]) return favKey('track', k.slice(0, -3), meta[k]);
  return t.path ? favKey('track', 'local', t.path) : '';
}
const favKeys = (items) => new Set((items || []).map((it) => favKey(it.kind, it.provider, it.id)));
const favFilter = (items, kind) => (items || []).filter((it) => kind === 'all' || it.kind === kind);
function favCounts(items) {
  const c = { all: 0, track: 0, album: 0, artist: 0, playlist: 0, radio: 0 };
  for (const it of items || []) { c.all++; if (it.kind in c) c[it.kind]++; }
  return c;
}
// the playing station is a favorite when a radio favorite's id is 'f:' + its stream URL
const isPlayingFavStation = (items, track) =>
  !!(track && track.path && (items || []).some((it) => it.kind === 'radio' && it.id === 'f:' + track.path));

// ---- skipping ----
// albumKey identifies the disc a track belongs to: same key, same disc.
const albumKey = (song) => (song && song.album ? song.album + '|' + (song.year || '') : '');
// artPlan says what a track change has to refresh: pictures belong to the album,
// so tracks of the same album keep the cover and the disc they have.
const artPlan = (prevKey, key, coverOk, discOn) => {
  const same = key !== '' && key === prevKey;
  return { needCover: !(same && coverOk), needDisc: !(same && discOn) };
};
// trackDir is which way the player moved through the queue: -1 for previous.
const trackDir = (prevIdx, idx) => (prevIdx != null && idx < prevIdx ? -1 : 1);
// skipDip: a skip makes a turning platter stumble to a third of its speed (the
// motor then brings it back); one that is already stopping is left alone.
const skipDip = (w) => (w > 5 ? w * 0.35 : w);

// position is the song position in seconds at time now, moved on from the
// last snapshot while playing. cliamp counts and seeks in seconds.
function position(s, now) {
  const st = s.state;
  if (!st || !st.track) return 0;
  const dur = Number(st.duration) || 0;
  let p = Number(st.position) || 0;
  if (st.state === 'playing') p += (now - s.at) / 1000;
  return Math.max(0, dur ? Math.min(p, dur) : p);
}

// backoff is the wait before reconnect number n (from 0): 1, 2, 4 ... 30 s.
function backoff(n) { return Math.min(30000, 1000 * Math.pow(2, n)); }

// The bridge sends a named ping every 15 s on a quiet stream; a stream that
// has said nothing for this long is dead (a phone that left the wifi never
// gets an error from EventSource).
const WATCHDOG_MS = 40000;
function makeWatchdog(clock) {
  let last = 0;
  return {
    heard() { last = clock(); },
    stale() { return clock() - last > WATCHDOG_MS; },
  };
}

// splitPath decodes the parts of a browse path, or gives null on a bad
// escape (the page then shows the Browse home).
function splitPath(rest) {
  try { return rest.split('/').map(decodeURIComponent); } catch (e) { return null; }
}

const TABS = ['now', 'queue', 'favorites', 'browse', 'search', 'settings'];
// parseHash maps '#', '#queue' or '#browse/prov/kind/id' to a route.
function parseHash(h) {
  const rest = String(h || '').replace(/^#/, '');
  const tab = TABS.includes(rest.split('/')[0]) ? rest.split('/')[0] : 'now';
  return tab === 'now' && rest !== 'now' ? { tab: 'now', rest: '' } : { tab, rest: tab === 'now' ? '' : rest.slice(tab.length + 1) };
}

// fmtTime renders seconds as m:ss (h:mm:ss past an hour).
function fmtTime(secs) {
  const s = Math.max(0, Math.round(Number(secs) || 0));
  const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), ss = String(s % 60).padStart(2, '0');
  return h ? h + ':' + String(m).padStart(2, '0') + ':' + ss : m + ':' + ss;
}

// lyricIndex: index of the line active at seek (last start <= seek), -1 before all.
function lyricIndex(lines, seek) {
  let idx = -1;
  for (let i = 0; i < lines.length; i++) {
    if (lines[i].start <= seek) idx = i;
    else break;
  }
  return idx;
}

// moveTarget is the {from, to} of moving row i one step (dir -1 or 1) —
// cliamp swaps the two rows — or null at the ends.
function moveTarget(i, n, dir) {
  const to = i + dir;
  return to < 0 || to >= n ? null : { from: i, to };
}

const plural = (n, w) => n + ' ' + w + (n === 1 ? '' : 's');

// makeApi wraps fetch: JSON in and out, and one error type
// with the status, the server's detail, and stale (409 revision clash).
function makeApi(fetchFn) {
  async function call(path, init) {
    const r = await fetchFn(path, Object.assign({ headers: { 'Content-Type': 'application/json' } }, init));
    if (!r.ok) {
      let detail = '';
      try {
        const j = await r.json();
        if (j && typeof j.detail === 'string') detail = j.detail;
      } catch (e) { /* non-JSON body */ }
      const err = new Error(detail || 'http ' + r.status);
      err.status = r.status;
      err.detail = detail;
      err.stale = r.status === 409;
      throw err;
    }
    return r.json();
  }
  return {
    get: (path) => call(path),
    post: (path, body) => call(path, { method: 'POST', body: JSON.stringify(body) }),
    put: (path, body) => call(path, { method: 'PUT', body: JSON.stringify(body) }),
  };
}

if (typeof module !== 'undefined') {
  module.exports = { initial, reduce, viewModeKey, albumFacts, albumFactsLine, placeholderStyle, itemArtUrl, stationArtUrl, folderArtUrl, nextChunk, filesRel, filesHash, filesParent, filterEntries, queueView, rowActions, menuMode, menuPlacement, menuKey, progressText, favKey, trackFavKey, favKeys, favFilter, favCounts, isPlayingFavStation, artPlan, albumKey, trackDir, skipDip, nextTrack, techLine, deviceLabel, EQ_FREQS, EQ_PRESETS, SPEEDS, speedLabel, fmtBytes, fmtUptime, LIST_VIEWS, ALBUM_ORDERS, nameCmp, numCmp, radioNow, streamInfo, stationSub, facetValues, viewRows, fsLayout, artUrl, spinStep, spinPlan, curIndex, curVolume, position, backoff, WATCHDOG_MS, makeWatchdog, splitPath, TABS, parseHash, fmtTime, lyricIndex, moveTarget, plural, makeApi };
}

// ---- the page ----

if (typeof document !== 'undefined') (function () {
  const api = makeApi((u, o) => fetch(u, o));
  const $ = (id) => document.getElementById(id);
  let S = initial();

  const ICONS = {
    play: 'M8 5v14l11-7z', pause: 'M6 19h4V5H6v14zm8-14v14h4V5h-4z',
    next: 'M6 18l8.5-6L6 6v12zM16 6v12h2V6h-2z', prev: 'M6 6h2v12H6zm3.5 6l8.5 6V6z',
    shuffle: 'M10.59 9.17L5.41 4 4 5.41l5.17 5.17 1.42-1.41zM14.5 4l2.04 2.04L4 18.59 5.41 20 17.96 7.46 20 9.5V4h-5.5zm.33 9.41l-1.41 1.41 3.13 3.13L14.5 20H20v-5.5l-2.04 2.04-3.13-3.13z',
    repeat: 'M7 7h10v3l4-4-4-4v3H5v6h2V7zm10 10H7v-3l-4 4 4 4v-3h12v-6h-2v4z',
    more: 'M6 10a2 2 0 1 0 0 4 2 2 0 0 0 0-4zm12 0a2 2 0 1 0 0 4 2 2 0 0 0 0-4zm-6 0a2 2 0 1 0 0 4 2 2 0 0 0 0-4z',
    note: 'M12 3v10.55A4 4 0 1 0 14 17V7h4V3h-6z',
    lyrics: 'M14 17H4v2h10v-2zm6-8H4v2h16V9zM4 15h16v-2H4v2zM4 5v2h16V5H4z',
    check: 'M9 16.2 4.8 12l-1.4 1.4L9 19 21 7l-1.4-1.4z',
    blank: '',
    tune: 'M3 17v2h6v-2H3zM3 5v2h10V5H3zm10 16v-2h8v-2h-8v-2h-2v6h2zM7 9v2H3v2h4v2h2V9H7zm14 4v-2H11v2h10zm-6-4h2V7h4V5h-4V3h-2v6z',
    gear: 'M19.14 12.94c.04-.3.06-.61.06-.94s-.02-.64-.07-.94l2.03-1.58a.49.49 0 0 0 .12-.61l-1.92-3.32a.49.49 0 0 0-.59-.22l-2.39.96a7.03 7.03 0 0 0-1.62-.94l-.36-2.54a.48.48 0 0 0-.48-.41h-3.84c-.24 0-.43.17-.47.41l-.36 2.54c-.59.24-1.13.56-1.62.94l-2.39-.96a.48.48 0 0 0-.59.22L2.74 8.87a.48.48 0 0 0 .12.61l2.03 1.58c-.05.3-.09.63-.09.94s.02.64.07.94l-2.03 1.58a.49.49 0 0 0-.12.61l1.92 3.32c.12.22.37.29.59.22l2.39-.96c.5.38 1.03.7 1.62.94l.36 2.54c.05.24.24.41.48.41h3.84c.24 0 .44-.17.47-.41l.36-2.54c.59-.24 1.13-.56 1.62-.94l2.39.96c.22.08.47 0 .59-.22l1.92-3.32a.48.48 0 0 0-.12-.61l-2.01-1.58zM12 15.6A3.6 3.6 0 1 1 12 8.4a3.6 3.6 0 0 1 0 7.2z',
    queue: 'M3 13h2v-2H3v2zm0 4h2v-2H3v2zm0-8h2V7H3v2zm4 4h14v-2H7v2zm0 4h14v-2H7v2zM7 7v2h14V7H7z',
    browse: 'M4 6H2v14c0 1.1.9 2 2 2h14v-2H4V6zm16-4H8c-1.1 0-2 .9-2 2v12c0 1.1.9 2 2 2h12c1.1 0 2-.9 2-2V4c0-1.1-.9-2-2-2zm-1 9H9V9h10v2zm-4 4H9v-2h6v2zm4-8H9V5h10v2z',
    search: 'M15.5 14h-.79l-.28-.27A6.47 6.47 0 0 0 16 9.5 6.5 6.5 0 1 0 9.5 16c1.61 0 3.09-.59 4.23-1.57l.27.28v.79l5 4.99L20.49 19l-4.99-5zm-6 0C7.01 14 5 11.99 5 9.5S7.01 5 9.5 5 14 7.01 14 9.5 11.99 14 9.5 14z',
    expand: 'M7 14H5v5h5v-2H7v-3zm-2-4h2V7h3V5H5v5zm12 7h-3v2h5v-5h-2v3zM14 5v2h3v3h2V5h-5z',
    collapse: 'M5 16h3v3h2v-5H5v2zm3-8H5v2h5V5H8v3zm6 11h2v-3h3v-2h-5v5zm2-11V5h-2v5h5V8h-3z',
    add: 'M19 13h-6v6h-2v-6H5v-2h6V5h2v6h6v2z',
    up: 'M7.41 15.41L12 10.83l4.59 4.58L18 14l-6-6-6 6z',
    down: 'M7.41 8.59L12 13.17l4.59-4.58L18 10l-6 6-6-6z',
    trash: 'M6 19c0 1.1.9 2 2 2h8c1.1 0 2-.9 2-2V7H6v12zM19 4h-3.5l-1-1h-5l-1 1H5v2h14V4z',
    grid: 'M3 3h8v8H3V3zm10 0h8v8h-8V3zM3 13h8v8H3v-8zm10 0h8v8h-8v-8z',
    radio: 'M3.24 6.15C2.51 6.43 2 7.17 2 8v12c0 1.1.89 2 2 2h16c1.11 0 2-.9 2-2V8c0-1.1-.89-2-2-2H8.3l8.26-3.34L15.88 1 3.24 6.15zM7 20c-1.66 0-3-1.34-3-3s1.34-3 3-3 3 1.34 3 3-1.34 3-3 3zm13-8h-2v-2h-2v2H4V8h16v4z',
    person: 'M12 12c2.21 0 4-1.79 4-4s-1.79-4-4-4-4 1.79-4 4 1.79 4 4 4zm0 2c-2.67 0-8 1.34-8 4v2h16v-2c0-2.66-5.33-4-8-4z',
    album: 'M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm0 14.5c-2.49 0-4.5-2.01-4.5-4.5S9.51 7.5 12 7.5s4.5 2.01 4.5 4.5-2.01 4.5-4.5 4.5zm0-5.5c-.55 0-1 .45-1 1s.45 1 1 1 1-.45 1-1-.45-1-1-1z',
    folder: 'M10 4H4c-1.1 0-2 .9-2 2v12c0 1.1.9 2 2 2h16c1.1 0 2-.9 2-2V8c0-1.1-.9-2-2-2h-8l-2-2z',
    heart: 'M16.5 3c-1.74 0-3.41.81-4.5 2.09C10.91 3.81 9.24 3 7.5 3 4.42 3 2 5.42 2 8.5c0 3.78 3.4 6.86 8.55 11.54L12 21.35l1.45-1.32C18.6 15.36 22 12.28 22 8.5 22 5.42 19.58 3 16.5 3zm-4.4 15.55l-.1.1-.1-.1C7.14 14.24 4 11.39 4 8.5 4 6.5 5.5 5 7.5 5c1.54 0 3.04.99 3.57 2.36h1.87C13.46 5.99 14.96 5 16.5 5c2 0 3.5 1.5 3.5 3.5 0 2.89-3.14 5.74-7.9 10.05z',
    heartfill: 'M12 21.35l-1.45-1.32C5.4 15.36 2 12.28 2 8.5 2 5.42 4.42 3 7.5 3c1.74 0 3.41.81 4.5 2.09C13.09 3.81 14.76 3 16.5 3 19.58 3 22 5.42 22 8.5c0 3.78-3.4 6.86-8.55 11.54L12 21.35z',
    back: 'M20 11H7.83l5.59-5.59L12 4l-8 8 8 8 1.41-1.41L7.83 13H20v-2z',
  };
  function svg(name) {
    const NS = 'http://www.w3.org/2000/svg';
    const s = document.createElementNS(NS, 'svg'), p = document.createElementNS(NS, 'path');
    s.setAttribute('viewBox', '0 0 24 24');
    s.setAttribute('aria-hidden', 'true');
    p.setAttribute('d', ICONS[name]);
    s.appendChild(p);
    return s;
  }
  function setIcon(el, name) { el.replaceChildren(svg(name), ...Array.from(el.children).filter((c) => c.tagName === 'B')); }
  function h(tag, props, ...kids) {
    const el = document.createElement(tag);
    for (const k in props || {}) {
      const v = props[k];
      if (v == null || v === false) continue;
      if (k === 'class') el.className = v;
      else if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
      else el.setAttribute(k, v === true ? '' : v);
    }
    for (const k of kids.flat()) if (k != null && k !== false) el.append(k);
    return el;
  }

  // ---- feedback ----
  function toast(msg, err) {
    const t = h('div', { class: 'toast' + (err ? ' err' : '') }, msg);
    $('toasts').append(t);
    while ($('toasts').children.length > 3) $('toasts').firstChild.remove();
    setTimeout(() => t.remove(), 2600);
  }
  function fail(e) {
    if (e.stale) { toast('The queue changed', true); loadQueue(); } else toast(e.message || String(e), true);
  }
  async function attempt(fn) { try { return await fn(); } catch (e) { fail(e); } }
  // cmd sends one operation; queue edits carry the revision guard. cliamp
  // answers commands with an optional fresh snapshot, which merges in.
  async function cmd(op, params, guard) {
    const p = Object.assign({}, params);
    if (guard) p.if_revision = S.queue.playlist_revision;
    const r = await attempt(() => api.post('/api/command', { op, params: p }));
    if (r && r.job && r.job.snapshot) dispatch({ type: 'tick', data: r.job.snapshot, now: now() });
    return r;
  }
  const rev = () => loadQueue();

  // ---- state ----
  function dispatch(ev) {
    const prev = S;
    S = reduce(S, ev);
    if (ev.type === 'state' || ev.type === 'tick') {
      renderNow();
      if (setUi && document.body.dataset.tab === 'settings') setUi.paintSound();
      if (curIndex(prev.state) !== curIndex(S.state)) renderQueue();
    } else if (ev.type === 'queue') {
      renderQueue();
      renderNext();
    } else if (ev.type === 'conn') {
      $('banner').hidden = ev.up;
    }
  }
  const now = () => performance.now();
  async function loadState() { const st = await attempt(() => api.get('/api/state')); if (st) dispatch({ type: 'state', data: st, now: now() }); }
  async function loadQueue() { const q = await attempt(() => api.get('/api/queue')); if (q) dispatch({ type: 'queue', data: q }); }

  // The event stream: one live EventSource, exponential backoff on error,
  // watchdog reopen when frames go stale, immediate reopen when the tab
  // becomes visible again (iOS freezes sockets without an error).
  let es = null, tries = 0, wasDown = false, isUp = false, retryTimer = 0;
  const dog = makeWatchdog(now);
  function down() {
    if (es) es.close();
    es = null;
    wasDown = true;
    isUp = false;
    dispatch({ type: 'conn', up: false });
    clearTimeout(retryTimer);
    retryTimer = setTimeout(connect, backoff(tries++));
  }
  function connect() {
    dog.heard();
    const mine = es = new EventSource('/api/events');
    const up = () => {
      dog.heard();
      if (isUp) return;
      isUp = true;
      tries = 0;
      dispatch({ type: 'conn', up: true });
      if (wasDown) { wasDown = false; providers = null; loadState(); loadQueue(); } // may be another server
    };
    mine.addEventListener('state', (e) => { up(); dispatch({ type: 'state', data: JSON.parse(e.data), now: now() }); });
    mine.addEventListener('playback', (e) => { up(); dispatch({ type: 'tick', data: JSON.parse(e.data), now: now() }); });
    mine.addEventListener('playlist', () => { up(); loadQueue(); });
    mine.addEventListener('resync', () => { up(); dispatch({ type: 'reset' }); loadState(); loadQueue(); loadFavs(); });
    mine.addEventListener('favorites', () => { up(); loadFavs(); });
    mine.addEventListener('enqueue', (e) => { up(); onEnqueue(JSON.parse(e.data)); });
    mine.addEventListener('player', (e) => {
      up();
      const d = JSON.parse(e.data);
      const was = S.state && S.state.player;
      dispatch({ type: 'tick', data: { player: d.status }, now: now() });
      dispatch({ type: 'reset' });
      if (was && was !== d.status) toast(d.status === 'online' ? 'player online' : 'player offline', d.status !== 'online');
    });
    mine.addEventListener('ping', () => up());
    mine.onerror = () => { if (es === mine) down(); };
  }
  setInterval(() => { if (es && dog.stale()) down(); }, 5000);
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'visible' && dog.stale()) { tries = 0; down(); clearTimeout(retryTimer); connect(); }
  });

  // ---- Now Playing art ----
  // The platter keeps turning (or coasting) whichever side is showing, like
  // the real thing; `art` holds its state between animation frames.
  const art = { path: null, seq: 0, coverOk: false, disc: false, showDisc: false, flipping: false,
    angle: 0, w: 0, a: 0, target: 0, k: 2.2, last: 0, raf: 0, info: null, infoTry: 0, infoDur: -1, infoAt: 0, infoBusy: false, pref: false,
    albumKey: '', idx: null, chain: Promise.resolve(), swapping: false,
    discPending: false }; // true from a track change until the disc of the new album has replaced the old one
  try { art.pref = sessionStorage.getItem('cliamp-disc') === '1'; } catch (e) { /* storage may be off */ }
  const fsOn = () => $('now').classList.contains('fs');
  const fsBoth = () => fsOn() && $('now').dataset.art === 'both';
  const reducedMotion = () => typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches;
  function loadImg(url, seq, ok, no) {
    const im = new Image();
    im.onload = () => { if (seq === art.seq) ok(url); };
    im.onerror = () => { if (seq === art.seq) no(); };
    im.src = url;
  }
  // Which faces show: normally one (cover, or disc after a tap). Fullscreen on a
  // wide window shows both; on a narrow one the cover with the disc peeking
  // out from behind it, or the disc alone after a tap.
  function paintArt() {
    const box = $('cover'), both = fsBoth(), peekable = !both;
    const discOnly = art.showDisc && art.disc && !both;
    const coverShown = art.coverOk && !discOnly;
    const discShown = art.disc; // behind the cover, beside it, or alone: always drawn when there is one
    const flippable = art.disc && !both;
    box.classList.toggle('has-disc', flippable);
    box.classList.toggle('show-disc', discOnly);
    box.classList.toggle('peek', peekable && art.disc && !discOnly);
    box.tabIndex = flippable ? 0 : -1;
    if (flippable) { box.setAttribute('role', 'button'); box.setAttribute('aria-label', discOnly ? 'Disc, tap for cover' : 'Cover, tap for disc'); }
    else { box.removeAttribute('role'); box.setAttribute('aria-label', 'Cover'); }
    $('art-disc').hidden = !discShown;
    $('art-cover').hidden = !coverShown;
    box.querySelector('.ph').hidden = discOnly || coverShown;
  }
  function renderTech() {
    const song = S.state && (S.state.track || S.state.logical_track);
    const t = techLine(Object.assign({}, streamInfo(song), art.info)); // what the stream or file says wins over cliamp's catalog
    $('tech').textContent = t;
    $('tech').hidden = !t;
  }
  function renderNext() {
    const n = nextTrack(S.state, S.queue);
    $('next').hidden = !(n && n.title);
    if (n && n.title) $('next').textContent = 'Next: ' + n.title + (n.artist ? ' — ' + n.artist : '');
  }
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
  const discSlide = () => $('art-disc').parentElement;
  const isPlaying = () => !!(S.state && S.state.state === 'playing');
  // Narrow layouts (normal and fullscreen) show the disc peeking behind the
  // cover; the wide fullscreen layout shows it beside the cover.
  const discPeeks = () => $('cover').classList.contains('peek');

  // One album change moves the cover and the disc together: both leave (the
  // disc under the cover, or sliding off to the side the queue is moving away
  // from), the pictures swap while hidden, both come back from the other side.
  const OUT = { duration: 260, easing: 'cubic-bezier(.4,0,1,1)', fill: 'forwards' };
  const IN = { duration: 320, easing: 'cubic-bezier(0,0,.2,1)' };
  const gone = (dir) => ({ opacity: 0, transform: 'translateX(' + (-dir * 16) + '%) scale(.9)' });
  const came = (dir) => ({ opacity: 0, transform: 'translateX(' + (dir * 16) + '%) scale(.9)' });
  const rest = { opacity: 1, transform: 'translateX(0) scale(1)' };
  const done = (an) => an.finished.catch(() => {});
  const release = (el) => el.getAnimations().forEach((an) => an.cancel()); // drop the hold from OUT

  async function retractDisc(dir) {
    if (discPeeks()) { $('cover').classList.add('swap'); $('cover').classList.remove('out'); await sleep(340); return; }
    await done(discSlide().animate([rest, gone(dir)], OUT));
  }
  async function extendDisc(dir) {
    if (discPeeks()) { $('cover').classList.toggle('out', isPlaying()); await sleep(340); $('cover').classList.remove('swap'); }
    else await done(discSlide().animate([came(dir), rest], IN));
    release(discSlide());
  }
  async function retractCover(dir) {
    $('fs-bg').animate([{ opacity: 1 }, { opacity: 0.15 }], OUT);
    // opaque for most of the exit, so the disc sliding under it never shows through
    await done($('art-cover').animate([rest, { opacity: 1, offset: 0.6 }, gone(dir)], OUT));
  }
  async function extendCover(dir) {
    $('fs-bg').animate([{ opacity: 0.15 }, { opacity: 1 }], IN).finished.catch(() => {}).then(() => release($('fs-bg')));
    await done($('art-cover').animate([came(dir), { opacity: 1, offset: 0.35 }, rest], IN)); // opaque early, same reason
    release($('art-cover'));
  }

  // The pictures of a new album arrive together and are shown in order; a
  // change overtaken by a newer track is skipped, the newest one does the work.
  function showAlbum(seq, dir, plan, coverUrl, discUrl) {
    art.chain = art.chain.then(async () => {
      if (seq !== art.seq) return;
      art.swapping = true;
      const calm = reducedMotion();
      const hadCover = plan.needCover && art.coverOk && !$('art-cover').hidden && !calm;
      const hadDisc = plan.needDisc && art.disc && !$('art-disc').hidden && !calm;
      try {
        if (hadDisc && isPlaying()) { art.w = skipDip(art.w); art.a = 0; } // the platter stumbles
        await Promise.all([hadCover && retractCover(dir), hadDisc && retractDisc(dir)]);
        if (plan.needCover) {
          if (coverUrl) { $('art-cover').src = coverUrl; art.coverOk = true; $('fs-bg').style.backgroundImage = 'url("' + coverUrl + '")'; }
          else { art.coverOk = false; $('fs-bg').style.backgroundImage = ''; }
        }
        if (plan.needDisc) {
          if (discUrl) { $('art-disc').src = discUrl; if (!art.disc) art.showDisc = art.pref; art.disc = true; }
          else { art.disc = false; art.showDisc = false; art.w = art.a = 0; }
        }
        paintArt();
        await Promise.all([hadCover && coverUrl && !$('art-cover').hidden && extendCover(dir), hadDisc && discUrl && extendDisc(dir)]);
        if (hadDisc && !discUrl) { $('cover').classList.remove('swap'); release(discSlide()); }
      } finally {
        $('cover').classList.remove('swap');
        art.swapping = false;
        if (seq === art.seq) art.discPending = false;
        setSpin(S.state && S.state.state);
      }
    });
  }
  const loadUrl = (url) => new Promise((resolve) => { const im = new Image(); im.onload = () => resolve(url); im.onerror = () => resolve(null); im.src = url; });

  function updateArt(song) {
    const p = (song && song.path) || '';
    if (p === art.path) return;
    art.path = p;
    const seq = ++art.seq;
    // a station's logo is small and rarely square: show it whole
    $('art-cover').classList.toggle('logo', /^https?:/.test(p) && !/\/rest\/stream/.test(p));
    if (!p) {
      art.coverOk = art.disc = art.showDisc = false; art.info = null; art.albumKey = ''; art.idx = null;
      $('artist-img').hidden = true; $('fs-bg').style.backgroundImage = '';
      paintArt(); renderTech(); setSpin(null);
      return;
    }
    const key = albumKey(song), idx = curIndex(S.state), dir = trackDir(art.idx, idx);
    const plan = artPlan(art.albumKey, key, art.coverOk, art.disc);
    art.albumKey = key;
    art.idx = idx;
    art.discPending = art.disc && plan.needDisc; // the disc on show is about to be replaced: do not start it
    if (/^https?:/.test(p) && !/\/rest\/stream/.test(p)) $('artist-img').hidden = true; // a station has no artist photo
    else loadImg(artUrl('artist', p), seq, (u) => { $('artist-img').src = u; $('artist-img').hidden = false; }, () => { $('artist-img').hidden = true; });
    if (plan.needCover && !art.coverOk) {
      // nothing on show to animate away: the cover appears as soon as it is loaded
      plan.needCover = false;
      loadUrl(artUrl('cover', p)).then((cu) => {
        if (seq !== art.seq || !cu) return;
        $('art-cover').src = cu; art.coverOk = true; $('fs-bg').style.backgroundImage = 'url("' + cu + '")'; paintArt();
      });
    }
    if (plan.needCover || plan.needDisc) {
      Promise.all([plan.needCover ? loadUrl(artUrl('cover', p)) : null, plan.needDisc ? loadUrl(artUrl('disc', p)) : null])
        .then(([cu, du]) => { if (seq === art.seq) showAlbum(seq, dir, plan, cu, du); });
    } else if (isPlaying() && !reducedMotion()) {
      // same album, same pictures: the platter just stumbles and recovers
      art.w = skipDip(art.w); art.a = 0; setSpin('playing');
    }
    art.info = null; art.infoTry = 0; art.infoDur = -1; art.infoBusy = false;
    loadInfo();
  }
  // The tech line comes from the bridge, which needs the track to be known and
  // (for MP3 and the like) the duration. Both may be missing right after a
  // track change, so ask again when the duration shows up or after a failure.
  function loadInfo() {
    const p = art.path, seq = art.seq;
    if (!p || art.infoBusy) return;
    const dur = Number(S.state && S.state.duration) || 0;
    art.infoBusy = true; art.infoDur = dur; art.infoAt = Date.now(); art.infoTry++;
    api.get('/api/trackinfo?path=' + encodeURIComponent(p) + (dur ? '&duration=' + dur : ''))
      .then((info) => { if (seq === art.seq) { art.info = info; renderTech(); } })
      .catch(() => { if (seq === art.seq && !art.info) renderTech(); })
      .then(() => { if (seq === art.seq) art.infoBusy = false; });
  }
  function refreshInfo() {
    if (!art.path || art.infoBusy) return;
    const dur = Number(S.state && S.state.duration) || 0;
    if (dur > 0 && art.infoDur === 0) loadInfo();
    else if (!art.info && art.infoTry < 4 && Date.now() - art.infoAt > 2500) loadInfo();
  }
  async function flipDisc() {
    if (!art.disc || art.flipping || fsBoth()) return;
    try { sessionStorage.setItem('cliamp-disc', art.showDisc ? '0' : '1'); } catch (e) { /* storage may be off */ }
    art.pref = !art.showDisc;
    if (reducedMotion()) { art.showDisc = !art.showDisc; paintArt(); return; }
    art.flipping = true;
    const box = $('cover'), persp = 'perspective(2000px) ';
    try {
      await box.animate([{ transform: persp + 'rotateY(0deg)' }, { transform: persp + 'rotateY(90deg)' }],
        { duration: 180, easing: 'cubic-bezier(.4,0,1,1)' }).finished;
      art.showDisc = !art.showDisc;
      paintArt();
      await box.animate([{ transform: persp + 'rotateY(-90deg)' }, { transform: persp + 'rotateY(0deg)' }],
        { duration: 240, easing: 'cubic-bezier(0,0,.2,1)' }).finished;
    } catch (e) { /* animation cancelled: the swap above still happened or did not, either is consistent */ }
    art.flipping = false;
  }
  function setSpin(state) {
    const held = art.swapping || art.discPending;
    if (!held) $('cover').classList.toggle('out', state === 'playing'); // the peeking disc slides out while playing
    const plan = spinPlan(state, reducedMotion());
    // a resting disc that is about to be replaced must not start turning (or slide out) first
    if (art.discPending && art.w < 1) plan.target = 0;
    art.target = plan.target;
    art.k = plan.k;
    if (!art.disc) { art.w = art.a = 0; return; } // nothing to turn
    if (!art.raf) { art.last = 0; art.raf = requestAnimationFrame(spinFrame); }
  }
  function spinFrame(t) {
    const dt = art.last ? Math.min(0.1, Math.max(0, (t - art.last) / 1000)) : 0;
    art.last = t;
    const before = art.w, m = spinStep({ w: art.w, a: art.a }, art.target, art.k, dt);
    art.w = m.w;
    art.a = m.a;
    art.angle = (art.angle + (before + m.w) / 2 * dt) % 360; // trapezoid: exact enough at 60 Hz
    $('art-disc').style.transform = 'rotate(' + art.angle.toFixed(2) + 'deg)';
    if (art.target === 0 && art.w < 0.05 && Math.abs(art.a) < 0.05) { art.w = art.a = 0; art.raf = 0; return; }
    art.raf = requestAnimationFrame(spinFrame);
  }

  // ---- Fullscreen ----
  function layoutFs() {
    if (!fsOn()) return;
    const l = fsLayout(innerWidth, innerHeight);
    if ($('now').dataset.art !== l) { $('now').dataset.art = l; paintArt(); }
  }
  function setFs(on) {
    if (on === fsOn()) return;
    $('now').classList.toggle('fs', on);
    if (on) fsWake(); else { clearTimeout(fsIdleTimer); $('now').classList.remove('fs-idle'); }
    document.body.classList.toggle('fs-open', on);
    if (on) {
      layoutFs(); art.infoTry = Math.min(art.infoTry, 2); refreshInfo();
      try { const r = document.documentElement.requestFullscreen && document.documentElement.requestFullscreen(); if (r && r.catch) r.catch(() => {}); } catch (e) { /* not available */ }
    } else {
      delete $('now').dataset.art;
      try { if (document.fullscreenElement) { const r = document.exitFullscreen(); if (r && r.catch) r.catch(() => {}); } } catch (e) { /* ignore */ }
    }
    paintArt();
  }
  // Controls hide after a few quiet seconds and return on mouse, touch, key or gamepad input.
  const fsIdleMs = () => { const v = Number(getPref(PREF_FSIDLE, '3.5')); return v > 0 ? v * 1000 : 0; };
  let fsIdleTimer = 0, padSig = '';
  function fsWake() {
    $('now').classList.remove('fs-idle');
    clearTimeout(fsIdleTimer);
    if (fsOn() && fsIdleMs()) fsIdleTimer = setTimeout(() => { if (fsOn()) $('now').classList.add('fs-idle'); }, fsIdleMs());
  }
  function pollPads() {
    if (!fsOn() || typeof navigator.getGamepads !== 'function') return;
    let sig = '';
    try {
      for (const p of navigator.getGamepads() || []) {
        if (p) sig += p.buttons.map((b) => (b.pressed ? 1 : 0)).join('') + p.axes.map((a) => (Math.abs(a) > 0.3 ? Math.sign(a) : 0)).join(',') + ';';
      }
    } catch (e) { return; }
    if (sig !== padSig && /[1-9-]/.test(sig)) fsWake();
    padSig = sig;
  }
  function wireFs() {
    for (const ev of ['mousemove', 'pointerdown', 'touchstart', 'keydown', 'wheel']) addEventListener(ev, () => { if (fsOn()) fsWake(); }, { passive: true });
    setInterval(pollPads, 200);
    $('b-fs').onclick = () => setFs(true);
    $('b-fs-close').onclick = () => setFs(false);
    addEventListener('resize', layoutFs);
    addEventListener('keydown', (e) => { if (e.key === 'Escape' && fsOn()) setFs(false); });
    // the browser's own Esc leaves fullscreen without telling our keydown handler
    document.addEventListener('fullscreenchange', () => { if (!document.fullscreenElement && fsOn()) setFs(false); });
    let t0 = null;
    $('now').addEventListener('touchstart', (e) => {
      t0 = fsOn() && e.touches.length === 1 && !e.target.closest('#bar') ? { x: e.touches[0].clientX, y: e.touches[0].clientY } : null;
    }, { passive: true });
    $('now').addEventListener('touchend', (e) => {
      if (!t0 || !e.changedTouches.length) return;
      const dy = e.changedTouches[0].clientY - t0.y, dx = e.changedTouches[0].clientX - t0.x;
      t0 = null;
      if (dy > 90 && Math.abs(dx) < 60) setFs(false); // swipe down
    }, { passive: true });
  }

  // ---- Now Playing ----
  let dragging = null, volTimer = 0, volHold = 0;
  function renderNow() {
    const st = S.state, song = st && (st.track || st.logical_track);
    $('idle').hidden = !!song;
    $('player').hidden = !song;
    document.title = song ? ((radioNow(song) || song).title || '') + ' · cliamp' : 'cliamp';
    if (!st) return;
    if (song) {
      $('trackno').textContent = song.track_number ? String(song.track_number).padStart(2, '0') + ' · ' : '';
      const rn = radioNow(song);
      $('titletext').textContent = rn ? rn.title : song.title || '';
      $('artist').textContent = rn ? rn.artist : song.artist || '';
      updateArt(song); refreshInfo(); renderTech();
      $('album').textContent = rn ? rn.album : [song.album, song.year].filter(Boolean).join(' · ');
      $('dur').textContent = fmtTime(st.duration);
      $('bar').setAttribute('aria-valuemax', Math.round(Number(st.duration) || 0));
      $('bar').classList.toggle('unseekable', !st.seekable);
    }
    if (!song) updateArt(null);
    renderNext();
    $('b-eq').title = $('b-eq-fs').title = 'Equalizer: ' + (st.eq_preset || 'Custom');
    $('b-eq').classList.toggle('on', !!st.eq_preset && st.eq_preset !== 'Flat');
    renderNowHeart();
    setSpin(st.state);
    setIcon($('b-toggle'), st.state === 'playing' ? 'pause' : 'play');
    $('b-shuffle').classList.toggle('on', !!st.shuffle);
    const rep = String(st.repeat || 'Off');
    $('b-repeat').classList.toggle('on', rep !== 'Off');
    $('rep1').hidden = rep !== 'One';
    $('b-lyrics').classList.toggle('on', !$('lyrics').hidden);
    if (!volHold) setVolume(curVolume(st));
    drawPosition();
  }
  function setVolume(db) {
    $('volume').value = db;
    $('voltxt').textContent = Math.round(db) + ' dB';
  }
  function drawPosition() {
    const st = S.state;
    if (!st || !st.track || dragging != null) return;
    showPosition(position(S, now()), Number(st.duration) || 0);
    drawLyrics();
  }
  function showPosition(p, d) {
    const f = d > 0 ? Math.min(1, p / d) * 100 : 0;
    $('fill').style.width = f + '%';
    $('knob').style.left = f + '%';
    $('pos').textContent = fmtTime(p);
    $('bar').setAttribute('aria-valuenow', Math.round(p));
  }
  function seekTo(secs) {
    const st = S.state;
    if (!st || !st.track || !st.seekable) return;
    secs = Math.max(0, Math.min(secs, Number(st.duration) || secs));
    dispatch({ type: 'tick', data: { position: secs }, now: now() });
    cmd('seek.absolute', { value: secs });
  }
  function wireNow() {
    $('cover').addEventListener('click', flipDisc);
    $('cover').addEventListener('keydown', (e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); flipDisc(); } });
    const bar = $('bar');
    const at = (e) => {
      const r = bar.getBoundingClientRect();
      return Math.max(0, Math.min(1, (e.clientX - r.left) / r.width)) * (S.state ? Number(S.state.duration) || 0 : 0);
    };
    bar.addEventListener('pointerdown', (e) => {
      if (!S.state || !S.state.track || !S.state.seekable) return;
      bar.setPointerCapture(e.pointerId);
      dragging = at(e);
      showPosition(dragging, Number(S.state.duration) || 0);
    });
    bar.addEventListener('pointermove', (e) => {
      if (dragging == null) return;
      dragging = at(e);
      showPosition(dragging, Number(S.state.duration) || 0);
    });
    bar.addEventListener('pointerup', (e) => {
      if (dragging == null) return;
      const v = at(e);
      dragging = null;
      seekTo(v);
    });
    bar.addEventListener('pointercancel', () => { dragging = null; });
    bar.addEventListener('keydown', (e) => {
      const d = { ArrowLeft: -5, ArrowRight: 5 }[e.key];
      if (d == null || !S.state) return;
      e.preventDefault();
      seekTo(position(S, now()) + d);
    });
    $('b-toggle').onclick = () => cmd('toggle');
    $('b-prev').onclick = () => cmd('prev');
    $('b-next').onclick = () => cmd('next');
    const openEq = (e) => {
      const cur = (S.state && S.state.eq_preset) || 'Custom';
      openMenu({ trigger: e.currentTarget, title: 'Equalizer', subtitle: cur, spec: { kind: 'eq', current: cur },
        run: Object.fromEntries(EQ_PRESETS.map((n) => ['eq:' + n, () => cmd('eq', { name: n })])) });
    };
    $('b-eq').onclick = openEq;
    $('b-eq-fs').onclick = openEq;
    $('b-shuffle').onclick = () => S.state && cmd('shuffle', { name: S.state.shuffle ? 'off' : 'on' });
    $('b-repeat').onclick = () => cmd('repeat', { name: 'cycle' });
    $('volume').addEventListener('input', (e) => {
      setVolume(Number(e.target.value));
      volHold = Date.now();
      if (!volTimer) volTimer = setTimeout(() => { volTimer = 0; cmd('volume', { value: Number($('volume').value) }); }, 80);
    });
    $('volume').addEventListener('change', () => setTimeout(() => { volHold = 0; }, 600));
    $('b-lyrics').onclick = openLyrics;
    $('b-lyrics-close').onclick = closeLyrics;
    $('scrim').onclick = closeLyrics;
    setInterval(() => { if (!document.hidden) drawPosition(); }, 250);
  }

  // ---- lyrics ----
  let lyricLines = [], lyricGen = 0, lyricShown = -1;
  async function openLyrics() {
    const gen = ++lyricGen; // drop replies that arrive after a close/reopen
    $('lyrics').hidden = false;
    $('scrim').hidden = false;
    $('lyric-lines').replaceChildren();
    $('lyric-empty').hidden = true;
    lyricLines = [];
    let lines = [], err = '';
    try {
      const r = await api.get('/api/lyrics');
      lines = r.lyrics || [];
    } catch (e) {
      err = e.detail || 'http ' + e.status;
    }
    if (gen !== lyricGen) return;
    if (err) {
      $('lyric-empty').hidden = false;
      $('lyric-empty').textContent = err === 'no lyrics found' ? 'no lyrics for this track' : err;
      return;
    }
    lyricLines = lines;
    if (!lines.length) { $('lyric-empty').hidden = false; $('lyric-empty').textContent = 'no lyrics for this track'; return; }
    $('lyric-lines').replaceChildren(...lines.map((ln, i) =>
      h('li', { 'data-i': i, onclick: () => seekTo(ln.start) }, ln.text)));
    lyricShown = -1;
    drawLyrics();
  }
  function closeLyrics() {
    lyricGen++;
    $('lyrics').hidden = true;
    $('scrim').hidden = true;
  }
  function drawLyrics() {
    if ($('lyrics').hidden || !lyricLines.length) return;
    const i = lyricIndex(lyricLines, position(S, now()));
    if (i === lyricShown) return;
    lyricShown = i;
    document.querySelectorAll('#lyric-lines li').forEach((li, j) => li.classList.toggle('on', j === i));
    const li = i >= 0 ? document.querySelector('#lyric-lines li[data-i="' + i + '"]') : null;
    if (li) li.scrollIntoView({ block: 'center' });
  }

  // ---- rows and menus ----
  // one "..." button and its menu for any entry, row or tile
  function moreButton(o) {
    if (!o.more) return null;
    const trigger = h('button', { class: 'ic more', 'aria-label': 'More actions', 'aria-haspopup': 'menu', 'aria-expanded': 'false' });
    trigger.append(svg('more'));
    const open = () => {
      if (menuState && menuState.trigger === trigger) { closeMenu(); return; }
      const m = o.more();
      openMenu({ trigger, title: o.title || '', subtitle: o.sub || '', spec: m.spec, run: m.run });
    };
    trigger.onclick = (e) => { e.preventDefault(); e.stopPropagation(); open(); };
    return { trigger, open };
  }
  // a list row; right-click opens the menu too
  function item(o) {
    const li = h('li', { class: o.cur ? 'cur' : null });
    const inner = [h('div', { class: 'txt' }, h('div', { class: 't1' }, o.title || ''), o.sub ? h('div', { class: 't2' }, o.sub) : null)];
    if (o.end) inner.push(h('div', { class: 'end' }, o.end));
    if (o.icon) inner.unshift(h('span', { class: 'lead' }, svg(o.icon)));
    const mb = moreButton(o);
    const tap = o.onTap || (mb ? mb.open : null);
    const row = o.href ? h('a', { class: 'row', href: o.href }, inner)
      : h('div', { class: 'row', role: 'button', tabindex: 0, onclick: tap,
        onkeydown: (e) => { if (e.key === 'Enter' && tap) tap(); } }, inner);
    const top = h('div', { class: 'toprow' }, row);
    if (o.heart) top.append(o.heart);
    if (mb) {
      row.addEventListener('contextmenu', (e) => { e.preventDefault(); mb.open(); });
      top.append(mb.trigger);
    }
    li.append(top);
    return li;
  }
  // the same entry as a tile: picture (or a coloured placeholder with the kind's icon),
  // name and subtitle underneath, heart and "..." over the picture's corner
  function tile(o) {
    const li = h('li', { class: 'tile2' });
    const mb = moreButton(o);
    const ps = placeholderStyle(o.title);
    const cover = h('div', { class: 'cover2' }, h('span', { class: 'ph2' }, svg(o.tkind || o.icon || 'note')));
    cover.style.background = 'hsl(' + ps.hue + ' ' + ps.sat + '% ' + ps.light + '%)';
    if (o.art) {
      const img = h('img', { class: 'art2' + (o.tkind === 'radio' ? ' logo' : ''), alt: '', loading: 'lazy', decoding: 'async', src: o.art });
      img.onerror = () => img.remove(); // no picture: the placeholder stays
      cover.append(img);
    }
    const body = [cover, h('div', { class: 'tname' }, o.title || ''), o.sub ? h('div', { class: 'tsub' }, o.sub) : null];
    const tap = o.onTap || (mb ? mb.open : null);
    const link = o.href ? h('a', { class: 'tlink', href: o.href }, body)
      : h('div', { class: 'tlink', role: 'button', tabindex: 0, onclick: tap, onkeydown: (e) => { if (e.key === 'Enter' && tap) tap(); } }, body);
    li.append(link);
    const act = h('div', { class: 'tact' });
    if (o.heart) act.append(o.heart);
    if (mb) {
      link.addEventListener('contextmenu', (e) => { e.preventDefault(); mb.open(); });
      act.append(mb.trigger);
    }
    if (act.children.length) li.append(act);
    // hover (or keyboard focus) offers Play and Open without leaving the list
    if (o.onPlay) {
      const play = h('button', { class: 'tplay', 'aria-label': 'Play ' + (o.title || ''), onclick: (e) => { e.preventDefault(); e.stopPropagation(); o.onPlay(); } });
      play.append(svg('play'));
      li.append(h('div', { class: 'thover' }, play, o.href ? h('a', { class: 'topen', href: o.href }, 'Open') : null));
    }
    return li;
  }

  // ---- Collections: entries as a list or as tiles, loaded as you scroll ----
  // Pages of PAGE rows. The player's `total` cannot be trusted (for albums it is just the
  // size of the page it returned), so a list is finished when a page comes back short.
  const PAGE = 50;
  const pageDone = (n) => n < PAGE;
  const getViewMode = (key) => { const v = getPref(key, null); return (v === null ? getPref(PREF_VIEW, 'tiles') : v) === 'list' ? 'list' : 'tiles'; };
  const saveViewMode = (key, m) => { try { localStorage.setItem(key, m); } catch (e) { /* storage may be off */ } };
  // chunkPage pages through a list that is already loaded (a folder, the favorites)
  const chunkPage = (get, size = 120) => {
    let shown = 0;
    return () => {
      const arr = get(), n = nextChunk(arr.length, shown, size), rows = arr.slice(shown, shown + n);
      shown += n;
      return Promise.resolve({ rows, done: shown >= arr.length });
    };
  };
  // collection({my, key, modes, page, opts, empty}) -> {toggle, body, start, destroy}
  //   page() -> {rows, done}; opts(row) -> entry options for item()/tile()
  //   An IntersectionObserver on a sentinel pulls in the next page shortly before it is
  //   reached; no "More" button. Without IntersectionObserver everything is loaded.
  function collection(c) {
    let mode = c.modes ? getViewMode(c.key) : 'list';
    const ul = h('ul'), status = h('div', { class: 'feedmsg dim', 'aria-live': 'polite' }), sentinel = h('div', { class: 'sentinel', 'aria-hidden': 'true' });
    const all = [];
    let done = false, busy = false, failed = false, filling = false, dead = false, io = null;
    const els = new Map();
    // built rows are kept, so redrawing after a filter or a late detail does not reload pictures (that was the blinking)
    const make = (o) => { let el = els.get(o); if (!el) { el = mode === 'tiles' ? tile(o) : item(o); els.set(o, el); } return el; };
    let paintTimer = 0;
    const repaint = () => { if (!paintTimer) paintTimer = setTimeout(() => { paintTimer = 0; if (!dead) paint(); }, 150); };
    // sort, facet filters and "hide offline" apply to what is loaded so far
    const view = { sort: '', facets: {}, hideDead: true, q: '' };
    const narrowed = () => !!view.sort || !!view.q || Object.values(view.facets).some(Boolean);
    const shown = () => viewRows(all, view, c.sorts, c.facets);
    const note = () => { status.textContent = done && !shown().length ? (all.length ? 'Nothing matches the filters' : c.empty || 'Nothing here') : ''; };
    const paint = () => { ul.className = mode === 'tiles' ? 'grid' : 'list'; ul.replaceChildren(...shown().map(make)); note(); };
    const nearBottom = () => sentinel.getBoundingClientRect().top < innerHeight + 700;
    async function more() {
      busy = true; failed = false; status.textContent = 'Loading…';
      try {
        const r = await c.page();
        if (dead || c.my !== nav) return;
        const rows = r.rows || [];
        if (rows.length) {
          const os = rows.map(c.opts); all.push(...os);
          if (narrowed()) repaint(); else ul.append(...os.filter((o) => !(view.hideDead && o.dead)).map(make));
          buildTools(); enrich(os);
        }
        if (!rows.length || r.done) done = true;
        note();
      } catch (e) {
        failed = true;
        status.replaceChildren(h('span', null, 'Could not load more. '), h('button', { class: 'btn', onclick: () => { failed = false; fill(); } }, 'Retry'));
      } finally { busy = false; }
    }
    // details that arrive after the rows (station codec, country, votes): redraw what changed
    async function enrich(os) {
      if (!c.enrich) return;
      try { await c.enrich(os); } catch (e) { /* the rows stay as they are */ }
      if (dead || c.my !== nav) return;
      const old = os.map((o) => els.get(o));
      os.forEach((o) => els.delete(o)); // their details changed: build them again
      if (narrowed() || (view.hideDead && os.some((o) => o.dead))) repaint();
      else os.forEach((o, i) => { if (old[i] && old[i].isConnected) old[i].replaceWith(make(o)); });
      buildTools();
    }
    async function fill() {
      if (filling) return;
      filling = true;
      try {
        // keep going while the end is near; a hidden list (another tab) waits for the observer
        while (!done && !failed && !dead && c.my === nav && ul.offsetParent !== null && (c.loadAll || !io || nearBottom())) await more();
      } finally { filling = false; }
      if (done || dead) { if (io) io.disconnect(); sentinel.remove(); }
    }
    function setMode(m) {
      if (m === mode) return;
      const first = Array.from(ul.children).findIndex((li) => li.getBoundingClientRect().bottom > 60); // stay on the same entry
      mode = m;
      els.clear();
      saveViewMode(c.key, m);
      paint();
      if (first > 0 && ul.children[first]) ul.children[first].scrollIntoView({ block: 'start' });
      toggle && Array.from(toggle.children).forEach((b) => b.setAttribute('aria-pressed', String(b.dataset.mode === mode)));
      fill();
    }
    const vbtn = (m, icon, label) => {
      const b = h('button', { class: 'ic', 'aria-label': label, title: label, 'aria-pressed': String(mode === m), onclick: () => setMode(m) }, svg(icon));
      b.dataset.mode = m;
      return b;
    };
    const toggle = c.modes ? h('div', { class: 'vtoggle', role: 'group', 'aria-label': 'View' }, vbtn('list', 'queue', 'List view'), vbtn('tiles', 'grid', 'Tile view')) : null;
    const tools = c.sorts || c.facets || c.search ? h('div', { class: 'viewtools' }) : null;
    let toolsLate = false, toolsSig = '';
    function buildTools() {
      if (!tools) return;
      // never rebuild a menu the user has open: it would close under their finger
      if (tools.contains(document.activeElement) && document.activeElement !== tools) { toolsLate = true; return; }
      toolsLate = false;
      const kids = [];
      if (c.search) {
        const q = h('input', { type: 'search', class: 'vq', placeholder: 'Filter', autocomplete: 'off', spellcheck: 'false', 'aria-label': 'Filter this list', value: view.q,
          oninput: () => { view.q = q.value.trim(); repaint(); fill(); } });
        kids.push(q);
      }
      const pick = (label, value, opts, onchange) => {
        const sel = h('select', { class: 'vsel', 'aria-label': label, onchange: () => onchange(sel.value) },
          h('option', { value: '' }, label), opts.map((o) => h('option', { value: o.v }, o.label)));
        sel.value = value;
        return sel;
      };
      if (c.sorts && c.sorts.some((x) => x.cmp)) kids.push(pick('Sort', view.sort, c.sorts.filter((x) => x.cmp).map((x) => ({ v: x.id, label: x.label })), (v) => { view.sort = v; paint(); }));
      for (const f of c.facets || []) {
        const vals = facetValues(all, f.get).slice(0, 500);
        if (vals.length < 2 && !view.facets[f.id]) continue;
        kids.push(pick(f.label, view.facets[f.id] || '', vals.map((x) => ({ v: x.v, label: x.v + ' (' + x.n + ')' })), (v) => { view.facets[f.id] = v; paint(); fill(); }));
      }
      if (all.some((o) => o.dead)) {
        const cb = h('input', { type: 'checkbox', onchange: () => { view.hideDead = cb.checked; paint(); fill(); } });
        cb.checked = view.hideDead;
        kids.push(h('label', { class: 'vchk' }, cb, ' Hide offline'));
      }
      const sig = kids.map((k) => k.outerHTML).join('') + JSON.stringify([view.sort, view.facets, view.hideDead]); // the filter text is not in it: the box is never rebuilt for it
      if (sig === toolsSig) return; // nothing changed: keep the controls as they are
      toolsSig = sig;
      tools.replaceChildren(...kids);
      tools.hidden = !kids.length;
    }
    if (tools) tools.addEventListener('focusout', () => setTimeout(() => { if (toolsLate && !dead) buildTools(); }, 0));
    buildTools();
    ul.className = mode === 'tiles' ? 'grid' : 'list';
    return {
      toggle, tools, body: [ul, status, sentinel],
      start() {
        if ('IntersectionObserver' in window) {
          io = new IntersectionObserver((es) => { if (dead || c.my !== nav) { io.disconnect(); return; } if (es.some((e) => e.isIntersecting)) fill(); }, { rootMargin: '700px 0px' });
          io.observe(sentinel);
        }
        fill();
      },
      destroy() { dead = true; if (io) io.disconnect(); },
    };
  }
  const listBar = (...kids) => { const k = kids.filter(Boolean); return k.length ? h('div', { class: 'listbar' }, k) : null; };

  // In-app trail: the hash of every history entry we showed, by position. The
  // back arrow goes to the page you came from (an artist's albums, favorites,
  // a search) and only falls back to the fixed parent when there is none.
  const trail = [];
  let trailAt = 0;
  function trackTrail() {
    let i = history.state && Number.isInteger(history.state.i) ? history.state.i : null;
    if (i === null) { i = trailAt + 1; try { history.replaceState({ i }, ''); } catch (e) { /* keep the fixed parent */ } }
    trailAt = i;
    trail[i] = location.hash;
    trail.length = i + 1; // entries after this one are gone from the forward history
  }
  function backClick(e) {
    const prev = trail[trailAt - 1];
    if (prev && prev !== location.hash && /^#(browse|favorites|search)/.test(prev)) { e.preventDefault(); history.back(); }
  }
  const head = (title, back, extra) => h('div', { class: 'head' },
    back ? h('a', { class: 'back', href: back, 'aria-label': 'Back', onclick: backClick }, svg('back')) : null, h('h2', null, title), extra);
  const msg = (cls, text) => h('div', { class: cls }, text);
  const enc = encodeURIComponent;

  // track actions: cliamp plays a track by path; queue ops guard on revision.
  // cliamp wants the whole track object here ("play a supplied track"); a bare
  // path string fails with invalid_params. Drop only row bookkeeping.
  const trackArg = (t) => { const o = Object.assign({}, t); delete o.index; delete o.queued; return o; };
  const playTrack = (t) => cmd('track.play', { track: trackArg(t) }, true);

  // ---- Play / Add to queue ----
  // Play clears the queue and plays; Add to queue appends. The bridge does the
  // multi-step work in the background and reports progress over SSE.
  let enqMode = 'append';
  async function enq(body) {
    enqMode = body.mode;
    try { await api.post('/api/enqueue', body); }
    catch (e) { toast(e.status === 409 ? 'Already adding to the queue' : e.status === 403 ? 'Unknown track' : (e.message || 'Could not start'), true); }
  }
  let progEl = null;
  function onEnqueue(ev) {
    const text = progressText(ev, enqMode);
    if (ev.state === 'running') {
      if (!progEl) {
        progEl = h('div', { class: 'toast prog' }, h('span', { class: 'ptext' }),
          h('button', { class: 'btn', onclick: () => api.post('/api/enqueue/cancel', {}).catch(() => {}) }, 'Stop'));
        $('toasts').append(progEl);
      }
      progEl.querySelector('.ptext').textContent = text;
      return;
    }
    if (progEl) { progEl.remove(); progEl = null; }
    toast(text, ev.state === 'failed');
  }

  // what each kind of item offers in its menu (see rowActions)
  const favState = (heart) => (heart ? heart.isOn() : undefined);
  const trackMore = (t, heart) => () => ({ spec: { kind: 'track', fav: favState(heart) }, run: {
    play: () => enq({ kind: 'track', track: trackArg(t), mode: 'play' }),
    add: () => enq({ kind: 'track', track: trackArg(t), mode: 'append' }),
    fav: () => heart && heart.toggle(),
  } });
  const collectionMore = (kind, provider, id, heart) => () => ({ spec: { kind, fav: favState(heart) }, run: {
    play: () => enq({ kind, provider, id, mode: 'play' }),
    add: () => enq({ kind, provider, id, mode: 'append' }),
    fav: () => heart && heart.toggle(),
  } });
  const radioMore = (provider, id, heart) => () => ({ spec: { kind: 'radio', fav: favState(heart) }, run: {
    play: () => { cmd('provider.load', { provider, playlist: id }, true); rev(); },
    fav: () => heart && heart.toggle(),
  } });

  // ---- Item menu ----
  // One menu for every item: a bottom sheet on narrow or touch screens, a popover
  // anchored to the "..." otherwise. Keyboard: arrows, Home/End, Enter/Space,
  // Escape (focus returns to the "...").
  let menuState = null;
  function closeMenu(refocus = true) {
    const m = menuState;
    if (!m) return;
    menuState = null;
    m.cleanup();
    m.root.remove();
    m.trigger.setAttribute('aria-expanded', 'false');
    if (refocus) m.trigger.focus();
  }
  function openMenu({ trigger, title, subtitle, spec, run }) {
    closeMenu(false);
    const acts = rowActions(spec);
    if (!acts.length) return;
    const sheet = menuMode(innerWidth, matchMedia('(pointer: coarse)').matches) === 'sheet';
    const list = h('div', { class: 'mlist', role: 'menu', 'aria-label': title || 'Actions' }, acts.map((a) => {
      const b = h('button', { class: 'mi' + (a.danger ? ' danger' : ''), role: 'menuitem', tabindex: '-1', disabled: a.disabled ? 'disabled' : null }, svg(a.icon), h('span', null, a.label));
      b.onclick = () => { closeMenu(); if (run[a.id]) run[a.id](); };
      return b;
    }));
    const items = () => Array.from(list.querySelectorAll('.mi'));
    const focusAt = (i) => { const el = items()[i]; if (el) el.focus(); };
    list.addEventListener('keydown', (e) => {
      const els = items();
      if (e.key === 'Escape') { e.preventDefault(); closeMenu(); }
      else if (e.key === 'Tab') closeMenu(false); // focus moves on naturally
      else if (['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(e.key)) {
        e.preventDefault();
        focusAt(menuKey(els.indexOf(document.activeElement), els.length, e.key, els.map((x) => x.disabled)));
      }
    });
    const cleanups = [];
    let root;
    if (sheet) {
      const box = h('div', { class: 'msheet' }, h('div', { class: 'mgrab' }),
        h('div', { class: 'mh' }, h('b', null, title), subtitle ? h('small', null, subtitle) : null), list,
        h('button', { class: 'mcancel', onclick: () => closeMenu() }, 'Cancel'));
      root = h('div', { class: 'mscrim', onclick: (e) => { if (e.target === root) closeMenu(); } }, box);
      let y0 = null;
      box.addEventListener('touchstart', (e) => { y0 = e.touches[0].clientY; box.style.transition = 'none'; }, { passive: true });
      box.addEventListener('touchmove', (e) => {
        if (y0 == null) return;
        const dy = e.touches[0].clientY - y0;
        if (dy > 0 && list.scrollTop === 0) box.style.transform = 'translateY(' + dy + 'px)';
      }, { passive: true });
      box.addEventListener('touchend', (e) => {
        if (y0 == null) return;
        const dy = e.changedTouches[0].clientY - y0;
        y0 = null; box.style.transition = '';
        if (dy > 80) closeMenu(); else box.style.transform = '';
      });
      document.body.append(root);
    } else {
      root = h('div', { class: 'mpop' }, list);
      document.body.append(root);
      const a = trigger.getBoundingClientRect(), sz = root.getBoundingClientRect();
      const pos = menuPlacement({ left: a.left, top: a.top, width: a.width, height: a.height }, { width: sz.width, height: sz.height }, { width: innerWidth, height: innerHeight });
      root.style.left = pos.left + 'px';
      root.style.top = pos.top + 'px';
      root.classList.toggle('up', pos.flipped);
      const outside = (e) => { if (!root.contains(e.target) && !trigger.contains(e.target)) closeMenu(false); };
      const away = (e) => { if (!root.contains(e.target)) closeMenu(false); };
      document.addEventListener('pointerdown', outside, true);
      addEventListener('scroll', away, true);
      addEventListener('resize', away);
      cleanups.push(() => { document.removeEventListener('pointerdown', outside, true); removeEventListener('scroll', away, true); removeEventListener('resize', away); });
    }
    menuState = { root, trigger, cleanup: () => cleanups.forEach((f) => f()) };
    trigger.setAttribute('aria-expanded', 'true');
    focusAt(menuKey(-1, acts.length, 'ArrowDown', acts.map((x) => !!x.disabled)));
  }

  // the entry options of a track row, shared by album pages and search
  const trackOpts = (t) => {
    const hh = trackHeart(t);
    return { title: t.title || rowTitle(t), sub: [t.artist, t.album].filter(Boolean).join(' · '), end: fmtTime(t.duration_secs), onTap: () => playTrack(t), heart: hh, more: trackMore(t, hh) };
  };

  // ---- Favorites ----
  // Tracks and stations live in cliamp, albums/artists/playlists in the bridge;
  // the page only holds the merged listing and a set of keys for the hearts.
  let favItems = [], favOn = new Set(), favPartial = false, favKind = 'all', favSig = '';
  const paintHearts = () => document.querySelectorAll('.heart').forEach((b) => b.paint && b.paint());
  async function loadFavs() {
    let r;
    try { r = await api.get('/api/favorites'); } catch (e) { return; } // quiet: hearts keep their last state
    favItems = r.items || [];
    favPartial = !!r.partial;
    favOn = favKeys(favItems);
    paintHearts();
    renderNowHeart();
    const sig = favItems.map((i) => i.kind + ':' + i.provider + ':' + i.id).join('|') + (favPartial ? '!' : '');
    if (document.body.dataset.tab === 'favorites' && sig !== favSig) renderFavorites();
    favSig = sig;
  }
  // toggleFav flips optimistically, asks the server, trusts its answer and
  // rolls back with a toast when it refuses.
  async function toggleFav(wasOn, req, setOn) {
    const want = !wasOn;
    setOn(want);
    try {
      const r = await api.put('/api/favorites', Object.assign({ favorite: want }, req));
      setOn(r.favorite);
    } catch (e) { setOn(wasOn); toast(e.message || 'could not update the favorite', true); return; }
    loadFavs();
  }
  // heartBtn: state in favOn under o.key, or (a radio catalog row, whose c:N id
  // is not in the listing) local to the button, seeded with o.on.
  function heartBtn(o) {
    const b = h('button', { class: 'ic heart', 'aria-label': 'Favorite' });
    let local = !!o.on;
    const isOn = () => (o.key ? favOn.has(o.key) : local);
    const setOn = (v) => { if (o.key) { if (v) favOn.add(o.key); else favOn.delete(o.key); paintHearts(); } else { local = v; b.paint(); } };
    b.paint = () => { const on = isOn(); b.classList.toggle('on', on); b.setAttribute('aria-pressed', String(on)); setIcon(b, on ? 'heartfill' : 'heart'); };
    b.isOn = isOn;
    b.toggle = () => toggleFav(isOn(), o.req, setOn);
    b.onclick = (e) => { e.preventDefault(); e.stopPropagation(); b.toggle(); };
    b.paint();
    return b;
  }
  const trackHeart = (t) => {
    const key = trackFavKey(t);
    return key ? heartBtn({ key, req: { kind: 'track', track: trackArg(t), title: t.title || '', subtitle: t.artist || '' } }) : null;
  };
  // the heart on Now Playing and fullscreen: the playing track, or a station
  // that is already a favorite (adding a station is done from the catalog)
  const nowFav = { key: '', req: null };
  function renderNowHeart() {
    const st = S.state, song = st && (st.track || st.logical_track);
    nowFav.key = ''; nowFav.req = null;
    if (song) {
      const key = trackFavKey(song);
      if (key) { nowFav.key = key; nowFav.req = { kind: 'track', track: trackArg(song), title: song.title || '', subtitle: song.artist || '' }; }
      else if (isPlayingFavStation(favItems, song)) {
        nowFav.key = favKey('radio', 'radio', 'f:' + song.path);
        nowFav.req = { kind: 'radio', provider: 'radio', id: 'f:' + song.path };
      }
    }
    const b = $('b-heart');
    b.hidden = !nowFav.req;
    b.paint();
  }
  function wireHeart() {
    const b = $('b-heart');
    b.paint = () => { const on = !!nowFav.key && favOn.has(nowFav.key); b.classList.toggle('on', on); b.setAttribute('aria-pressed', String(on)); setIcon(b, on ? 'heartfill' : 'heart'); };
    b.onclick = () => {
      const { key, req } = nowFav;
      if (!req) return;
      toggleFav(favOn.has(key), req, (v) => { if (v) favOn.add(key); else favOn.delete(key); paintHearts(); });
    };
  }

  const FAV_KINDS = [['all', 'All'], ['track', 'Tracks'], ['album', 'Albums'], ['artist', 'Artists'], ['playlist', 'Playlists'], ['radio', 'Radio']];
  function favOpts(it) {
    const prov = it.provider, k = it.kind, title = it.title || '', sub = it.subtitle || '';
    const heart = (req) => heartBtn({ key: favKey(k, prov, it.id), req });
    if (k === 'track') {
      const t = it.track || {};
      const hh = heart({ kind: 'track', track: trackArg(t), title, subtitle: sub });
      return { title, sub, tkind: 'note', onTap: () => playTrack(t), heart: hh, more: trackMore(t, hh) };
    }
    if (k === 'radio') {
      const hh = heart({ kind: 'radio', provider: 'radio', id: it.id });
      return { title, sub: 'radio', tkind: 'radio', art: stationArtUrl(title), heart: hh, more: radioMore('radio', it.id, hh),
        onTap: () => { cmd('provider.load', { provider: 'radio', playlist: it.id }, true); rev(); } };
    }
    const dir = k === 'album' ? 'albums' : k === 'artist' ? 'artists' : 'playlists';
    const hh = heart({ kind: k, provider: prov, id: it.id, title, subtitle: sub });
    return { title, sub, tkind: k === 'artist' ? 'person' : k === 'album' ? 'album' : 'queue', art: itemArtUrl(k, prov, it.id),
      href: '#browse/' + enc(prov) + '/' + dir + '/' + k + '/' + enc(it.id), heart: hh, more: collectionMore(k, prov, it.id, hh) };
  }
  let favCol = null;
  function renderFavorites() {
    if (favCol) favCol.destroy();
    const counts = favCounts(favItems);
    if (favKind !== 'all' && !counts[favKind]) favKind = 'all';
    const chips = h('div', { class: 'seg' }, FAV_KINDS.filter(([k]) => k === 'all' || counts[k]).map(([k, label]) =>
      h('button', { class: k === favKind ? 'on' : null, onclick: () => { favKind = k; renderFavorites(); } }, label + (counts[k] ? ' ' + counts[k] : ''))));
    const rows = favFilter(favItems, favKind);
    favCol = collection({ my: nav, key: viewModeKey('favorites'), modes: true, ...LIST_VIEWS.favorites, page: chunkPage(() => rows), opts: favOpts,
      empty: 'Nothing here yet. Tap the heart on anything to keep it.' });
    $('favorites').replaceChildren(...[head('Favorites'), chips,
      favPartial && msg('empty', 'The player is offline: tracks and stations are not shown'),
      listBar(favCol.toggle), favCol.tools, ...favCol.body].filter(Boolean));
    favCol.start();
  }

  // ---- Queue ----
  let confirming = false;
  function renderQueue() {
    const q = S.queue, view = queueView(S.state, q), pn = q.play_next || [], cur = curIndex(S.state);
    const statusAt = new Map(view.map((r) => [r.index, r.status]));
    const clear = confirming
      ? h('span', { class: 'confirm' }, 'Clear the queue?',
        h('button', { class: 'btn danger', onclick: () => { confirming = false; cmd('queue.clear', {}, true); rev(); } }, 'Clear'),
        h('button', { class: 'btn', onclick: () => { confirming = false; renderQueue(); } }, 'Cancel'))
      : null;
    const rows = view.map((r) => {
      const t = r.t, i = r.index;
      // moving works inside a group: up-next entries among themselves, the rest of the list among itself
      let canUp = false, canDown = false;
      if (r.status === 'next') {
        const prev = pn[r.pn - 1], next = pn[r.pn + 1];
        canUp = !!prev && Number(prev.index || 0) !== cur;
        canDown = !!next;
      } else if (r.status === 'later') {
        canUp = statusAt.get(i - 1) === 'later';
        canDown = statusAt.get(i + 1) === 'later';
      }
      const mv = (dir) => (r.status === 'next' ? moveCmd('playnext.move', r.pn, dir) : moveCmd('queue.move', i, dir));
      const li = item({
        title: t.title || t.path, sub: t.artist || '', end: fmtTime(t.duration_secs != null ? t.duration_secs : t.duration), cur: r.status === 'current',
        onTap: () => { if (r.status !== 'current') cmd('queue.play', { index: i }, true); },
        more: () => ({ spec: { kind: 'queue', status: r.status, canUp, canDown }, run: {
          playfrom: () => cmd('queue.play', { index: i }, true),
          next: () => { cmd('queue.enqueue', { index: i }, true); rev(); },
          up: () => mv(-1),
          down: () => mv(1),
          remove: () => { cmd('queue.remove', { index: i }, true); rev(); },
        } }),
      });
      li.dataset.i = i;
      if (r.status === 'played') li.classList.add('played');
      return li;
    });
    $('queue').replaceChildren(
      h('div', { class: 'head' }, h('h2', null, 'Queue'), clear),
      ...(rows.length ? [h('ul', { class: 'list' }, rows)] : [msg('empty', 'The queue is empty')]));
  }
  function moveCmd(op, i, dir) {
    const n = op === 'queue.move' ? (S.queue.tracks || []).length : (S.queue.play_next || []).length;
    const t = moveTarget(i, n, dir);
    if (t) { cmd(op, { index: t.from, to: t.to }, true); rev(); }
  }
  // ---- Browse ----
  let nav = 0, providers = null, provSeq = 0;
  const PROVIDER_KINDS = [
    ['playlists', 'Playlists', 'playlists'],
    ['albums', 'Albums', 'albums'],
    ['artists', 'Artists', 'artists'],
    ['catalog', 'Radio', 'catalog'],
    ['files', 'Files', 'files'], // the Local provider's folder view, when the bridge has a music folder
  ];
  const kindsOf = (p) => PROVIDER_KINDS.filter(([k]) =>
    k === 'playlists' || (k === 'albums' && p.browse_albums) || (k === 'artists' && p.browse_artists) || (k === 'catalog' && p.catalog) || (k === 'files' && p.browse_files));
  async function loadProviders() {
    if (providers) return providers;
    const seq = ++provSeq;
    try {
      const r = await api.get('/api/library/providers');
      if (seq === provSeq) {
        providers = r.providers || [];
        // cliamp's file browser lives under Local, next to its playlists
        if (await filesRoot()) providers.forEach((pr) => { if (pr.key === 'local') pr.browse_files = true; });
      }
    } catch (e) { fail(e); return null; }
    return providers;
  }
  const rowTitle = (r) => r.name || r.title || '';
  async function renderBrowse(rest) {
    const my = ++nav, root = $('browse');
    const p = splitPath(rest) || [''];
    const show = (...kids) => { if (my === nav) root.replaceChildren(...kids.filter(Boolean)); }; // a null child would print as "null"
    const listUrl = (prov, kind, off) => '/api/library/' + enc(prov) + '/' + kind + '?offset=' + off + '&limit=' + PAGE;

    if (p[0] === '' || p[0] === 'home') {
      const ps = await loadProviders();
      if (!ps) { show(head('Browse'), msg('error', 'no providers')); return; }
      show(head('Browse'), h('div', { class: 'tiles' }, ps.map((pr) =>
        h('a', { class: 'tile', href: '#browse/' + enc(pr.key) + '/playlists' }, pr.name,
          h('span', null, kindsOf(pr).map((k) => k[1]).join(' · '))))));
      return;
    }
    if (p[0] === 'files') { await renderFiles(my, show, p[1] || ''); return; }
    const prov = p[0];
    const ps = await loadProviders();
    const info = (ps || []).find((x) => x.key === prov);
    if (!info) { show(head('Browse', '#browse'), msg('error', 'unknown provider: ' + prov)); return; }
    const kind = p[1] || 'playlists';
    const seg = h('div', { class: 'seg' }, kindsOf(info).map(([k, label]) =>
      h('a', { href: k === 'files' ? filesHash('') : '#browse/' + enc(prov) + '/' + k, class: k === kind ? 'on' : null }, label)));
    const hdr = head(info.name, '#browse', seg);

    if (p.length >= 4 && (p[2] === 'album' || p[2] === 'artist' || p[2] === 'playlist')) {
      return renderDrill(my, show, hdr, prov, p[2], p[3], kind);
    }
    // a plain kind list, as a list or tiles, loaded as you scroll
    let off = 0;
    const radio = prov === 'radio' && kind === 'catalog';
    const seenIds = new Set();
    const orderKey = 'order:' + prov + ':' + kind;
    const ordered = kind === 'albums'; // the provider sorts the whole library
    let order = '';
    if (ordered) { try { order = localStorage.getItem(orderKey) || ''; } catch (e) { /* storage may be off */ } if (!ALBUM_ORDERS.some((o) => o[0] === order)) order = ''; }
    const makeCol = () => collection({ my, key: viewModeKey('browse', prov, kind), modes: true, empty: 'Nothing here',
      page: async () => {
        if (!radio) await loadGroups();
        const r = await api.get(listUrl(prov, kind, radio ? seenIds.size : off) + (order ? '&sort=' + order : ''));
        let rows = r[kind] || r.playlists || [];
        if (radio) {
          // cliamp's station list ignores the offset and answers with everything it has loaded so far, a bit more each time
          const fresh = rows.filter((x) => !seenIds.has(x.id));
          fresh.forEach((x) => seenIds.add(x.id));
          return { rows: fresh, done: !fresh.length };
        }
        off += rows.length;
        return { rows, done: pageDone(rows.length) };
      },
      opts: (row) => browseOpts(prov, kind, row),
      ...(radio ? radioView(my) : LIST_VIEWS[kind] || LIST_VIEWS.catalog) });
    let col = makeCol();
    if (!radio) {
      const sortSel = ordered ? h('select', { class: 'vsel', 'aria-label': 'Show', onchange: () => {
        order = sortSel.value;
        try { localStorage.setItem(orderKey, order); } catch (e) { /* storage may be off */ }
        col.destroy(); off = 0; col = makeCol();
        show(hdr, listBar(sortSel, col.toggle), col.tools, ...col.body); col.start();
      } }, ALBUM_ORDERS.map(([v, l]) => h('option', { value: v }, l))) : null;
      if (sortSel) sortSel.value = order;
      show(hdr, listBar(sortSel, col.toggle), col.tools, ...col.body); col.start();
      return;
    }
    // radio: a search box over the public station directory replaces the catalog while it has text
    const box = h('input', { type: 'search', class: 'filter', placeholder: 'Search stations by name, genre or country', autocomplete: 'off', spellcheck: 'false', 'aria-label': 'Search stations' });
    const results = h('div', { class: 'stnresults', hidden: true });
    const catalog = h('div', { class: 'stncatalog' }, listBar(col.toggle), col.tools, ...col.body);
    let timer = 0, seq = 0, scol = null;
    box.oninput = () => {
      clearTimeout(timer);
      timer = setTimeout(async () => {
        const q = box.value.trim(), mine = ++seq;
        if (scol) { scol.destroy(); scol = null; }
        results.hidden = !q; catalog.hidden = !!q;
        if (!q) { results.replaceChildren(); return; }
        results.replaceChildren(h('div', { class: 'feedmsg dim' }, 'Searching…'));
        try {
          const r = await api.get('/api/radio/search?q=' + enc(q));
          if (mine !== seq || my !== nav) return;
          scol = collection({ my, key: viewModeKey('radio', 'search'), modes: true, empty: 'No stations found',
            page: async () => ({ rows: r.stations || [], done: true }), opts: stationHitOpts, ...radioView(my, true) });
          results.replaceChildren(listBar(scol.toggle), scol.tools, ...scol.body);
          scol.start();
        } catch (e) { if (mine === seq) results.replaceChildren(msg('error', e.message || 'Search failed')); }
      }, 350);
    };
    show(hdr, box, results, catalog);
    col.start();
  }
  // sorting and filters of station lists; details of catalog stations are fetched per page
  const byNum = (k) => (a, b) => ((b.meta && b.meta[k]) || 0) - ((a.meta && a.meta[k]) || 0);
  function radioView(my, hasMeta) {
    return {
      search: true,
      sorts: [{ id: '', label: 'Default' }, { id: 'name', label: 'Name', cmp: (a, b) => String(a.title).localeCompare(String(b.title)) },
        { id: 'votes', label: 'Most popular', cmp: byNum('votes') }, { id: 'bitrate', label: 'Highest bitrate', cmp: byNum('bitrate') }],
      facets: [
        { id: 'country', label: 'Country', get: (o) => [o.meta && o.meta.country] },
        { id: 'tag', label: 'Genre', get: (o) => (o.meta && o.meta.tags) || [] },
        { id: 'codec', label: 'Format', get: (o) => [o.meta && o.meta.codec] },
        { id: 'lang', label: 'Language', get: (o) => [o.meta && o.meta.language] },
      ],
      enrich: hasMeta ? null : async (os) => {
        const st = os.filter((o) => o.station).slice(0, 240);
        for (let i = 0; i < st.length; i += 60) {
          const chunk = st.slice(i, i + 60);
          const r = await api.post('/api/radio/meta', { names: chunk.map((o) => o.title) });
          const by = new Map((r.stations || []).map((m) => [m.name, m]));
          for (const o of chunk) {
            const m = by.get(o.title);
            // a logo is asked for only when the directory has one: no 404s for the rest
            if (m && m.found) { o.meta = m; o.sub = stationSub(m) || o.sub; o.dead = !m.online; if (m.logo) o.art = stationArtUrl(o.title); }
          }
        }
      },
    };
  }
  // a station found by the directory search: plays its stream as given
  function stationHitOpts(hit) {
    const hh = heartBtn({ req: { kind: 'radio', provider: 'radio', id: 'f:' + hit.url, title: hit.name } });
    const play = () => playTrack({ title: hit.name, path: hit.url, stream: true, genre: (hit.tags || []).join(',') });
    return { title: hit.name, sub: stationSub(hit) || (hit.tags || []).join(', '), tkind: 'radio', art: hit.logo ? '/api/art/station?id=' + enc(hit.id) : '',
      meta: hit, dead: !hit.online, heart: hh, onTap: play,
      more: () => ({ spec: { kind: 'radio', fav: favState(hh) }, run: { play, fav: () => hh.toggle() } }) };
  }
  // Which top-level folder of the music directory (Rus, French, Rock, ...) a library artist or album
  // belongs to, matched by name; empty when the library is not that folder tree or the name is ambiguous.
  let folderGroups = {}, groupsAt = 0;
  async function loadGroups() {
    if (Date.now() - groupsAt < 300000) return;
    groupsAt = Date.now();
    try { folderGroups = (await api.get('/api/files/groups')).groups || {}; } catch (e) { /* no music folder: the facet stays empty */ }
  }
  const folderOf = (artist, name) => folderGroups[String(artist || '').trim().toLowerCase()] || folderGroups[String(name || '').trim().toLowerCase()] || '';
  // the entry options of one row of a provider list
  function browseOpts(prov, kind, row) {
    const name = rowTitle(row);
    const drillBase = '#browse/' + enc(prov) + '/' + kind + '/';
    const coll = (k, href, sub, end, tkind) => {
      const hh = heartBtn({ key: favKey(k, prov, row.id), req: { kind: k, provider: prov, id: row.id, title: name, subtitle: k === 'artist' ? '' : (row.artist || '') } });
      const attrs = { artist: row.artist || '', genre: row.genre || '', year: Number(row.year) || 0, decade: row.year ? Math.floor(row.year / 10) * 10 + 's' : '',
        tracks: Number(row.track_count) || 0, albums: Number(row.album_count) || 0, section: row.section || '',
        folder: folderOf(k === 'artist' ? name : row.artist, name) };
      return { title: name, sub, end, href, tkind, attrs, art: itemArtUrl(k, prov, row.id), heart: hh, more: collectionMore(k, prov, row.id, hh),
        onPlay: k === 'album' ? () => enq({ kind: 'album', provider: prov, id: row.id, mode: 'play' }) : null };
    };
    if (kind === 'playlists') {
      return coll('playlist', drillBase + 'playlist/' + enc(row.id || ''), row.artist || '', row.track_count != null ? plural(row.track_count, 'track') : '', 'queue');
    }
    if (kind === 'albums') {
      return coll('album', drillBase + 'album/' + enc(row.id || ''), [row.artist, row.year].filter(Boolean).join(' · '), '', 'album');
    }
    if (kind === 'artists') {
      return coll('artist', drillBase + 'artist/' + enc(row.id || ''), row.album_count != null ? plural(row.album_count, 'album') : row.artist || '', '', 'person');
    }
    // radio catalog: stations have no path; tap plays via provider.load
    const hh = row.favoritable ? heartBtn({ on: !!row.favorite || /^★ /.test(row.name || ''), req: { kind: 'radio', provider: prov, id: row.id } }) : null;
    return { title: name, sub: row.genre || '', tkind: 'radio', station: !!row.favoritable, heart: hh, more: radioMore(prov, row.id, hh),
      onTap: () => { cmd('provider.load', { provider: prov, playlist: row.id }, true); rev(); } };
  }
  // ---- Files: the music folder cliamp's own file browser starts in ----
  let filesName;
  async function filesRoot() {
    if (filesName === undefined) {
      try { filesName = (await api.get('/api/files/root')).name; } catch (e) { filesName = null; } // 404: the feature is off
    }
    return filesName;
  }
  async function renderFiles(my, show, rel) {
    let l;
    try { l = await api.get('/api/files?path=' + enc(rel)); }
    catch (e) {
      show(head('Files', '#browse'), msg('error', e.status === 404 ? 'That folder is not there any more' : e.status === 400 ? 'That is not a folder in the music library' : (e.message || 'Could not read the folder')));
      return;
    }
    if (my !== nav) return;
    const parent = filesParent(l.path);
    const kindOf = (e) => (e.dir ? 'folder' : 'file');
    const go = (kind, id, mode) => enq({ kind, provider: 'files', id, mode });
    const fileOpts = (e) => {
      const id = filesRel(l.path, e.name);
      return {
        title: e.name, icon: e.dir ? 'folder' : 'note', tkind: e.dir ? 'folder' : 'note',
        art: e.dir ? folderArtUrl(id) : '', // a folder shows its cover (or its first album's)
        href: e.dir ? filesHash(id) : null,
        onTap: e.dir ? null : () => go('file', id, 'play'),
        more: () => ({ spec: { kind: kindOf(e) }, run: { play: () => go(kindOf(e), id, 'play'), add: () => go(kindOf(e), id, 'append') } }),
      };
    };
    const filter = h('input', { type: 'search', class: 'filter', placeholder: 'Filter this folder', autocomplete: 'off', autocapitalize: 'off', spellcheck: 'false', 'aria-label': 'Filter this folder' });
    // the folder is fully loaded; it is drawn in chunks as you scroll, and again when the filter changes
    const host = h('div', { class: 'feedhost' });
    let col = null;
    const draw = () => {
      if (col) col.destroy();
      const es = filterEntries(l.entries, filter.value);
      col = collection({ my, key: viewModeKey('files'), modes: true, page: chunkPage(() => es), opts: fileOpts,
        empty: l.entries.length ? 'Nothing matches' : 'No folders or audio files here' });
      host.replaceChildren(...col.body);
      slot.replaceChildren(...[col.toggle].filter(Boolean)); // the filter box itself stays put, so typing keeps its focus
      col.start();
    };
    const slot = h('span', { class: 'vslot' });
    const bar = h('div', { class: 'listbar' }, ...[l.entries.length > 8 ? filter : null, slot].filter(Boolean));
    filter.addEventListener('input', draw);
    // the whole folder (everything below it); not offered for the library root
    const actions = l.path && l.entries.length ? h('div', { class: 'hdr-actions' },
      h('button', { class: 'btn primary', onclick: () => go('folder', l.path, 'play') }, '▶ Play'),
      h('button', { class: 'btn', onclick: () => go('folder', l.path, 'append') }, '+ Add')) : null;
    const seg = h('div', { class: 'seg' },
      h('a', { href: '#browse/local/playlists' }, 'Playlists'), h('a', { href: filesHash(''), class: 'on' }, 'Files'));
    show(head(l.path ? l.name : 'Local', parent === null ? '#browse' : filesHash(parent), actions), seg,
      l.path ? h('div', { class: 'crumb dim' }, l.path.split('/').join(' › ')) : null, bar, host);
    draw();
  }

  async function renderDrill(my, show, hdr, prov, what, id, backKind) {
    // an artist drills into its albums (each opens the album page); album and
    // playlist drill into tracks
    const isArtist = what === 'artist';
    const url = (off) => '/api/library/' + enc(prov) + '/' + what + '/' + enc(id) + '/' + (isArtist ? 'albums' : 'tracks') + '?offset=' + off + '&limit=' + PAGE;
    let off = 0;
    const title = what === 'album' ? 'Album' : what === 'artist' ? 'Artist' : 'Playlist';
    const back = '#browse/' + enc(prov) + '/' + (backKind || 'playlists');
    // the page header's name is generic, so the heart learns the album/artist
    // name from the first rows; playlists are hearted from their list row
    const hreq = { kind: what, provider: prov, id, title: '', subtitle: '' };
    const hheart = what === 'playlist' ? null : heartBtn({ key: favKey(what, prov, id), req: hreq });
    let first = true;
    const seen = [];
    const info = what === 'album' ? h('div', { class: 'ainfo' }) : null;
    const paintInfo = () => {
      const f = albumFacts(seen);
      info.replaceChildren(
        h('div', { class: 'acover' }, h('span', { class: 'ph2' }, svg('album')), h('img', { alt: '', src: itemArtUrl('album', prov, id), onerror: (e) => e.target.remove() })),
        h('div', { class: 'afacts' }, h('div', { class: 'aname' }, f.name || ''), f.artist ? h('div', { class: 'aartist' }, f.artist) : null,
          h('div', { class: 'ameta dim' }, albumFactsLine(f))));
    };
    const col = collection({ my, key: viewModeKey('browse', prov, 'artist-albums'), modes: isArtist, ...(isArtist ? LIST_VIEWS['artist-albums'] : {}), empty: isArtist ? 'No albums' : 'No tracks',
      page: async () => {
        const r = await api.get(url(off));
        const rows = (isArtist ? r.albums : r.tracks) || [];
        if (first && rows[0]) {
          first = false;
          if (what === 'album') { hreq.title = rows[0].album || ''; hreq.subtitle = rows[0].artist || ''; }
          if (isArtist) hreq.title = rows[0].artist || '';
        }
        if (info) { seen.push(...rows); paintInfo(); }
        off += rows.length;
        return { rows, done: pageDone(rows.length) };
      },
      opts: isArtist ? (a) => browseOpts(prov, 'albums', a) : trackOpts });
    const go = (mode) => enq({ kind: what, provider: prov, id, mode });
    const actions = h('div', { class: 'hdr-actions' }, hheart,
      h('button', { class: 'btn primary', onclick: () => go('play') }, '▶ Play'),
      h('button', { class: 'btn', onclick: () => go('append') }, '+ Add'));
    show(head(title, back, actions), info, listBar(col.toggle), col.tools, ...col.body);
    col.start();
  }

  // ---- Search ----
  let searchTimer = 0, searchSeq = 0, searchProv = localStorage.getItem('cliamp-next-prov') || '';
  async function renderProviderChips() {
    const ps = await loadProviders();
    const list = (ps || []).filter((x) => x.searchable);
    if (!list.find((x) => x.key === searchProv)) searchProv = list.length ? list[0].key : '';
    $('prov').replaceChildren(...list.map((x) =>
      h('a', { href: '#search', class: x.key === searchProv ? 'on' : null, onclick: (e) => {
        e.preventDefault();
        searchProv = x.key;
        localStorage.setItem('cliamp-next-prov', x.key);
        renderProviderChips();
        runSearch();
      } }, x.name)));
    if (searchProv) runSearch();
  }
  function wireSearch() {
    $('q').addEventListener('input', () => {
      clearTimeout(searchTimer);
      searchTimer = setTimeout(runSearch, 300);
    });
  }
  async function runSearch() {
    const q = $('q').value.trim(), my = ++searchSeq, out = $('results');
    if (!searchProv) { out.replaceChildren(msg('empty', 'no searchable provider')); return; }
    if (!q) { out.replaceChildren(); return; }
    try {
      if (searchProv === 'radio') { // the public station directory: name, genre and country at once
        const rs = await api.get('/api/radio/search?q=' + enc(q));
        if (my !== searchSeq) return;
        const hits = rs.stations || [];
        out.replaceChildren(hits.length ? h('ul', { class: 'list' }, hits.map((x) => item(stationHitOpts(x)))) : msg('empty', 'No stations found'));
        return;
      }
      const r = await api.get('/api/search/' + enc(searchProv) + '?q=' + enc(q) + '&offset=0&limit=50');
      if (my !== searchSeq) return;
      const rows = r.tracks || [];
      out.replaceChildren(rows.length ? h('ul', { class: 'list' }, rows.map((t) => item(trackOpts(t)))) : msg('empty', 'Nothing found'));
    } catch (e) {
      if (my === searchSeq) out.replaceChildren(msg('error', e.message));
    }
  }


  // ---- Settings ----
  let setUi = null; // the live controls of the Settings page, refreshed from state
  const setCard = (title, ...kids) => h('section', { class: 'scard' }, h('h3', null, title), ...kids);
  const setRow = (label, control, sub) => h('div', { class: 'srow' }, h('div', { class: 'slabel' }, h('div', null, label), sub ? h('div', { class: 'dim ssub' }, sub) : null), control);
  const switchBtn = (on, onchange, label) => {
    const b = h('button', { class: 'switch', role: 'switch', 'aria-label': label, 'aria-checked': String(!!on), onclick: () => { const v = b.getAttribute('aria-checked') !== 'true'; b.setAttribute('aria-checked', String(v)); onchange(v); } }, h('span'));
    return b;
  };
  const chipRow = (items, current, pick, fmt) => h('div', { class: 'chips' }, items.map((v) =>
    h('button', { class: 'chip', 'aria-pressed': String(String(v) === String(current)), onclick: () => pick(v) }, fmt ? fmt(v) : String(v))));

  async function renderSettings() {
    const root = $('settings'), my = ++setSeq;
    const st = () => S.state || {};
    // audio output
    const devList = h('div', { class: 'radios', role: 'radiogroup', 'aria-label': 'Audio output' }, h('div', { class: 'dim' }, 'Loading…'));
    async function loadDevices() {
      const r = await cmd('device', { name: 'list' }, false);
      if (my !== setSeq) return;
      const devs = (r && r.job && r.job.result && r.job.result.devices) || [];
      if (!devs.length) { devList.replaceChildren(h('div', { class: 'dim' }, r ? 'No output devices reported' : 'Player offline')); return; }
      devList.replaceChildren(...devs.map((d) => h('button', { class: 'radio', role: 'radio', 'aria-checked': String(!!d.active), onclick: async () => {
        if (d.active) return;
        devList.querySelectorAll('.radio').forEach((x) => { x.disabled = true; });
        const done = await cmd('device', { name: d.name }, false);
        toast(done ? 'Output: ' + deviceLabel(d.name) : 'Could not switch the output', !done);
        loadDevices();
      } }, h('span', { class: 'dot' }), h('span', { class: 'rtext' }, h('span', null, deviceLabel(d.name)), h('span', { class: 'dim ssub' }, d.name)))));
    }
    // sound
    const presetSel = h('select', { class: 'vsel', 'aria-label': 'EQ preset', onchange: async () => { if (presetSel.value) await cmd('eq', { name: presetSel.value }, false); } },
      h('option', { value: '' }, 'Custom'), EQ_PRESETS.map((p) => h('option', { value: p }, p)));
    const sliders = EQ_FREQS.map((f, i) => {
      const val = h('span', { class: 'eqv' }, '0');
      const inp = h('input', { type: 'range', min: -12, max: 12, step: 1, value: 0, 'aria-label': f + ' Hz', orient: 'vertical' });
      let t = 0;
      inp.oninput = () => { val.textContent = inp.value; clearTimeout(t); t = setTimeout(() => cmd('eq', { band: i, value: Number(inp.value) }, false), 120); };
      return { el: h('div', { class: 'eqband' }, val, inp, h('span', { class: 'eqf dim' }, f)), inp, val };
    });
    const monoSw = switchBtn(false, (v) => cmd('mono', { name: v ? 'on' : 'off' }, false), 'Mono output');
    const speedChips = h('div');
    const paintSound = () => {
      const s = st();
      presetSel.value = EQ_PRESETS.includes(s.eq_preset) ? s.eq_preset : '';
      (s.eq_bands || []).forEach((v, i) => { if (sliders[i] && document.activeElement !== sliders[i].inp) { sliders[i].inp.value = v; sliders[i].val.textContent = String(Math.round(v)); } });
      monoSw.setAttribute('aria-checked', String(!!s.mono));
      speedChips.replaceChildren(chipRow(SPEEDS, Number(s.speed) || 1, (v) => cmd('speed', { value: v }, false), speedLabel));
    };
    setUi = { my, paintSound };
    // this browser
    const viewChips = chipRow(['tiles', 'list'], getPref(PREF_VIEW, 'tiles'), (v) => { setPref(PREF_VIEW, v); toast('New lists open as ' + v); renderSettings(); }, (v) => (v === 'tiles' ? 'Tiles' : 'List'));
    const idleChips = chipRow(['0', '3.5', '6', '12'], getPref(PREF_FSIDLE, '3.5'), (v) => { setPref(PREF_FSIDLE, v); renderSettings(); }, (v) => (v === '0' ? 'Never' : Number(v) + ' s'));
    const resetBtn = h('button', { class: 'btn', onclick: () => {
      try { Object.keys(localStorage).filter((k) => /^(view|order):/.test(k)).forEach((k) => localStorage.removeItem(k)); } catch (e) { /* storage may be off */ }
      toast('Saved view choices cleared');
    } }, 'Reset saved view choices');
    // library
    const lib = h('div', { class: 'dim' }, 'Loading…');
    // about
    const about = h('div', { class: 'dim' }, 'Loading…');

    root.replaceChildren(
      h('div', { class: 'head' }, h('h2', null, 'Settings')),
      setCard('Audio output', devList, h('p', { class: 'dim snote' }, 'Switching restarts the audio stream; playback may pause for a moment.')),
      setCard('Sound', setRow('Equalizer', presetSel), h('div', { class: 'eq' }, sliders.map((x) => x.el)),
        setRow('Mono', monoSw, 'Mix both channels into one'), setRow('Speed', speedChips)),
      setCard('This browser', setRow('New lists open as', viewChips), setRow('Hide fullscreen controls after', idleChips), setRow('Sort and filter choices', resetBtn)),
      setCard('Library', lib),
      setCard('About', about));
    paintSound();
    loadDevices();

    // library and about come from the bridge
    try {
      const [prov, ab, caps] = await Promise.all([loadProviders(), api.get('/api/about'), api.get('/api/capabilities').catch(() => null)]);
      if (my !== setSeq) return;
      const b = ab.bridge || {};
      lib.replaceChildren(
        ...(prov || []).map((p) => setRow(p.name, h('span', { class: 'dim' }, [p.searchable && 'search', p.browse_albums && 'albums', p.browse_artists && 'artists', p.catalog && 'catalog'].filter(Boolean).join(' · ')))),
        setRow('Navidrome covers', h('span', { class: 'dim' }, b.navidrome ? 'login found in cliamp config' : 'no login: placeholders'), 'Album, artist and playlist pictures'),
        setRow('Music folder', h('span', { class: 'dim' }, b.music_root ? b.music_root : 'off'), 'The file browser and the Folder filter'),
        setRow('Radio directory', h('span', { class: 'dim' }, ab.features && ab.features.radio_directory ? 'radio-browser.info' : 'off'), 'Station logos, details and search'));
      const used = b.art_cache_bytes || 0, max = b.art_cache_max || 0;
      about.replaceChildren(
        setRow('cliamp-web', h('span', null, (ab.commit ? ab.commit : 'dev') + (ab.modified ? ' +local changes' : '')), ab.built ? 'built ' + new Date(ab.built).toLocaleString() : ''),
        setRow('Bridge', h('span', { class: 'dim' }, (b.listen || '') + (b.lan ? ' · reachable on the network' : ' · this machine only')), ab.go + ' · ' + ab.os),
        setRow('Running for', h('span', { class: 'dim' }, fmtUptime(ab.uptime))),
        setRow('Browsers connected', h('span', { class: 'dim' }, String(ab.clients == null ? '–' : ab.clients))),
        setRow('cliamp', h('span', { class: ab.player && ab.player.online ? 'ok' : 'bad' }, ab.player && ab.player.online ? 'online' : 'offline'),
          caps && Array.isArray(caps.result) ? 'API v' + (caps.version || 2) + ' · ' + caps.result.length + ' operations' : ''),
        setRow('Picture cache', h('span', { class: 'dim' }, fmtBytes(used) + (max ? ' of ' + fmtBytes(max) : '')),
          'Covers, discs and logos kept on the bridge'));
    } catch (e) {
      if (my === setSeq) { lib.replaceChildren(h('div', { class: 'dim' }, 'Could not load')); about.replaceChildren(h('div', { class: 'dim' }, 'Could not load')); }
    }
  }
  let setSeq = 0;

  // ---- routing ----
  function route() {
    trackTrail();
    const r = parseHash(location.hash);
    document.body.dataset.tab = r.tab;
    closeMenu(false);
    if (r.tab === 'browse') renderBrowse(r.rest);
    if (r.tab === 'favorites') { renderFavorites(); loadFavs(); }
    if (r.tab === 'search') renderProviderChips();
    if (r.tab === 'settings') renderSettings();
    if (r.tab === 'queue' || (r.tab === 'now' && matchMedia('(min-width: 900px)').matches)) {
      const cur = document.querySelector('#queue li.cur');
      if (cur) cur.scrollIntoView({ block: 'center' });
    }
    if (r.tab === 'search' && matchMedia('(min-width: 900px)').matches) $('q').focus();
  }

  document.querySelectorAll('[data-icon]').forEach((el) => setIcon(el, el.dataset.icon));
  window.addEventListener('hashchange', route);
  wireNow();
  wireFs();
  wireHeart();
  setInterval(() => { if (!document.hidden) loadFavs(); }, 60000); // changes made in cliamp itself send no event
  loadFavs();
  wireSearch();
  renderQueue();
  route();
  loadState();
  loadQueue();
  connect();
})();

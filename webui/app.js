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

// fsLayout: with room for two squares side by side show both art faces.
function fsLayout(w, h) { return w >= 700 && h > 0 && w / h >= 1.25 ? 'both' : 'single'; }

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

const TABS = ['now', 'queue', 'favorites', 'browse', 'search'];
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
  module.exports = { initial, reduce, filesRel, filesHash, filesParent, filterEntries, queueView, rowActions, menuMode, menuPlacement, menuKey, progressText, favKey, trackFavKey, favKeys, favFilter, favCounts, isPlayingFavStation, artPlan, albumKey, trackDir, skipDip, nextTrack, techLine, fsLayout, artUrl, spinStep, spinPlan, curIndex, curVolume, position, backoff, WATCHDOG_MS, makeWatchdog, splitPath, TABS, parseHash, fmtTime, lyricIndex, moveTarget, plural, makeApi };
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
    queue: 'M3 13h2v-2H3v2zm0 4h2v-2H3v2zm0-8h2V7H3v2zm4 4h14v-2H7v2zm0 4h14v-2H7v2zM7 7v2h14V7H7z',
    browse: 'M4 6H2v14c0 1.1.9 2 2 2h14v-2H4V6zm16-4H8c-1.1 0-2 .9-2 2v12c0 1.1.9 2 2 2h12c1.1 0 2-.9 2-2V4c0-1.1-.9-2-2-2zm-1 9H9V9h10v2zm-4 4H9v-2h6v2zm4-8H9V5h10v2z',
    search: 'M15.5 14h-.79l-.28-.27A6.47 6.47 0 0 0 16 9.5 6.5 6.5 0 1 0 9.5 16c1.61 0 3.09-.59 4.23-1.57l.27.28v.79l5 4.99L20.49 19l-4.99-5zm-6 0C7.01 14 5 11.99 5 9.5S7.01 5 9.5 5 14 7.01 14 9.5 11.99 14 9.5 14z',
    expand: 'M7 14H5v5h5v-2H7v-3zm-2-4h2V7h3V5H5v5zm12 7h-3v2h5v-5h-2v3zM14 5v2h3v3h2V5h-5z',
    collapse: 'M5 16h3v3h2v-5H5v2zm3-8H5v2h5V5H8v3zm6 11h2v-3h3v-2h-5v5zm2-11V5h-2v5h5V8h-3z',
    add: 'M19 13h-6v6h-2v-6H5v-2h6V5h2v6h6v2z',
    up: 'M7.41 15.41L12 10.83l4.59 4.58L18 14l-6-6-6 6z',
    down: 'M7.41 8.59L12 13.17l4.59-4.58L18 10l-6 6-6-6z',
    trash: 'M6 19c0 1.1.9 2 2 2h8c1.1 0 2-.9 2-2V7H6v12zM19 4h-3.5l-1-1h-5l-1 1H5v2h14V4z',
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
    angle: 0, w: 0, a: 0, target: 0, k: 2.2, last: 0, raf: 0, info: null, pref: false,
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
    const t = techLine(art.info);
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
    loadImg(artUrl('artist', p), seq, (u) => { $('artist-img').src = u; $('artist-img').hidden = false; }, () => { $('artist-img').hidden = true; });
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
    const dur = Number(S.state && S.state.duration) || 0;
    api.get('/api/trackinfo?path=' + encodeURIComponent(p) + (dur ? '&duration=' + dur : ''))
      .then((info) => { if (seq === art.seq) { art.info = info; renderTech(); } })
      .catch(() => { if (seq === art.seq) { art.info = null; renderTech(); } });
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
    document.body.classList.toggle('fs-open', on);
    if (on) {
      layoutFs();
      try { const r = document.documentElement.requestFullscreen && document.documentElement.requestFullscreen(); if (r && r.catch) r.catch(() => {}); } catch (e) { /* not available */ }
    } else {
      delete $('now').dataset.art;
      try { if (document.fullscreenElement) { const r = document.exitFullscreen(); if (r && r.catch) r.catch(() => {}); } } catch (e) { /* ignore */ }
    }
    paintArt();
  }
  function wireFs() {
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
    document.title = song ? (song.title || '') + ' · cliamp' : 'cliamp';
    if (!st) return;
    if (song) {
      $('trackno').textContent = song.track_number ? String(song.track_number).padStart(2, '0') + ' · ' : '';
      $('titletext').textContent = song.title || '';
      $('artist').textContent = song.artist || '';
      updateArt(song);
      $('album').textContent = [song.album, song.year].filter(Boolean).join(' · ');
      $('dur').textContent = fmtTime(st.duration);
      $('bar').setAttribute('aria-valuemax', Math.round(Number(st.duration) || 0));
      $('bar').classList.toggle('unseekable', !st.seekable);
    }
    if (!song) updateArt(null);
    renderNext();
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
  function item(o) {
    const li = h('li', { class: o.cur ? 'cur' : null });
    const inner = [h('div', { class: 'txt' }, h('div', { class: 't1' }, o.title || ''), o.sub ? h('div', { class: 't2' }, o.sub) : null)];
    if (o.end) inner.push(h('div', { class: 'end' }, o.end));
    if (o.icon) inner.unshift(h('span', { class: 'lead' }, o.icon));
    // every item has the same "..." menu; right-click opens it too
    let trigger = null;
    const open = () => {
      if (menuState && menuState.trigger === trigger) { closeMenu(); return; }
      const m = o.more();
      openMenu({ trigger, title: o.title || '', subtitle: o.sub || '', spec: m.spec, run: m.run });
    };
    const tap = o.onTap || (o.more ? open : null);
    const row = o.href ? h('a', { class: 'row', href: o.href }, inner)
      : h('div', { class: 'row', role: 'button', tabindex: 0, onclick: tap,
        onkeydown: (e) => { if (e.key === 'Enter' && tap) tap(); } }, inner);
    const top = h('div', { class: 'toprow' }, row);
    if (o.heart) top.append(o.heart);
    if (o.more) {
      trigger = h('button', { class: 'ic more', 'aria-label': 'More actions', 'aria-haspopup': 'menu', 'aria-expanded': 'false' });
      trigger.append(svg('more'));
      trigger.onclick = (e) => { e.preventDefault(); e.stopPropagation(); open(); };
      row.addEventListener('contextmenu', (e) => { e.preventDefault(); open(); });
      top.append(trigger);
    }
    li.append(top);
    return li;
  }
  const head = (title, back, extra) => h('div', { class: 'head' },
    back ? h('a', { class: 'back', href: back, 'aria-label': 'Back' }, svg('back')) : null, h('h2', null, title), extra);
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

  // ---- Favorites ----
  // Tracks and stations live in cliamp, albums/artists/playlists in the bridge;
  // the page only holds the merged listing and a set of keys for the hearts.
  let favItems = [], favOn = new Set(), favPartial = false, favKind = 'all';
  const paintHearts = () => document.querySelectorAll('.heart').forEach((b) => b.paint && b.paint());
  async function loadFavs() {
    let r;
    try { r = await api.get('/api/favorites'); } catch (e) { return; } // quiet: hearts keep their last state
    favItems = r.items || [];
    favPartial = !!r.partial;
    favOn = favKeys(favItems);
    paintHearts();
    renderNowHeart();
    if (document.body.dataset.tab === 'favorites') renderFavorites();
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
  function favRow(it) {
    const prov = it.provider, k = it.kind, title = it.title || '', sub = it.subtitle || '';
    const heart = (req) => heartBtn({ key: favKey(k, prov, it.id), req });
    if (k === 'track') {
      const t = it.track || {};
      const hh = heart({ kind: 'track', track: trackArg(t), title, subtitle: sub });
      return item({ title, sub, onTap: () => playTrack(t), heart: hh, more: trackMore(t, hh) });
    }
    if (k === 'radio') {
      const hh = heart({ kind: 'radio', provider: 'radio', id: it.id });
      return item({ title, sub: 'radio', heart: hh, more: radioMore('radio', it.id, hh),
        onTap: () => { cmd('provider.load', { provider: 'radio', playlist: it.id }, true); rev(); } });
    }
    const dir = k === 'album' ? 'albums' : k === 'artist' ? 'artists' : 'playlists';
    const hh = heart({ kind: k, provider: prov, id: it.id, title, subtitle: sub });
    return item({ title, sub, href: '#browse/' + enc(prov) + '/' + dir + '/' + k + '/' + enc(it.id), heart: hh, more: collectionMore(k, prov, it.id, hh) });
  }
  function renderFavorites() {
    const counts = favCounts(favItems);
    if (favKind !== 'all' && !counts[favKind]) favKind = 'all';
    const chips = h('div', { class: 'seg' }, FAV_KINDS.filter(([k]) => k === 'all' || counts[k]).map(([k, label]) =>
      h('button', { class: k === favKind ? 'on' : null, onclick: () => { favKind = k; renderFavorites(); } }, label + (counts[k] ? ' ' + counts[k] : ''))));
    const rows = favFilter(favItems, favKind).map(favRow);
    $('favorites').replaceChildren(...[head('Favorites'), chips,
      favPartial && msg('empty', 'The player is offline: tracks and stations are not shown'),
      rows.length ? h('ul', { class: 'list' }, rows) : msg('empty', 'Nothing here yet. Tap the heart on anything to keep it.')].filter(Boolean));
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
  ];
  const kindsOf = (p) => PROVIDER_KINDS.filter(([k]) =>
    k === 'playlists' || (k === 'albums' && p.browse_albums) || (k === 'artists' && p.browse_artists) || (k === 'catalog' && p.catalog));
  async function loadProviders() {
    if (providers) return providers;
    const seq = ++provSeq;
    try {
      const r = await api.get('/api/library/providers');
      if (seq === provSeq) providers = r.providers || [];
    } catch (e) { fail(e); return null; }
    return providers;
  }
  const rowTitle = (r) => r.name || r.title || '';
  async function renderBrowse(rest) {
    const my = ++nav, root = $('browse');
    const p = splitPath(rest) || [''];
    const show = (...kids) => { if (my === nav) root.replaceChildren(...kids); };
    const listUrl = (prov, kind, off) => '/api/library/' + enc(prov) + '/' + kind + '?offset=' + off + '&limit=50';

    if (p[0] === '' || p[0] === 'home') {
      const ps = await loadProviders();
      if (!ps) { show(head('Browse'), msg('error', 'no providers')); return; }
      const fname = await filesRoot();
      show(head('Browse'), h('div', { class: 'tiles' },
        ps.map((pr) => h('a', { class: 'tile', href: '#browse/' + enc(pr.key) + '/playlists' }, pr.name,
          h('span', null, kindsOf(pr).map((k) => k[1]).join(' · ')))),
        fname ? h('a', { class: 'tile', href: filesHash('') }, 'Files', h('span', null, fname)) : null));
      return;
    }
    if (p[0] === 'files') { await renderFiles(my, show, p[1] || ''); return; }
    const prov = p[0];
    const ps = await loadProviders();
    const info = (ps || []).find((x) => x.key === prov);
    if (!info) { show(head('Browse', '#browse'), msg('error', 'unknown provider: ' + prov)); return; }
    const kind = p[1] || 'playlists';
    const seg = h('div', { class: 'seg' }, kindsOf(info).map(([k, label]) =>
      h('a', { href: '#browse/' + enc(prov) + '/' + k, class: k === kind ? 'on' : null }, label)));
    const hdr = head(info.name, '#browse', seg);

    if (p.length >= 4 && (p[2] === 'album' || p[2] === 'artist' || p[2] === 'playlist')) {
      return renderDrill(my, show, hdr, prov, p[2], p[3], kind);
    }
    // a plain kind list with More paging
    let off = 0, shown = 0;
    const ul = h('ul', { class: 'list' }), more = h('button', { class: 'btn', hidden: true }, 'More');
    const load = async () => {
      more.disabled = true;
      let r;
      try { r = await api.get(listUrl(prov, kind, off)); } catch (e) { more.disabled = false; return toast(e.message, true); }
      if (my !== nav) return;
      const rows = r[kind] || r.playlists || [];
      shown += rows.length; off += rows.length;
      ul.append(...rows.map((row) => browseRow(prov, kind, row)));
      more.hidden = !(r.total != null && shown < r.total);
      more.disabled = false;
      if (!shown) ul.append(msg('empty', 'Nothing here'));
    };
    more.onclick = load;
    show(hdr, ul, h('div', { class: 'bar' }, more));
    await load();
  }
  function browseRow(prov, kind, row) {
    const name = rowTitle(row);
    const drillBase = '#browse/' + enc(prov) + '/' + kind + '/';
    const coll = (k, href, sub, end) => {
      const hh = heartBtn({ key: favKey(k, prov, row.id), req: { kind: k, provider: prov, id: row.id, title: name, subtitle: k === 'artist' ? '' : (row.artist || '') } });
      return item({ title: name, sub, end, href, heart: hh, more: collectionMore(k, prov, row.id, hh) });
    };
    if (kind === 'playlists') {
      return coll('playlist', drillBase + 'playlist/' + enc(row.id || ''), row.artist || '', row.track_count != null ? plural(row.track_count, 'track') : '');
    }
    if (kind === 'albums') {
      return coll('album', drillBase + 'album/' + enc(row.id || ''), [row.artist, row.year].filter(Boolean).join(' · '));
    }
    if (kind === 'artists') {
      return coll('artist', drillBase + 'artist/' + enc(row.id || ''), row.album_count != null ? plural(row.album_count, 'album') : row.artist || '');
    }
    // radio catalog: stations have no path; tap plays via provider.load
    const hh = row.favoritable ? heartBtn({ on: !!row.favorite || /^★ /.test(row.name || ''), req: { kind: 'radio', provider: prov, id: row.id } }) : null;
    return item({ title: name, sub: row.genre || '', heart: hh, more: radioMore(prov, row.id, hh),
      onTap: () => { cmd('provider.load', { provider: prov, playlist: row.id }, true); rev(); } });
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
    const row = (e) => {
      const id = filesRel(l.path, e.name);
      return item({
        title: e.name, icon: svg(e.dir ? 'folder' : 'note'),
        href: e.dir ? filesHash(id) : null,
        onTap: e.dir ? null : () => go('file', id, 'play'),
        more: () => ({ spec: { kind: kindOf(e) }, run: { play: () => go(kindOf(e), id, 'play'), add: () => go(kindOf(e), id, 'append') } }),
      });
    };
    const ul = h('ul', { class: 'list' });
    const filter = h('input', { type: 'search', class: 'filter', placeholder: 'Filter this folder', autocomplete: 'off', autocapitalize: 'off', spellcheck: 'false', 'aria-label': 'Filter this folder' });
    const draw = () => {
      const es = filterEntries(l.entries, filter.value);
      ul.replaceChildren(...(es.length ? es.map(row) : [msg('empty', l.entries.length ? 'Nothing matches' : 'No folders or audio files here')]));
    };
    filter.addEventListener('input', draw);
    // the whole folder (everything below it); not offered for the library root
    const actions = l.path && l.entries.length ? h('div', { class: 'hdr-actions' },
      h('button', { class: 'btn primary', onclick: () => go('folder', l.path, 'play') }, '▶ Play'),
      h('button', { class: 'btn', onclick: () => go('folder', l.path, 'append') }, '+ Add')) : null;
    draw();
    show(head(l.name || 'Files', parent === null ? '#browse' : filesHash(parent), actions),
      l.path ? h('div', { class: 'crumb dim' }, l.path.split('/').join(' › ')) : null,
      l.entries.length > 8 ? filter : null, ul);
  }

  async function renderDrill(my, show, hdr, prov, what, id, backKind) {
    // an artist drills into its albums (each opens the album page); album and
    // playlist drill into tracks
    const isArtist = what === 'artist';
    const url = (off) => '/api/library/' + enc(prov) + '/' + what + '/' + enc(id) + '/' + (isArtist ? 'albums' : 'tracks') + '?offset=' + off + '&limit=50';
    let off = 0, shown = 0, title = what === 'album' ? 'Album' : what === 'artist' ? 'Artist' : 'Playlist';
    const ul = h('ul', { class: 'list' }), more = h('button', { class: 'btn', hidden: true }, 'More');
    const back = '#browse/' + enc(prov) + '/' + (backKind || 'playlists');
    // the page header's name is generic, so the heart learns the album/artist
    // name from the first rows; playlists are hearted from their list row
    const hreq = { kind: what, provider: prov, id, title: '', subtitle: '' };
    const hheart = what === 'playlist' ? null : heartBtn({ key: favKey(what, prov, id), req: hreq });
    const load = async () => {
      more.disabled = true;
      let r;
      try { r = await api.get(url(off)); } catch (e) { show(head(title, back)); ul.append(msg('error', e.message)); return; }
      if (my !== nav) return;
      const rows = (isArtist ? r.albums : r.tracks) || [];
      if (!shown && rows[0]) {
        if (what === 'album') { hreq.title = rows[0].album || ''; hreq.subtitle = rows[0].artist || ''; }
        if (isArtist) hreq.title = rows[0].artist || '';
      }
      shown += rows.length; off += rows.length;
      ul.append(...rows.map(isArtist
        ? (a) => browseRow(prov, 'albums', a)
        : (t) => { const hh = trackHeart(t); return item({ title: t.title || rowTitle(t), sub: [t.artist, t.album].filter(Boolean).join(' · '),
          end: fmtTime(t.duration_secs), onTap: () => playTrack(t), heart: hh, more: trackMore(t, hh) }); }));
      more.hidden = !(r.total != null && shown < r.total);
      more.disabled = false;
      if (!shown) ul.append(msg('empty', isArtist ? 'No albums' : 'No tracks'));
    };
    more.onclick = load;
    const extra = what === 'artist' ? null : h('button', { class: 'btn primary', onclick: () => {
      if (!window.confirm('replace the queue with ' + what + ' "' + title + '"?')) return;
      cmd(what === 'album' ? 'provider.load_album' : 'provider.load',
        what === 'album' ? { provider: prov, album: id } : { provider: prov, playlist: id }, true);
      rev();
    } }, '▶ play ' + what);
    show(head(title, back, h('div', { class: 'hdr-actions' }, hheart, extra)), ul, h('div', { class: 'bar' }, more));
    await load();
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
      const r = await api.get('/api/search/' + enc(searchProv) + '?q=' + enc(q) + '&offset=0&limit=50');
      if (my !== searchSeq) return;
      const rows = r.tracks || [];
      out.replaceChildren(rows.length ? h('ul', { class: 'list' }, rows.map((t) => {
        const hh = trackHeart(t);
        return item({ title: t.title || rowTitle(t), sub: [t.artist, t.album].filter(Boolean).join(' · '),
          end: fmtTime(t.duration_secs), onTap: () => playTrack(t), heart: hh, more: trackMore(t, hh) });
      })) : msg('empty', 'Nothing found'));
    } catch (e) {
      if (my === searchSeq) out.replaceChildren(msg('error', e.message));
    }
  }

  // ---- routing ----
  function route() {
    const r = parseHash(location.hash);
    document.body.dataset.tab = r.tab;
    closeMenu(false);
    if (r.tab === 'browse') renderBrowse(r.rest);
    if (r.tab === 'favorites') { renderFavorites(); loadFavs(); }
    if (r.tab === 'search') renderProviderChips();
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

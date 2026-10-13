'use strict';
// The "next" cliamp-web page, ported from the MiSTer Subsonic web remote:
// same shape (pure testable core, one EventSource with backoff and a
// watchdog, hash routing, imperative rows). The data layer is the cliamp
// bridge: snapshot state, {op, params} commands, library lists.

// ---- token ----
const token =
  (typeof sessionStorage !== 'undefined' && sessionStorage.getItem('cliamp-token')) ||
  (typeof location !== 'undefined' && new URLSearchParams(location.search).get('token')) ||
  '';
if (typeof sessionStorage !== 'undefined' && token) sessionStorage.setItem('cliamp-token', token);
if (typeof history !== 'undefined' && location.search) history.replaceState(null, '', location.pathname);

// ---- pure ----

function initial() {
  return { state: null, at: 0, queue: { tracks: [], play_next: [], playlist_revision: 0, count: 0 }, connected: false };
}

// reduce applies one event to the page state and returns the new state.
// Events: {type:'state', data, now} full snapshot; {type:'tick', data}
// partial (position pulses); {type:'queue', data}; {type:'conn', up}.
function reduce(s, ev) {
  if (ev.type === 'state') return Object.assign({}, s, { state: ev.data, at: ev.now });
  if (ev.type === 'tick') return Object.assign({}, s, { state: Object.assign({}, s.state, ev.data), at: ev.now });
  if (ev.type === 'queue') return Object.assign({}, s, { queue: ev.data });
  if (ev.type === 'conn') return Object.assign({}, s, { connected: ev.up });
  return s;
}

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

const TABS = ['now', 'queue', 'browse', 'search'];
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

// makeApi wraps fetch: token header, JSON in and out, and one error type
// with the status, the server's detail, and stale (409 revision clash).
function makeApi(fetchFn) {
  async function call(path, init) {
    const r = await fetchFn(path, Object.assign({ headers: { 'X-Token': token } }, init));
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
  };
}

if (typeof module !== 'undefined') {
  module.exports = { initial, reduce, position, backoff, WATCHDOG_MS, makeWatchdog, splitPath, TABS, parseHash, fmtTime, lyricIndex, moveTarget, plural, makeApi };
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
  const iconBtn = (name, label, fn) => {
    const b = h('button', { class: 'more', 'aria-label': label, onclick: fn });
    b.append(svg(name));
    return b;
  };

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
      if ((prev.state && prev.state.index) !== (S.state && S.state.index)) markCurrent();
    } else if (ev.type === 'queue') {
      renderQueue();
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
    const mine = es = new EventSource('/api/events?token=' + encodeURIComponent(token));
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
    mine.addEventListener('resync', () => { up(); loadState(); loadQueue(); });
    mine.addEventListener('player', (e) => {
      up();
      const d = JSON.parse(e.data);
      const was = S.state && S.state.player;
      dispatch({ type: 'tick', data: { player: d.status }, now: now() });
      if (was && was !== d.status) toast(d.status === 'online' ? 'player online' : 'player offline', d.status !== 'online');
    });
    mine.addEventListener('ping', () => up());
    mine.onerror = () => { if (es === mine) down(); };
  }
  setInterval(() => { if (es && dog.stale()) down(); }, 5000);
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'visible' && dog.stale()) { tries = 0; down(); clearTimeout(retryTimer); connect(); }
  });

  // ---- Now Playing ----
  let dragging = null, volTimer = 0, volHold = 0;
  function renderNow() {
    const st = S.state, song = st && (st.track || st.logical_track);
    $('idle').hidden = !!song;
    $('player').hidden = !song;
    document.title = song ? (song.title || '') + ' · cliamp' : 'cliamp';
    if (!st) return;
    if (song) {
      $('title').textContent = song.title || '';
      $('artist').textContent = song.artist || '';
      $('album').textContent = song.album || '';
      $('dur').textContent = fmtTime(st.duration);
      $('bar').setAttribute('aria-valuemax', Math.round(Number(st.duration) || 0));
      $('bar').classList.toggle('unseekable', !st.seekable);
    }
    setIcon($('b-toggle'), st.state === 'playing' ? 'pause' : 'play');
    $('b-shuffle').classList.toggle('on', !!st.shuffle);
    const rep = String(st.repeat || 'Off');
    $('b-repeat').classList.toggle('on', rep !== 'Off');
    $('rep1').hidden = rep !== 'One';
    $('b-lyrics').classList.toggle('on', !$('lyrics').hidden);
    if (!volHold) setVolume(Number(st.volume == null ? $('volume').value : st.volume));
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
  function closeMenus(except) {
    document.querySelectorAll('li.open').forEach((l) => { if (l !== except) l.classList.remove('open'); });
  }
  // item is one list entry: a row (a link, or tappable with onTap), plus a ⋯
  // button opening its menu of {label, fn}.
  function item(o) {
    const li = h('li', { class: o.cur ? 'cur' : null });
    const inner = [h('div', { class: 'txt' }, h('div', { class: 't1' }, o.title || ''), o.sub ? h('div', { class: 't2' }, o.sub) : null)];
    if (o.end) inner.push(h('div', { class: 'end' }, o.end));
    const toggle = () => { closeMenus(li); li.classList.toggle('open'); };
    const row = o.href ? h('a', { class: 'row', href: o.href }, inner)
      : h('div', { class: 'row', role: 'button', tabindex: 0, onclick: o.onTap || toggle,
        onkeydown: (e) => { if (e.key === 'Enter') (o.onTap || toggle)(); } }, inner);
    const top = h('div', { class: 'toprow' }, row);
    if (o.menu) {
      top.append(iconBtn('more', 'More', toggle));
      li.append(top, h('div', { class: 'menu' }, o.menu.map((m) =>
        h('button', { class: 'btn' + (m.danger ? ' danger' : ''), onclick: () => { li.classList.remove('open'); m.fn(); } }, m.label))));
    } else li.append(top);
    return li;
  }
  const head = (title, back, extra) => h('div', { class: 'head' },
    back ? h('a', { class: 'back', href: back, 'aria-label': 'Back' }, svg('back')) : null, h('h2', null, title), extra);
  const msg = (cls, text) => h('div', { class: cls }, text);
  const enc = encodeURIComponent;

  // track actions: cliamp plays a track by path; queue ops guard on revision.
  const playTrack = (t) => cmd('track.play', { track: t.path }, true);
  const nextTrack = (t) => cmd('track.queue', { track: t.path }, true);
  const trackMenu = (t) => [{ label: 'Play', fn: () => playTrack(t) }, { label: 'Play next', fn: () => nextTrack(t) }];
  // replacing the queue is audible and destructive — confirm first (spec v3).
  const replaceMenu = (kind, provider, id, name) => [{
    label: '▶ play ' + kind, fn: () => {
      if (!window.confirm('replace the queue with ' + kind + ' "' + name + '"?')) return;
      cmd(kind === 'album' ? 'provider.load_album' : 'provider.load', kind === 'album' ? { provider, album: id } : { provider, playlist: id }, true);
      rev();
    },
  }];

  // ---- Queue ----
  let confirming = false;
  function renderQueue() {
    const q = S.queue, cur = S.state ? Number(S.state.index ?? -1) : -1;
    const sections = [];
    if (q.play_next && q.play_next.length) {
      sections.push(h('div', { class: 'sub' }, 'Play next'), h('ul', { class: 'list' },
        q.play_next.map((t, i) => item({
          title: t.title || t.path, sub: t.artist || '', end: fmtTime(t.duration),
          menu: [
            { label: 'Remove', danger: true, fn: () => { cmd('playnext.remove', { index: i }, true); rev(); } },
            { label: 'Move up', fn: () => moveCmd('playnext.move', i, -1) },
            { label: 'Move down', fn: () => moveCmd('playnext.move', i, 1) },
          ],
        })))
      );
    }
    const clear = confirming
      ? h('span', { class: 'confirm' }, 'Clear the queue?',
        h('button', { class: 'btn danger', onclick: () => { confirming = false; cmd('queue.clear', {}, true); rev(); } }, 'Clear'),
        h('button', { class: 'btn', onclick: () => { confirming = false; renderQueue(); } }, 'Cancel'))
      : null;
    sections.push(h('div', { class: 'sub' }, 'Queue'), h('ul', { class: 'list' },
      (q.tracks || []).map((t, i) => {
        const li = item({
          title: t.title || t.path, sub: t.artist || '', end: fmtTime(t.duration), cur: i === cur,
          onTap: () => { if (i !== cur) cmd('queue.play', { index: i }, true); },
          menu: [
            { label: 'Remove', danger: true, fn: () => { cmd('queue.remove', { index: i }, true); rev(); } },
            { label: 'Move up', fn: () => moveCmd('queue.move', i, -1) },
            { label: 'Move down', fn: () => moveCmd('queue.move', i, 1) },
          ],
        });
        li.dataset.i = i;
        return li;
      })));
    $('queue').replaceChildren(
      h('div', { class: 'head' }, h('h2', null, 'Queue'), clear),
      ...(q.tracks && q.tracks.length ? sections : [msg('empty', 'The queue is empty')]));
  }
  function moveCmd(op, i, dir) {
    const n = op === 'queue.move' ? (S.queue.tracks || []).length : (S.queue.play_next || []).length;
    const t = moveTarget(i, n, dir);
    if (t) { cmd(op, { index: t.from, to: t.to }, true); rev(); }
  }
  function markCurrent() {
    document.querySelectorAll('#queue li.cur').forEach((l) => l.classList.remove('cur'));
    const li = document.querySelector('#queue li[data-i="' + (S.state ? S.state.index : -1) + '"]');
    if (li) li.classList.add('cur');
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
      show(head('Browse'), h('div', { class: 'tiles' }, ps.map((pr) =>
        h('a', { class: 'tile', href: '#browse/' + enc(pr.key) + '/playlists' }, pr.name,
          h('span', null, kindsOf(pr).map((k) => k[1]).join(' · '))))));
      return;
    }
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
    if (kind === 'playlists') {
      return item({ title: name, sub: row.artist || '', end: row.track_count != null ? plural(row.track_count, 'track') : '',
        href: drillBase + 'playlist/' + enc(row.id || ''), menu: replaceMenu('playlist', prov, row.id, name) });
    }
    if (kind === 'albums') {
      return item({ title: name, sub: [row.artist, row.year].filter(Boolean).join(' · '),
        href: drillBase + 'album/' + enc(row.id || ''), menu: replaceMenu('album', prov, row.id, name) });
    }
    if (kind === 'artists') {
      return item({ title: name, sub: row.album_count != null ? plural(row.album_count, 'album') : row.artist || '',
        href: drillBase + 'artist/' + enc(row.id || '') });
    }
    // radio catalog: stations have no path; tap plays via provider.load
    return item({ title: name, sub: row.genre || '', onTap: () => {
      if (!window.confirm('play station "' + name + '"? this replaces the queue')) return;
      cmd('provider.load', { provider: prov, playlist: row.id }, true); rev();
    } });
  }
  async function renderDrill(my, show, hdr, prov, what, id, backKind) {
    const url = (off) => '/api/library/' + enc(prov) + '/' + what + '/' + enc(id) + '/tracks?offset=' + off + '&limit=50';
    let off = 0, shown = 0, title = what === 'album' ? 'Album' : what === 'artist' ? 'Artist' : 'Playlist';
    const ul = h('ul', { class: 'list' }), more = h('button', { class: 'btn', hidden: true }, 'More');
    const back = '#browse/' + enc(prov) + '/' + (backKind || 'playlists');
    const load = async () => {
      more.disabled = true;
      let r;
      try { r = await api.get(url(off)); } catch (e) { show(head(title, back)); ul.append(msg('error', e.message)); return; }
      if (my !== nav) return;
      const rows = r.tracks || [];
      shown += rows.length; off += rows.length;
      ul.append(...rows.map((t) => item({ title: t.title || rowTitle(t), sub: [t.artist, t.album].filter(Boolean).join(' · '),
        end: fmtTime(t.duration_secs), onTap: () => playTrack(t), menu: trackMenu(t) })));
      more.hidden = !(r.total != null && shown < r.total);
      more.disabled = false;
      if (!shown) ul.append(msg('empty', 'No tracks'));
    };
    more.onclick = load;
    const extra = what === 'artist' ? null : h('button', { class: 'btn primary', onclick: () => {
      if (!window.confirm('replace the queue with ' + what + ' "' + title + '"?')) return;
      cmd(what === 'album' ? 'provider.load_album' : 'provider.load',
        what === 'album' ? { provider: prov, album: id } : { provider: prov, playlist: id }, true);
      rev();
    } }, '▶ play ' + what);
    show(head(title, back, extra), ul, h('div', { class: 'bar' }, more));
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
      out.replaceChildren(rows.length ? h('ul', { class: 'list' }, rows.map((t) => item({
        title: t.title || rowTitle(t), sub: [t.artist, t.album].filter(Boolean).join(' · '),
        end: fmtTime(t.duration_secs), onTap: () => playTrack(t), menu: trackMenu(t) }))) : msg('empty', 'Nothing found'));
    } catch (e) {
      if (my === searchSeq) out.replaceChildren(msg('error', e.message));
    }
  }

  // ---- routing ----
  function route() {
    const r = parseHash(location.hash);
    document.body.dataset.tab = r.tab;
    closeMenus();
    if (r.tab === 'browse') renderBrowse(r.rest);
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
  wireSearch();
  renderQueue();
  route();
  loadState();
  loadQueue();
  connect();
})();

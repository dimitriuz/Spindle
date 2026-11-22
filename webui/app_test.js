'use strict';
// Node tests for the pure half of app.js: run with `node webui/app_test.js`.
const assert = require('node:assert');
const A = require('./app.js');

let pass = 0;
const ok = async (name, fn) => { await fn(); pass++; void name; };

(async () => {
  await ok('initial', () => {
    const s = A.initial();
    assert.equal(s.state, null);
    assert.equal(s.connected, false);
  });

  await ok('reduce drops snapshots older than the one held', () => {
    // provider.load_album's reply carries a snapshot taken before playback
    // began; it lands after the SSE "playing" event and must not undo it.
    let s = A.reduce(A.initial(), { type: 'state', data: { revision: 32, state: 'playing', track: {} }, now: 1 });
    s = A.reduce(s, { type: 'tick', data: { revision: 31, state: 'stopped' }, now: 2 });
    assert.equal(s.state.state, 'playing');
    s = A.reduce(s, { type: 'state', data: { revision: 30, state: 'stopped', track: {} }, now: 3 });
    assert.equal(s.state.state, 'playing');
    s = A.reduce(s, { type: 'tick', data: { position: 5 }, now: 4 });
    assert.equal(s.state.position, 5, 'revision-less ticks still merge');
    s = A.reduce(s, { type: 'state', data: { revision: 33, state: 'paused', track: {} }, now: 5 });
    assert.equal(s.state.state, 'paused');
    // a restarted player counts from 1 again: a reconnect, a player event or
    // a resync resets the guard (the SSE stream itself may never drop)
    for (const ev of [{ type: 'conn', up: true }, { type: 'reset' }]) {
      let r = A.reduce(s, { type: 'state', data: { revision: 900, state: 'playing', track: {} }, now: 6 });
      r = A.reduce(r, { type: 'state', data: { revision: 2, state: 'stopped', track: {} }, now: 7 });
      assert.equal(r.state.state, 'playing', 'still guarded before the reset');
      r = A.reduce(r, ev);
      r = A.reduce(r, { type: 'state', data: { revision: 2, state: 'stopped', track: {} }, now: 8 });
      assert.equal(r.state.state, 'stopped', ev.type + ' lets the restarted player through');
    }
  });

  await ok('reduce state/tick/queue/conn', () => {
    let s = A.initial();
    s = A.reduce(s, { type: 'state', data: { track: { title: 'a' }, position: 10, duration: 60, state: 'playing' }, now: 1000 });
    assert.equal(s.state.track.title, 'a');
    assert.equal(s.at, 1000);
    s = A.reduce(s, { type: 'tick', data: { position: 20 }, now: 2000 });
    assert.equal(s.state.position, 20);
    assert.equal(s.state.track.title, 'a', 'tick keeps the rest of the snapshot');
    s = A.reduce(s, { type: 'queue', data: { tracks: [1, 2], playlist_revision: 7 } });
    assert.equal(s.queue.playlist_revision, 7);
    s = A.reduce(s, { type: 'conn', up: false });
    assert.equal(s.connected, false);
  });

  await ok('position interpolates seconds while playing', () => {
    const s = A.reduce(A.initial(), { type: 'state', data: { track: {}, position: 10, duration: 60, state: 'playing' }, now: 0 });
    assert.equal(A.position(s, 2500), 12.5);
    const paused = A.reduce(s, { type: 'state', data: { track: {}, position: 10, duration: 60, state: 'paused' }, now: 0 });
    assert.equal(A.position(paused, 5000), 10, 'paused does not move');
    assert.equal(A.position(s, 500_000), 60, 'clamped to duration');
    assert.equal(A.position(A.initial(), 5000), 0, 'no track is zero');
  });

  await ok('backoff climbs to 30 s', () => {
    assert.equal(A.backoff(0), 1000);
    assert.equal(A.backoff(1), 2000);
    assert.equal(A.backoff(5), 30000);
    assert.equal(A.backoff(50), 30000);
  });

  await ok('watchdog goes stale after WATCHDOG_MS', () => {
    let t = 0;
    const w = A.makeWatchdog(() => t);
    w.heard();
    t = A.WATCHDOG_MS - 1;
    assert.equal(w.stale(), false);
    t = A.WATCHDOG_MS + 1;
    assert.equal(w.stale(), true);
    w.heard();
    assert.equal(w.stale(), false);
  });

  await ok('parseHash routes tabs and browse paths', () => {
    assert.deepEqual(A.parseHash(''), { tab: 'now', rest: '' });
    assert.deepEqual(A.parseHash('#queue'), { tab: 'queue', rest: '' });
    assert.deepEqual(A.parseHash('#browse/navidrome/playlists'), { tab: 'browse', rest: 'navidrome/playlists' });
    assert.deepEqual(A.parseHash('#anything'), { tab: 'now', rest: '' });
    assert.deepEqual(A.splitPath(A.parseHash('#browse/p/playlist/x%201').rest), ['p', 'playlist', 'x 1']);
    assert.equal(A.splitPath('%E0%A4%A'), null);
  });

  await ok('fmtTime handles seconds past an hour', () => {
    assert.equal(A.fmtTime(0), '0:00');
    assert.equal(A.fmtTime(65), '1:05');
    assert.equal(A.fmtTime(3599), '59:59');
    assert.equal(A.fmtTime(3661), '1:01:01');
    assert.equal(A.fmtTime(-3), '0:00');
  });

  await ok('lyricIndex finds the active line', () => {
    const L = [{ start: 5 }, { start: 10 }, { start: 20 }];
    assert.equal(A.lyricIndex([], 9), -1);
    assert.equal(A.lyricIndex(L, 4), -1);
    assert.equal(A.lyricIndex(L, 5), 0);
    assert.equal(A.lyricIndex(L, 15), 1);
    assert.equal(A.lyricIndex(L, 99), 2);
  });

  await ok('absent index/volume mean 0 (cliamp omits zero values)', () => {
    assert.equal(A.curIndex(null), -1);
    assert.equal(A.curIndex({ state: 'stopped' }), -1, 'no track, no row');
    assert.equal(A.curIndex({ track: { title: 'a' } }), 0, 'first track has no index key');
    assert.equal(A.curIndex({ track: {}, index: 4 }), 4);
    assert.equal(A.curVolume({}), 0);
    assert.equal(A.curVolume({ volume: -3.5 }), -3.5);
  });

  await ok('artUrl encodes the track path', () => {
    assert.equal(A.artUrl('disc', '/m/a b?x&y.flac'), '/api/art/disc?v=3&path=%2Fm%2Fa%20b%3Fx%26y.flac');
    assert.equal(A.artUrl('cover', 'https://h/rest/stream?id=1'), '/api/art/cover?v=3&path=https%3A%2F%2Fh%2Frest%2Fstream%3Fid%3D1');
  });

  await ok('spinStep: smooth start, settles at speed, monotonic', () => {
    let m = { w: 0, a: 0 }, prev = 0;
    m = A.spinStep(m, 200, 4, 0.01);
    assert.ok(m.w > 0 && m.w < 1, 'no jolt: ' + m.w);
    prev = m.w;
    for (let i = 0; i < 300; i++) { m = A.spinStep(m, 200, 4, 1 / 60); assert.ok(m.w >= prev - 1e-9, 'never slows while spinning up'); prev = m.w; }
    assert.ok(m.w > 199.9 && m.w < 200.1, 'settles on 200 deg/s: ' + m.w);
    assert.ok(Math.abs(m.a) < 0.05, 'and the acceleration dies away');
  });

  await ok('spinStep is frame-rate independent', () => {
    let a = { w: 0, a: 0 }, b = { w: 0, a: 0 };
    for (let i = 0; i < 60; i++) a = A.spinStep(a, 200, 4, 1 / 60);
    b = A.spinStep(b, 200, 4, 1);
    assert.ok(Math.abs(a.w - b.w) < 1e-6, a.w + ' vs ' + b.w);
    assert.ok(Math.abs(a.a - b.a) < 1e-6);
  });

  await ok('spinStep: stop coasts for seconds, pause brakes faster, never reverses', () => {
    const run = (k, t) => { let m = { w: 200, a: 0 }; for (let i = 0; i < Math.round(t * 60); i++) { m = A.spinStep(m, 0, k, 1 / 60); assert.ok(m.w >= 0); } return m.w; };
    assert.ok(run(2.2, 1) > 60, 'still turning after 1 s of coasting: ' + run(2.2, 1));
    assert.ok(run(2.2, 5) < 1, 'and at rest within 5 s');
    assert.ok(run(5, 1) < run(2.2, 1), 'pause brake is quicker than a coast');
  });

  await ok('spinStep: reversing mid spin-up keeps acceleration continuous', () => {
    let m = { w: 0, a: 0 };
    for (let i = 0; i < 20; i++) m = A.spinStep(m, 200, 4, 1 / 60);
    const before = m.a, after = A.spinStep(m, 0, 5, 1e-9).a;
    assert.ok(Math.abs(before - after) < 1e-3, 'no torque jump at the switch: ' + before + ' -> ' + after);
  });

  await ok('spinPlan maps player state to target speed and settle rate', () => {
    assert.deepEqual(A.spinPlan('playing', false), { target: 200, k: 4 });
    assert.deepEqual(A.spinPlan('paused', false), { target: 0, k: 5 });
    assert.deepEqual(A.spinPlan('stopped', false), { target: 0, k: 2.2 });
    assert.deepEqual(A.spinPlan(undefined, false), { target: 0, k: 2.2 });
    assert.equal(A.spinPlan('playing', true).target, 0, 'reduced motion: never spin');
  });

  await ok('no pure function is shadowed by another declaration in app.js', () => {
    // a closure helper once reused the name nextTrack, so the page ran a player
    // command where it meant to read the queue
    const src = require('node:fs').readFileSync(require('node:path').join(__dirname, 'app.js'), 'utf8');
    for (const name of Object.keys(A)) {
      const decls = src.match(new RegExp('(?:^|[^.\\w])(?:function\\s+' + name + '\\s*\\(|(?:const|let|var)\\s+' + name + '\\b)', 'gm')) || [];
      assert.ok(decls.length <= 1, name + ' is declared ' + decls.length + ' times');
    }
  });

  await ok('nextTrack: play-next first, then the row after the current one', () => {
    const rows = [{ title: 'a' }, { title: 'b' }, { title: 'c' }];
    const st = (o) => Object.assign({ track: {}, index: 1, shuffle: false, repeat: 'Off' }, o);
    assert.equal(A.nextTrack(st(), { tracks: rows, play_next: [] }).title, 'c');
    assert.equal(A.nextTrack(st(), { tracks: rows, play_next: [{ title: 'pn' }] }).title, 'pn', 'play-next wins');
    assert.equal(A.nextTrack(st({ index: undefined }), { tracks: rows, play_next: [] }).title, 'b', 'absent index means 0');
    assert.equal(A.nextTrack(st({ index: 2 }), { tracks: rows, play_next: [] }), null, 'end of queue');
    assert.equal(A.nextTrack(st({ index: 2, repeat: 'All' }), { tracks: rows, play_next: [] }).title, 'a', 'repeat all wraps');
    assert.equal(A.nextTrack(st({ shuffle: true }), { tracks: rows, play_next: [] }), null, 'shuffle hides it');
    assert.equal(A.nextTrack(st({ shuffle: true }), { tracks: rows, play_next: [{ title: 'pn' }] }).title, 'pn', 'play-next is certain even when shuffled');
    assert.equal(A.nextTrack(st({ repeat: 'One' }), { tracks: rows, play_next: [] }), null, 'repeat one');
    assert.equal(A.nextTrack(st(), { tracks: [], play_next: [] }), null);
    assert.equal(A.nextTrack(st({ track: undefined }), { tracks: rows, play_next: [] }), null, 'nothing playing');
    assert.equal(A.nextTrack(null, null), null);
    assert.equal(A.nextTrack(st(), { tracks: rows }).title, 'c', 'play_next may be absent');
  });

  await ok('techLine formats what is known and nothing else', () => {
    assert.equal(A.techLine({ format: 'FLAC', bitrate_kbps: 1161, bit_depth: 24, sample_rate: 44100, channels: 2 }), 'FLAC • 1161 kbps • 24 bit • 44.1 kHz');
    assert.equal(A.techLine({ format: 'FLAC', sample_rate: 96000, bit_depth: 24 }), 'FLAC • 24 bit • 96 kHz');
    assert.equal(A.techLine({ sample_rate: 88200 }), '88.2 kHz');
    assert.equal(A.techLine({ sample_rate: 48000 }), '48 kHz');
    assert.equal(A.techLine({ format: 'MP3', bitrate_kbps: 320 }), 'MP3 • 320 kbps');
    assert.equal(A.techLine({ format: 'FLAC', channels: 1 }), 'FLAC • mono');
    assert.equal(A.techLine({ format: 'FLAC', channels: 6 }), 'FLAC • 6 ch');
    assert.equal(A.techLine({}), '');
    assert.equal(A.techLine(null), '');
    assert.ok(!/undefined|null|NaN/.test(A.techLine({ format: 'X', bitrate_kbps: undefined })));
  });

  await ok('fsLayout: both only when wide and landscape', () => {
    assert.equal(A.fsLayout(1280, 720), 'both');
    assert.equal(A.fsLayout(390, 844), 'single');
    assert.equal(A.fsLayout(800, 900), 'single');
    assert.equal(A.fsLayout(700, 500), 'both');
    assert.equal(A.fsLayout(699, 400), 'single');
    assert.equal(A.fsLayout(1000, 800), 'both', '1.25 exactly counts');
    assert.equal(A.fsLayout(0, 0), 'single');
  });

  await ok('albumKey says when two tracks share a disc', () => {
    assert.equal(A.albumKey({ album: 'Let There Be Cello', year: 2018, artist: 'x' }), A.albumKey({ album: 'Let There Be Cello', year: 2018, artist: 'y' }), 'compilation tracks share the disc');
    assert.notEqual(A.albumKey({ album: 'A', year: 2018 }), A.albumKey({ album: 'B', year: 2018 }));
    assert.notEqual(A.albumKey({ album: 'A', year: 2018 }), A.albumKey({ album: 'A', year: 2020 }), 'same title, other release');
    assert.equal(A.albumKey({ title: 'no album' }), '', 'unknown album never counts as the same');
    assert.equal(A.albumKey(null), '');
  });

  await ok('queueView: one list in play order, played items first', () => {
    const rows = ['a', 'b', 'c', 'd', 'e'].map((title) => ({ title }));
    const view = (st, q) => A.queueView(st, q).map((r) => r.title + ':' + r.status);
    // nothing queued next: plain list order, everything before the current track is played
    assert.deepEqual(view({ track: {}, index: 2 }, { tracks: rows, play_next: [] }), ['a:played', 'b:played', 'c:current', 'd:later', 'e:later']);
    assert.deepEqual(view({ track: {} }, { tracks: rows, play_next: null }), ['a:current', 'b:later', 'c:later', 'd:later', 'e:later'], 'absent index is the first row');
    // d and e are queued next, in the order e, d: they play before the rest, so they are listed right after the current track
    assert.deepEqual(view({ track: {}, index: 0 }, { tracks: rows, play_next: [{ index: 4 }, { index: 3 }] }), ['a:current', 'e:next', 'd:next', 'b:later', 'c:later']);
    // a queued row that sits before the current index has NOT been played (appended rows are played from the end)
    assert.deepEqual(view({ track: {}, index: 4 }, { tracks: rows, play_next: [{ index: 1 }, { index: 2 }] }), ['a:played', 'd:played', 'e:current', 'b:next', 'c:next']);
    // the current track itself may be in the up-next list: it is shown once, as current
    assert.deepEqual(view({ track: {}, index: 1 }, { tracks: rows, play_next: [{ index: 1 }, { index: 2 }, { index: 3 }] }), ['a:played', 'b:current', 'c:next', 'd:next', 'e:later']);
    assert.deepEqual(A.queueView({ track: {} }, { tracks: [], play_next: [] }), []);
    assert.deepEqual(A.queueView(null, null), []);
    assert.deepEqual(view({ track: {}, index: 0 }, { tracks: rows, play_next: [{ index: 99 }] }), ['a:current', 'b:later', 'c:later', 'd:later', 'e:later'], 'a reference past the list is ignored');
  });

  await ok('queueView: live index and position inside the up-next list are kept for the menu', () => {
    const rows = ['a', 'b', 'c', 'd'].map((title) => ({ title }));
    const v = A.queueView({ track: {}, index: 0 }, { tracks: rows, play_next: [{ index: 3 }, { index: 2 }] });
    assert.deepEqual(v.map((r) => [r.title, r.index, r.pn]), [['a', 0, -1], ['d', 3, 0], ['c', 2, 1], ['b', 1, -1]]);
  });

  await ok('rowActions: the menu of every kind of item', () => {
    const ids = (spec) => A.rowActions(spec).map((a) => a.id);
    assert.deepEqual(ids({ kind: 'track', fav: false }), ['play', 'add', 'fav'], 'Add to queue is cliamp up-next, so there is no separate Play next');
    assert.deepEqual(ids({ kind: 'album', fav: false }), ['play', 'add', 'fav']);
    assert.deepEqual(ids({ kind: 'playlist', fav: true }), ['play', 'add', 'fav']);
    assert.deepEqual(ids({ kind: 'artist', fav: false }), ['play', 'add', 'fav'], 'artists have a menu too');
    assert.deepEqual(ids({ kind: 'radio', fav: false }), ['play', 'fav'], 'a station can only be played');
    assert.deepEqual(ids({ kind: 'queue', status: 'later', canUp: true, canDown: true }), ['playfrom', 'next', 'up', 'down', 'remove']);
    assert.deepEqual(ids({ kind: 'queue', status: 'next', canUp: true, canDown: true }), ['playfrom', 'up', 'down', 'remove'], 'already next: no Play next');
    assert.deepEqual(ids({ kind: 'queue', status: 'played' }), ['playfrom', 'next', 'remove'], 'played rows cannot be moved');
    assert.deepEqual(ids({ kind: 'queue', status: 'current' }), ['remove'], 'the playing row cannot be played from here or queued');
    assert.deepEqual(ids({ kind: 'track' }), ['play', 'add'], 'no favorite entry where favorites are not supported');
    assert.deepEqual(A.rowActions({ kind: 'nonsense' }), []);
    const play = A.rowActions({ kind: 'album', fav: false })[0];
    assert.equal(play.label, 'Play', 'one action, one name: no separate Play now / Replace queue');
    assert.ok(!A.rowActions({ kind: 'track', fav: false }).some((a) => /replace/i.test(a.label)));
  });

  await ok('rowActions: favorite label, disabled edges, danger', () => {
    assert.equal(A.rowActions({ kind: 'album', fav: false }).find((a) => a.id === 'fav').label, 'Favorite');
    assert.equal(A.rowActions({ kind: 'album', fav: true }).find((a) => a.id === 'fav').label, 'Remove from favorites');
    const top = A.rowActions({ kind: 'queue', status: 'later', canUp: false, canDown: true });
    assert.equal(top.find((a) => a.id === 'up').disabled, true, 'cannot move past the edge of its group');
    assert.ok(!top.find((a) => a.id === 'down').disabled);
    const bottom = A.rowActions({ kind: 'queue', status: 'later', canUp: true, canDown: false });
    assert.equal(bottom.find((a) => a.id === 'down').disabled, true);
    assert.equal(A.rowActions({ kind: 'queue', status: 'next', canUp: false, canDown: false }).filter((a) => a.disabled).length, 2, 'a lone row cannot move either way');
    assert.equal(top.find((a) => a.id === 'remove').danger, true);
    assert.ok(!A.rowActions({ kind: 'track', fav: false }).some((a) => a.danger));
    assert.ok(A.rowActions({ kind: 'track', fav: false }).every((a) => a.icon && a.label));
  });

  await ok('menuMode: sheet on narrow or touch screens, popover otherwise', () => {
    assert.equal(A.menuMode(599, false), 'sheet');
    assert.equal(A.menuMode(600, false), 'popover');
    assert.equal(A.menuMode(1200, true), 'sheet', 'a coarse pointer always gets the sheet');
    assert.equal(A.menuMode(402, true), 'sheet');
  });

  await ok('menuPlacement keeps the popover on screen', () => {
    const view = { width: 1000, height: 700 }, size = { width: 240, height: 200 };
    const mid = A.menuPlacement({ left: 500, top: 100, width: 44, height: 44 }, size, view);
    assert.equal(mid.flipped, false);
    assert.equal(mid.top, 148, 'just under the button');
    assert.equal(mid.left, 500 + 44 - 240, 'right edges aligned');
    const low = A.menuPlacement({ left: 500, top: 640, width: 44, height: 44 }, size, view);
    assert.equal(low.flipped, true, 'no room below: opens upward');
    assert.equal(low.top, 640 - 200 - 4);
    const right = A.menuPlacement({ left: 990, top: 100, width: 44, height: 44 }, size, view);
    assert.ok(right.left + size.width <= view.width - 8, 'never past the right edge');
    const left = A.menuPlacement({ left: 0, top: 100, width: 44, height: 44 }, size, view);
    assert.ok(left.left >= 8, 'never past the left edge');
    const tiny = A.menuPlacement({ left: 10, top: 300, width: 44, height: 44 }, { width: 240, height: 900 }, view);
    assert.ok(tiny.top >= 8, 'a menu taller than the window still starts on screen');
  });

  await ok('menuKey: roving focus that wraps and skips disabled items', () => {
    assert.equal(A.menuKey(-1, 4, 'ArrowDown'), 0);
    assert.equal(A.menuKey(3, 4, 'ArrowDown'), 0, 'wraps');
    assert.equal(A.menuKey(0, 4, 'ArrowUp'), 3, 'wraps');
    assert.equal(A.menuKey(-1, 4, 'ArrowUp'), 3);
    assert.equal(A.menuKey(2, 4, 'Home'), 0);
    assert.equal(A.menuKey(1, 4, 'End'), 3);
    assert.equal(A.menuKey(1, 4, 'x'), 1, 'other keys leave it');
    assert.equal(A.menuKey(0, 0, 'ArrowDown'), -1, 'empty menu');
    assert.equal(A.menuKey(0, 1, 'ArrowDown'), 0);
    assert.equal(A.menuKey(0, 4, 'ArrowDown', [false, true, true, false]), 3, 'skips disabled');
    assert.equal(A.menuKey(3, 4, 'ArrowUp', [false, true, true, false]), 0);
    assert.equal(A.menuKey(0, 2, 'ArrowDown', [true, true]), 0, 'all disabled: stay');
  });

  await ok('progressText says what the background add is doing', () => {
    assert.equal(A.progressText({ state: 'running', done: 340, total: 12323 }, 'append'), 'Adding 340 / 12,323…');
    assert.equal(A.progressText({ state: 'running', done: 0, total: 0 }, 'append'), 'Adding…');
    assert.equal(A.progressText({ state: 'done', done: 12323, total: 12323 }, 'append'), 'Added 12,323 tracks');
    assert.equal(A.progressText({ state: 'done', done: 1, total: 1 }, 'append'), 'Added 1 track');
    assert.equal(A.progressText({ state: 'done', done: 1, total: 1 }, 'play'), 'Playing');
    assert.equal(A.progressText({ state: 'cancelled', done: 340, total: 12323 }, 'append'), 'Stopped at 340 of 12,323');
    assert.equal(A.progressText({ state: 'failed', done: 3, total: 9, error: 'navidrome: down' }, 'append'), 'Could not add: navidrome: down');
    assert.equal(A.progressText({ state: 'failed', done: 0, total: 0 }, 'play'), 'Could not play: unknown error');
  });

  await ok('favorite keys cover all five kinds', () => {
    assert.equal(A.favKey('album', 'navidrome', 'al1'), 'album:navidrome:al1');
    assert.equal(A.favKey('artist', 'navidrome', 'ar1'), 'artist:navidrome:ar1');
    assert.equal(A.favKey('playlist', 'navidrome', 'p1'), 'playlist:navidrome:p1');
    assert.equal(A.favKey('radio', 'radio', 'f:http://x/y'), 'radio:radio:f:http://x/y');
    assert.equal(A.trackFavKey({ path: 'https://h/rest/stream?id=s1', provider_meta: { 'navidrome.id': 's1' } }), 'track:navidrome:s1');
    assert.equal(A.trackFavKey({ path: '/music/a.flac' }), 'track:local:/music/a.flac');
    assert.equal(A.trackFavKey({ title: 'no identity' }), '');
    assert.equal(A.trackFavKey(null), '');
  });

  await ok('favKeys, favFilter, favCounts over a listing', () => {
    const items = [
      { kind: 'album', provider: 'navidrome', id: 'al1', title: 'A' },
      { kind: 'track', provider: 'navidrome', id: 's1', title: 'T', track: { path: 'p' } },
      { kind: 'radio', provider: 'radio', id: 'f:http://x/y', title: 'R' },
      { kind: 'track', provider: 'local', id: '/m/a.flac', title: 'L' },
    ];
    const keys = A.favKeys(items);
    assert.ok(keys.has('album:navidrome:al1') && keys.has('track:navidrome:s1') && keys.has('track:local:/m/a.flac') && keys.has('radio:radio:f:http://x/y'));
    assert.equal(keys.size, 4);
    assert.equal(A.favFilter(items, 'all').length, 4);
    assert.deepEqual(A.favFilter(items, 'track').map((i) => i.id), ['s1', '/m/a.flac']);
    assert.deepEqual(A.favFilter(items, 'artist'), []);
    assert.deepEqual(A.favCounts(items), { all: 4, track: 2, album: 1, artist: 0, playlist: 0, radio: 1 });
    assert.deepEqual(A.favCounts(null), { all: 0, track: 0, album: 0, artist: 0, playlist: 0, radio: 0 });
    assert.equal(A.favKeys(null).size, 0);
    assert.deepEqual(A.favFilter(null, 'all'), []);
  });

  await ok('isPlayingFavStation matches a favorited station by its stream URL', () => {
    const items = [{ kind: 'radio', provider: 'radio', id: 'f:http://stream.example/aac', title: 'R' }];
    assert.equal(A.isPlayingFavStation(items, { path: 'http://stream.example/aac' }), true);
    assert.equal(A.isPlayingFavStation(items, { path: 'http://other/aac' }), false);
    assert.equal(A.isPlayingFavStation(items, null), false);
    assert.equal(A.isPlayingFavStation(null, { path: 'x' }), false);
    assert.equal(A.isPlayingFavStation([{ kind: 'album', id: 'f:x' }], { path: 'x' }), false, 'only radio items count');
  });

  await ok('artPlan: what has to be loaded and animated on a track change', () => {
    const k = 'A|2011';
    assert.deepEqual(A.artPlan('', k, false, false), { needCover: true, needDisc: true }, 'first track');
    assert.deepEqual(A.artPlan(k, k, true, true), { needCover: false, needDisc: false }, 'same album: nothing changes');
    assert.deepEqual(A.artPlan(k, 'B|2018', true, true), { needCover: true, needDisc: true }, 'another album');
    assert.deepEqual(A.artPlan(k, k, true, false), { needCover: false, needDisc: true }, 'same album, no disc yet: try again');
    assert.deepEqual(A.artPlan(k, k, false, true), { needCover: true, needDisc: false }, 'same album, cover missing: try again');
    assert.deepEqual(A.artPlan('', '', true, true), { needCover: true, needDisc: true }, 'unknown album is never "the same"');
  });

  await ok('trackDir: previous is -1, next and everything else +1', () => {
    assert.equal(A.trackDir(3, 2), -1);
    assert.equal(A.trackDir(2, 3), 1);
    assert.equal(A.trackDir(undefined, 3), 1);
    assert.equal(A.trackDir(3, 3), 1);
    assert.equal(A.trackDir(-1, 0), 1);
  });

  await ok('skipDip slows a turning platter to a third and leaves a resting one alone', () => {
    assert.ok(Math.abs(A.skipDip(200) - 70) < 1e-9);
    assert.equal(A.skipDip(0), 0);
    assert.equal(A.skipDip(3), 3, 'a platter that is stopping is left to coast');
    // and the motor brings it back: from the dip to speed within ~2 s, smoothly
    let m = { w: A.skipDip(200), a: 0 };
    for (let i = 0; i < 120; i++) m = A.spinStep(m, 200, 4, 1 / 60);
    assert.ok(m.w > 199, 'recovers: ' + m.w);
  });

  await ok('moveTarget stops at the ends', () => {
    assert.deepEqual(A.moveTarget(0, 3, -1), null);
    assert.deepEqual(A.moveTarget(1, 3, 1), { from: 1, to: 2 });
    assert.deepEqual(A.moveTarget(2, 3, 1), null);
  });

  await ok('makeApi sends JSON and maps errors', async () => {
    const calls = [];
    const api = A.makeApi(async (u, o) => {
      calls.push([u, o]);
      if (u === '/boom409') return { ok: false, status: 409, json: async () => ({ detail: 'revision clash' }) };
      if (u === '/boom500') return { ok: false, status: 500, json: async () => { throw new Error('nope'); } };
      return { ok: true, json: async () => ({ hello: 1 }) };
    });
    await api.get('/api/state');
    assert.equal(calls[0][1].headers['Content-Type'], 'application/json', 'JSON content type (the bridge refuses anything else on POST)');
    assert.equal(calls[0][1].headers['X-Token'], undefined, 'no token any more');
    assert.deepEqual(await api.get('/x'), { hello: 1 });
    const e409 = await api.get('/boom409').catch((e) => e);
    assert.equal(e409.stale, true);
    assert.equal(e409.message, 'revision clash');
    const e500 = await api.get('/boom500').catch((e) => e);
    assert.equal(e500.stale, false);
    assert.equal(e500.message, 'http 500');
    await api.post('/api/command', { op: 'toggle' });
    assert.equal(JSON.parse(calls.at(-1)[1].body).op, 'toggle');
    await api.put('/api/favorites', { kind: 'album', favorite: true });
    assert.equal(calls.at(-1)[1].method, 'PUT', 'put sends PUT');
    assert.equal(calls.at(-1)[1].headers['Content-Type'], 'application/json', 'with the JSON header the bridge insists on');
    assert.equal(JSON.parse(calls.at(-1)[1].body).favorite, true);
  });

  console.log('all ' + pass + ' groups passed');
})().catch((e) => {
  console.error('FAIL', e);
  process.exit(1);
});

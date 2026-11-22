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
  });

  console.log('all ' + pass + ' groups passed');
})().catch((e) => {
  console.error('FAIL', e);
  process.exit(1);
});

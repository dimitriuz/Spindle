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

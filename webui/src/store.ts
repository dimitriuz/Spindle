import { reactive } from "vue";
import * as api from "./api";

export interface Notice {
  id: number;
  text: string;
}

export const store = reactive({
  online: true,
  snapshot: {} as Record<string, any>,
  queue: {
    tracks: [],
    play_next: [],
    playlist_revision: 0,
    truncated: false,
    count: 0,
  } as api.Queue,
  caps: new Set<string>(),
  view: "nowplaying" as "nowplaying" | "queue" | "browse",
  browse: {
    providers: [] as api.ProviderInfo[],
    provider: "",
    tab: "",
    list: [] as api.BrowseRow[],
    total: -1,
    offset: 0,
    loading: false,
    error: "",
    query: "",
    stack: [] as Drill[],
  },
  notices: [] as Notice[],
});

let noticeSeq = 0;
export function notify(text: string) {
  const n: Notice = { id: ++noticeSeq, text };
  store.notices.push(n);
  setTimeout(() => {
    const i = store.notices.findIndex((x) => x.id === n.id);
    if (i >= 0) store.notices.splice(i, 1);
  }, 4000);
}

export async function refreshState() {
  store.snapshot = await api.getState();
}

export async function refreshQueue() {
  store.queue = await api.getQueue();
}

export async function refreshCaps() {
  try {
    const c = await api.getCaps();
    store.caps = new Set((c.result ?? []).map((o) => o.name));
  } catch {
    /* caps stay unknown; buttons stay enabled */
  }
}

export async function runCommand(
  op: string,
  params: Record<string, unknown> = {},
) {
  try {
    const r = await api.command(op, params);
    if (r.job?.snapshot) Object.assign(store.snapshot, r.job.snapshot);
  } catch (e: unknown) {
    const err = e as { status?: number };
    if (err.status === 409) {
      notify("playlist changed elsewhere");
      await refreshQueue();
    } else {
      notify(`command failed: ${op}`);
    }
  }
}

export function withRevision(
  op: string,
  params: Record<string, unknown> = {},
) {
  return runCommand(op, { ...params, if_revision: store.queue.playlist_revision });
}

// --- SSE resilience (spec §5.4): one live EventSource, backoff reopen on
// error, watchdog reopen when frames go stale (iOS half-open sockets),
// immediate reopen when the tab becomes visible again.
const BACKOFF = [1000, 2000, 5000, 10000, 30000];
let es: EventSource | null = null;
let lastFrameAt = 0;
let backoffIdx = 0;
let reconnectTimer: number | undefined;
let resyncPending = true;

function touch() {
  lastFrameAt = Date.now();
  backoffIdx = 0;
  if (resyncPending) {
    resyncPending = false;
    void refreshState();
    void refreshQueue();
  }
}

function attach(e: EventSource) {
  const on = (name: string, fn: (data: string) => void) =>
    e.addEventListener(name, (ev) => {
      touch();
      fn((ev as MessageEvent).data);
    });
  on("state", (d) => Object.assign(store.snapshot, JSON.parse(d)));
  on("playback", (d) => Object.assign(store.snapshot, JSON.parse(d)));
  on("playlist", () => void refreshQueue());
  on("resync", () => {
    void refreshState();
    void refreshQueue();
    notify("state resynced");
  });
  on("player", (d) => {
    const was = store.online;
    store.online = JSON.parse(d).status === "online";
    if (was !== store.online) notify(store.online ? "player online" : "player offline");
  });
  on("ping", () => {});
  e.onopen = () => touch();
  e.onerror = () => {
    // Review Focus 1: the dying instance is ALWAYS closed, ours or not.
    e.close();
    if (es === e) es = null;
    scheduleReopen();
  };
}

function scheduleReopen() {
  if (reconnectTimer !== undefined || es !== null) return;
  resyncPending = true;
  const d = BACKOFF[Math.min(backoffIdx, BACKOFF.length - 1)];
  backoffIdx++;
  reconnectTimer = window.setTimeout(() => {
    reconnectTimer = undefined;
    open();
  }, d);
}

function forceReopen() {
  if (reconnectTimer !== undefined) {
    window.clearTimeout(reconnectTimer);
    reconnectTimer = undefined;
  }
  if (es) es.close();
  es = null;
  resyncPending = true;
  open();
}

function open() {
  const e = new EventSource(`/api/events?token=${encodeURIComponent(api.token)}`);
  es = e;
  attach(e);
}

export function startEventStream() {
  open();
  window.setInterval(() => {
    if (Date.now() - lastFrameAt > 35000 && reconnectTimer === undefined) forceReopen();
  }, 5000);
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "visible" && Date.now() - lastFrameAt > 5000) forceReopen();
  });
}

export interface Drill {
  kind: "album" | "artist" | "playlist";
  id: string;
  title: string;
}

export function setView(
  v: "nowplaying" | "queue" | "browse",
) {
  store.view = v;
}

const LIST_FIELDS: Record<string, "playlists" | "albums" | "artists" | "tracks"> = {
  playlists: "playlists",
  albums: "albums",
  artists: "artists",
  catalog: "playlists",
  album: "tracks",
  artist: "tracks",
  search: "tracks",
  playlist: "tracks",
};

export function errText(e: unknown): string {
  if (e instanceof api.HttpError) return e.detail || `http ${e.status}`;
  return "provider unreachable";
}

function rows(reply: api.ListReply, field: "playlists" | "albums" | "artists" | "tracks"): api.BrowseRow[] {
  if (field === "tracks") return reply.tracks ?? [];
  if (field === "playlists") return reply.playlists ?? [];
  if (field === "albums") return reply.albums ?? [];
  return reply.artists ?? [];
}

export async function loadProviders() {
  const b = store.browse;
  if (b.providers.length) return;
  try {
    const r = await api.getProviders();
    b.providers = r.providers ?? [];
    if (!b.provider && b.providers.length) pickProvider(b.providers[0].key);
  } catch (e: unknown) {
    b.error = errText(e);
  }
}

export function tabsFor(provider: api.ProviderInfo): string[] {
  const t: string[] = [];
  if (provider.catalog) t.push("catalog");
  if (provider.key === "navidrome") t.push("playlists");
  if (provider.browse_albums) t.push("albums");
  if (provider.browse_artists) t.push("artists");
  if (provider.searchable) t.push("search");
  return t;
}

export function pickProvider(key: string) {
  const p = store.browse.providers.find((x) => x.key === key);
  if (!p) return;
  store.browse.provider = key;
  store.browse.error = "";
  store.browse.stack = [];
  store.browse.query = "";
  pickTab(tabsFor(p)[0] ?? "");
}

export function pickTab(kind: string) {
  const b = store.browse;
  b.tab = kind;
  b.error = "";
  if (kind === "search") {
    b.list = [];
    b.total = -1;
    b.offset = 0;
    return;
  }
  void fetchPage(kind, 0, true);
}

export function openDrill(entry: Drill) {
  store.browse.stack = [...store.browse.stack, entry];
  void fetchPage(entry.kind, 0, true);
}

export function closeDrill() {
  const b = store.browse;
  b.stack = b.stack.slice(0, -1);
  const top = b.stack[b.stack.length - 1];
  void fetchPage(top ? top.kind : b.tab, 0, true);
}

export function retryBrowse() {
  const b = store.browse;
  b.error = "";
  if (!b.providers.length) {
    void loadProviders();
    return;
  }
  const top = b.stack[b.stack.length - 1];
  void fetchPage(top ? top.kind : b.tab, b.offset, b.offset === 0);
}

export function browseMore() {
  const b = store.browse;
  if (b.total >= 0 && b.offset >= b.total) return;
  const top = b.stack[b.stack.length - 1];
  const kind = top ? top.kind : b.tab;
  if (kind) void fetchPage(kind, b.offset, false);
}

// navigation (reset) fetches always proceed and supersede whatever is in
// flight; page fetches coalesce while loading. responses from a superseded
// generation are dropped, so a slow navidrome reply can never paint over
// the radio list the user switched to.
let reqGen = 0;
async function fetchPage(kind: string, offset: number, reset: boolean) {
  const b = store.browse;
  if (!kind || (!reset && b.loading)) return;
  if (kind === "search" && !b.query) return;
  const gen = ++reqGen;
  b.loading = true;
  try {
    const top = b.stack[b.stack.length - 1];
    let reply: api.ListReply;
    const field = LIST_FIELDS[kind];
    if (kind === "album" && top) reply = await api.libraryTracks(b.provider, "album", top.id, offset);
    else if (kind === "playlist" && top) reply = await api.libraryPlaylistTracks(b.provider, top.id, offset);
    else if (kind === "search") reply = await api.searchProvider(b.provider, b.query, offset);
    else if (kind === "playlists" || kind === "albums" || kind === "artists" || kind === "catalog")
      reply = await api.libraryList(b.provider, kind, offset);
    else {
      b.error = "unknown browse tab";
      return;
    }
    if (gen !== reqGen) return;
    const add = rows(reply, field);
    b.list = reset ? [...add] : [...b.list, ...add];
    b.total = typeof reply.total === "number" ? reply.total : b.list.length;
    b.offset = b.list.length;
    b.error = "";
  } catch (e: unknown) {
    if (gen === reqGen) b.error = errText(e);
  } finally {
    if (gen === reqGen) b.loading = false;
  }
}

// search debounce: one fetch 300 ms after the last keystroke, not per key.
let searchTimer: number | undefined;
export function browseSearch(q: string) {
  if (searchTimer !== undefined) window.clearTimeout(searchTimer);
  searchTimer = window.setTimeout(() => {
    const b = store.browse;
    b.query = q;
    if (b.tab === "search") void fetchPage("search", 0, true);
  }, 300);
}

export function playTrack(row: api.BrowseRow) {
  if (!row.path) return;
  withRevision("track.play", { track: row.path });
}

export function queueTrack(row: api.BrowseRow) {
  if (!row.path) return;
  withRevision("track.queue", { track: row.path });
}

// drill header action: replace the queue with the drilled collection.
export function playDrill() {
  const b = store.browse;
  const top = b.stack[b.stack.length - 1];
  if (!top) return;
  if (top.kind === "album") {
    if (!window.confirm(`replace the queue with album "${top.title}"?`)) return;
    runCommand("provider.load_album", { provider: b.provider, album: top.id });
  } else if (top.kind === "playlist") {
    if (!window.confirm(`replace the queue with playlist "${top.title}"?`)) return;
    runCommand("provider.load", { provider: b.provider, playlist: top.id });
  }
}

// radio catalog rows carry no path; stations play via provider.load only
// (spec §2) — whole-row tap behind the same confirm.
export function tapCatalogRow(row: api.BrowseRow) {
  if (!row.id) return;
  if (!window.confirm(`play station "${row.name ?? ""}"? this replaces the queue`)) return;
  runCommand("provider.load", { provider: store.browse.provider, playlist: row.id });
}

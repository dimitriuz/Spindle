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

export function connectEvents(): EventSource {
  const es = new EventSource(
    `/api/events?token=${encodeURIComponent(api.token)}`,
  );
  es.addEventListener("state", (e) =>
    Object.assign(store.snapshot, JSON.parse((e as MessageEvent).data)),
  );
  es.addEventListener("playback", (e) =>
    Object.assign(store.snapshot, JSON.parse((e as MessageEvent).data)),
  );
  es.addEventListener("playlist", () => refreshQueue());
  es.addEventListener("resync", () => {
    refreshState();
    refreshQueue();
    notify("state resynced");
  });
  es.addEventListener("player", (e) => {
    const was = store.online;
    store.online = JSON.parse((e as MessageEvent).data).status === "online";
    if (was !== store.online)
      notify(store.online ? "player online" : "player offline");
  });
  return es;
}

export interface Drill {
  kind: "album" | "artist";
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

export function loadPlaylist(id: string, name: string) {
  if (!id) return;
  if (!window.confirm(`replace the queue with playlist "${name}"?`)) return;
  runCommand("provider.load", { provider: store.browse.provider, playlist: id });
}

// album detail (drill stack top is an album): the "play album" button.
export function playAlbumDrill() {
  const b = store.browse;
  const top = b.stack[b.stack.length - 1];
  if (!top || top.kind !== "album") return;
  if (!window.confirm(`replace the queue with album "${top.title}"?`)) return;
  runCommand("provider.load_album", { provider: b.provider, album: top.id });
}

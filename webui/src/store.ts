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
    list: [] as (api.ListItem | api.FoundTrack)[],
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

export const token =
  sessionStorage.getItem("cliamp-token") ||
  new URLSearchParams(location.search).get("token") ||
  "";
if (token) sessionStorage.setItem("cliamp-token", token);
if (location.search) history.replaceState(null, "", location.pathname);

async function req<T>(path: string, init: RequestInit = {}): Promise<T> {
  const r = await fetch(path, {
    ...init,
    headers: { ...(init.headers || {}), "X-Token": token },
  });
  if (!r.ok) {
    throw Object.assign(new Error("http " + r.status), {
      status: r.status,
      body: await r.json().catch(() => ({})),
    });
  }
  return r.json();
}

export interface Track {
  path: string;
  title?: string;
  artist?: string;
  album?: string;
  duration?: number;
  stream?: boolean;
  queued?: boolean;
  index?: number;
}

export interface Queue {
  tracks: Track[];
  play_next: Track[];
  playlist_revision: number;
  truncated: boolean;
  count: number;
}

export interface JobReply {
  job?: {
    state: string;
    result?: unknown;
    snapshot?: Record<string, unknown>;
  };
  error?: string;
  code?: string;
}

export const getState = () => req<Record<string, any>>("/api/state");
export const getQueue = () => req<Queue>("/api/queue");
export const getCaps = () =>
  req<{ result?: { name: string }[] }>("/api/capabilities");

export function command(op: string, params: Record<string, unknown> = {}) {
  return req<JobReply>("/api/command", {
    method: "POST",
    body: JSON.stringify({ op, params }),
  });
}

export interface ProviderInfo {
  key: string;
  name: string;
  searchable?: boolean;
  catalog?: boolean;
  browse_albums?: boolean;
  browse_artists?: boolean;
}

export interface ListItem {
  id: string;
  name: string;
  provider?: string;
  track_count?: number;
  album_count?: number;
  artist?: string;
  artist_id?: string;
  year?: number;
  genre?: string;
  favoritable?: boolean;
}

export interface FoundTrack {
  title: string;
  artist?: string;
  path: string;
  duration_secs?: number;
  stream?: boolean;
}

export interface ListReply {
  ok: boolean;
  total?: number;
  playlists?: ListItem[];
  albums?: ListItem[];
  artists?: ListItem[];
  tracks?: FoundTrack[];
}

export const getProviders = () =>
  req<ListReply & { providers: ProviderInfo[] }>("/api/library/providers");

export const libraryList = (
  provider: string,
  kind: "playlists" | "albums" | "artists" | "catalog",
  offset = 0,
  limit = 50,
) =>
  req<ListReply>(
    `/api/library/${encodeURIComponent(provider)}/${kind}?offset=${offset}&limit=${limit}`,
  );

export const libraryTracks = (
  provider: string,
  detail: "album" | "artist",
  id: string,
  offset = 0,
  limit = 50,
) =>
  req<ListReply>(
    `/api/library/${encodeURIComponent(provider)}/${detail}/${encodeURIComponent(id)}/tracks?offset=${offset}&limit=${limit}`,
  );

export const searchProvider = (
  provider: string,
  q: string,
  offset = 0,
  limit = 50,
) =>
  req<ListReply>(
    `/api/search/${encodeURIComponent(provider)}?q=${encodeURIComponent(q)}&offset=${offset}&limit=${limit}`,
  );

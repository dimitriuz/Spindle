export const token =
  sessionStorage.getItem("cliamp-token") ||
  new URLSearchParams(location.search).get("token") ||
  "";
if (token) sessionStorage.setItem("cliamp-token", token);
if (location.search) history.replaceState(null, "", location.pathname);

export class HttpError extends Error {
  status: number = 0;
  detail: string = "";
}

async function req<T>(path: string, init: RequestInit = {}): Promise<T> {
  const r = await fetch(path, {
    ...init,
    headers: { ...(init.headers || {}), "X-Token": token },
  });
  if (!r.ok) {
    const raw = await r.text();
    let detail = "";
    try {
      const parsed: unknown = JSON.parse(raw);
      if (typeof parsed === "object" && parsed !== null && "detail" in parsed) {
        detail = String(parsed.detail);
      }
    } catch {
      /* non-JSON error body: detail stays empty */
    }
    const err = new HttpError(`http ${r.status}`);
    err.status = r.status;
    err.detail = detail;
    throw err;
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

// one permissive row shape for every browse result (playlists/albums/artists/
// catalog/search tracks share it; each tab reads its own subset).
export interface BrowseRow {
  id?: string;
  name?: string;
  title?: string;
  artist?: string;
  artist_id?: string;
  provider?: string;
  year?: number;
  genre?: string;
  track_count?: number;
  album_count?: number;
  favoritable?: boolean;
  path?: string;
  duration_secs?: number;
  stream?: boolean;
}

export interface ListReply {
  ok: boolean;
  total?: number;
  playlists?: BrowseRow[];
  albums?: BrowseRow[];
  artists?: BrowseRow[];
  tracks?: BrowseRow[];
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

export const libraryPlaylistTracks = (
  provider: string,
  id: string,
  offset = 0,
  limit = 50,
) =>
  req<ListReply>(
    `/api/library/${encodeURIComponent(provider)}/playlist/${encodeURIComponent(id)}/tracks?offset=${offset}&limit=${limit}`,
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

export interface LyricLine {
  start: number;
  text: string;
}
export interface LyricsReply {
  lyrics?: LyricLine[];
}
export const getLyrics = () => req<LyricsReply>("/api/lyrics");

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

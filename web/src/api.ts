// Thin client for the seedstrem admin API. All mutating requests carry
// the CSRF header; a 401 anywhere redirects to the login page.

export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

// Fired on an unexpected 401 (session expired mid-use). The app shell listens
// and shows a graceful prompt instead of yanking the user to /login silently.
export const SESSION_EXPIRED_EVENT = "seedstrem:session-expired";

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: {
      "X-Requested-With": "XMLHttpRequest",
      ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  if (res.status === 401 && !path.endsWith("/session")) {
    window.dispatchEvent(new CustomEvent(SESSION_EXPIRED_EVENT));
    throw new ApiError(401, "not authenticated");
  }
  if (!res.ok) {
    let message = res.statusText;
    try {
      const data = await res.json();
      if (data?.error) message = data.error;
    } catch {
      /* not JSON */
    }
    throw new ApiError(res.status, message);
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

export interface Mapping {
  remote: string;
  local: string;
}

// A per-indexer override of cleanup.seed_time_hours. Indexer names match
// the Prowlarr indexer, case-insensitively; 0 hours means never remove
// that indexer's torrents on seed time.
export interface IndexerSeedTime {
  indexer: string;
  seed_time_hours: number;
}

export interface Config {
  server: {
    listen: string;
    external_url: string;
    admin_password: string;
  };
  downloader: {
    type: string; // "qbittorrent" | "deluge"
  };
  qbittorrent: {
    url: string;
    username: string;
    password: string;
    category: string;
  };
  deluge: {
    host: string;
    port: number;
    username: string;
    password: string;
    label: string;
  };
  prowlarr: {
    url: string;
    api_key: string;
    movie_categories: number[];
    tv_categories: number[];
    anime_categories: number[];
    indexer_ids: number[];
    search_timeout_seconds: number;
  };
  addon: {
    enable_movies: boolean;
    enable_series: boolean;
    enable_anime: boolean;
  };
  filters: {
    min_seeders: number;
    min_size_mb: number;
    max_size_mb: number;
    max_results: number;
  };
  meta: {
    cinemeta_url: string;
    metadata_timeout_seconds: number;
    tmdb_api_key: string;
  };
  paths: { mappings: Mapping[] };
  storage: {
    database: string;
    delete_files_on_remove: boolean;
    max_disk_usage_percent: number;
    max_download_storage_gb: number;
  };
  stream: { wait_timeout_seconds: number; read_chunk: number };
  cleanup: {
    seed_time_hours: number;
    min_progress_for_cancel_percent: number;
    target_ratio: number;
    delete_policy: string;
    indexer_seed_times: IndexerSeedTime[];
  };
  seeding: { full: boolean };
  rss: {
    enabled: boolean;
    interval_minutes: number;
    max_grabs_per_cycle: number;
    max_concurrent_downloads: number;
    max_active_torrents: number;
    freeleech_only: boolean;
    filters: {
      min_size_mb: number;
      max_size_mb: number;
      categories: number[];
      include_keywords: string[];
      exclude_keywords: string[];
    };
  };
  log: { level: string };
}

export interface ProwlarrIndexer {
  id: number;
  name: string;
}

export interface Status {
  version: string;
  external_url: string;
  manifest_url: string;
  qbittorrent: { connected: boolean; version?: string; error?: string };
  downloader: { type: string; connected: boolean; version?: string; error?: string };
  torrents: Record<string, number>;
  total_uploaded: number;
  // used is seedstrem's own footprint; free is the whole filesystem's
  // headroom, reported by the download client or, when it cannot answer,
  // by a local statfs. They do not sum to a disk total, so there is no
  // percentage to show. free is absent when neither source answered.
  disk?: {
    used: number;
    free?: number;
    free_source?: "client" | "local";
  };
}

export interface TorrentLink {
  path: string;
  bytes: number;
  url: string;
}

export interface Torrent {
  id: string;
  name: string;
  hash: string;
  status: string;
  progress: number;
  speed: number;
  seeders: number;
  size: number;
  uploaded: number;
  ratio: number;
  indexer?: string;
  // HTTP caller that first added the torrent; absent when unknown.
  user_agent?: string;
  // "native" when seedstrem added it, "adopted" when it was picked up
  // from the download client by label.
  origin?: string;
  // Seed time governing this torrent (its indexer's override when it has
  // one, else the global default), in seconds.
  seed_time: number;
  seeding_time: number;
  added_at: number;
  error?: string;
  links: TorrentLink[];
}

// Deletion is one entry of the 48h removal history. Durations are
// seconds, matching Torrent.
export interface Deletion {
  id: string;
  torrent_id: string;
  hash: string;
  name: string;
  indexer: string;
  origin: string;
  deleted_at: number;
  reason: string;
  seeding_time: number;
  seed_limit: number;
  ratio: number;
  ratio_limit: number;
  progress: number;
  progress_limit: number;
  files_deleted: boolean;
}

export const api = {
  login: (password: string) => request<void>("POST", "/api/session", { password }),
  logout: () => request<void>("DELETE", "/api/session"),
  sessionInfo: () => request<{ authenticated: boolean }>("GET", "/api/session"),
  getConfig: () => request<Config>("GET", "/api/config"),
  putConfig: (cfg: Config) =>
    request<{ config: Config; restart_required?: boolean }>("PUT", "/api/config", cfg),
  testQbittorrent: (url: string, username: string, password: string, category: string) =>
    request<{ ok: boolean; version?: string; error?: string }>(
      "POST",
      "/api/config/test-qbittorrent",
      {
        url,
        username,
        password,
        category,
      },
    ),
  testDeluge: (host: string, port: number, username: string, password: string) =>
    request<{ ok: boolean; version?: string; error?: string }>(
      "POST",
      "/api/config/test-deluge",
      {
        host,
        port,
        username,
        password,
      },
    ),
  testProwlarr: (url: string, apiKey: string) =>
    request<{ ok: boolean; error?: string }>("POST", "/api/config/test-prowlarr", {
      url,
      api_key: apiKey,
    }),
  listProwlarrIndexers: (url: string, apiKey: string) =>
    request<{ ok: boolean; error?: string; indexers?: ProwlarrIndexer[] }>(
      "POST",
      "/api/config/prowlarr-indexers",
      { url, api_key: apiKey },
    ),
  status: () => request<Status>("GET", "/api/status"),
  torrents: () => request<Torrent[]>("GET", "/api/torrents"),
  forceSyncTorrents: () => request<void>("POST", "/api/torrents/sync"),
  deletions: () => request<Deletion[]>("GET", "/api/deletions"),
  deleteTorrent: (id: string) =>
    request<void>("DELETE", `/api/torrents/${encodeURIComponent(id)}`),
};

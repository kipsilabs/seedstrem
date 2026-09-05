// Pure filtering and ordering for the torrents table. The API returns the
// raw list; the page composes these so the view logic stays testable.
import type { Torrent } from "../api";

export type SortKey = "name" | "status" | "progress" | "speed" | "size" | "ratio" | "added_at";
export type SortDir = "asc" | "desc";

export interface SortSpec {
  key: SortKey;
  dir: SortDir;
}

export const DEFAULT_SORT: SortSpec = { key: "added_at", dir: "desc" };

// Rank used when sorting by status: what needs eyes first, then what is
// moving, then what is settled.
const STATUS_RANK: Record<string, number> = {
  error: 0,
  waiting_files_selection: 1,
  magnet_conversion: 2,
  downloading: 3,
  queued: 4,
  downloaded: 5,
};

export function matchesQuery(t: Torrent, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  return (
    (t.name ?? "").toLowerCase().includes(q) ||
    (t.hash ?? "").toLowerCase().includes(q) ||
    (t.indexer ?? "").toLowerCase().includes(q)
  );
}

export function filterTorrents(
  list: Torrent[],
  query: string,
  status: string | null,
): Torrent[] {
  return list.filter((t) => (status === null || t.status === status) && matchesQuery(t, query));
}

function compare(a: Torrent, b: Torrent, key: SortKey): number {
  switch (key) {
    case "name":
      return (a.name || a.hash).localeCompare(b.name || b.hash, undefined, {
        sensitivity: "base",
      });
    case "status":
      return (STATUS_RANK[a.status] ?? 99) - (STATUS_RANK[b.status] ?? 99);
    default:
      return (a[key] ?? 0) - (b[key] ?? 0);
  }
}

export function sortTorrents(list: Torrent[], spec: SortSpec): Torrent[] {
  const sign = spec.dir === "asc" ? 1 : -1;
  return [...list].sort((a, b) => sign * compare(a, b, spec.key));
}

// A freshly chosen column starts with the direction that puts the
// interesting values on top.
export function initialSort(key: SortKey): SortSpec {
  return { key, dir: key === "name" || key === "status" ? "asc" : "desc" };
}

// Clicking the active column flips direction.
export function nextSort(current: SortSpec, key: SortKey): SortSpec {
  if (current.key === key) {
    return { key, dir: current.dir === "asc" ? "desc" : "asc" };
  }
  return initialSort(key);
}

// Count per status, preserving the caller's key order for stable chips.
export function countByStatus(list: Torrent[]): Record<string, number> {
  return list.reduce<Record<string, number>>((acc, t) => {
    acc[t.status] = (acc[t.status] ?? 0) + 1;
    return acc;
  }, {});
}

export function totalDownloadSpeed(list: Torrent[]): number {
  return list.reduce((sum, t) => (t.status === "downloading" ? sum + t.speed : sum), 0);
}

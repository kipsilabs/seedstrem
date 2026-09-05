import { describe, expect, it } from "vitest";
import type { Torrent } from "../api";
import {
  countByStatus,
  DEFAULT_SORT,
  filterTorrents,
  nextSort,
  sortTorrents,
  totalDownloadSpeed,
} from "./torrentList";

function torrent(overrides: Partial<Torrent>): Torrent {
  return {
    id: "id",
    name: "name",
    hash: "hash",
    status: "downloaded",
    progress: 1,
    speed: 0,
    seeders: 0,
    size: 0,
    uploaded: 0,
    ratio: 0,
    seed_time: 0,
    seeding_time: 0,
    added_at: 0,
    links: [],
    ...overrides,
  };
}

const list: Torrent[] = [
  torrent({ id: "a", name: "Alpha Movie", status: "downloading", speed: 100, size: 30, ratio: 0.5, added_at: 1, indexer: "TrackerOne" }),
  torrent({ id: "b", name: "beta show", status: "downloaded", speed: 0, size: 10, ratio: 2, added_at: 3 }),
  torrent({ id: "c", name: "", hash: "deadbeef", status: "error", speed: 0, size: 20, ratio: 0, added_at: 2 }),
  torrent({ id: "d", name: "Delta", status: "downloading", speed: 50, size: 5, ratio: 1, added_at: 4 }),
];

describe("filterTorrents", () => {
  it("returns everything with no query or status", () => {
    expect(filterTorrents(list, "", null)).toHaveLength(4);
  });
  it("matches name case-insensitively", () => {
    expect(filterTorrents(list, "ALPHA", null).map((t) => t.id)).toEqual(["a"]);
  });
  it("matches the hash when the name is empty", () => {
    expect(filterTorrents(list, "dead", null).map((t) => t.id)).toEqual(["c"]);
  });
  it("matches the indexer", () => {
    expect(filterTorrents(list, "trackerone", null).map((t) => t.id)).toEqual(["a"]);
  });
  it("combines status and query", () => {
    expect(filterTorrents(list, "d", "downloading").map((t) => t.id)).toEqual(["d"]);
  });
});

describe("sortTorrents", () => {
  it("defaults to newest first", () => {
    expect(sortTorrents(list, DEFAULT_SORT).map((t) => t.id)).toEqual(["d", "b", "c", "a"]);
  });
  it("sorts names ignoring case and falls back to hash", () => {
    expect(sortTorrents(list, { key: "name", dir: "asc" }).map((t) => t.id)).toEqual([
      "a",
      "b",
      "c",
      "d",
    ]);
  });
  it("puts errors first when sorting by status ascending", () => {
    expect(sortTorrents(list, { key: "status", dir: "asc" })[0].id).toBe("c");
  });
  it("does not mutate the input", () => {
    const before = list.map((t) => t.id);
    sortTorrents(list, { key: "size", dir: "desc" });
    expect(list.map((t) => t.id)).toEqual(before);
  });
});

describe("nextSort", () => {
  it("flips direction on the active column", () => {
    expect(nextSort({ key: "size", dir: "desc" }, "size")).toEqual({ key: "size", dir: "asc" });
  });
  it("starts text columns ascending and numeric columns descending", () => {
    expect(nextSort(DEFAULT_SORT, "name").dir).toBe("asc");
    expect(nextSort(DEFAULT_SORT, "speed").dir).toBe("desc");
  });
});

describe("aggregates", () => {
  it("counts per status", () => {
    expect(countByStatus(list)).toEqual({ downloading: 2, downloaded: 1, error: 1 });
  });
  it("sums speed of downloading torrents only", () => {
    expect(totalDownloadSpeed(list)).toBe(150);
  });
});

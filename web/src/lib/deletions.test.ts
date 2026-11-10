import { describe, expect, it } from "vitest";
import { deletionEvidence, deletionReasonLabel, matchesDeletionQuery } from "./deletions";
import type { Deletion } from "../api";

function make(over: Partial<Deletion>): Deletion {
  return {
    id: "d1",
    torrent_id: "t1",
    hash: "aabb",
    name: "Example",
    indexer: "",
    origin: "native",
    deleted_at: 1700000000,
    reason: "manual",
    seeding_time: 0,
    seed_limit: 0,
    ratio: 0,
    ratio_limit: 0,
    progress: 0,
    progress_limit: 0,
    files_deleted: false,
    has_torrent_file: false,
    ...over,
  };
}

describe("deletionEvidence", () => {
  it("shows seeded time against the limit that governed it", () => {
    const d = make({ reason: "seed_time", seeding_time: 49 * 3600, seed_limit: 48 * 3600 });
    expect(deletionEvidence(d)).toBe("seeded 49h of 48h");
  });

  it("shows the ratio against the target", () => {
    const d = make({ reason: "ratio", ratio: 2.5, ratio_limit: 2 });
    expect(deletionEvidence(d)).toBe("ratio 2.50 of 2.00");
  });

  it("shows progress against the threshold for abandoned downloads", () => {
    const d = make({ reason: "abandoned", progress: 0.02, progress_limit: 0.05 });
    expect(deletionEvidence(d)).toBe("progress 2% of 5%");
  });

  it("explains that un-adopted torrents remain in the client", () => {
    const d = make({ reason: "unadopted" });
    expect(deletionEvidence(d)).toBe("label disappeared — still in client");
  });

  it("names the admin UI for manual removals", () => {
    expect(deletionEvidence(make({ reason: "manual" }))).toBe("removed from the admin UI");
  });

  it("falls back to an em dash for an unknown reason", () => {
    expect(deletionEvidence(make({ reason: "who_knows" }))).toBe("—");
  });
});

describe("deletionReasonLabel", () => {
  it("gives human labels for known reasons", () => {
    expect(deletionReasonLabel("seed_time")).toBe("Seed time");
    expect(deletionReasonLabel("unadopted")).toBe("Un-adopted");
  });

  it("passes an unknown reason through unchanged", () => {
    expect(deletionReasonLabel("who_knows")).toBe("who_knows");
  });
});

describe("matchesDeletionQuery", () => {
  const d = make({ name: "Some.Show.S01E01", hash: "AABBCC", indexer: "MyIndexer" });

  it("matches everything for a blank query", () => {
    expect(matchesDeletionQuery(d, "  ")).toBe(true);
  });
  it("matches name, hash and indexer case-insensitively", () => {
    expect(matchesDeletionQuery(d, "some.show")).toBe(true);
    expect(matchesDeletionQuery(d, "aabb")).toBe(true);
    expect(matchesDeletionQuery(d, "myindexer")).toBe(true);
  });
  it("rejects non-matching queries", () => {
    expect(matchesDeletionQuery(d, "other")).toBe(false);
  });
});

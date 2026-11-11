import { describe, expect, it } from "vitest";
import { addTorrentFiles, canSubmitAdd, isTorrentFile } from "./addTorrent";

describe("isTorrentFile", () => {
  it("checks the extension case-insensitively", () => {
    expect(isTorrentFile({ name: "a.TORRENT" })).toBe(true);
    expect(isTorrentFile({ name: "a.txt" })).toBe(false);
  });
});

describe("canSubmitAdd", () => {
  it("requires at least one file and rejects mixed invalid selections", () => {
    expect(canSubmitAdd([])).toBe(false);
    expect(canSubmitAdd([{ name: "a.torrent" }])).toBe(true);
    expect(canSubmitAdd([{ name: "a.torrent" }, { name: "b.TORRENT" }])).toBe(true);
    expect(canSubmitAdd([{ name: "a.torrent" }, { name: "b.txt" }])).toBe(false);
  });
});

describe("addTorrentFiles", () => {
  const files = ["a.torrent", "b.torrent", "c.torrent"].map((name) => new File([name], name));

  it("uploads every file and reports the successful batch", async () => {
    const uploaded: string[] = [];
    const result = await addTorrentFiles(files, async (file) => {
      uploaded.push(file.name);
    });
    expect(uploaded).toEqual(["a.torrent", "b.torrent", "c.torrent"]);
    expect(result).toEqual({ added: 3, failures: [] });
  });

  it("continues after a failed upload and retains only failed files for retry", async () => {
    const uploaded: string[] = [];
    const result = await addTorrentFiles(files, async (file) => {
      if (file.name === "b.torrent") throw new Error("Upload rejected");
      uploaded.push(file.name);
    });
    expect(uploaded).toEqual(["a.torrent", "c.torrent"]);
    expect(result.added).toBe(2);
    expect(result.failures).toEqual([{ file: files[1], error: "Upload rejected" }]);

    const retry = await addTorrentFiles(result.failures.map(({ file }) => file), async (file) => {
      uploaded.push(file.name);
    });
    expect(retry).toEqual({ added: 1, failures: [] });
    expect(uploaded).toEqual(["a.torrent", "c.torrent", "b.torrent"]);
  });

  it("reports a batch where all uploads fail", async () => {
    const result = await addTorrentFiles(files, async () => { throw new Error("Offline"); });
    expect(result.added).toBe(0);
    expect(result.failures.map(({ file, error }) => [file.name, error])).toEqual([
      ["a.torrent", "Offline"], ["b.torrent", "Offline"], ["c.torrent", "Offline"],
    ]);
  });
});

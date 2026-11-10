import { describe, expect, it } from "vitest";
import { canSubmitAdd, isMagnet, isTorrentFile } from "./addTorrent";

const HASH = "0123456789abcdef0123456789abcdef01234567";

describe("isMagnet", () => {
  it("accepts a btih magnet, trimming whitespace", () => {
    expect(isMagnet(`  magnet:?xt=urn:btih:${HASH}&dn=Show `)).toBe(true);
  });
  it("rejects other input", () => {
    expect(isMagnet("")).toBe(false);
    expect(isMagnet("http://example.com/a.torrent")).toBe(false);
    expect(isMagnet("magnet:?dn=NoHash")).toBe(false);
  });
});

describe("isTorrentFile", () => {
  it("checks the extension case-insensitively", () => {
    expect(isTorrentFile({ name: "a.TORRENT" })).toBe(true);
    expect(isTorrentFile({ name: "a.txt" })).toBe(false);
  });
});

describe("canSubmitAdd", () => {
  it("needs a magnet or a .torrent", () => {
    expect(canSubmitAdd("", null)).toBe(false);
    expect(canSubmitAdd(`magnet:?xt=urn:btih:${HASH}`, null)).toBe(true);
    expect(canSubmitAdd("", { name: "a.torrent" })).toBe(true);
  });
  it("lets a valid file win over a bad magnet but not a bad file", () => {
    expect(canSubmitAdd("junk", { name: "a.torrent" })).toBe(true);
    expect(canSubmitAdd(`magnet:?xt=urn:btih:${HASH}`, { name: "a.txt" })).toBe(false);
  });
});

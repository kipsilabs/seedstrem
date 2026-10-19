import { describe, expect, it } from "vitest";
import { deleteTorrents, toggleVisibleSelection } from "./torrentSelection";

describe("toggleVisibleSelection", () => {
  it("selects the filtered list without losing hidden selections", () => {
    expect([...toggleVisibleSelection(new Set(["hidden", "a"]), ["a", "b"])]).toEqual(["hidden", "a", "b"]);
  });
  it("deselects the filtered list while retaining hidden selections", () => {
    expect([...toggleVisibleSelection(new Set(["hidden", "a", "b"]), ["a", "b"])]).toEqual(["hidden"]);
  });
});

describe("deleteTorrents", () => {
  it("removes every requested torrent", async () => {
    const remaining = new Set(["a", "b", "other"]);
    const result = await deleteTorrents(["a", "b"], async (id) => { remaining.delete(id); });
    expect([...remaining]).toEqual(["other"]);
    expect(result).toEqual({ deleted: ["a", "b"], failed: [] });
  });
  it("continues after a failure and returns failed IDs for retry", async () => {
    const remaining = new Set(["a", "b", "c"]);
    const result = await deleteTorrents(["a", "b", "c"], async (id) => {
      if (id === "b") throw new Error("Client unavailable");
      remaining.delete(id);
    });
    expect([...remaining]).toEqual(["b"]);
    expect(result).toEqual({ deleted: ["a", "c"], failed: [{ id: "b", message: "Client unavailable" }] });
  });
});

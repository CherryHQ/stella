import { beforeEach, describe, expect, it, vi } from "vitest";
import { loadDraft, saveDraft } from "./draft-store";

class MemoryStorage implements Storage {
  private map = new Map<string, string>();
  get length() {
    return this.map.size;
  }
  key(i: number) {
    return [...this.map.keys()][i] ?? null;
  }
  getItem(k: string) {
    return this.map.get(k) ?? null;
  }
  setItem(k: string, v: string) {
    this.map.set(k, v);
  }
  removeItem(k: string) {
    this.map.delete(k);
  }
  clear() {
    this.map.clear();
  }
}

let store: MemoryStorage;

beforeEach(() => {
  store = new MemoryStorage();
  Object.defineProperty(globalThis, "sessionStorage", { configurable: true, value: store });
});

describe("draft-store", () => {
  it("round-trips text, chips and the last sent message", () => {
    saveDraft("s1", {
      text: "hello",
      chips: [{ key: "compact", label: "/compact" }],
      lastSent: "previous",
    });
    expect(loadDraft("s1")).toEqual({
      text: "hello",
      chips: [{ key: "compact", label: "/compact" }],
      lastSent: "previous",
    });
  });

  it("keeps the entry alive for lastSent alone, and drops a fully empty draft", () => {
    saveDraft("s1", { text: "", chips: [], lastSent: "previous" });
    expect(loadDraft("s1").lastSent).toBe("previous");

    saveDraft("s1", { text: "", chips: [] });
    expect(store.getItem("stella-draft:s1")).toBeNull();
  });

  it("reads drafts written in the pre-JSON bare-string format", () => {
    store.setItem("stella-draft:legacy", "half typed");
    expect(loadDraft("legacy")).toEqual({ text: "half typed", chips: [] });
  });

  it("returns an empty draft for a missing key or no key at all", () => {
    expect(loadDraft("nope")).toEqual({ text: "", chips: [] });
    expect(loadDraft(null)).toEqual({ text: "", chips: [] });
  });

  it("evicts the least recently updated drafts past the cap", () => {
    let now = 1_000;
    vi.spyOn(Date, "now").mockImplementation(() => (now += 1_000));
    for (let i = 0; i < 25; i++) saveDraft(`s${i}`, { text: `draft ${i}`, chips: [] });

    const remaining = [];
    for (let i = 0; i < store.length; i++) remaining.push(store.key(i));
    expect(remaining).toHaveLength(20);
    expect(loadDraft("s0").text).toBe("");
    expect(loadDraft("s24").text).toBe("draft 24");
    vi.restoreAllMocks();
  });

  it("survives a storage that throws on write", () => {
    vi.spyOn(store, "setItem").mockImplementation(() => {
      throw new Error("quota");
    });
    expect(() => saveDraft("s1", { text: "x", chips: [] })).not.toThrow();
    vi.restoreAllMocks();
  });
});

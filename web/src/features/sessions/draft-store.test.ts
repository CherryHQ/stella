import { beforeEach, describe, expect, it, vi } from "vitest";
import { loadDraft, patchDraft } from "./draft-store";

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
    patchDraft("s1", {
      text: "hello",
      chips: [{ key: "compact", label: "/compact" }],
      attachments: [{ name: "a.png", path: "/user/a.png" }],
      lastSent: "previous",
    });
    expect(loadDraft("s1")).toEqual({
      text: "hello",
      chips: [{ key: "compact", label: "/compact" }],
      attachments: [{ name: "a.png", path: "/user/a.png" }],
      lastSent: "previous",
    });
  });

  it("keeps the entry alive for lastSent alone, and drops a fully empty draft", () => {
    patchDraft("s1", { lastSent: "previous" });
    expect(loadDraft("s1").lastSent).toBe("previous");

    patchDraft("s1", { lastSent: undefined });
    expect(store.getItem("stella-draft:s1")).toBeNull();
  });

  it("merges each owner's fields instead of overwriting the record", () => {
    patchDraft("s1", { text: "typed" });
    patchDraft("s1", { attachments: [{ name: "a.png", path: "/user/a.png" }] });
    expect(loadDraft("s1")).toMatchObject({
      text: "typed",
      attachments: [{ name: "a.png", path: "/user/a.png" }],
    });
  });

  it("reads drafts written in the pre-JSON bare-string format", () => {
    store.setItem("stella-draft:legacy", "half typed");
    expect(loadDraft("legacy")).toEqual({ text: "half typed", chips: [], attachments: [] });
  });

  it("returns an empty draft for a missing key or no key at all", () => {
    expect(loadDraft("nope")).toEqual({ text: "", chips: [], attachments: [] });
    expect(loadDraft(null)).toEqual({ text: "", chips: [], attachments: [] });
  });

  it("evicts the least recently updated drafts past the cap", () => {
    let now = 1_000;
    vi.spyOn(Date, "now").mockImplementation(() => (now += 1_000));
    for (let i = 0; i < 25; i++) patchDraft(`s${i}`, { text: `draft ${i}` });

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
    expect(() => patchDraft("s1", { text: "x" })).not.toThrow();
    vi.restoreAllMocks();
  });
});

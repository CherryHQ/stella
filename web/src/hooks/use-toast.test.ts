import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { getToasts, showToast, subscribeToasts } from "./use-toast";

/**
 * The bug this store replaced was invisible in every component that raised a
 * toast: `useState` in each caller meant a detail panel published into its own
 * array while the list page rendered a different one, and the toast simply
 * never appeared. Nothing threw, so the only way to catch a regression is to
 * assert that two unrelated call sites share one queue.
 */
describe("toast store", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.runAllTimers(); // drain, so the queue is empty for the next test
    vi.useRealTimers();
  });

  it("publishes to every subscriber, not to the caller's own copy", () => {
    const listPage = vi.fn();
    const detailPanel = vi.fn();
    const unsubList = subscribeToasts(listPage);
    const unsubDetail = subscribeToasts(detailPanel);

    showToast("saved");

    expect(listPage).toHaveBeenCalledTimes(1);
    expect(detailPanel).toHaveBeenCalledTimes(1);
    expect(getToasts()).toEqual([{ id: expect.any(Number), text: "saved", kind: "success" }]);

    unsubList();
    unsubDetail();
  });

  it("stops notifying after unsubscribe", () => {
    const listener = vi.fn();
    subscribeToasts(listener)();
    showToast("gone");
    expect(listener).not.toHaveBeenCalled();
  });

  it("returns a new array per change so useSyncExternalStore re-renders", () => {
    // Mutating in place would keep the snapshot reference identical and React
    // would skip the render — the toast would be in the store and off the screen.
    const before = getToasts();
    showToast("first");
    expect(getToasts()).not.toBe(before);
  });

  it("keeps the snapshot stable between changes so it does not loop", () => {
    showToast("stable");
    expect(getToasts()).toBe(getToasts());
  });

  it("dismisses on its own timeout, keeping other toasts queued", () => {
    showToast("short", "success", 1000);
    showToast("long", "error", 5000);
    expect(getToasts()).toHaveLength(2);

    vi.advanceTimersByTime(1000);
    expect(getToasts().map((t) => t.text)).toEqual(["long"]);

    vi.advanceTimersByTime(4000);
    expect(getToasts()).toEqual([]);
  });

  it("gives each toast its own identity so a repeat does not dismiss the first", () => {
    showToast("same text");
    showToast("same text");
    const [a, b] = getToasts();
    expect(a.id).not.toBe(b.id);
  });
});

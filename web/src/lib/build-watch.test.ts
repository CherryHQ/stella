import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  buildSnapshot,
  observeBuild,
  resetBuildWatchForTest,
  subscribeToBuild,
} from "./build-watch";

describe("build-watch", () => {
  beforeEach(() => {
    resetBuildWatchForTest();
  });

  it("treats the first stamped response as this tab's build", () => {
    observeBuild("1.0.0@aaa");
    expect(buildSnapshot()).toEqual({ stale: false, build: "1.0.0@aaa" });
  });

  it("goes stale once the server reports a different build", () => {
    observeBuild("1.0.0@aaa");
    observeBuild("1.1.0@bbb");
    expect(buildSnapshot().stale).toBe(true);
  });

  it("clears once the server rolls back to this tab's build", () => {
    observeBuild("1.0.0@aaa");
    observeBuild("1.1.0@bbb");
    observeBuild("1.0.0@aaa");
    expect(buildSnapshot().stale).toBe(false);
  });

  it("ignores responses with no build header", () => {
    observeBuild("1.0.0@aaa");
    observeBuild(null);
    expect(buildSnapshot()).toEqual({ stale: false, build: "1.0.0@aaa" });
  });

  it("keeps the snapshot identity stable so useSyncExternalStore settles", () => {
    observeBuild("1.0.0@aaa");
    const first = buildSnapshot();
    observeBuild("1.0.0@aaa");
    expect(buildSnapshot()).toBe(first);
  });

  it("notifies subscribers only when the answer changes", () => {
    const listener = vi.fn();
    subscribeToBuild(listener);

    observeBuild("1.0.0@aaa");
    observeBuild("1.0.0@aaa");
    expect(listener).toHaveBeenCalledTimes(1);

    observeBuild("1.1.0@bbb");
    expect(listener).toHaveBeenCalledTimes(2);
  });

  it("stops notifying after unsubscribe", () => {
    const listener = vi.fn();
    subscribeToBuild(listener)();

    observeBuild("1.0.0@aaa");
    expect(listener).not.toHaveBeenCalled();
  });
});

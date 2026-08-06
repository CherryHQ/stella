import { describe, expect, it } from "vitest";

import { sessionOriginLabel } from "./session-origin";

/** Echo the key so a test failure names the key that was missing or wrong. */
const t = (key: string) => key;
const session = (kind: string, channel: string) =>
  ({ kind, channel }) as Parameters<typeof sessionOriginLabel>[0];

describe("sessionOriginLabel", () => {
  it("says nothing about an ordinary web chat", () => {
    // 18 of 25 sessions in a live deployment are exactly this. A chip here
    // would be on most rows, which is the same as no chip at all.
    expect(sessionOriginLabel(session("chat", "web"), t)).toBeNull();
  });

  it.each([
    ["chat", "webhook", "sessions.origin.webhook"],
    ["chat", "telegram", "sessions.origin.telegram"],
    ["chat", "feishu", "sessions.origin.feishu"],
    ["chat", "weixin", "sessions.origin.wechat"],
    ["chat", "WeChat", "sessions.origin.wechat"],
  ])("labels %s over %s", (kind, channel, expected) => {
    expect(sessionOriginLabel(session(kind, channel), t)).toBe(expected);
  });

  it.each([
    ["main", "sessions.origin.main"],
    ["scheduler", "sessions.origin.scheduler"],
    ["task", "sessions.origin.goal"],
    ["delegate", "sessions.origin.delegate"],
  ])("lets kind %s outrank the channel", (kind, expected) => {
    expect(sessionOriginLabel(session(kind, "web"), t)).toBe(expected);
  });

  it("keeps internal routing addresses off the screen", () => {
    // Real values from a live deployment: the agent's own bound channel. This
    // is a routing key, not a name a person should ever read.
    expect(
      sessionOriginLabel(
        session("chat", "anna:user:019f7ab2-6deb-7213-a11c-450a1b93fb95:private"),
        t,
      ),
    ).toBeNull();
    expect(
      sessionOriginLabel(
        session("chat", "019f7ab2-3cd5-76b2-b845-772f5e91bb3d:user:019f7ab2-6deb:private"),
        t,
      ),
    ).toBeNull();
  });

  it("still names a channel inside a composite address", () => {
    expect(sessionOriginLabel(session("chat", "agent:channel:telegram:private"), t)).toBe(
      "sessions.origin.telegram",
    );
  });

  it("passes an unknown channel through rather than inventing a key", () => {
    expect(sessionOriginLabel(session("chat", "matrix"), t)).toBe("matrix");
  });

  it.each([
    ["chat", ""],
    ["chat", "   "],
  ])("says nothing for a missing channel (%s, %s)", (kind, channel) => {
    expect(sessionOriginLabel(session(kind, channel), t)).toBeNull();
  });
});

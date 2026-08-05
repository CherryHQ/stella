import { describe, expect, it } from "vitest";

import { sessionDisplayTitle } from "./session-title";

const UNTITLED = "Untitled session";

describe("sessionDisplayTitle", () => {
  // Read verbatim out of a running deployment. Every one is cut mid-value by
  // the backend's 60-character truncation, so none of them parse as JSON —
  // which is the whole reason this reads pairs instead of parsing.
  it.each([
    [
      '{"event":"persistent_test_1","message":"Remember the code…',
      "persistent_test_1 · Remember the code…",
    ],
    [
      '{"event":"sync_smoke_test","message":"Reply with exactly:…',
      "sync_smoke_test · Reply with exactly:…",
    ],
    ['{"event":"smoke_test","message":"Second public HTTPS…', "smoke_test · Second public HTTPS…"],
    ['{"event":"smoke_test","message":"Verify webhook…', "smoke_test · Verify webhook…"],
  ])("reads %s", (raw, expected) => {
    expect(sessionDisplayTitle(raw, UNTITLED)).toBe(expected);
  });

  it("refuses to depend on the fragment being valid JSON", () => {
    const raw = '{"event":"smoke_test","message":"Verify webhook…';
    expect(() => JSON.parse(raw)).toThrow();
    expect(sessionDisplayTitle(raw, UNTITLED)).not.toBe(raw);
  });

  it("reads complete JSON too", () => {
    expect(sessionDisplayTitle('{"event":"deploy","message":"shipped v2"}', UNTITLED)).toBe(
      "deploy · shipped v2",
    );
  });

  it.each([
    ["", UNTITLED],
    ["   ", UNTITLED],
    [null, UNTITLED],
    [undefined, UNTITLED],
  ])("falls back for %s", (raw, expected) => {
    expect(sessionDisplayTitle(raw, UNTITLED)).toBe(expected);
  });

  it("leaves prose alone", () => {
    expect(sessionDisplayTitle("what a good day", UNTITLED)).toBe("what a good day");
    // A sentence that merely mentions a brace is not a payload.
    expect(sessionDisplayTitle('use the {"a":1} syntax', UNTITLED)).toBe('use the {"a":1} syntax');
  });

  it("uses whichever half the payload carries", () => {
    expect(sessionDisplayTitle('{"message":"disk almost full"}', UNTITLED)).toBe(
      "disk almost full",
    );
    expect(sessionDisplayTitle('{"event":"heartbeat"}', UNTITLED)).toBe("heartbeat");
  });

  it("still beats raw JSON when no key is recognized", () => {
    expect(sessionDisplayTitle('{"foo":"alpha","bar":"beta","baz":"gamma"}', UNTITLED)).toBe(
      "alpha · beta",
    );
  });

  it("prefers the more specific label key", () => {
    expect(sessionDisplayTitle('{"type":"generic","event":"push","text":"main"}', UNTITLED)).toBe(
      "push · main",
    );
  });

  it("unescapes and flattens whitespace", () => {
    expect(sessionDisplayTitle('{"event":"ci","message":"line one\\nline two"}', UNTITLED)).toBe(
      "ci · line one line two",
    );
    expect(sessionDisplayTitle('{"event":"quote","message":"say \\"hi\\""}', UNTITLED)).toBe(
      'quote · say "hi"',
    );
  });

  it("decodes unicode escapes, which is how most senders send non-ASCII", () => {
    // Python's json.dumps escapes by default, so this is the common shape, not
    // an edge case — and it is exactly the history this helper exists to fix.
    expect(
      sessionDisplayTitle('{"event":"deploy","message":"\\u90e8\\u7f72\\u5b8c\\u6210"}', UNTITLED),
    ).toBe("deploy · 部署完成");
    expect(sessionDisplayTitle('{"event":"emoji","message":"\\ud83d\\ude80 up"}', UNTITLED)).toBe(
      "emoji · 🚀 up",
    );
  });

  it("keeps an escaped backslash instead of eating the character after it", () => {
    expect(sessionDisplayTitle('{"event":"literal","message":"a\\\\nb"}', UNTITLED)).toBe(
      "literal · a\\nb",
    );
  });

  it("survives a cut that lands inside an escape", () => {
    // 60-character truncation does not respect escape boundaries.
    expect(sessionDisplayTitle('{"event":"deploy","message":"done \\u90e', UNTITLED)).toBe(
      "deploy · done",
    );
    expect(sessionDisplayTitle('{"event":"deploy","message":"done \\', UNTITLED)).toBe(
      "deploy · done",
    );
  });

  it("matches the backend on key case", () => {
    expect(sessionDisplayTitle('{"Event":"Deploy","Message":"ok"}', UNTITLED)).toBe("Deploy · ok");
  });

  it("handles an array payload", () => {
    expect(sessionDisplayTitle('[{"event":"batch","message":"3 items"}]', UNTITLED)).toBe(
      "batch · 3 items",
    );
  });

  it("ignores empty values rather than emitting a dangling separator", () => {
    expect(sessionDisplayTitle('{"event":"","message":"only a body"}', UNTITLED)).toBe(
      "only a body",
    );
  });

  it("returns the fragment when it carries no readable pair", () => {
    expect(sessionDisplayTitle('{"count":42,"ok":true}', UNTITLED)).toBe('{"count":42,"ok":true}');
  });
});

import { describe, expect, it } from "vitest";
import { formatToolOutput, toolCallFailed } from "./utils";
import type { ContentBlock } from "@/lib/types";

type ToolCall = ContentBlock & { type: "tool_call" };

function call(result?: { content?: string; is_error?: boolean }): ToolCall {
  // SAFETY: the fixture is a tool_call content block whose shape ToolCall requires.
  return { type: "tool_call", name: "bash", arguments: {}, result } as ToolCall;
}

describe("toolCallFailed", () => {
  it("reads the exit code, not just the transport's error flag", () => {
    // A command that runs to completion and exits non-zero is a successful
    // *call* — `is_error` stays false — and a failed *step*. Reporting the
    // transport's view here would leave a red row invisible in the summary,
    // which is the whole reason the summary exists.
    expect(toolCallFailed(call({ content: "boom\n[exit:1 | 12ms]" }))).toBe(true);
    expect(toolCallFailed(call({ content: "ok\n[exit:0 | 12ms]" }))).toBe(false);
    expect(toolCallFailed(call({ content: "ok", is_error: true }))).toBe(true);
  });

  it("does not read a trailer that is not at the end", () => {
    // Tool output can quote an earlier run's trailer. Only the runner's own,
    // which is always last, is the verdict.
    expect(toolCallFailed(call({ content: "saw [exit:1 | 3ms] earlier\ndone" }))).toBe(false);
  });

  it("treats a call still in flight as not failed", () => {
    // Mid-stream a tool_call arrives before its result. Counting that as a
    // failure would flash a red badge on every step while it runs.
    expect(toolCallFailed(call())).toBe(false);
    expect(toolCallFailed(call({ content: "" }))).toBe(false);
  });
});

describe("formatToolOutput", () => {
  it("pretty-prints JSON objects and arrays", () => {
    expect(formatToolOutput('{"session_id":"s1","reply":"ok"}')).toBe(`{
  "session_id": "s1",
  "reply": "ok"
}`);
    expect(formatToolOutput('[{"id":"s1"}]')).toBe(`[
  {
    "id": "s1"
  }
]`);
  });

  it("leaves prose and malformed JSON untouched", () => {
    expect(formatToolOutput("plain output")).toBe("plain output");
    expect(formatToolOutput('{"broken"')).toBe('{"broken"');
  });
});

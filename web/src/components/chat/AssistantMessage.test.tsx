import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import { AssistantMessage } from "./AssistantMessage";

vi.hoisted(() => {
  Object.defineProperty(globalThis, "localStorage", {
    configurable: true,
    value: { getItem: () => "en", setItem: () => undefined },
  });
});

describe("AssistantMessage tool failures", () => {
  it("marks the failed tool row without adding an aggregate failure count", () => {
    const html = renderToStaticMarkup(
      <AssistantMessage
        agentName="Stella"
        agentId="stella"
        streaming
        blocks={[
          { type: "thinking", thinking: "Checking the file." },
          {
            type: "tool_call",
            id: "call-1",
            name: "read",
            arguments: { path: "/missing.txt" },
            status: "done",
            result: {
              tool_call_id: "call-1",
              content: "no such file or directory",
              is_error: true,
            },
          },
        ]}
      />,
    );

    expect(html).toContain("text-destructive-foreground");
    expect(html).not.toContain("1 failed");
    expect(html).not.toContain("1 个失败");
  });

  it("shows a specific waiting state for synchronous session calls", () => {
    const html = renderToStaticMarkup(
      <AssistantMessage
        agentName="Stella"
        agentId="stella"
        streaming
        blocks={[
          {
            type: "tool_call",
            id: "call-1",
            name: "session_send",
            arguments: { session_id: "session-1", message: "continue", wait: true },
          },
        ]}
      />,
    );

    expect(html).toContain("Waiting for session reply");
    expect(html).toContain("animate-spin");
  });

  // Transcripts written before the split still hold the union names. A row that
  // loses its metadata degrades to a generic wrench, which is a visible
  // regression on history the user can still scroll back to.
  it("keeps the wording of pre-split session and skills rows", () => {
    const html = renderToStaticMarkup(
      <AssistantMessage
        agentName="Stella"
        agentId="stella"
        streaming
        blocks={[
          {
            type: "tool_call",
            id: "legacy-1",
            name: "session",
            arguments: { action: "send", session_id: "session-1", wait: true },
          },
        ]}
      />,
    );

    expect(html).toContain("Waiting for session reply");
  });

  it("labels a completed pre-split session create row", () => {
    const html = renderToStaticMarkup(
      <AssistantMessage
        agentName="Stella"
        agentId="stella"
        streaming
        blocks={[
          {
            type: "tool_call",
            id: "legacy-2",
            name: "session",
            arguments: { action: "create", message: "research the outage" },
            status: "done",
            result: { tool_call_id: "legacy-2", content: '{"session_id":"s-2"}', is_error: false },
          },
        ]}
      />,
    );

    expect(html).toContain("Created session");
  });

  it("labels a pre-split skills load row as a skill", () => {
    const html = renderToStaticMarkup(
      <AssistantMessage
        agentName="Stella"
        agentId="stella"
        streaming
        blocks={[
          {
            type: "tool_call",
            id: "legacy-3",
            name: "skills",
            arguments: { action: "load", name: "planner" },
            status: "done",
            result: { tool_call_id: "legacy-3", content: "# Planner", is_error: false },
          },
        ]}
      />,
    );

    expect(html).toContain("Used skill");
    expect(html).toContain("planner");
  });

  it("labels the split memory rows by tool name", () => {
    const html = renderToStaticMarkup(
      <AssistantMessage
        agentName="Stella"
        agentId="stella"
        streaming
        blocks={[
          {
            type: "tool_call",
            id: "call-mem-1",
            name: "memory_search",
            arguments: { q: "the deploy checklist" },
            status: "done",
            result: { tool_call_id: "call-mem-1", content: '{"results":[]}', is_error: false },
          },
          {
            type: "tool_call",
            id: "call-mem-2",
            name: "memory_read",
            arguments: { ref: "profile" },
            status: "done",
            result: { tool_call_id: "call-mem-2", content: "Prefers tea", is_error: false },
          },
        ]}
      />,
    );

    expect(html).toContain("Searched memory");
    expect(html).toContain("the deploy checklist");
    expect(html).toContain("Read memory");
    expect(html).toContain("profile");
  });

  it("keeps the wording of a pre-split memory row", () => {
    const html = renderToStaticMarkup(
      <AssistantMessage
        agentName="Stella"
        agentId="stella"
        streaming
        blocks={[
          {
            type: "tool_call",
            id: "legacy-4",
            name: "memory",
            arguments: { action: "search", query: "the deploy checklist" },
            status: "done",
            result: { tool_call_id: "legacy-4", content: '{"results":[]}', is_error: false },
          },
        ]}
      />,
    );

    expect(html).toContain("Searched memory");
    expect(html).toContain("the deploy checklist");
  });
});

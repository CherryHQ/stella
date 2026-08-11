import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import { AssistantMessage } from "./AssistantMessage";

vi.hoisted(() => {
  Object.defineProperty(globalThis, "localStorage", {
    configurable: true,
    value: { getItem: () => "en", setItem: () => undefined },
  });
});

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({ t: (key: string) => key, locale: "en", setLocale: vi.fn() }),
}));

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
            name: "session",
            arguments: { action: "send", session_id: "session-1", wait: true },
          },
        ]}
      />,
    );

    expect(html).toContain("sessions.tool.waitingForReply");
    expect(html).toContain("animate-spin");
  });
});

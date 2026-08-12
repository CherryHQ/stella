import { describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { userMessageRenderInput } from "./utils";
import { UserMessage } from "./UserMessage";

vi.hoisted(() => {
  Object.defineProperty(globalThis, "localStorage", {
    configurable: true,
    value: { getItem: () => "en", setItem: () => undefined },
  });
});

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({ t: (key: string) => key }),
}));

describe("userMessageRenderInput", () => {
  it("keeps canonical text ordered while exposing an unrelated PDF marker as a workspace file", () => {
    const marker = "[file: /user/assets/report.pdf]";
    const input = userMessageRenderInput({
      content: `caption\n${marker}`,
      blocks: [
        { type: "text", text: "caption" },
        {
          type: "image",
          media_id: "media-id",
          mime_type: "image/png",
          url: "/api/agents/agent/sessions/session/media/media-id",
        },
        { type: "text", text: marker },
      ],
    });

    expect(input.hasCanonicalImage).toBe(true);
    expect(
      input.canonicalBlocks?.map((block) => (block.type === "text" ? block.text : block.media_id)),
    ).toEqual(["caption", "media-id", marker]);
    expect(input.text).toBe("caption");
    expect(input.otherFiles).toEqual(["/user/assets/report.pdf"]);
  });
});

describe("UserMessage provenance", () => {
  it("labels Agent input that came from another session", () => {
    const html = renderToStaticMarkup(
      <UserMessage
        msg={{ content: "Review this." }}
        actorType="agent"
        actorId="stella"
        sourceSessionId="source-session"
      />,
    );

    expect(html).toContain("chat.fromSession");
    expect(html).toContain('title="source-session"');
  });
});

import { describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { attachmentDisplayName, userMessageRenderInput } from "./utils";
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

describe("attachmentDisplayName", () => {
  it("strips the upload date+uuid prefix so the original name is readable", () => {
    expect(
      attachmentDisplayName(
        "/user/assets/202608/20260817-01a00ddf-cdb1-7923-9c4e-2b1f8a0d5e77-quarterly report.pdf",
      ),
    ).toBe("quarterly report.pdf");
  });

  it("leaves names that do not carry the prefix alone", () => {
    expect(attachmentDisplayName("/user/assets/notes.md")).toBe("notes.md");
    expect(attachmentDisplayName("20260817-not-a-uuid-report.pdf")).toBe(
      "20260817-not-a-uuid-report.pdf",
    );
  });
});

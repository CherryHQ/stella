import { describe, expect, it } from "vitest";
import { userMessageRenderInput } from "./utils";

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

import { describe, expect, it } from "vitest";
import type { UIMessage } from "ai";
import { SESSION_MESSAGE_ACTOR_TYPE, type SessionMessage } from "./types";
import {
  mergeToolResults,
  messageToUIMessage,
  reconcileHistoryUIMessages,
  sessionMessagesToMessages,
  uiMessageToMessage,
} from "./chat-transport";

const mediaURL = "/api/agents/agent/sessions/session/media/media-id";

describe("session history conversion", () => {
  it("preserves canonical text and image order without turning media into a marker", () => {
    const history: SessionMessage[] = [
      {
        id: "canonical-user",
        role: "user",
        actor_type: SESSION_MESSAGE_ACTOR_TYPE.human,
        timestamp: "2026-08-01T00:00:00Z",
        token_count: 3,
        content: "baseline text must not render beside the image",
        blocks: [
          { type: "text", text: "before" },
          { type: "image", media_id: "media-id", mime_type: "image/png", url: mediaURL },
          { type: "text", text: "after" },
        ],
      },
    ];

    const message = sessionMessagesToMessages(history)[0];
    expect(message.blocks).toEqual([
      { type: "text", text: "before" },
      { type: "image", media_id: "media-id", mime_type: "image/png", url: mediaURL },
      { type: "text", text: "after" },
    ]);

    const restored = uiMessageToMessage(messageToUIMessage(message));
    expect(restored.content).toBe("beforeafter");
    expect(restored.blocks).toEqual(message.blocks);
  });

  it("preserves Agent and source Session provenance through the AI SDK message cache", () => {
    const message = sessionMessagesToMessages([
      {
        id: "agent-input",
        role: "user",
        actor_type: SESSION_MESSAGE_ACTOR_TYPE.agent,
        actor_id: "stella",
        source_session_id: "source-session",
        timestamp: "2026-08-01T00:00:00Z",
        token_count: 3,
        content: "Review this.",
      },
    ])[0];

    expect(uiMessageToMessage(messageToUIMessage(message))).toMatchObject({
      actor_type: "agent",
      actor_id: "stella",
      source_session_id: "source-session",
    });
  });

  it("hides only an upload marker directly before a durable image in Web presentation", () => {
    const marker = "[file: /user/assets/photo.png]";
    const message = sessionMessagesToMessages([
      {
        id: "canonical-user",
        role: "user",
        actor_type: SESSION_MESSAGE_ACTOR_TYPE.human,
        timestamp: "2026-08-01T00:00:00Z",
        token_count: 1,
        content: `caption\n${marker}`,
        blocks: [
          { type: "text", text: "caption" },
          { type: "text", text: marker },
          { type: "image", media_id: "media-id", mime_type: "image/png", url: mediaURL },
        ],
      },
    ])[0];

    expect(messageToUIMessage(message).parts).toEqual([
      { type: "text", text: "caption" },
      { type: "file", url: mediaURL, mediaType: "image/png" },
    ]);
  });

  it("hides a successful image marker whose filename contains a closing bracket", () => {
    const marker = "[file: /user/assets/photo].png]";
    const message = sessionMessagesToMessages([
      {
        id: "canonical-bracket",
        role: "user",
        actor_type: SESSION_MESSAGE_ACTOR_TYPE.human,
        timestamp: "2026-08-01T00:00:00Z",
        token_count: 1,
        blocks: [
          { type: "text", text: marker },
          { type: "image", media_id: "media-id", mime_type: "image/png", url: mediaURL },
        ],
      },
    ])[0];

    expect(messageToUIMessage(message).parts).toEqual([
      { type: "file", url: mediaURL, mediaType: "image/png" },
    ]);
  });

  it("keeps legacy workspace markers on the optimistic path", () => {
    const optimistic: UIMessage = {
      id: "optimistic",
      role: "user",
      parts: [{ type: "file", url: "/user/assets/photo.png", mediaType: "image/png" }],
    };

    expect(uiMessageToMessage(optimistic)).toMatchObject({
      content: "[file: /user/assets/photo.png]",
      blocks: [{ type: "text", text: "[file: /user/assets/photo.png]" }],
    });
  });

  it("merges canonical tool-result images into expanded tool output", () => {
    const messages = mergeToolResults(
      sessionMessagesToMessages([
        {
          id: "assistant",
          role: "assistant",
          actor_type: SESSION_MESSAGE_ACTOR_TYPE.agent,
          timestamp: "2026-08-01T00:00:01Z",
          token_count: 1,
          blocks: [{ type: "tool_call", id: "call", name: "read", arguments: {} }],
        },
        {
          id: "tool",
          role: "tool",
          actor_type: SESSION_MESSAGE_ACTOR_TYPE.agent,
          timestamp: "2026-08-01T00:00:02Z",
          token_count: 1,
          tool_call_id: "call",
          tool_name: "read",
          is_error: true,
          content: "baseline",
          blocks: [
            { type: "text", text: "tool text" },
            { type: "image", media_id: "media-id", mime_type: "image/png", url: mediaURL },
          ],
        },
      ]),
    );

    const restored = uiMessageToMessage(messageToUIMessage(messages[0]));
    const tool = restored.blocks?.find((block) => block.type === "tool_call");
    expect(tool).toMatchObject({
      result: {
        is_error: true,
        blocks: [
          { type: "text", text: "tool text" },
          { type: "image", media_id: "media-id", url: mediaURL },
        ],
      },
    });
  });

  it("replaces an optimistic workspace attachment with its canonical history row", () => {
    const optimistic: UIMessage = {
      id: "optimistic",
      role: "user",
      parts: [
        { type: "text", text: "look" },
        { type: "file", url: "/user/assets/photo.png", mediaType: "image/png" },
      ],
      metadata: { timestamp: "2026-08-01T00:00:05Z" },
    };
    const canonical = messageToUIMessage(
      sessionMessagesToMessages([
        {
          id: "canonical",
          role: "user",
          actor_type: SESSION_MESSAGE_ACTOR_TYPE.human,
          timestamp: "2026-08-01T00:00:00Z",
          token_count: 1,
          blocks: [
            { type: "text", text: "look" },
            { type: "image", media_id: "media-id", mime_type: "image/png", url: mediaURL },
          ],
        },
      ])[0],
    );

    expect(reconcileHistoryUIMessages([canonical], [optimistic])).toEqual([canonical]);
  });

  it("reconciles mixed PDF and image uploads", () => {
    const optimistic: UIMessage = {
      id: "optimistic-mixed",
      role: "user",
      parts: [
        { type: "text", text: "look" },
        { type: "file", url: "/user/assets/report.pdf", mediaType: "application/pdf" },
        { type: "file", url: "/user/assets/photo.png", mediaType: "image/png" },
      ],
      metadata: { timestamp: "2026-08-01T00:00:05Z" },
    };
    const canonical = messageToUIMessage(
      sessionMessagesToMessages([
        {
          id: "canonical-mixed",
          role: "user",
          actor_type: SESSION_MESSAGE_ACTOR_TYPE.human,
          timestamp: "2026-08-01T00:00:00Z",
          token_count: 1,
          blocks: [
            { type: "text", text: "look" },
            { type: "text", text: "[file: /user/assets/report.pdf]" },
            { type: "text", text: "[file: /user/assets/photo.png]" },
            { type: "image", media_id: "media-id", mime_type: "image/png", url: mediaURL },
          ],
        },
      ])[0],
    );
    expect(reconcileHistoryUIMessages([canonical], [optimistic])).toEqual([canonical]);
  });

  it("reconciles when one of two optimistic images remains only as a marker", () => {
    const optimistic: UIMessage = {
      id: "optimistic-partial",
      role: "user",
      parts: [
        { type: "text", text: "look" },
        { type: "file", url: "/user/assets/kept.png", mediaType: "image/png" },
        { type: "file", url: "/user/assets/unreadable].png", mediaType: "image/png" },
      ],
      metadata: { timestamp: "2026-08-01T00:00:05Z" },
    };
    const canonical = messageToUIMessage(
      sessionMessagesToMessages([
        {
          id: "canonical-partial",
          role: "user",
          actor_type: SESSION_MESSAGE_ACTOR_TYPE.human,
          timestamp: "2026-08-01T00:00:00Z",
          token_count: 1,
          blocks: [
            { type: "text", text: "look" },
            { type: "text", text: "[file: /user/assets/kept.png]" },
            { type: "image", media_id: "media-id", mime_type: "image/png", url: mediaURL },
            { type: "text", text: "[file: /user/assets/unreadable].png]" },
          ],
        },
      ])[0],
    );

    expect(reconcileHistoryUIMessages([canonical], [optimistic])).toEqual([canonical]);
  });

  it("consumes only one of two otherwise identical optimistic uploads", () => {
    const canonical = messageToUIMessage(
      sessionMessagesToMessages([
        {
          id: "canonical",
          role: "user",
          actor_type: SESSION_MESSAGE_ACTOR_TYPE.human,
          timestamp: "2026-08-01T00:00:00Z",
          token_count: 1,
          blocks: [
            { type: "text", text: "look" },
            { type: "image", media_id: "media-id", mime_type: "image/png", url: mediaURL },
          ],
        },
      ])[0],
    );
    const first: UIMessage = {
      id: "first",
      role: "user",
      parts: [
        { type: "text", text: "look" },
        { type: "file", url: "/user/assets/first.png", mediaType: "image/png" },
      ],
      metadata: { timestamp: "2026-08-01T00:00:05Z" },
    };
    const second: UIMessage = { ...first, id: "second" };

    expect(reconcileHistoryUIMessages([canonical], [first, second])).toEqual([canonical, second]);
  });

  it("keeps a failed earlier upload and replaces the nearest successful match", () => {
    const canonical = messageToUIMessage(
      sessionMessagesToMessages([
        {
          id: "canonical",
          role: "user",
          actor_type: SESSION_MESSAGE_ACTOR_TYPE.human,
          timestamp: "2026-08-01T00:00:20Z",
          token_count: 1,
          blocks: [
            { type: "text", text: "look" },
            { type: "image", media_id: "media-id", mime_type: "image/png", url: mediaURL },
          ],
        },
      ])[0],
    );
    const failedFirst: UIMessage = {
      id: "failed-first",
      role: "user",
      parts: [
        { type: "text", text: "look" },
        { type: "file", url: "/user/assets/failed.png", mediaType: "image/png" },
      ],
      metadata: { timestamp: "2026-08-01T00:00:00Z" },
    };
    const successfulSecond: UIMessage = {
      ...failedFirst,
      id: "successful-second",
      parts: [
        { type: "text", text: "look" },
        { type: "file", url: "/user/assets/success.png", mediaType: "image/png" },
      ],
      metadata: { timestamp: "2026-08-01T00:00:18Z" },
    };

    expect(reconcileHistoryUIMessages([canonical], [failedFirst, successfulSecond])).toEqual([
      canonical,
      failedFirst,
    ]);
  });

  it("does not replace a new optimistic upload with an old matching history row", () => {
    const canonical = messageToUIMessage(
      sessionMessagesToMessages([
        {
          id: "old-canonical",
          role: "user",
          actor_type: SESSION_MESSAGE_ACTOR_TYPE.human,
          timestamp: "2026-08-01T00:00:00Z",
          token_count: 1,
          blocks: [
            { type: "text", text: "look" },
            { type: "image", media_id: "media-id", mime_type: "image/png", url: mediaURL },
          ],
        },
      ])[0],
    );
    const optimistic: UIMessage = {
      id: "new-optimistic",
      role: "user",
      parts: [
        { type: "text", text: "look" },
        { type: "file", url: "/user/assets/new.png", mediaType: "image/png" },
      ],
      metadata: { timestamp: "2026-08-01T00:05:00Z" },
    };

    expect(reconcileHistoryUIMessages([canonical], [optimistic])).toEqual([canonical, optimistic]);
  });

  it("replaces live turn IDs when persisted history is authoritative", () => {
    const canonical: UIMessage[] = [
      { id: "db-user", role: "user", parts: [{ type: "text", text: "hi" }] },
      { id: "db-assistant", role: "assistant", parts: [{ type: "text", text: "hello" }] },
    ];
    const live: UIMessage[] = [
      { id: "client-user", role: "user", parts: [{ type: "text", text: "hi" }] },
      { id: "sse-assistant", role: "assistant", parts: [{ type: "text", text: "hello" }] },
    ];

    expect(reconcileHistoryUIMessages(canonical, live, { authoritative: true })).toEqual(canonical);
  });
});

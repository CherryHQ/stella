import { describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  vi.stubGlobal("localStorage", { getItem: () => null, setItem: () => {}, removeItem: () => {} });
});

import { channelConfig, normalizeChannel, platformConfigDefaults } from "./ChannelFields";

describe("Telegram channel configuration", () => {
  it("keeps chat and topic allowlists when an existing channel is saved", () => {
    const channel = normalizeChannel({
      id: "telegram-main",
      name: "Telegram",
      type: "telegram",
      agent_id: "agent-1",
      enabled: true,
      config: JSON.stringify({
        token: "redacted",
        allowed_chat_ids: ["-100"],
        allowed_topic_ids: ["-100:42"],
      }),
    });

    expect(platformConfigDefaults("telegram")).toMatchObject({
      allowed_chat_ids: [],
      allowed_topic_ids: [],
    });
    expect(JSON.parse(channelConfig(channel))).toMatchObject({
      allowed_chat_ids: ["-100"],
      allowed_topic_ids: ["-100:42"],
    });
  });
});

import { describe, expect, it } from "vitest";
import type { Provider } from "@/lib/types";
import { parseProviderJSON } from "./provider-helpers";

describe("parseProviderJSON", () => {
  it("preserves omitted connection fields and model overrides", () => {
    const provider: Provider = {
      id: "openai",
      type: "openai-response",
      name: "OpenAI",
      enabled: false,
      api_key: "sk-secret",
      base_url: "https://api.openai.com/v1",
      models: { "gpt-4o": { enabled: true } },
    };

    expect(parseProviderJSON('{"name":"Renamed"}', provider)).toMatchObject({
      name: "Renamed",
      enabled: false,
      api_key: "sk-secret",
      base_url: "https://api.openai.com/v1",
      models: { "gpt-4o": { enabled: true } },
    });
  });
});

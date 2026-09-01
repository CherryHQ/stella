import { describe, expect, it } from "vitest";
import type { ModelConfig, Provider } from "@/lib/types";
import {
  createCustomModelForm,
  formFromModelConfig,
  modelConfigFromForm,
  parseProviderJSON,
  withModelEnabledOverride,
} from "./provider-helpers";

function modelConfig(overrides: Partial<ModelConfig>): ModelConfig {
  return {
    id: "m",
    name: "m",
    enabled: true,
    reasoning: false,
    input: [],
    output: [],
    ...overrides,
  };
}

describe("createCustomModelForm", () => {
  // Load-bearing default. `input` is not advisory: the runtime reads an explicit
  // list without "image" as "this model CANNOT see" and rewrites images to text.
  // Seeding "text" would make every hand-added model silently image-blind, so a
  // new form must declare nothing and let capability stay unknown (fail open).
  it("declares no input modality", () => {
    expect(createCustomModelForm().input).toBe("");
    expect(modelConfigFromForm(createCustomModelForm()).input).toEqual([]);
  });
});

describe("formFromModelConfig", () => {
  it("shows the stored modalities when editing", () => {
    const form = formFromModelConfig(
      "gpt-4o",
      modelConfig({ input: ["text", "image"], output: ["text"] }),
    );
    expect(form.input).toBe("text, image");
  });

  // Editing a model that never declared modalities must not invent them: the
  // blank round-trips back to an empty list rather than to ["text"].
  it("leaves undeclared modalities undeclared", () => {
    const form = formFromModelConfig("mystery-model", modelConfig({}));
    expect(form.input).toBe("");
    expect(modelConfigFromForm(form).input).toEqual([]);
  });
});

describe("modelConfigFromForm", () => {
  it("splits and trims a comma-separated modality list", () => {
    const form = { ...createCustomModelForm(), id: "m", input: " text ,image, " };
    expect(modelConfigFromForm(form).input).toEqual(["text", "image"]);
  });
});

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

describe("withModelEnabledOverride", () => {
  it("persists only enabled when toggling a catalog model without an override", () => {
    expect(withModelEnabledOverride({}, "gpt-4o", undefined, false)).toEqual({
      "gpt-4o": { enabled: false },
    });
  });

  it("preserves explicit override fields without copying effective catalog metadata", () => {
    expect(
      withModelEnabledOverride({}, "gpt-4o", { name: "Pinned name", reasoning: true }, true),
    ).toEqual({
      "gpt-4o": { name: "Pinned name", reasoning: true, enabled: true },
    });
  });
});

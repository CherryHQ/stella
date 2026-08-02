import { describe, expect, it } from "vitest";
import type { ModelConfig } from "@/lib/types";
import {
  createCustomModelForm,
  formFromModelConfig,
  modelConfigFromForm,
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

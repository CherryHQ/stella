import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import type { ProviderModel } from "@/lib/types";
import { ProviderModelDetail } from "./ProviderModelDetail";

vi.hoisted(() => {
  Object.defineProperty(globalThis, "localStorage", {
    configurable: true,
    value: { getItem: () => "en", setItem: () => undefined },
  });
});

function model(): ProviderModel {
  return {
    id: "gpt-4o",
    source: "catalog",
    enabled: true,
    config: {
      enabled: true,
      name: "GPT-4o",
      input: ["text", "image"],
      output: ["text"],
      contextWindow: 128000,
      maxTokens: 16384,
      cost: { input: 2.5, output: 10 },
    },
    catalog: {
      id: "gpt-4o",
      name: "GPT-4o",
      family: "gpt-4",
      description: "Multimodal flagship",
      attachment: true,
      tool_call: true,
      structured_output: true,
      input: ["text", "image"],
      output: ["text"],
      contextWindow: 128000,
      maxTokens: 16384,
      cost: { input: 2.5, output: 10 },
    },
  };
}

function render(override: ProviderModel["override"], item: ProviderModel = model()) {
  return renderToStaticMarkup(
    <ProviderModelDetail
      model={item}
      override={override}
      catalogModels={item.catalog ? [item.catalog] : []}
      summary={["128K ctx", "$2.50 / $10.00"]}
      disabled={false}
      onFieldChange={vi.fn()}
      onCatalogMatchChange={vi.fn()}
      onCostChange={vi.fn()}
      onClearOverrides={vi.fn()}
      onInvalid={vi.fn()}
    />,
  );
}

describe("ProviderModelDetail", () => {
  it("shows the catalog metadata the list row cannot fit", () => {
    const markup = render(undefined);
    expect(markup).toContain("Multimodal flagship");
    expect(markup).toContain("128K");
    expect(markup).toContain("16K");
    expect(markup).toContain("text, image");
    expect(markup).toContain("$2.50");
    expect(markup).toContain("Structured output");
    expect(markup).toContain("gpt-4");
  });

  // An inherited field is blank and shows what it would inherit as its
  // placeholder; a pinned field carries a value and says what it replaced. That
  // contrast is the whole reason the panel exists.
  it("leaves an inherited field blank behind its inherited value", () => {
    const markup = render(undefined);
    expect(markup).toContain('placeholder="128K"');
    expect(markup).toContain('value=""');
    expect(markup).not.toContain("Inherits");
  });

  it("fills a pinned field and names the layer it replaced", () => {
    const pinned = render({ maxTokens: 8192 });
    expect(pinned).toContain('value="8192"');
    expect(pinned).toContain("Inherits 16K from Catalog");
  });

  // `config` is the server's already-resolved model, so a saved override is
  // baked into it. Reading the inherited value from there reports the
  // operator's own pin back to them as the thing it replaced.
  it("names the catalog value, not the saved pin, as what an override replaced", () => {
    const saved = model();
    saved.config = {
      enabled: true,
      name: "GPT-4o",
      input: ["text", "image"],
      output: ["text"],
      contextWindow: 128000,
      maxTokens: 8192,
      cost: { input: 1.5, output: 10 },
    };
    saved.override = { maxTokens: 8192, cost: { input: 1.5 } };

    const markup = render(saved.override, saved);
    expect(markup).toContain("Inherits 16K from Catalog");
    expect(markup).toContain("Inherits $2.50 from Catalog");
  });

  it("shows the catalog reasoning value after a saved pin is cleared", () => {
    const stale = model();
    const config = stale.config;
    if (!config) throw new Error("test model config is required");
    stale.config = { ...config, reasoning: true };
    const markup = render(undefined, stale);
    expect(markup).toContain("Inherit (No)");
    expect(markup).not.toContain("Inherit (Yes)");
  });

  it("offers a reset only for fields that carry an override", () => {
    expect(render(undefined)).not.toContain("Reset Max tokens");
    expect(render({ maxTokens: 8192 })).toContain("Reset Max tokens to the inherited value");
  });

  it("exposes every editable field through a labelled control", () => {
    const markup = render(undefined);
    for (const field of [
      "Display name",
      "Context window",
      "Max tokens",
      "Input modalities",
      "Output modalities",
      "Cache read",
    ]) {
      expect(markup).toContain(`aria-label="${field}"`);
    }
    expect(markup).toContain('aria-label="Reasoning"');
  });
});

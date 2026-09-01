import { describe, expect, it } from "vitest";
import type { ProviderModelOverride } from "@/lib/api-client/types.gen";
import type { ProviderModel } from "@/lib/types";
import {
  capabilitiesOf,
  costOrigin,
  effectiveCost,
  effectiveValue,
  fieldOrigin,
  formatPriceSummary,
  formatRate,
  formatTokenLimit,
  matchesModelFilters,
  matchingCatalogModels,
  overrideCount,
  selectedCatalogModel,
  withCatalogMatch,
  withCostOverride,
  withEnabledOverrides,
  withFieldOverride,
  withoutModelOverride,
} from "./provider-model-view";

function catalogModel(override: Partial<ProviderModel> = {}): ProviderModel {
  return {
    id: "gpt-4o",
    source: "catalog",
    enabled: true,
    config: {
      enabled: true,
      name: "GPT-4o",
      reasoning: false,
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
      reasoning: false,
      tool_call: true,
      structured_output: true,
      input: ["text", "image"],
      output: ["text"],
      contextWindow: 128000,
      maxTokens: 16384,
      cost: { input: 2.5, output: 10 },
    },
    ...override,
  };
}

describe("sparse override writes", () => {
  // The whole point of the override map: it holds what an operator chose, not a
  // snapshot of the catalog. Copying an inherited value in would freeze that
  // field against every later catalog sync.
  it("pins only the edited field on a model with no prior override", () => {
    expect(withFieldOverride({}, "gpt-4o", undefined, "maxTokens", 8192)).toEqual({
      "gpt-4o": { maxTokens: 8192 },
    });
  });

  it("keeps previously pinned fields when pinning another", () => {
    expect(
      withFieldOverride({}, "gpt-4o", { name: "Pinned", reasoning: true }, "enabled", false),
    ).toEqual({ "gpt-4o": { name: "Pinned", reasoning: true, enabled: false } });
  });

  it("drops the model entry once its last pin is cleared", () => {
    expect(
      withFieldOverride(
        { "gpt-4o": { maxTokens: 8192 } },
        "gpt-4o",
        { maxTokens: 8192 },
        "maxTokens",
        undefined,
      ),
    ).toEqual({});
  });

  it("clears one field without touching the others", () => {
    const override: ProviderModelOverride = { name: "Pinned", maxTokens: 8192 };
    expect(
      withFieldOverride({ "gpt-4o": override }, "gpt-4o", override, "name", undefined),
    ).toEqual({ "gpt-4o": { maxTokens: 8192 } });
  });

  it("stores only an explicit catalog-model binding and can restore automatic matching", () => {
    expect(withCatalogMatch({}, "gateway-gpt", undefined, "openai/gpt-4o")).toEqual({
      "gateway-gpt": { catalogModel: "openai/gpt-4o" },
    });
    expect(
      withCatalogMatch(
        { "gateway-gpt": { catalogModel: "openai/gpt-4o" } },
        "gateway-gpt",
        { catalogModel: "openai/gpt-4o" },
        undefined,
      ),
    ).toEqual({});
  });

  it("caps Catalog search results while preserving an existing selection", () => {
    const catalog = Array.from({ length: 200 }, (_, index) => ({
      id: `${index % 2 === 0 ? "openai" : "anthropic"}/model-${index}`,
      name: `Model ${index}`,
    }));
    const visible = matchingCatalogModels(catalog, "", "anthropic/model-199");
    expect(visible).toHaveLength(51);
    expect(visible.at(-1)?.id).toBe("anthropic/model-199");
    expect(matchingCatalogModels(catalog, "anthropic model 19", undefined)).toHaveLength(7);
    expect(matchingCatalogModels(catalog, "openai model", undefined)).toHaveLength(50);
  });

  it("resolves a manual binding across the complete Catalog for a custom Provider", () => {
    const model = catalogModel({ id: "gateway-sonnet", source: "custom", catalog: undefined });
    const selected = selectedCatalogModel(model, { catalogModel: "anthropic/claude-sonnet-4" }, [
      { id: "anthropic/claude-sonnet-4", name: "Claude Sonnet 4" },
    ]);
    expect(selected?.name).toBe("Claude Sonnet 4");
  });

  it("writes a single rate without materializing the rest of the price table", () => {
    expect(withCostOverride({}, "gpt-4o", undefined, "output", 12)).toEqual({
      "gpt-4o": { cost: { output: 12 } },
    });
  });

  it("removes the cost object, and the entry, when the last rate is cleared", () => {
    const override: ProviderModelOverride = { cost: { output: 12 } };
    expect(
      withCostOverride({ "gpt-4o": override }, "gpt-4o", override, "output", undefined),
    ).toEqual({});
  });

  it("keeps a zero rate, which means free rather than unset", () => {
    expect(withCostOverride({}, "gpt-4o", undefined, "input", 0)).toEqual({
      "gpt-4o": { cost: { input: 0 } },
    });
  });

  it("writes one enabled key per model on a bulk toggle", () => {
    const models = [catalogModel(), catalogModel({ id: "o3" })];
    expect(withEnabledOverrides({ o3: { name: "Kept" } }, models, false)).toEqual({
      "gpt-4o": { enabled: false },
      o3: { name: "Kept", enabled: false },
    });
  });

  it("returns a model to full inheritance", () => {
    expect(
      withoutModelOverride({ "gpt-4o": { maxTokens: 1 }, o3: { enabled: true } }, "gpt-4o"),
    ).toEqual({
      o3: { enabled: true },
    });
  });
});

describe("provenance", () => {
  it("reports catalog for an untouched catalog field", () => {
    expect(fieldOrigin(catalogModel(), undefined, "maxTokens")).toBe("catalog");
  });

  it("reports override once the field is pinned", () => {
    expect(fieldOrigin(catalogModel(), { maxTokens: 8192 }, "maxTokens")).toBe("override");
  });

  // The catalog never carries `enabled`; it comes from the provider's model
  // policy, so an unpinned switch must not claim a catalog origin.
  it("never attributes enabled to the catalog", () => {
    expect(fieldOrigin(catalogModel(), undefined, "enabled")).toBe("default");
  });

  it("reports provider for a discovered model with no catalog entry", () => {
    const fetched = catalogModel({ source: "fetched", catalog: undefined });
    expect(fieldOrigin(fetched, undefined, "contextWindow")).toBe("provider");
    expect(costOrigin(fetched, undefined, "input")).toBe("provider");
  });

  it("counts pinned scalars and rates together", () => {
    expect(overrideCount({ name: "x", cost: { input: 1, output: 2 } })).toBe(3);
    expect(overrideCount(undefined)).toBe(0);
    expect(overrideCount({})).toBe(0);
  });
});

describe("effective values", () => {
  it("prefers a pin over the server's merged config", () => {
    expect(effectiveValue(catalogModel(), { maxTokens: 8192 }, "maxTokens")).toBe(8192);
    expect(effectiveValue(catalogModel(), undefined, "maxTokens")).toBe(16384);
  });

  it("reads enabled from the effective row when unpinned", () => {
    expect(effectiveValue(catalogModel({ enabled: false }), undefined, "enabled")).toBe(false);
    expect(effectiveValue(catalogModel({ enabled: false }), { enabled: true }, "enabled")).toBe(
      true,
    );
  });

  it("merges a pinned rate over the inherited price table", () => {
    expect(effectiveCost(catalogModel(), { cost: { output: 12 } })).toEqual({
      input: 2.5,
      output: 12,
    });
  });

  it("shows inherited values immediately after a saved pin is cleared", () => {
    const stale = catalogModel();
    const config = stale.config;
    if (!config) throw new Error("test model config is required");
    stale.config = {
      ...config,
      maxTokens: 8192,
      cost: { input: 1.5, output: 10 },
    };
    expect(effectiveValue(stale, undefined, "maxTokens")).toBe(16384);
    expect(effectiveCost(stale, undefined)).toEqual({ input: 2.5, output: 10 });
  });

  it("lists capabilities from the catalog and the effective modalities", () => {
    expect(capabilitiesOf(catalogModel(), undefined)).toEqual(["tools", "structured", "vision"]);
    expect(capabilitiesOf(catalogModel(), { reasoning: true, input: ["text"] })).toEqual([
      "reasoning",
      "tools",
      "structured",
      "vision",
    ]);
  });
});

describe("filters", () => {
  const filters = { search: "", source: "all", status: "all" } as const;

  it("matches on id, display name, and family", () => {
    expect(matchesModelFilters(catalogModel(), undefined, { ...filters, search: "gpt-4" })).toBe(
      true,
    );
    expect(matchesModelFilters(catalogModel(), undefined, { ...filters, search: "flagship" })).toBe(
      false,
    );
  });

  it("uses the pinned enabled state, not the stale server row", () => {
    const model = catalogModel({ enabled: true });
    expect(matchesModelFilters(model, { enabled: false }, { ...filters, status: "enabled" })).toBe(
      false,
    );
  });

  it("isolates models an operator has touched", () => {
    expect(
      matchesModelFilters(catalogModel(), undefined, { ...filters, status: "overridden" }),
    ).toBe(false);
    expect(
      matchesModelFilters(catalogModel(), { maxTokens: 1 }, { ...filters, status: "overridden" }),
    ).toBe(true);
  });
});

describe("formatting", () => {
  it("abbreviates token limits", () => {
    expect(formatTokenLimit(128000)).toBe("128K");
    expect(formatTokenLimit(1000000)).toBe("1M");
    expect(formatTokenLimit(undefined)).toBe("—");
  });

  // An absent rate is unknown; an explicit zero is a promise that it is free.
  it("separates an unknown rate from a free one", () => {
    expect(formatRate(undefined)).toBe("—");
    expect(formatRate(0)).toBe("$0");
    expect(formatRate(2.5)).toBe("$2.50");
    expect(formatRate(0.0004)).toBe("$0.0004");
  });

  it("summarizes a price pair, and says nothing when unpriced", () => {
    expect(formatPriceSummary({ input: 2.5, output: 10 }, "Free")).toBe("$2.50 / $10.00");
    expect(formatPriceSummary({ input: 0, output: 0 }, "Free")).toBe("Free");
    expect(formatPriceSummary({}, "Free")).toBe("");
    expect(formatPriceSummary(undefined, "Free")).toBe("");
  });
});

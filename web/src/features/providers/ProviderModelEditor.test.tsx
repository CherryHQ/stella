import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import type { ProviderModel } from "@/lib/types";
import { ProviderModelEditor } from "./ProviderModelEditor";
import type { ProviderOverrides } from "./provider-model-view";

vi.hoisted(() => {
  Object.defineProperty(globalThis, "localStorage", {
    configurable: true,
    value: { getItem: () => "en", setItem: () => undefined },
  });
});

const models: ProviderModel[] = [
  {
    id: "gpt-4o",
    source: "catalog",
    enabled: true,
    config: {
      enabled: true,
      name: "GPT-4o",
      contextWindow: 128000,
      cost: { input: 2.5, output: 10 },
    },
    catalog: { id: "gpt-4o", name: "GPT-4o", tool_call: true, contextWindow: 128000 },
  },
  { id: "local-llama", source: "custom", enabled: false, config: { enabled: false } },
];

function render(
  overrides: ProviderOverrides,
  extra: { isError?: boolean; isLoading?: boolean } = {},
) {
  return renderToStaticMarkup(
    <ProviderModelEditor
      models={extra.isError || extra.isLoading ? [] : models}
      overrides={overrides}
      isLoading={extra.isLoading ?? false}
      isError={extra.isError ?? false}
      saving={false}
      onRetry={vi.fn()}
      onCommit={vi.fn()}
      onFetchModels={vi.fn()}
      showToast={vi.fn()}
    />,
  );
}

describe("ProviderModelEditor", () => {
  it("summarizes each model on one scannable row", () => {
    const markup = render({});
    expect(markup).toContain("gpt-4o");
    expect(markup).toContain("128K ctx");
    expect(markup).toContain("$2.50 / $10.00");
    expect(markup).toContain("Catalog");
    expect(markup).toContain("1 of 2 enabled");
  });

  it("flags how many fields an operator has pinned on a model", () => {
    expect(render({})).not.toContain("overridden");
    expect(render({ "gpt-4o": { maxTokens: 8192, cost: { output: 12 } } })).toContain(
      "2 overridden",
    );
  });

  it("gives every row switch and menu an accessible name", () => {
    const markup = render({});
    expect(markup).toContain('aria-label="Enable gpt-4o"');
    expect(markup).toContain('aria-label="Select gpt-4o"');
  });

  // A failed models request and an empty provider are different sentences; the
  // list must never report "no models" for "the server did not answer".
  it("separates a load failure from an empty provider", () => {
    expect(render({}, { isError: true })).toContain("Failed to load models from this provider.");
    expect(render({}, { isError: true })).not.toContain("No models yet");
  });
});

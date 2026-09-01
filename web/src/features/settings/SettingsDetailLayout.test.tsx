import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import { SettingsDetailLayout } from "./SettingsDetailLayout";

vi.hoisted(() => {
  Object.defineProperty(globalThis, "localStorage", {
    configurable: true,
    value: { getItem: () => "en", setItem: () => undefined },
  });
});

describe("SettingsDetailLayout", () => {
  it("renders the provider list and full-width detail surface together on desktop", () => {
    const html = renderToStaticMarkup(
      <SettingsDetailLayout list={<div>Provider list</div>} detail={<div>Provider models</div>} />,
    );

    expect(html).toContain("Provider list");
    expect(html).toContain("Provider models");
    expect(html).toContain("md:w-[240px]");
    expect(html).toContain("min-w-0 flex-1");
    expect(html).toContain("Back");
  });

  it("renders the empty state when no provider is selected", () => {
    const html = renderToStaticMarkup(
      <SettingsDetailLayout
        list={<div>Provider list</div>}
        emptyState={<div>Select provider</div>}
      />,
    );

    expect(html).toContain("Select provider");
  });
});

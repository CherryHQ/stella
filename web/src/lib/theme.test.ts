import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

import { DARK_TOKENS, DEFAULT_ACCENT_HUE, LIGHT_TOKENS, type TokenSpec } from "./theme";

/**
 * `LIGHT_TOKENS` / `DARK_TOKENS` hand-mirror the chromatic half of tokens.css so
 * a chosen accent hue can rotate the whole family. A stale mirror is silent:
 * the default theme still renders from tokens.css and looks right, and only the
 * users who picked a custom hue get the old palette back. This parses the real
 * stylesheet so drift fails the build instead.
 */

const css = readFileSync(fileURLToPath(new URL("../tokens.css", import.meta.url)), "utf8");

function block(selector: string): Map<string, [number, number, number]> {
  const start = css.indexOf(`${selector} {`);
  if (start < 0) throw new Error(`tokens.css has no ${selector} block`);
  const body = css.slice(start, css.indexOf("\n}", start));
  const out = new Map<string, [number, number, number]>();
  for (const [, name, l, c, h] of body.matchAll(
    /--([a-z0-9-]+):\s*oklch\(([\d.]+)\s+([\d.]+)\s+([\d.]+)\)/g,
  )) {
    out.set(name, [Number(l), Number(c), Number(h)]);
  }
  return out;
}

const blocks = { light: block(":root"), dark: block(".dark") };

describe.each([
  ["light", LIGHT_TOKENS, blocks.light],
  ["dark", DARK_TOKENS, blocks.dark],
] as const)("%s accent mirror", (_theme, specs: readonly TokenSpec[], declared) => {
  it.each(specs)("--%s matches tokens.css", (name, l, c, h) => {
    expect(declared.get(name), `--${name} drifted from (or is missing in) tokens.css`).toEqual([
      l,
      c,
      h,
    ]);
  });
});

describe("accent rotation invariants", () => {
  it("keeps both themes over the same token names", () => {
    // applyAccent clears overrides using the light names only, so a dark-only
    // token would stay pinned to a rotated hue after the user resets to default.
    expect(DARK_TOKENS.map(([n]) => n)).toEqual(LIGHT_TOKENS.map(([n]) => n));
  });

  it("anchors the rotation on the shipped primary hue", () => {
    // A mismatch makes the shipped theme a rotation of itself: picking the Teal
    // preset would shift every chromatic token off the values in tokens.css.
    for (const specs of [LIGHT_TOKENS, DARK_TOKENS]) {
      expect(specs.find(([n]) => n === "primary")?.[3]).toBe(DEFAULT_ACCENT_HUE);
    }
  });
});

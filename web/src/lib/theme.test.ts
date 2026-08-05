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

/**
 * `getAgentAvatarStyle` picks a monogram color from the fill's own lightness,
 * flipping at 0.56 L. The chart tokens are tuned for plotting, so a future tweak
 * that parks one near the flip point silently drops its avatar toward the 4.27:1
 * floor — and nothing on screen says so. This recomputes the pairing from the
 * real token values.
 */
const AVATAR_FLIP_L = 0.56;

function toSrgb([L, C, H]: [number, number, number]): [number, number, number] {
  const h = (H * Math.PI) / 180;
  const a = C * Math.cos(h);
  const b = C * Math.sin(h);
  const [l, m, s] = [
    (L + 0.3963377774 * a + 0.2158037573 * b) ** 3,
    (L - 0.1055613458 * a - 0.0638541728 * b) ** 3,
    (L - 0.0894841775 * a - 1.291485548 * b) ** 3,
  ];
  return [
    4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s,
    -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s,
    -0.0041960863 * l - 0.7034186147 * m + 1.707614701 * s,
  ].map((v) => (v <= 0.0031308 ? 12.92 * v : 1.055 * Math.max(v, 0) ** (1 / 2.4) - 0.055)) as [
    number,
    number,
    number,
  ];
}

function contrast(a: [number, number, number], b: [number, number, number]): number {
  const lum = (c: [number, number, number]) => {
    const [r, g, bl] = c.map((v) => {
      const x = Math.min(1, Math.max(0, v));
      return x <= 0.03928 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4;
    });
    return 0.2126 * r + 0.7152 * g + 0.0722 * bl;
  };
  const [la, lb] = [lum(a), lum(b)];
  return (Math.max(la, lb) + 0.05) / (Math.min(la, lb) + 0.05);
}

describe.each([
  ["light", blocks.light],
  ["dark", blocks.dark],
] as const)("%s avatar monograms stay legible", (_theme, declared) => {
  it.each(["chart-1", "chart-2", "chart-3", "chart-4", "chart-5"])("%s", (name) => {
    const fill = declared.get(name);
    expect(fill, `--${name} missing`).toBeDefined();
    const label: [number, number, number] = [fill![0] < AVATAR_FLIP_L ? 0.99 : 0.16, 0, 0];
    const ratio = contrast(toSrgb(fill!), toSrgb(label));
    expect(ratio, `monogram on --${name} is ${ratio.toFixed(2)}:1`).toBeGreaterThanOrEqual(4.5);
  });
});

/**
 * The status tokens exist to be *read* — an icon, a badge label, a run status in
 * 12px mono. That is a number, and nothing on screen reports it: a token 0.02L
 * too bright still looks like the right color and still fails AA. `RunsTimeline`
 * reached 2.19:1 by reaching for `chart-4` instead, which looked entirely
 * plausible in review.
 *
 * Measured against `--muted`, the darkest neutral surface in the theme, so every
 * lighter surface it renders on inherits the margin.
 */
describe.each([
  ["light", blocks.light],
  ["dark", blocks.dark],
] as const)("%s status tokens stay readable as text", (_theme, declared) => {
  const surface = declared.get("muted");

  // `--destructive` is absent because it is the one status token that is also a
  // solid fill, and CossUI hardcodes white on it. It answers to the fill gate,
  // not this one; `--destructive-foreground` is its text half.
  it.each([
    "success",
    "success-foreground",
    "warning",
    "warning-foreground",
    "info",
    "info-foreground",
    "destructive-foreground",
  ])("--%s", (name) => {
    const color = declared.get(name);
    expect(color, `--${name} missing from tokens.css`).toBeDefined();
    const ratio = contrast(toSrgb(color!), toSrgb(surface!));
    expect(ratio, `--${name} as text on --muted is ${ratio.toFixed(2)}:1`).toBeGreaterThanOrEqual(
      4.5,
    );
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

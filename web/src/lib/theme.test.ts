import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

import { getAgentAvatarStyle, getAgentColor } from "./agent-colors";
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

/**
 * `getAgentAvatarStyle` picks a monogram color from the fill's own lightness via
 * relative color syntax, which jsdom cannot evaluate. So read the rule the
 * production string actually encodes and replay *that* against the real token
 * values, rather than restating the rule here — a copy of the formula in the
 * test would keep passing after the formula was deleted.
 *
 * The chart tokens are tuned for plotting, so a future tweak that parks one near
 * the flip point silently drops its avatar toward the 4.27:1 floor, and nothing
 * on screen says so.
 */
const AVATAR_RULE =
  /^oklch\(from (var\(--chart-\d\)) clamp\(([\d.]+), \(([\d.]+) - l\) \* (\d+), ([\d.]+)\) 0 0\)$/;

function avatarRule(index: number) {
  const style = getAgentAvatarStyle("agent", index);
  const parsed = AVATAR_RULE.exec(String(style.color ?? ""));
  expect(parsed, `avatar monogram rule is gone or reshaped: ${String(style.color)}`).not.toBeNull();
  const [, fillRef, low, flip, gain, high] = parsed!;
  expect(style.background, "monogram is derived from a fill it is not painted on").toBe(fillRef);
  expect(fillRef).toBe(getAgentColor("agent", index));
  return { token: fillRef.slice(6, -1), low: +low, flip: +flip, gain: +gain, high: +high };
}

describe.each([
  ["light", blocks.light],
  ["dark", blocks.dark],
] as const)("%s avatar monograms stay legible", (_theme, declared) => {
  it.each([0, 1, 2, 3, 4])("chart slot %i", (index) => {
    const { token, low, flip, gain, high } = avatarRule(index);
    const fill = declared.get(token);
    expect(fill, `--${token} missing`).toBeDefined();

    // The clamp is a branch written as arithmetic: the gain has to be steep
    // enough that every real token saturates to one end, or a fill sitting near
    // the flip point gets a mid-gray monogram nobody sized for.
    const saturates = Math.abs(flip - fill![0]) * gain;
    expect(saturates, `--${token} lands inside the clamp ramp`).toBeGreaterThanOrEqual(high - low);

    const label: [number, number, number] = [fill![0] < flip ? high : low, 0, 0];
    const ratio = contrast(toSrgb(fill!), toSrgb(label));
    expect(ratio, `monogram on --${token} is ${ratio.toFixed(2)}:1`).toBeGreaterThanOrEqual(4.5);
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

  // `--destructive` is absent because it is a fill, not a word. It answers to
  // the fill gates below; `--destructive-foreground` is its text half.
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

/**
 * Passing on `--muted` is not passing on the pill.
 *
 * Every status pill in this app is `bg-X/10 text-X-foreground` (CossUI's own
 * badges use `/8`), and Tailwind renders that opacity as a real composite over
 * whatever surface is behind it. `--success/10` over `--muted` lands *below*
 * `--muted`, so the bare-token check above says nothing about the surface the
 * text is actually on — which is how `destructive/10` shipped at 4.08:1 under a
 * green suite.
 *
 * `bg-X/10` compiles to `color-mix(in oklab, var(--X) 10%, transparent)`, which
 * is the token at 10% alpha; the browser then composites that over the backdrop
 * in sRGB. So blend the encoded channels, not the linear ones. Spot-checked
 * against pixels read out of Chrome: within 0.05 of the rendered ratio.
 */
function over(
  tint: [number, number, number],
  surface: [number, number, number],
  alpha: number,
): [number, number, number] {
  const [s, t] = [toSrgb(surface), toSrgb(tint)];
  return t.map((v, i) => v * alpha + s[i] * (1 - alpha)) as [number, number, number];
}

describe.each([
  ["light", blocks.light],
  ["dark", blocks.dark],
] as const)("%s status pills stay readable on their own tint", (_theme, declared) => {
  // Every surface a pill renders on. `--muted` is the darkest in light mode and
  // therefore binding there; in dark mode the tint lightens whatever is behind
  // it, so all three are checked rather than reasoned about.
  const surfaces = ["background", "card", "muted"] as const;
  const washes = [0.08, 0.1] as const;

  it.each(["success", "warning", "info", "destructive"])("--%s tint", (name) => {
    const tint = declared.get(name);
    const text = declared.get(`${name}-foreground`);
    expect(tint, `--${name} missing`).toBeDefined();
    expect(text, `--${name}-foreground missing`).toBeDefined();

    for (const surface of surfaces) {
      for (const alpha of washes) {
        const ratio = contrast(toSrgb(text!), over(tint!, declared.get(surface)!, alpha));
        expect(
          ratio,
          `--${name}-foreground on ${name}/${alpha * 100} over --${surface} is ${ratio.toFixed(2)}:1`,
        ).toBeGreaterThanOrEqual(4.5);
      }
    }
  });
});

/**
 * `--destructive` is the one status color that is also a solid fill, and CossUI
 * hardcodes `text-white` on it (button.tsx, badge.tsx). That pins it from both
 * sides, and the two sides disagree in dark mode: text wants a light value on a
 * dark canvas, a white label wants a dark one. Tuning it as though it were text
 * is how dark mode reached 3.70:1 on the app's most dangerous button.
 */
describe.each([
  ["light", blocks.light],
  ["dark", blocks.dark],
] as const)("%s destructive works as a fill", (_theme, declared) => {
  const fill = declared.get("destructive")!;
  const canvas = declared.get("background")!;
  const white: [number, number, number] = [1, 0, 0];

  it("carries the white label CossUI hardcodes on it (1.4.3)", () => {
    const ratio = contrast(toSrgb(fill), toSrgb(white));
    expect(ratio, `white on --destructive is ${ratio.toFixed(2)}:1`).toBeGreaterThanOrEqual(4.5);
  });

  it("stays visible as a shape against the canvas (1.4.11)", () => {
    // Dots, bars and borders use it with no text at all, so the fill itself has
    // to clear the non-text threshold. This is the gate that darkening breaks.
    const ratio = contrast(toSrgb(fill), toSrgb(canvas));
    expect(ratio, `--destructive on --background is ${ratio.toFixed(2)}:1`).toBeGreaterThanOrEqual(
      3,
    );
  });
});

/**
 * `--primary` answers to three gates at once and they pull against each other:
 * brightening it to help the white label costs it its separation from the page.
 * tokens.css states the numbers in prose; this is where they are true.
 */
describe.each([
  ["light", blocks.light],
  ["dark", blocks.dark],
] as const)("%s primary survives its own three gates", (_theme, declared) => {
  const fill = declared.get("primary")!;
  const label = declared.get("primary-foreground")!;
  const canvas = declared.get("background")!;

  it("carries its own label (1.4.3)", () => {
    const ratio = contrast(toSrgb(fill), toSrgb(label));
    expect(
      ratio,
      `--primary-foreground on --primary is ${ratio.toFixed(2)}:1`,
    ).toBeGreaterThanOrEqual(4.5);
  });

  it("separates from the canvas as a shape (1.4.11)", () => {
    const ratio = contrast(toSrgb(fill), toSrgb(canvas));
    expect(ratio, `--primary on --background is ${ratio.toFixed(2)}:1`).toBeGreaterThanOrEqual(3);
  });

  it("reads as a link or an active label (1.4.3)", () => {
    // `text-primary` is all over the app — selected nav, links, the review pill.
    const ratio = contrast(toSrgb(fill), toSrgb(declared.get("muted")!));
    expect(ratio, `--primary as text on --muted is ${ratio.toFixed(2)}:1`).toBeGreaterThanOrEqual(
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

  it("keeps tokens.css itself symmetrical between the modes", () => {
    // The check above compares two hand-written mirrors to each other, which
    // stays green if tokens.css grows a token in only one block. Compare what
    // the stylesheet actually declares: a `.dark`-only token renders as the
    // light value in dark mode, and a `:root`-only one is simply missing there.
    expect([...blocks.dark.keys()].sort()).toEqual([...blocks.light.keys()].sort());
  });

  it("anchors the rotation on the shipped primary hue", () => {
    // A mismatch makes the shipped theme a rotation of itself: picking the Teal
    // preset would shift every chromatic token off the values in tokens.css.
    for (const specs of [LIGHT_TOKENS, DARK_TOKENS]) {
      expect(specs.find(([n]) => n === "primary")?.[3]).toBe(DEFAULT_ACCENT_HUE);
    }
  });
});

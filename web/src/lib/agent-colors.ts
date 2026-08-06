import type { CSSProperties } from "react";

// Agent identity colors come from the theme's categorical chart tokens so
// avatars follow theme swaps. `chart-*` is the right family: it means "this is a
// category", which is exactly what an agent is. Verdict colors (`success`,
// `warning`, `info`, `destructive`) are a different vocabulary and must not be
// borrowed for identity, or a green avatar starts reading as a passing run.
const AGENT_COLOR_TOKENS = [
  "var(--chart-1)",
  "var(--chart-2)",
  "var(--chart-3)",
  "var(--chart-4)",
  "var(--chart-5)",
];

export function getAgentColorIndex(id: string): number {
  let h = 0;
  for (let i = 0; i < id.length; i++) {
    h = (h * 31 + id.charCodeAt(i)) & 0xffffff;
  }
  return h;
}

export function getAgentColor(id: string, index?: number): string {
  const idx = typeof index === "number" ? index : getAgentColorIndex(id);
  return AGENT_COLOR_TOKENS[idx % AGENT_COLOR_TOKENS.length];
}

/**
 * Fill plus a monogram color that survives it.
 *
 * The chart tokens span a wide lightness range because they are tuned to be
 * plotted, not written on: in light mode they run 0.5–0.74 L, so a single fixed
 * label color cannot work for all five. A white monogram — which is what every
 * avatar shipped — measures 5.21:1 on `chart-1` and 2.29:1 on `chart-4`.
 *
 * Rather than hand-maintain a per-token, per-mode table, derive the label from
 * the fill's own lightness: white below 0.56 L, near-black above. Every current
 * token lands at 4.69:1 or better under that rule, in both modes, and it keeps
 * holding if a chart token is retuned. Relative color syntax needs Chrome 119 /
 * Safari 16.4 / Firefox 128; where it is missing the declaration is dropped and
 * the call site's own text class applies, which is today's behavior.
 */
export function getAgentAvatarStyle(id: string, index?: number): CSSProperties {
  const fill = getAgentColor(id, index);
  return {
    background: fill,
    color: `oklch(from ${fill} clamp(0.16, (0.56 - l) * 1000, 0.99) 0 0)`,
  };
}

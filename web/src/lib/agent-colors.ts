// Agent identity colors come from the theme's categorical chart tokens so
// avatars follow theme swaps.
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

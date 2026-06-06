export interface AgentColor {
  bg: string;
  border: string;
}

export const AGENT_AVATAR_COLORS: AgentColor[] = [
  { bg: "linear-gradient(145deg, #111, #3d3d42)", border: "var(--border)" },
  { bg: "linear-gradient(145deg, #005fb8, #2997ff)", border: "#005fb8" },
  { bg: "linear-gradient(145deg, #2d6a4f, #52b788)", border: "#2d6a4f" },
  { bg: "linear-gradient(145deg, #7b2d8e, #b06ef5)", border: "#7b2d8e" },
  { bg: "linear-gradient(145deg, #b8860b, #e8b84b)", border: "#b8860b" },
  { bg: "linear-gradient(145deg, #b02020, #e05050)", border: "#b02020" },
  { bg: "linear-gradient(145deg, #1a8a8a, #3bc9db)", border: "#1a8a8a" },
];

export function getAgentColorIndex(id: string): number {
  let h = 0;
  for (let i = 0; i < id.length; i++) {
    h = (h * 31 + id.charCodeAt(i)) & 0xffffff;
  }
  return h;
}

export function getAgentColor(id: string, index?: number): AgentColor {
  const idx = typeof index === "number" ? index : getAgentColorIndex(id);
  return AGENT_AVATAR_COLORS[idx % AGENT_AVATAR_COLORS.length];
}

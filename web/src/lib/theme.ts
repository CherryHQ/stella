export type ThemeAppearance = "system" | "light" | "dark";

export interface ThemeSettings {
  appearance: ThemeAppearance;
  /** OKLch hue (0–360) for the accent family. Undefined = default teal theme. */
  accentHue?: number;
}

export const THEME_STORAGE_KEY = "stella-theme";

/** Hue of the shipped teal accent (`--primary`). A chosen hue rotates the whole
 *  chromatic family by its delta from this base. */
export const DEFAULT_ACCENT_HUE = 190;

/** The default before the accent moved to 190. Anyone who had explicitly picked
 *  the Teal preset stored this hue, and it would now read as a -16 rotation —
 *  pinning them to the old teal, whose light-mode button label sits at 4.2:1.
 *  Treat it as "no override" once so the fix reaches them too. */
const LEGACY_TEAL_HUE = 174;

export const DEFAULT_THEME: ThemeSettings = {
  appearance: "system",
};

/** Preset accent hues offered as quick swatches. */
export const ACCENT_PRESETS: { name: string; hue: number }[] = [
  { name: "Teal", hue: 190 },
  { name: "Blue", hue: 245 },
  { name: "Indigo", hue: 275 },
  { name: "Violet", hue: 305 },
  { name: "Rose", hue: 0 },
  { name: "Orange", hue: 55 },
  { name: "Green", hue: 145 },
];

export function getStoredTheme(): ThemeSettings {
  if (typeof window === "undefined") return DEFAULT_THEME;

  const stored = window.localStorage.getItem(THEME_STORAGE_KEY);
  if (stored === "light" || stored === "dark" || stored === "system") {
    return { appearance: stored };
  }

  if (!stored) return DEFAULT_THEME;

  try {
    const parsed = JSON.parse(stored) as Partial<ThemeSettings>;
    const appearance = isAppearance(parsed.appearance)
      ? parsed.appearance
      : DEFAULT_THEME.appearance;
    const hue =
      typeof parsed.accentHue === "number" && Number.isFinite(parsed.accentHue)
        ? normalizeHue(parsed.accentHue)
        : undefined;
    const accentHue = hue === LEGACY_TEAL_HUE ? undefined : hue;
    return { appearance, accentHue };
  } catch {
    return DEFAULT_THEME;
  }
}

export function applyTheme(settings: ThemeSettings) {
  const dark =
    settings.appearance === "dark" ||
    (settings.appearance === "system" && window.matchMedia("(prefers-color-scheme: dark)").matches);
  document.documentElement.classList.toggle("dark", dark);
  applyAccent(settings.accentHue, dark);
}

export function setStoredTheme(settings: ThemeSettings) {
  window.localStorage.setItem(THEME_STORAGE_KEY, JSON.stringify(settings));
  applyTheme(settings);
}

function isAppearance(value: unknown): value is ThemeAppearance {
  return value === "system" || value === "light" || value === "dark";
}

function normalizeHue(hue: number): number {
  return ((hue % 360) + 360) % 360;
}

// --- Accent rotation ---------------------------------------------------------
//
// Each chromatic token in tokens.css is mirrored here as [name, L, C, H]. When
// the user picks a hue, every token rotates by (hue - DEFAULT_ACCENT_HUE),
// preserving the tuned lightness/chroma and the relative hue offsets between
// tokens (e.g. neutrals sit ~26° off the accent). Default hue applies nothing,
// so tokens.css stays the single source of truth for the shipped look.
//
// Status colors (chart-2..5, destructive) and the pure-white card/popover are
// intentionally absent — they must not rotate with the brand accent.
//
// These are a mirror of tokens.css, and a comment cannot keep a mirror honest —
// theme.test.ts parses tokens.css and fails on any drift.

export type TokenSpec = [name: string, l: number, c: number, h: number];

export const LIGHT_TOKENS: TokenSpec[] = [
  ["background", 0.975, 0.008, 200],
  ["foreground", 0.2, 0.022, 220],
  ["card-foreground", 0.2, 0.022, 220],
  ["popover-foreground", 0.2, 0.022, 220],
  ["primary", 0.5, 0.12, 190],
  ["primary-foreground", 0.99, 0.01, 190],
  ["secondary", 0.95, 0.012, 198],
  ["secondary-foreground", 0.2, 0.022, 220],
  ["muted", 0.95, 0.012, 198],
  ["muted-foreground", 0.48, 0.025, 205],
  ["accent", 0.93, 0.045, 190],
  ["accent-foreground", 0.4, 0.09, 190],
  ["border", 0.9, 0.012, 200],
  ["input", 0.9, 0.012, 200],
  ["ring", 0.5, 0.12, 190],
  ["chart-1", 0.5, 0.12, 190],
  ["sidebar", 0.965, 0.01, 198],
  ["sidebar-foreground", 0.2, 0.022, 220],
  ["sidebar-primary", 0.5, 0.12, 190],
  ["sidebar-primary-foreground", 0.99, 0.01, 190],
  ["sidebar-accent", 0.93, 0.045, 190],
  ["sidebar-accent-foreground", 0.4, 0.09, 190],
  ["sidebar-border", 0.9, 0.012, 200],
  ["sidebar-ring", 0.5, 0.12, 190],
];

export const DARK_TOKENS: TokenSpec[] = [
  ["background", 0.18, 0.014, 215],
  ["foreground", 0.95, 0.006, 200],
  ["card-foreground", 0.95, 0.006, 200],
  ["popover-foreground", 0.95, 0.006, 200],
  ["primary", 0.72, 0.13, 190],
  ["primary-foreground", 0.18, 0.04, 200],
  ["secondary", 0.24, 0.018, 215],
  ["secondary-foreground", 0.95, 0.006, 200],
  ["muted", 0.24, 0.018, 215],
  ["muted-foreground", 0.7, 0.018, 200],
  ["accent", 0.3, 0.055, 190],
  ["accent-foreground", 0.82, 0.12, 190],
  ["border", 0.28, 0.016, 210],
  ["input", 0.28, 0.016, 210],
  ["ring", 0.72, 0.13, 190],
  ["chart-1", 0.72, 0.13, 190],
  ["sidebar", 0.2, 0.016, 215],
  ["sidebar-foreground", 0.95, 0.006, 200],
  ["sidebar-primary", 0.72, 0.13, 190],
  ["sidebar-primary-foreground", 0.18, 0.04, 200],
  ["sidebar-accent", 0.3, 0.055, 190],
  ["sidebar-accent-foreground", 0.82, 0.12, 190],
  ["sidebar-border", 0.28, 0.016, 210],
  ["sidebar-ring", 0.72, 0.13, 190],
];

const ALL_TOKEN_NAMES = LIGHT_TOKENS.map(([name]) => name);

function applyAccent(accentHue: number | undefined, dark: boolean) {
  const root = document.documentElement.style;

  // Default (or unset) hue: clear overrides, let tokens.css drive everything.
  if (accentHue === undefined || normalizeHue(accentHue) === DEFAULT_ACCENT_HUE) {
    for (const name of ALL_TOKEN_NAMES) root.removeProperty(`--${name}`);
    return;
  }

  const delta = normalizeHue(accentHue) - DEFAULT_ACCENT_HUE;
  const tokens = dark ? DARK_TOKENS : LIGHT_TOKENS;
  for (const [name, l, c, h] of tokens) {
    root.setProperty(`--${name}`, `oklch(${l} ${c} ${normalizeHue(h + delta)})`);
  }
}

/** A single swatch color (the primary at the given hue) for picker UI. */
export function accentSwatch(hue: number, dark = false): string {
  const [, l, c] = (dark ? DARK_TOKENS : LIGHT_TOKENS).find(([n]) => n === "primary")!;
  return `oklch(${l} ${c} ${normalizeHue(hue)})`;
}

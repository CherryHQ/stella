export type ThemeAppearance = "system" | "light" | "dark";
export type ThemePreset = "default" | "blue" | "green" | "rose" | "orange" | "violet";

export interface ThemeSettings {
  appearance: ThemeAppearance;
  preset: ThemePreset;
}

export const THEME_STORAGE_KEY = "stella-theme";
export const THEME_PRESETS: ThemePreset[] = [
  "default",
  "blue",
  "green",
  "rose",
  "orange",
  "violet",
];

export const DEFAULT_THEME: ThemeSettings = {
  appearance: "system",
  preset: "default",
};

export function getStoredTheme(): ThemeSettings {
  if (typeof window === "undefined") return DEFAULT_THEME;

  const stored = window.localStorage.getItem(THEME_STORAGE_KEY);
  if (stored === "light" || stored === "dark" || stored === "system") {
    return { ...DEFAULT_THEME, appearance: stored };
  }

  if (!stored) return DEFAULT_THEME;

  try {
    const parsed = JSON.parse(stored) as Partial<ThemeSettings>;
    const appearance = isAppearance(parsed.appearance)
      ? parsed.appearance
      : DEFAULT_THEME.appearance;
    const preset = isPreset(parsed.preset) ? parsed.preset : DEFAULT_THEME.preset;

    return { appearance, preset };
  } catch {
    return DEFAULT_THEME;
  }
}

export function applyTheme(settings: ThemeSettings) {
  const dark =
    settings.appearance === "dark" ||
    (settings.appearance === "system" && window.matchMedia("(prefers-color-scheme: dark)").matches);
  const root = document.documentElement;

  root.classList.toggle("dark", dark);
  for (const preset of THEME_PRESETS) {
    root.classList.toggle(`theme-${preset}`, preset === settings.preset);
  }
}

export function setStoredTheme(settings: ThemeSettings) {
  window.localStorage.setItem(THEME_STORAGE_KEY, JSON.stringify(settings));
  applyTheme(settings);
}

function isAppearance(value: unknown): value is ThemeAppearance {
  return value === "system" || value === "light" || value === "dark";
}

function isPreset(value: unknown): value is ThemePreset {
  return typeof value === "string" && THEME_PRESETS.includes(value as ThemePreset);
}

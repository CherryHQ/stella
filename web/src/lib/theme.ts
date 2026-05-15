export type ThemeAppearance = "system" | "light" | "dark";

export interface ThemeSettings {
  appearance: ThemeAppearance;
}

export const THEME_STORAGE_KEY = "stella-theme";

export const DEFAULT_THEME: ThemeSettings = {
  appearance: "system",
};

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
    return { appearance };
  } catch {
    return DEFAULT_THEME;
  }
}

export function applyTheme(settings: ThemeSettings) {
  const dark =
    settings.appearance === "dark" ||
    (settings.appearance === "system" && window.matchMedia("(prefers-color-scheme: dark)").matches);
  document.documentElement.classList.toggle("dark", dark);
}

export function setStoredTheme(settings: ThemeSettings) {
  window.localStorage.setItem(THEME_STORAGE_KEY, JSON.stringify(settings));
  applyTheme(settings);
}

function isAppearance(value: unknown): value is ThemeAppearance {
  return value === "system" || value === "light" || value === "dark";
}

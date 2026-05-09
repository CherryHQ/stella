export type Locale = "en" | "zh";

export const DEFAULT_LOCALE: Locale = "en";
export const SUPPORTED_LOCALES: readonly Locale[] = ["en", "zh"];
export const LOCALE_STORAGE_KEY = "stella-locale";

export function normalizeLocale(s: string): Locale {
  if (s.startsWith("zh")) return "zh";
  return "en";
}

export function detectLocale(): Locale {
  return normalizeLocale(navigator.language);
}

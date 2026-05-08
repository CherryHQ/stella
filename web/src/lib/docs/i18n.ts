export const i18n = {
  defaultLanguage: "en" as const,
  languages: ["en", "zh"] as const,
};

export type Lang = (typeof i18n)["languages"][number];

import i18n from "i18next";
import { initReactI18next } from "react-i18next";
import { messages } from "./messages";
import { DEFAULT_LOCALE, SUPPORTED_LOCALES, LOCALE_STORAGE_KEY, detectLocale, type Locale } from "./locales";

function getInitialLocale(): Locale {
  const stored = localStorage.getItem(LOCALE_STORAGE_KEY);
  if (stored && (SUPPORTED_LOCALES as readonly string[]).includes(stored)) return stored as Locale;
  return detectLocale();
}

i18n.use(initReactI18next).init({
  resources: {
    en: { translation: messages.en },
    zh: { translation: messages.zh },
  },
  lng: getInitialLocale(),
  fallbackLng: DEFAULT_LOCALE,
  interpolation: { escapeValue: false },
});

export default i18n;

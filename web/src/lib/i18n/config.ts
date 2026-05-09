import i18n from "i18next";
import { initReactI18next } from "react-i18next";
import { messages } from "./messages";
import { DEFAULT_LOCALE, SUPPORTED_LOCALES, detectLocale, type Locale } from "./locales";

const STORAGE_KEY = "stella-locale";

function getInitialLocale(): Locale {
  const stored = localStorage.getItem(STORAGE_KEY);
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

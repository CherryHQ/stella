import { createContext, useContext, useState, useEffect, type ReactNode } from "react";
import { type Locale, DEFAULT_LOCALE, SUPPORTED_LOCALES, detectLocale } from "./locales";
import { messages, type MessageKey } from "./messages";

const STORAGE_KEY = "stella-locale";

type Params = Record<string, string | number>;

interface I18nContextValue {
  locale: Locale;
  setLocale: (locale: Locale) => void;
  t: (key: MessageKey, params?: Params) => string;
}

const I18nContext = createContext<I18nContextValue | null>(null);

export function I18nProvider({ children }: { children: ReactNode }) {
  const [locale, setLocaleState] = useState<Locale>(() => {
    const stored = localStorage.getItem(STORAGE_KEY);
    if (stored && (SUPPORTED_LOCALES as readonly string[]).includes(stored)) {
      return stored as Locale;
    }
    return detectLocale();
  });

  useEffect(() => {
    document.documentElement.lang = locale;
  }, [locale]);

  function setLocale(next: Locale) {
    localStorage.setItem(STORAGE_KEY, next);
    setLocaleState(next);
  }

  function t(key: MessageKey, params?: Params): string {
    let msg: string = messages[locale][key] ?? messages[DEFAULT_LOCALE][key] ?? key;
    if (params) {
      msg = msg.replace(/\{\{(\w+)\}\}/g, (_, p) => String(params[p] ?? `{{${p}}}`));
    }
    return msg;
  }

  return <I18nContext.Provider value={{ locale, setLocale, t }}>{children}</I18nContext.Provider>;
}

export function useI18n(): I18nContextValue {
  const ctx = useContext(I18nContext);
  if (!ctx) throw new Error("useI18n must be used within I18nProvider");
  return ctx;
}

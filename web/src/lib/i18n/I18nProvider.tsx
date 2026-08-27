import { useEffect, type ReactNode } from "react";
import { I18nextProvider, useTranslation } from "react-i18next";
import i18n from "./config";
import { LOCALE_STORAGE_KEY, type Locale } from "./locales";

export function I18nProvider({ children }: { children: ReactNode }) {
  useEffect(() => {
    document.documentElement.lang = i18n.language;
    function handleLangChange(lng: string) {
      document.documentElement.lang = lng;
      localStorage.setItem(LOCALE_STORAGE_KEY, lng);
    }
    i18n.on("languageChanged", handleLangChange);
    return () => {
      i18n.off("languageChanged", handleLangChange);
    };
  }, []);

  return <I18nextProvider i18n={i18n}>{children}</I18nextProvider>;
}

export function useI18n() {
  const { t, i18n: instance } = useTranslation();
  return {
    t,
    // SAFETY: instance.language is one of the SUPPORTED_LOCALES values.
    locale: instance.language as Locale,
    setLocale: (l: Locale) => instance.changeLanguage(l),
  };
}

import { useI18n } from "@/lib/i18n";

export function RecallyPage() {
  const { t } = useI18n();

  return (
    <div className="flex flex-col h-full">
      <div className="flex items-center justify-center flex-1">
        <h1 className="text-lg font-medium text-muted-foreground">{t("nav.recally")}</h1>
      </div>
    </div>
  );
}

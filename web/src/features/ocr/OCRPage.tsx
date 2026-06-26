import { useCallback, useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { updateOcrSettings } from "@/lib/api-client/sdk.gen";
import { ocrSettingsQueryOptions } from "@/lib/queries/ocr";
import { useI18n } from "@/lib/i18n";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { SettingsPageHeader } from "@/features/settings/SettingsPageHeader";

type Toast = { message: string; type: "success" | "error" } | null;

function ToastAlert({ toast }: { toast: Toast }) {
  if (!toast) return null;
  return (
    <div
      className={`fixed bottom-4 right-4 z-50 w-auto max-w-sm rounded-xl border px-4 py-3 text-sm ${
        toast.type === "error"
          ? "border-destructive/20 bg-destructive/10 text-destructive-foreground"
          : "border-success/20 bg-success/10 text-success-foreground"
      }`}
    >
      {toast.message}
    </div>
  );
}

export function OCRPage() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { data: settings } = useQuery(ocrSettingsQueryOptions);

  const [enabled, setEnabled] = useState(false);
  const [toast, setToast] = useState<Toast>(null);

  useEffect(() => {
    if (!settings) return;
    setEnabled(settings.enabled);
  }, [settings]);

  const showToast = useCallback((message: string, type: "success" | "error" = "success") => {
    setToast({ message, type });
    setTimeout(() => setToast(null), 3000);
  }, []);

  const available = settings?.available ?? false;

  const save = useMutation({
    mutationFn: async () => {
      const { data } = await updateOcrSettings({ body: { enabled }, throwOnError: true });
      return data;
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["ocr-settings"] });
      showToast(t("ocr.saved"));
    },
    onError: (e) => showToast(e instanceof Error ? e.message : t("ocr.saveFailed"), "error"),
  });

  return (
    <div className="h-full overflow-y-auto bg-background">
      <div className="mx-auto max-w-3xl p-6 sm:p-8 lg:p-10 space-y-8">
        <SettingsPageHeader title={t("ocr.title")} description={t("ocr.description")} />

        <section className="rounded-xl border border-border bg-card p-6 space-y-6">
          <div className="flex items-start justify-between gap-4">
            <div className="space-y-1">
              <h2 className="text-sm font-semibold text-foreground">{t("ocr.enableTitle")}</h2>
              <p className="text-xs text-muted-foreground">{t("ocr.enableHint")}</p>
            </div>
            <Switch checked={enabled} onCheckedChange={setEnabled} />
          </div>

          {enabled && !available ? (
            <p className="rounded-lg border border-border bg-muted/40 px-4 py-3 text-xs text-muted-foreground">
              {t("ocr.unavailable")}
            </p>
          ) : null}

          <div className="flex justify-end pt-2">
            <Button size="sm" loading={save.isPending} onClick={() => save.mutate()}>
              {t("ocr.save")}
            </Button>
          </div>
        </section>
      </div>

      <ToastAlert toast={toast} />
    </div>
  );
}

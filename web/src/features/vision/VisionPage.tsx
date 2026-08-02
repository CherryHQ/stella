import { useCallback, useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { updateVisionSettings } from "@/lib/api-client/sdk.gen";
import { modelsQueryOptions } from "@/lib/queries/models";
import { visionSettingsQueryOptions } from "@/lib/queries/vision";
import { useI18n } from "@/lib/i18n";
import { Button } from "@/components/ui/button";
import { SettingsPageHeader } from "@/features/settings/SettingsPageHeader";

type Toast = { message: string; type: "success" | "error" } | null;

export function VisionPage() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { data: settings } = useQuery(visionSettingsQueryOptions);
  const { data: models } = useQuery(modelsQueryOptions);

  const [model, setModel] = useState("");
  const [toast, setToast] = useState<Toast>(null);

  // Re-seed the draft whenever the server snapshot changes (initial load, or
  // after a successful save invalidates the query).
  useEffect(() => {
    if (!settings) return;
    setModel(settings.model);
  }, [settings]);

  const showToast = useCallback((message: string, type: "success" | "error" = "success") => {
    setToast({ message, type });
    setTimeout(() => setToast(null), 3000);
  }, []);

  const save = useMutation({
    mutationFn: async () => {
      const { data } = await updateVisionSettings({ body: { model }, throwOnError: true });
      return data;
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["vision-settings"] });
      showToast(t("vision.saved"));
    },
    onError: (e) => showToast(e instanceof Error ? e.message : t("vision.saveFailed"), "error"),
  });

  const options = (models ?? []).map((m) => ({
    value: `${m.provider}/${m.model}`,
    label: `${m.provider_name || m.provider}/${m.model}`,
  }));
  // A model saved before its provider was removed (or renamed) would otherwise
  // vanish from the select and be silently cleared on the next save.
  const stale = model && !options.some((o) => o.value === model);

  return (
    <div className="h-full overflow-y-auto bg-background">
      <div className="mx-auto max-w-3xl p-6 sm:p-8 lg:p-10 space-y-8">
        <SettingsPageHeader title={t("vision.title")} description={t("vision.description")} />

        <section className="rounded-xl border border-border bg-card p-6 space-y-6">
          <div className="space-y-1">
            <h2 className="text-sm font-semibold text-foreground">{t("vision.modelTitle")}</h2>
            <p className="text-xs text-muted-foreground">{t("vision.modelHint")}</p>
          </div>

          <div className="space-y-1.5">
            <label
              htmlFor="vision-model"
              className="block text-xs font-medium text-muted-foreground"
            >
              {t("vision.model")}
            </label>
            <select
              id="vision-model"
              value={model}
              onChange={(e) => setModel(e.target.value)}
              className="w-full rounded-md border border-input bg-background px-3 py-2 text-sm"
            >
              <option value="">{t("vision.modelUnset")}</option>
              {stale && <option value={model}>{model}</option>}
              {options.map((o) => (
                <option key={o.value} value={o.value}>
                  {o.label}
                </option>
              ))}
            </select>
          </div>

          <div className="flex justify-end pt-2">
            <Button size="sm" loading={save.isPending} onClick={() => save.mutate()}>
              {t("vision.save")}
            </Button>
          </div>
        </section>
      </div>

      {toast && (
        <div
          className={`fixed bottom-4 right-4 z-50 w-auto max-w-sm rounded-xl border px-4 py-3 text-sm ${
            toast.type === "error"
              ? "border-destructive/20 bg-destructive/10 text-destructive-foreground"
              : "border-success/20 bg-success/10 text-success-foreground"
          }`}
        >
          {toast.message}
        </div>
      )}
    </div>
  );
}

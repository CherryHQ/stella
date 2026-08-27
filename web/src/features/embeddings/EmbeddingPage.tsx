import { useCallback, useEffect, useState } from "react";
import { Link } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { updateEmbeddingSettings } from "@/lib/api-client/sdk.gen";
import { embeddingSettingsQueryOptions } from "@/lib/queries/embedding";
import { defaultModelsQueryOptions } from "@/lib/queries/default-models";
import { useI18n } from "@/lib/i18n";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
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

export function EmbeddingPage() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { data: settings } = useQuery(embeddingSettingsQueryOptions);
  const { data: defaults } = useQuery(defaultModelsQueryOptions);

  const [enabled, setEnabled] = useState(false);
  const [dim, setDim] = useState("");
  const [normalize, setNormalize] = useState(false);
  const [toast, setToast] = useState<Toast>(null);

  // Re-seed the draft whenever the server snapshot changes (initial load or
  // after a successful save invalidates the query).
  useEffect(() => {
    if (!settings) return;
    setEnabled(settings.enabled);
    setDim(String(settings.dim));
    setNormalize(settings.normalize);
  }, [settings]);

  const showToast = useCallback((message: string, type: "success" | "error" = "success") => {
    setToast({ message, type });
    setTimeout(() => setToast(null), 3000);
  }, []);

  const save = useMutation({
    mutationFn: async () => {
      const { data } = await updateEmbeddingSettings({
        body: { enabled, dim: Number(dim) || 0, normalize },
        throwOnError: true,
      });
      return data;
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["embedding-settings"] });
      showToast(t("embedding.saved"));
    },
    onError: (e) => showToast(e instanceof Error ? e.message : t("embedding.saveFailed"), "error"),
  });

  const embeddingModel = defaults?.model_embedding ?? "";

  return (
    <div className="h-full overflow-y-auto bg-background">
      <div className="mx-auto max-w-3xl p-6 sm:p-8 lg:p-10 space-y-8">
        <SettingsPageHeader title={t("embedding.title")} description={t("embedding.description")} />

        <section className="rounded-xl border border-border bg-card p-6 space-y-6">
          {/* Enable toggle */}
          <div className="flex items-start justify-between gap-4">
            <div className="space-y-1">
              <h2 className="text-sm font-semibold text-foreground">
                {t("embedding.enableTitle")}
              </h2>
              <p className="text-xs text-muted-foreground">{t("embedding.enableHint")}</p>
            </div>
            <Switch checked={enabled} onCheckedChange={setEnabled} />
          </div>

          <div className="border-t border-border" />

          {/* The model and its credentials live with every other model role, so
              this page reports which one is in use and links to where it is set. */}
          <div className="space-y-1.5">
            <p className="text-xs font-medium text-muted-foreground">{t("embedding.model")}</p>
            <p className="text-sm font-mono text-foreground">
              {embeddingModel || t("embedding.modelUnset")}
            </p>
            <p className="text-xs text-muted-foreground">
              {t("embedding.modelHint")}{" "}
              <Link to="/admin/ai/models" className="underline underline-offset-2">
                {t("settings.nav.defaultModels")}
              </Link>
            </p>
          </div>

          <div className="space-y-1.5">
            <label
              htmlFor="embedding-dim"
              className="block text-xs font-medium text-muted-foreground"
            >
              {t("embedding.dim")}
            </label>
            <Input
              id="embedding-dim"
              type="number"
              value={dim}
              onChange={(e) => setDim(e.target.value)}
              placeholder="1536"
              min={0}
              nativeInput
            />
            <p className="text-xs text-muted-foreground">{t("embedding.dimHint")}</p>
          </div>

          {/* Normalize toggle */}
          <div className="flex items-start justify-between gap-4">
            <div className="space-y-1">
              <h2 className="text-sm font-semibold text-foreground">
                {t("embedding.normalizeTitle")}
              </h2>
              <p className="text-xs text-muted-foreground">{t("embedding.normalizeHint")}</p>
            </div>
            <Switch checked={normalize} onCheckedChange={setNormalize} />
          </div>

          <div className="flex justify-end pt-2">
            <Button size="sm" loading={save.isPending} onClick={() => save.mutate()}>
              {t("embedding.save")}
            </Button>
          </div>
        </section>
      </div>

      <ToastAlert toast={toast} />
    </div>
  );
}

import { useCallback, useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { updateEmbeddingSettings } from "@/lib/api-client/sdk.gen";
import type { EmbeddingSettingsUpdate } from "@/lib/api-client/types.gen";
import { embeddingSettingsQueryOptions } from "@/lib/queries/embedding";
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

  // Form draft. The api_key is write-only: blank means "keep the stored key",
  // so we never seed it from the server (which only reports has_api_key).
  const [enabled, setEnabled] = useState(false);
  const [model, setModel] = useState("");
  const [dim, setDim] = useState("");
  const [baseURL, setBaseURL] = useState("");
  const [normalize, setNormalize] = useState(false);
  const [apiKey, setApiKey] = useState("");
  const [toast, setToast] = useState<Toast>(null);

  // Re-seed the draft whenever the server snapshot changes (initial load or
  // after a successful save invalidates the query).
  useEffect(() => {
    if (!settings) return;
    setEnabled(settings.enabled);
    setModel(settings.model);
    setDim(String(settings.dim));
    setBaseURL(settings.base_url);
    setNormalize(settings.normalize);
    setApiKey("");
  }, [settings]);

  const showToast = useCallback((message: string, type: "success" | "error" = "success") => {
    setToast({ message, type });
    setTimeout(() => setToast(null), 3000);
  }, []);

  const hasStoredKey = settings?.has_api_key ?? false;

  const save = useMutation({
    mutationFn: async () => {
      const body: EmbeddingSettingsUpdate = {
        enabled,
        model: model.trim(),
        dim: Number(dim) || 0,
        base_url: baseURL.trim(),
        normalize,
      };
      // Only send a key when the operator typed a new one; a blank field keeps
      // the stored key untouched.
      if (apiKey.trim()) body.api_key = apiKey.trim();
      const { data } = await updateEmbeddingSettings({ body, throwOnError: true });
      return data;
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["embedding-settings"] });
      showToast(t("embedding.saved"));
    },
    onError: (e) => showToast(e instanceof Error ? e.message : t("embedding.saveFailed"), "error"),
  });

  const onSave = useCallback(() => {
    if (enabled && !hasStoredKey && !apiKey.trim()) {
      showToast(t("embedding.apiKeyRequired"), "error");
      return;
    }
    save.mutate();
  }, [enabled, hasStoredKey, apiKey, save, showToast, t]);

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

          {/* Provider credentials */}
          <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            <div className="space-y-1.5 md:col-span-2">
              <label className="block text-xs font-medium text-muted-foreground">
                {t("embedding.apiKey")}
              </label>
              <Input
                type="password"
                value={apiKey}
                onChange={(e) => setApiKey(e.target.value)}
                placeholder={hasStoredKey ? t("embedding.apiKeyStored") : "sk-..."}
                autoComplete="off"
                nativeInput
              />
              <p className="text-xs text-muted-foreground">{t("embedding.apiKeyHint")}</p>
            </div>

            <div className="space-y-1.5 md:col-span-2">
              <label className="block text-xs font-medium text-muted-foreground">
                {t("embedding.baseURL")}
              </label>
              <Input
                value={baseURL}
                onChange={(e) => setBaseURL(e.target.value)}
                placeholder="https://api.openai.com/v1"
                nativeInput
              />
              <p className="text-xs text-muted-foreground">{t("embedding.baseURLHint")}</p>
            </div>

            <div className="space-y-1.5">
              <label className="block text-xs font-medium text-muted-foreground">
                {t("embedding.model")}
              </label>
              <Input
                value={model}
                onChange={(e) => setModel(e.target.value)}
                placeholder="text-embedding-3-small"
                nativeInput
              />
            </div>

            <div className="space-y-1.5">
              <label className="block text-xs font-medium text-muted-foreground">
                {t("embedding.dim")}
              </label>
              <Input
                type="number"
                value={dim}
                onChange={(e) => setDim(e.target.value)}
                placeholder="1536"
                min={1}
                nativeInput
              />
              <p className="text-xs text-muted-foreground">{t("embedding.dimHint")}</p>
            </div>
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
            <Button size="sm" loading={save.isPending} onClick={onSave}>
              {t("embedding.save")}
            </Button>
          </div>
        </section>
      </div>

      <ToastAlert toast={toast} />
    </div>
  );
}

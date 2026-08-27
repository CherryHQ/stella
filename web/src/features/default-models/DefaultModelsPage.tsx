import { useCallback, useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { updateDefaultModels, updateEmbeddingSettings } from "@/lib/api-client/sdk.gen";
import type { DefaultModels } from "@/lib/api-client/types.gen";
import { defaultModelsQueryOptions } from "@/lib/queries/default-models";
import { embeddingSettingsQueryOptions } from "@/lib/queries/embedding";
import { modelsQueryOptions } from "@/lib/queries/models";
import { useI18n } from "@/lib/i18n";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { SettingsPageHeader } from "@/features/settings/SettingsPageHeader";

type Toast = { message: string; type: "success" | "error" } | null;

const EMPTY: DefaultModels = {
  model: "",
  model_thinking: "",
  model_strong: "",
  model_strong_thinking: "",
  model_fast: "",
  model_fast_thinking: "",
  model_vision: "",
  model_embedding: "",
};

const thinkingLevels = ["", "minimal", "low", "medium", "high", "xhigh"] as const;

type ModelOption = { value: string; label: string };

function ModelSelect({
  id,
  value,
  options,
  unsetLabel,
  onChange,
}: {
  id: string;
  value: string;
  options: ModelOption[];
  unsetLabel: string;
  onChange: (v: string) => void;
}) {
  // A model saved before its provider was removed (or renamed) would otherwise
  // vanish from the select and be silently cleared on the next save.
  const stale = value && !options.some((o) => o.value === value);
  return (
    <select
      id={id}
      value={value}
      onChange={(e) => onChange(e.target.value)}
      className="w-full rounded-md border border-input bg-background px-3 py-2 text-sm"
    >
      <option value="">{unsetLabel}</option>
      {stale && <option value={value}>{value}</option>}
      {options.map((o) => (
        <option key={o.value} value={o.value}>
          {o.label}
        </option>
      ))}
    </select>
  );
}

function ThinkingSelect({
  id,
  value,
  onChange,
}: {
  id: string;
  value: string;
  onChange: (v: string) => void;
}) {
  const { t } = useI18n();
  return (
    <select
      id={id}
      value={value}
      onChange={(e) => onChange(e.target.value)}
      className="w-full rounded-md border border-input bg-background px-3 py-2 text-sm"
    >
      {thinkingLevels.map((level) => (
        <option key={level || "default"} value={level}>
          {level || t("agents.form.thinkingDefault")}
        </option>
      ))}
    </select>
  );
}

function Field({
  label,
  htmlFor,
  hint,
  children,
}: {
  label: string;
  htmlFor: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="space-y-1.5">
      <label htmlFor={htmlFor} className="block text-xs font-medium text-muted-foreground">
        {label}
      </label>
      {children}
      {hint && <p className="text-xs text-muted-foreground">{hint}</p>}
    </div>
  );
}

export function DefaultModelsPage() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { data: settings } = useQuery(defaultModelsQueryOptions);
  const { data: embedding } = useQuery(embeddingSettingsQueryOptions);
  const { data: models } = useQuery(modelsQueryOptions);

  const [draft, setDraft] = useState<DefaultModels>(EMPTY);
  const [lane, setLane] = useState({ enabled: false, dim: "", normalize: false });
  const [toast, setToast] = useState<Toast>(null);

  // Re-seed the drafts whenever the server snapshots change (initial load, or
  // after a successful save invalidates the queries).
  useEffect(() => {
    if (!settings) return;
    setDraft(settings);
  }, [settings]);

  useEffect(() => {
    if (!embedding) return;
    setLane({
      enabled: embedding.enabled,
      dim: String(embedding.dim),
      normalize: embedding.normalize,
    });
  }, [embedding]);

  const set = (patch: Partial<DefaultModels>) => setDraft((prev) => ({ ...prev, ...patch }));

  const showToast = useCallback((message: string, type: "success" | "error" = "success") => {
    setToast({ message, type });
    setTimeout(() => setToast(null), 3000);
  }, []);

  const save = useMutation({
    mutationFn: async () => {
      // Order matters: enabling the embedding lane is refused unless the
      // embedding model already resolves to a provider with a key, so the model
      // write has to land first.
      await updateDefaultModels({ body: draft, throwOnError: true });
      await updateEmbeddingSettings({
        body: { enabled: lane.enabled, dim: Number(lane.dim) || 0, normalize: lane.normalize },
        throwOnError: true,
      });
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["default-models"] });
      void queryClient.invalidateQueries({ queryKey: ["embedding-settings"] });
      showToast(t("defaultModels.saved"));
    },
    onError: (e) =>
      showToast(e instanceof Error ? e.message : t("defaultModels.saveFailed"), "error"),
  });

  const options: ModelOption[] = (models ?? []).map((m) => ({
    value: `${m.provider}/${m.model}`,
    label: `${m.provider_name || m.provider}/${m.model}`,
  }));

  return (
    <div className="h-full overflow-y-auto bg-background">
      <div className="mx-auto max-w-3xl p-6 sm:p-8 lg:p-10 space-y-8">
        <SettingsPageHeader
          title={t("defaultModels.title")}
          description={t("defaultModels.description")}
        />

        <section className="rounded-xl border border-border bg-card p-6 space-y-6">
          <div className="space-y-1">
            <h2 className="text-sm font-semibold text-foreground">
              {t("defaultModels.agentTitle")}
            </h2>
            <p className="text-xs text-muted-foreground">{t("defaultModels.agentHint")}</p>
          </div>

          <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
            <Field label={t("agents.form.modelDefault")} htmlFor="default-model">
              <ModelSelect
                id="default-model"
                value={draft.model}
                options={options}
                unsetLabel={t("defaultModels.unset")}
                onChange={(v) => set({ model: v })}
              />
            </Field>
            <Field label={t("agents.form.modelThinking")} htmlFor="default-model-thinking">
              <ThinkingSelect
                id="default-model-thinking"
                value={draft.model_thinking}
                onChange={(v) => set({ model_thinking: v })}
              />
            </Field>
            <Field label={t("agents.form.modelStrong")} htmlFor="default-model-strong">
              <ModelSelect
                id="default-model-strong"
                value={draft.model_strong}
                options={options}
                unsetLabel={t("agents.form.modelFallback")}
                onChange={(v) => set({ model_strong: v })}
              />
            </Field>
            <Field
              label={t("agents.form.modelStrongThinking")}
              htmlFor="default-model-strong-thinking"
            >
              <ThinkingSelect
                id="default-model-strong-thinking"
                value={draft.model_strong_thinking}
                onChange={(v) => set({ model_strong_thinking: v })}
              />
            </Field>
            <Field label={t("agents.form.modelFast")} htmlFor="default-model-fast">
              <ModelSelect
                id="default-model-fast"
                value={draft.model_fast}
                options={options}
                unsetLabel={t("agents.form.modelFallback")}
                onChange={(v) => set({ model_fast: v })}
              />
            </Field>
            <Field label={t("agents.form.modelFastThinking")} htmlFor="default-model-fast-thinking">
              <ThinkingSelect
                id="default-model-fast-thinking"
                value={draft.model_fast_thinking}
                onChange={(v) => set({ model_fast_thinking: v })}
              />
            </Field>
          </div>
        </section>

        <section className="rounded-xl border border-border bg-card p-6 space-y-6">
          <div className="space-y-1">
            <h2 className="text-sm font-semibold text-foreground">
              {t("defaultModels.visionTitle")}
            </h2>
            <p className="text-xs text-muted-foreground">{t("defaultModels.auxiliaryHint")}</p>
          </div>

          <Field
            label={t("defaultModels.vision")}
            htmlFor="default-model-vision"
            hint={t("defaultModels.visionHint")}
          >
            <ModelSelect
              id="default-model-vision"
              value={draft.model_vision}
              options={options}
              unsetLabel={t("defaultModels.visionUnset")}
              onChange={(v) => set({ model_vision: v })}
            />
          </Field>
        </section>

        <section className="rounded-xl border border-border bg-card p-6 space-y-6">
          <div className="space-y-1">
            <h2 className="text-sm font-semibold text-foreground">
              {t("defaultModels.embeddingTitle")}
            </h2>
            <p className="text-xs text-muted-foreground">{t("embedding.description")}</p>
          </div>

          <Field
            label={t("defaultModels.embedding")}
            htmlFor="default-model-embedding"
            hint={t("defaultModels.embeddingHint")}
          >
            <ModelSelect
              id="default-model-embedding"
              value={draft.model_embedding}
              options={options}
              unsetLabel={t("defaultModels.embeddingUnset")}
              onChange={(v) => set({ model_embedding: v })}
            />
          </Field>

          <div className="flex items-start justify-between gap-4">
            <div className="space-y-1">
              <p className="text-sm font-medium text-foreground">{t("embedding.enableTitle")}</p>
              <p className="text-xs text-muted-foreground">{t("embedding.enableHint")}</p>
            </div>
            <Switch
              checked={lane.enabled}
              onCheckedChange={(checked) => setLane((prev) => ({ ...prev, enabled: checked }))}
            />
          </div>

          <Field label={t("embedding.dim")} htmlFor="embedding-dim" hint={t("embedding.dimHint")}>
            <Input
              id="embedding-dim"
              type="number"
              value={lane.dim}
              onChange={(e) => setLane((prev) => ({ ...prev, dim: e.target.value }))}
              placeholder="1536"
              min={0}
              nativeInput
            />
          </Field>

          <div className="flex items-start justify-between gap-4">
            <div className="space-y-1">
              <p className="text-sm font-medium text-foreground">{t("embedding.normalizeTitle")}</p>
              <p className="text-xs text-muted-foreground">{t("embedding.normalizeHint")}</p>
            </div>
            <Switch
              checked={lane.normalize}
              onCheckedChange={(checked) => setLane((prev) => ({ ...prev, normalize: checked }))}
            />
          </div>
        </section>

        <div className="flex justify-end">
          <Button size="sm" loading={save.isPending} onClick={() => save.mutate()}>
            {t("defaultModels.save")}
          </Button>
        </div>
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

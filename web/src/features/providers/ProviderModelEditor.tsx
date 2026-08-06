import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Badge } from "@/components/ui/badge";
import { useI18n } from "@/lib/i18n";
import type { CustomModelForm, ModelConfig, ProviderModel } from "@/lib/types";
import { createCustomModelForm, formFromModelConfig } from "./provider-helpers";

interface ProviderModelEditorProps {
  models: ProviderModel[];
  providerModels: Record<string, ModelConfig>;
  onToggleModel: (model: ProviderModel) => void;
  onAddCustomModel: (form: CustomModelForm) => void;
  onEditCustomModel: (modelID: string, form: CustomModelForm) => void;
  onRemoveCustomModel: (modelID: string) => void;
  onFetchModels: () => Promise<void>;
  showToast: (text: string, kind?: "success" | "error") => void;
}

export function ProviderModelEditor({
  models,
  providerModels,
  onToggleModel,
  onAddCustomModel,
  onEditCustomModel,
  onRemoveCustomModel,
  onFetchModels,
  showToast,
}: ProviderModelEditorProps) {
  const { t } = useI18n();
  const [showForm, setShowForm] = useState(false);
  const [form, setForm] = useState<CustomModelForm>(createCustomModelForm());
  const [fetching, setFetching] = useState(false);

  const handleSubmit = () => {
    const modelID = (form.id || "").trim();
    if (!modelID) {
      showToast(t("providers.modelIdRequired"), "error");
      return;
    }
    const finalForm = { ...form, id: modelID };
    if (form.original_id) {
      onEditCustomModel(form.original_id, finalForm);
    } else {
      onAddCustomModel(finalForm);
    }
    setForm(createCustomModelForm());
    setShowForm(false);
  };

  const handleEdit = (model: ProviderModel) => {
    const config = providerModels[model.id];
    setForm(formFromModelConfig(model.id, config));
    setShowForm(true);
  };

  const handleFetch = async () => {
    setFetching(true);
    try {
      await onFetchModels();
    } finally {
      setFetching(false);
    }
  };

  const updateField = <K extends keyof CustomModelForm>(field: K, value: CustomModelForm[K]) => {
    setForm((f) => ({ ...f, [field]: value }));
  };

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <span className="text-xs font-semibold text-muted-foreground">{t("providers.models")}</span>
        <Button
          onClick={() => {
            setForm(createCustomModelForm());
            setShowForm(true);
          }}
          variant="ghost"
          size="sm"
        >
          {t("providers.addCustomModel")}
        </Button>
      </div>

      {showForm && (
        <div className="rounded-lg border border-border bg-muted p-4 space-y-4">
          <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            <div>
              <label className="text-xs font-medium mb-1 block">{t("providers.modelId")}</label>
              <Input
                type="text"
                value={form.id}
                placeholder="llama3.1:8b"
                onChange={(e) =>
                  updateField("id", (e as React.ChangeEvent<HTMLInputElement>).target.value)
                }
                nativeInput
                className="font-mono"
              />
            </div>
            <div>
              <label className="text-xs font-medium mb-1 block">{t("providers.displayName")}</label>
              <Input
                type="text"
                value={form.name}
                placeholder={t("providers.modelPlaceholder")}
                onChange={(e) =>
                  updateField("name", (e as React.ChangeEvent<HTMLInputElement>).target.value)
                }
                nativeInput
              />
            </div>
            <div>
              <label className="text-xs font-medium mb-1 block">{t("providers.input")}</label>
              <Input
                type="text"
                value={form.input}
                placeholder="text, image"
                onChange={(e) =>
                  updateField("input", (e as React.ChangeEvent<HTMLInputElement>).target.value)
                }
                nativeInput
              />
            </div>
            <div>
              <label className="text-xs font-medium mb-1 block">{t("providers.output")}</label>
              <Input
                type="text"
                value={form.output}
                placeholder="text"
                onChange={(e) =>
                  updateField("output", (e as React.ChangeEvent<HTMLInputElement>).target.value)
                }
                nativeInput
              />
            </div>
            <div>
              <label className="text-xs font-medium mb-1 block">
                {t("providers.contextWindow")}
              </label>
              <Input
                type="number"
                min={0}
                value={form.context_window}
                placeholder="128000"
                onChange={(e) =>
                  updateField(
                    "context_window",
                    (e as React.ChangeEvent<HTMLInputElement>).target.value,
                  )
                }
                nativeInput
              />
            </div>
            <div>
              <label className="text-xs font-medium mb-1 block">{t("providers.maxTokens")}</label>
              <Input
                type="number"
                min={0}
                value={form.max_tokens}
                placeholder="32000"
                onChange={(e) =>
                  updateField("max_tokens", (e as React.ChangeEvent<HTMLInputElement>).target.value)
                }
                nativeInput
              />
            </div>
          </div>

          <div className="grid grid-cols-1 md:grid-cols-2 gap-4 items-start">
            <div className="flex items-center gap-3 rounded-lg border border-border px-4 py-3">
              <Switch
                checked={form.enabled}
                onCheckedChange={(checked) => updateField("enabled", checked)}
              />
              <div>
                <p className="text-sm">{t("providers.enabled")}</p>
                <p className="text-xs text-muted-foreground">{t("providers.enabledDesc")}</p>
              </div>
            </div>
            <div className="flex items-center gap-3 rounded-lg border border-border px-4 py-3">
              <Switch
                checked={form.reasoning}
                onCheckedChange={(checked) => updateField("reasoning", checked)}
              />
              <div>
                <p className="text-sm">{t("providers.reasoning")}</p>
                <p className="text-xs text-muted-foreground">{t("providers.reasoningDesc")}</p>
              </div>
            </div>
          </div>

          <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-4 gap-4">
            {(
              [
                { label: t("providers.costInput"), field: "cost_input" as const },
                { label: t("providers.costOutput"), field: "cost_output" as const },
                { label: t("providers.costCacheRead"), field: "cost_cache_read" as const },
                { label: t("providers.costCacheWrite"), field: "cost_cache_write" as const },
              ] as const
            ).map(({ label, field }) => (
              <div key={field}>
                <label className="text-xs font-medium mb-1 block">{label}</label>
                <Input
                  type="number"
                  step="any"
                  value={form[field]}
                  placeholder="0"
                  onChange={(e) =>
                    updateField(field, (e as React.ChangeEvent<HTMLInputElement>).target.value)
                  }
                  nativeInput
                />
              </div>
            ))}
          </div>

          <div className="flex items-center gap-3 justify-end">
            <Button
              onClick={() => {
                setForm(createCustomModelForm());
                setShowForm(false);
              }}
              variant="ghost"
              size="sm"
            >
              {t("common.cancel")}
            </Button>
            <Button onClick={handleSubmit} variant="default" size="sm">
              {form.original_id ? t("providers.updateModel") : t("providers.addModel")}
            </Button>
          </div>
        </div>
      )}

      {models.length > 0 ? (
        <div className="space-y-2">
          <p className="text-xs text-muted-foreground">{t("providers.modelsHint")}</p>
          {models.map((m) => (
            <div
              key={`${m.id}:${m.source}`}
              className="flex items-center justify-between gap-3 rounded-lg border border-border px-3 py-2"
            >
              <div className="min-w-0 space-y-1">
                <div className="flex items-center gap-2 flex-wrap">
                  <span className="font-mono text-sm">{m.id}</span>
                  <Badge variant="outline" size="sm">
                    {m.source}
                  </Badge>
                  <Badge variant={m.enabled ? "success" : "outline"} size="sm">
                    {m.enabled ? t("common.enable") : t("common.disable")}
                  </Badge>
                </div>
                {m.name && m.name !== m.id && (
                  <p className="text-xs text-muted-foreground">{m.name}</p>
                )}
              </div>
              <div className="flex items-center gap-3 shrink-0">
                <div className="flex items-center gap-2">
                  <Switch checked={m.enabled} onCheckedChange={() => onToggleModel(m)} />
                  <span className="text-sm">{t("providers.enabled")}</span>
                </div>
                {m.source === "custom" && (
                  <>
                    <Button onClick={() => handleEdit(m)} variant="ghost" size="xs">
                      {t("common.edit")}
                    </Button>
                    <Button
                      onClick={() => onRemoveCustomModel(m.id)}
                      variant="ghost"
                      size="xs"
                      className="text-destructive-foreground"
                    >
                      {t("common.delete")}
                    </Button>
                  </>
                )}
              </div>
            </div>
          ))}
        </div>
      ) : (
        <div className="text-xs text-muted-foreground py-2">{t("providers.noModels")}</div>
      )}

      <div>
        <Button
          onClick={handleFetch}
          loading={fetching}
          variant="ghost"
          size="sm"
          className="text-muted-foreground"
        >
          {fetching ? t("providers.fetching") : t("providers.fetchModels")}
        </Button>
      </div>
    </div>
  );
}

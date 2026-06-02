import { useCallback, useEffect, useState } from "react";
import {
  createProvider,
  deleteProvider,
  fetchProviderModels,
  listProviderModels,
  listProviders,
  listProviderTypes,
  updateProvider,
} from "@/lib/api-client/sdk.gen";
import type {
  CustomModelForm,
  ModelConfig,
  Provider,
  ProviderModel,
  ProviderType,
} from "@/lib/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Switch } from "@/components/ui/switch";
import { Badge } from "@/components/ui/badge";
import { useI18n } from "@/lib/i18n";
import { useToast, ToastContainer } from "@/hooks/use-toast";
import { SettingsPageHeader } from "@/features/settings/SettingsPageHeader";
import { SettingsEmptyState } from "@/features/settings/SettingsEmptyState";
import {
  DetailPanel,
  DetailPanelHeader,
  FormSectionTitle,
} from "@/features/settings/SettingsDetailPanel";
import { ConfirmDialog } from "@/features/settings/ConfirmDialog";
import { ArrowLeft, Cpu } from "lucide-react";

// ── helpers ──────────────────────────────────────────────────────────────────

function createCustomModelForm(): CustomModelForm {
  return {
    original_id: "",
    id: "",
    name: "",
    enabled: true,
    reasoning: false,
    input: "text",
    output: "text",
    context_window: "",
    max_tokens: "",
    cost_input: "",
    cost_output: "",
    cost_cache_read: "",
    cost_cache_write: "",
  };
}

function normalizeModalities(value: string): string[] {
  if (!value) return [];
  return value
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);
}

function textValue(value: unknown): string {
  if (value === undefined || value === null) return "";
  if (typeof value === "string") return value;
  if (typeof value === "number" || typeof value === "boolean" || typeof value === "bigint") {
    return String(value);
  }
  return "";
}

function numberOrZero(value: string | number): number {
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : 0;
}

function modelConfigFromForm(form: CustomModelForm): ModelConfig {
  const model: ModelConfig = {
    id: form.id,
    name: form.name || form.id,
    enabled: form.enabled !== false,
    reasoning: Boolean(form.reasoning),
    input: normalizeModalities(form.input),
    output: normalizeModalities(form.output),
  };
  const contextWindow = numberOrZero(form.context_window);
  const maxTokens = numberOrZero(form.max_tokens);
  if (contextWindow > 0) model.contextWindow = contextWindow;
  if (maxTokens > 0) model.maxTokens = maxTokens;

  const cost = {
    input: numberOrZero(form.cost_input),
    output: numberOrZero(form.cost_output),
    cacheRead: numberOrZero(form.cost_cache_read),
    cacheWrite: numberOrZero(form.cost_cache_write),
  };
  if (cost.input !== 0 || cost.output !== 0 || cost.cacheRead !== 0 || cost.cacheWrite !== 0) {
    model.cost = cost;
  }
  return model;
}

function formFromModelConfig(modelID: string, config: ModelConfig | undefined): CustomModelForm {
  const form = createCustomModelForm();
  form.original_id = modelID;
  form.id = modelID;
  form.name = config?.name || "";
  form.enabled = config?.enabled !== false;
  form.reasoning = Boolean(config?.reasoning);
  form.input = (config?.input || []).join(", ");
  form.output = (config?.output || []).join(", ");
  form.context_window = config?.contextWindow != null ? String(config.contextWindow) : "";
  form.max_tokens = config?.maxTokens != null ? String(config.maxTokens) : "";
  form.cost_input = config?.cost?.input != null ? String(config.cost.input) : "";
  form.cost_output = config?.cost?.output != null ? String(config.cost.output) : "";
  form.cost_cache_read = config?.cost?.cacheRead != null ? String(config.cost.cacheRead) : "";
  form.cost_cache_write = config?.cost?.cacheWrite != null ? String(config.cost.cacheWrite) : "";
  return form;
}

function providerJSONValue(p: Provider): object {
  return {
    type: p.type,
    name: p.name,
    enabled: p.enabled,
    api_key: p.api_key,
    base_url: p.base_url,
    models: p.models || {},
  };
}

// ── ProviderDetail ────────────────────────────────────────────────────────────

interface ProviderDetailProps {
  provider: Provider;
  models: ProviderModel[];
  providerTypes: ProviderType[];
  providerDefaults: Record<string, { base_url: string; name: string }>;
  onSave: (p: Provider) => void;
  onDelete: (id: string) => void;
  onFetchModels: (p: Provider) => Promise<void>;
  onToggleModel: (providerId: string, model: ProviderModel, enabled: boolean) => void;
  onRemoveCustomModel: (providerId: string, modelID: string) => void;
  onAddCustomModel: (providerId: string, form: CustomModelForm) => void;
  showToast: (text: string, kind?: "success" | "error") => void;
}

function ProviderDetail({
  provider: initialProvider,
  models,
  providerTypes,
  providerDefaults,
  onSave,
  onDelete,
  onFetchModels,
  onToggleModel,
  onRemoveCustomModel,
  onAddCustomModel,
  showToast,
}: ProviderDetailProps) {
  const { t } = useI18n();
  const [provider, setProvider] = useState<Provider>(initialProvider);
  const [showCustomModelForm, setShowCustomModelForm] = useState(false);
  const [customModelForm, setCustomModelForm] = useState<CustomModelForm>(createCustomModelForm());
  const [showAdvancedJSON, setShowAdvancedJSON] = useState(false);
  const [providerJSON, setProviderJSON] = useState(() =>
    JSON.stringify(providerJSONValue(initialProvider), null, 2),
  );
  const [fetching, setFetching] = useState(false);
  const [confirmDeleteOpen, setConfirmDeleteOpen] = useState(false);

  // Sync local state when the selected provider changes
  useEffect(() => {
    setProvider(initialProvider);
    setProviderJSON(JSON.stringify(providerJSONValue(initialProvider), null, 2));
    setShowCustomModelForm(false);
    setShowAdvancedJSON(false);
    setCustomModelForm(createCustomModelForm());
  }, [initialProvider]);

  const syncJSON = useCallback((p: Provider) => {
    setProviderJSON(JSON.stringify(providerJSONValue(p), null, 2));
  }, []);

  const updateField = (field: keyof Provider, value: Provider[keyof Provider]) => {
    const next = { ...provider, [field]: value };
    setProvider(next);
    syncJSON(next);
  };

  const parseJSON = (): Provider => {
    const raw = providerJSON.trim();
    if (!raw) throw new Error("Provider JSON is required");
    let parsed: Record<string, unknown>;
    try {
      parsed = JSON.parse(raw) as Record<string, unknown>;
    } catch (e) {
      throw new Error("Provider JSON is invalid: " + String(e));
    }
    if (!parsed || Array.isArray(parsed) || typeof parsed !== "object") {
      throw new Error("Provider JSON must be an object");
    }
    return {
      ...provider,
      type: (textValue(parsed.type) || provider.type).trim(),
      name: (textValue(parsed.name) || provider.name || provider.id).trim() || provider.id,
      enabled: parsed.enabled !== false,
      api_key: textValue(parsed.api_key),
      base_url: textValue(parsed.base_url),
      models:
        parsed.models && !Array.isArray(parsed.models) && typeof parsed.models === "object"
          ? (parsed.models as Record<string, ModelConfig>)
          : {},
    };
  };

  const handleSave = () => {
    try {
      const parsed = parseJSON();
      const next = { ...parsed };
      setProvider(next);
      syncJSON(next);
      onSave(next);
    } catch (e) {
      showToast(String(e instanceof Error ? e.message : e), "error");
    }
  };

  const handleApplyJSON = () => {
    try {
      const parsed = parseJSON();
      setProvider(parsed);
      setProviderJSON(JSON.stringify(providerJSONValue(parsed), null, 2));
      setCustomModelForm(createCustomModelForm());
      setShowCustomModelForm(false);
      showToast("Provider JSON applied");
    } catch (e) {
      showToast(String(e instanceof Error ? e.message : e), "error");
    }
  };

  const handleFetchModels = async () => {
    setFetching(true);
    try {
      await onFetchModels(provider);
    } finally {
      setFetching(false);
    }
  };

  const handleSubmitCustomModel = () => {
    try {
      const modelID = (customModelForm.id || "").trim();
      if (!modelID) throw new Error("Model ID is required");
      const nextModels = { ...provider.models };
      if (customModelForm.original_id && customModelForm.original_id !== modelID) {
        delete nextModels[customModelForm.original_id];
      }
      nextModels[modelID] = modelConfigFromForm({ ...customModelForm, id: modelID });
      const next = { ...provider, models: nextModels };
      setProvider(next);
      syncJSON(next);
      setCustomModelForm(formFromModelConfig(modelID, nextModels[modelID] as ModelConfig));
      setShowCustomModelForm(false);
      onAddCustomModel(provider.id, { ...customModelForm, id: modelID });
    } catch (e) {
      showToast(String(e instanceof Error ? e.message : e), "error");
    }
  };

  const handleEditCustomModel = (model: ProviderModel) => {
    const id = model.id ?? "";
    const config = (provider.models || {})[id];
    setCustomModelForm(formFromModelConfig(id, config as ModelConfig));
    setShowCustomModelForm(true);
  };

  const handleToggleModel = (model: ProviderModel) => {
    const nextModels = { ...provider.models };
    const current = nextModels[model.id] || {
      id: model.id,
      name: model.name || model.id,
      enabled: model.enabled,
      reasoning: false,
      input: [],
      output: [],
    };
    current.enabled = !model.enabled;
    nextModels[model.id] = current;
    const next = { ...provider, models: nextModels };
    setProvider(next);
    syncJSON(next);
    onToggleModel(provider.id, model, !model.enabled);
  };

  const handleRemoveCustomModel = (modelID: string) => {
    const next = { ...provider.models };
    delete next[modelID];
    const nextProvider = { ...provider, models: next };
    setProvider(nextProvider);
    syncJSON(nextProvider);
    if (customModelForm.original_id === modelID || customModelForm.id === modelID) {
      setCustomModelForm(createCustomModelForm());
      setShowCustomModelForm(false);
    }
    onRemoveCustomModel(provider.id, modelID);
  };

  return (
    <DetailPanel
      onSave={handleSave}
      onDelete={() => setConfirmDeleteOpen(true)}
      saveLabel={t("common.save")}
      deleteLabel={t("common.delete")}
    >
      <DetailPanelHeader
        title={provider.name || provider.id}
        subtitle={
          <Badge variant="outline" size="sm">
            {provider.type}
          </Badge>
        }
        action={
          <div className="flex items-center gap-2">
            <Switch
              checked={provider.enabled}
              onCheckedChange={(checked) => updateField("enabled", checked)}
            />
            <span className="text-sm">{t("providers.enabled")}</span>
          </div>
        }
      />

      {/* Connection section */}
      <div className="space-y-4">
        <FormSectionTitle>Connection</FormSectionTitle>
        <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
          <div>
            <label className="text-xs font-medium mb-1 block">Type</label>
            <select
              value={provider.type}
              onChange={(e) => updateField("type", e.target.value)}
              className="h-9 w-full rounded-lg border border-input bg-background px-3 text-sm outline-none sm:h-8"
            >
              {providerTypes.map((pt) => (
                <option key={pt.id} value={pt.id}>
                  {pt.name} ({pt.id})
                </option>
              ))}
            </select>
          </div>
          <div>
            <label className="text-xs font-medium mb-1 block">Name</label>
            <Input
              type="text"
              value={provider.name}
              placeholder={provider.id}
              onChange={(e) =>
                updateField("name", (e as React.ChangeEvent<HTMLInputElement>).target.value)
              }
              nativeInput
            />
          </div>
          <div>
            <label className="text-xs font-medium mb-1 block">API Key</label>
            <Input
              type="password"
              value={provider.api_key}
              placeholder="sk-..."
              onChange={(e) =>
                updateField("api_key", (e as React.ChangeEvent<HTMLInputElement>).target.value)
              }
              nativeInput
              className="font-mono"
            />
          </div>
          <div>
            <label className="text-xs font-medium mb-1 block">Base URL</label>
            <Input
              type="text"
              value={provider.base_url}
              placeholder={providerDefaults[provider.type]?.base_url || ""}
              onChange={(e) =>
                updateField("base_url", (e as React.ChangeEvent<HTMLInputElement>).target.value)
              }
              nativeInput
              className="font-mono"
            />
          </div>
        </div>
      </div>

      {/* Models section */}
      <div className="space-y-3">
        <div className="flex items-center justify-between">
          <FormSectionTitle>Models</FormSectionTitle>
          <Button
            onClick={() => {
              setCustomModelForm(createCustomModelForm());
              setShowCustomModelForm(true);
            }}
            variant="ghost"
            size="sm"
          >
            Add custom model
          </Button>
        </div>

        {/* Custom model form */}
        {showCustomModelForm && (
          <div className="rounded-lg border border-border bg-muted/40 p-4 space-y-4">
            <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
              <div>
                <label className="text-xs font-medium mb-1 block">Model ID</label>
                <Input
                  type="text"
                  value={customModelForm.id}
                  placeholder="llama3.1:8b"
                  onChange={(e) =>
                    setCustomModelForm((f) => ({
                      ...f,
                      id: (e as React.ChangeEvent<HTMLInputElement>).target.value,
                    }))
                  }
                  nativeInput
                  className="font-mono"
                />
              </div>
              <div>
                <label className="text-xs font-medium mb-1 block">Display name</label>
                <Input
                  type="text"
                  value={customModelForm.name}
                  placeholder="Llama 3.1 8B (Local)"
                  onChange={(e) =>
                    setCustomModelForm((f) => ({
                      ...f,
                      name: (e as React.ChangeEvent<HTMLInputElement>).target.value,
                    }))
                  }
                  nativeInput
                />
              </div>
              <div>
                <label className="text-xs font-medium mb-1 block">Input</label>
                <Input
                  type="text"
                  value={customModelForm.input}
                  placeholder="text, image"
                  onChange={(e) =>
                    setCustomModelForm((f) => ({
                      ...f,
                      input: (e as React.ChangeEvent<HTMLInputElement>).target.value,
                    }))
                  }
                  nativeInput
                />
              </div>
              <div>
                <label className="text-xs font-medium mb-1 block">Output</label>
                <Input
                  type="text"
                  value={customModelForm.output}
                  placeholder="text"
                  onChange={(e) =>
                    setCustomModelForm((f) => ({
                      ...f,
                      output: (e as React.ChangeEvent<HTMLInputElement>).target.value,
                    }))
                  }
                  nativeInput
                />
              </div>
              <div>
                <label className="text-xs font-medium mb-1 block">Context window</label>
                <Input
                  type="number"
                  min={0}
                  value={customModelForm.context_window}
                  placeholder="128000"
                  onChange={(e) =>
                    setCustomModelForm((f) => ({
                      ...f,
                      context_window: (e as React.ChangeEvent<HTMLInputElement>).target.value,
                    }))
                  }
                  nativeInput
                />
              </div>
              <div>
                <label className="text-xs font-medium mb-1 block">Max tokens</label>
                <Input
                  type="number"
                  min={0}
                  value={customModelForm.max_tokens}
                  placeholder="32000"
                  onChange={(e) =>
                    setCustomModelForm((f) => ({
                      ...f,
                      max_tokens: (e as React.ChangeEvent<HTMLInputElement>).target.value,
                    }))
                  }
                  nativeInput
                />
              </div>
            </div>

            <div className="grid grid-cols-1 md:grid-cols-2 gap-4 items-start">
              <div className="flex items-center gap-3 rounded-lg border border-border px-4 py-3">
                <Switch
                  checked={customModelForm.enabled}
                  onCheckedChange={(checked) =>
                    setCustomModelForm((f) => ({
                      ...f,
                      enabled: checked,
                    }))
                  }
                />
                <div>
                  <p className="text-sm">Enabled</p>
                  <p className="text-xs text-muted-foreground">
                    Persist model availability on this provider instance.
                  </p>
                </div>
              </div>
              <div className="flex items-center gap-3 rounded-lg border border-border px-4 py-3">
                <Switch
                  checked={customModelForm.reasoning}
                  onCheckedChange={(checked) =>
                    setCustomModelForm((f) => ({
                      ...f,
                      reasoning: checked,
                    }))
                  }
                />
                <div>
                  <p className="text-sm">Reasoning</p>
                  <p className="text-xs text-muted-foreground">
                    Stores the provider-facing <span className="font-mono">reasoning</span> flag.
                  </p>
                </div>
              </div>
            </div>

            <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-4 gap-4">
              {[
                { label: "Cost input", field: "cost_input" as const },
                { label: "Cost output", field: "cost_output" as const },
                { label: "Cost cacheRead", field: "cost_cache_read" as const },
                { label: "Cost cacheWrite", field: "cost_cache_write" as const },
              ].map(({ label, field }) => (
                <div key={field}>
                  <label className="text-xs font-medium mb-1 block">{label}</label>
                  <Input
                    type="number"
                    step="any"
                    value={customModelForm[field]}
                    placeholder="0"
                    onChange={(e) =>
                      setCustomModelForm((f) => ({
                        ...f,
                        [field]: (e as React.ChangeEvent<HTMLInputElement>).target.value,
                      }))
                    }
                    nativeInput
                  />
                </div>
              ))}
            </div>

            <div className="flex items-center gap-3 justify-end">
              <Button
                onClick={() => {
                  setCustomModelForm(createCustomModelForm());
                  setShowCustomModelForm(false);
                }}
                variant="ghost"
                size="sm"
              >
                {t("common.cancel")}
              </Button>
              <Button onClick={handleSubmitCustomModel} variant="default" size="sm">
                {customModelForm.original_id ? t("providers.updateModel") : t("providers.addModel")}
              </Button>
            </div>
          </div>
        )}

        {/* Model list */}
        {models.length > 0 ? (
          <div className="space-y-2">
            <p className="text-xs text-muted-foreground">
              Toggle models on or off, then save. Custom models can be edited with the form or in
              the advanced JSON editor.
            </p>
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
                      {m.enabled ? "enabled" : "disabled"}
                    </Badge>
                  </div>
                  {m.name && m.name !== m.id && (
                    <p className="text-xs text-muted-foreground">{m.name}</p>
                  )}
                </div>
                <div className="flex items-center gap-3 shrink-0">
                  <div className="flex items-center gap-2">
                    <Switch checked={m.enabled} onCheckedChange={() => handleToggleModel(m)} />
                    <span className="text-sm">Enabled</span>
                  </div>
                  {m.source === "custom" && (
                    <>
                      <Button onClick={() => handleEditCustomModel(m)} variant="ghost" size="xs">
                        edit
                      </Button>
                      <Button
                        onClick={() => handleRemoveCustomModel(m.id)}
                        variant="ghost"
                        size="xs"
                        className="text-destructive"
                      >
                        remove
                      </Button>
                    </>
                  )}
                </div>
              </div>
            ))}
          </div>
        ) : (
          <div className="text-xs text-muted-foreground py-2">
            No models yet. Fetch from the provider or add custom models above.
          </div>
        )}

        {/* Fetch models button */}
        <div>
          <Button
            onClick={handleFetchModels}
            loading={fetching}
            variant="ghost"
            size="sm"
            className="text-muted-foreground"
          >
            {fetching ? "Fetching..." : "Fetch models"}
          </Button>
        </div>
      </div>

      {/* Advanced JSON editor */}
      <div className="border-t border-border pt-4 space-y-3">
        <button
          onClick={() => {
            setProviderJSON(JSON.stringify(providerJSONValue(provider), null, 2));
            setShowAdvancedJSON((v) => !v);
          }}
          className="text-xs font-mono text-muted-foreground hover:text-foreground"
        >
          {showAdvancedJSON ? "Hide advanced JSON editor" : "Show advanced JSON editor"}
        </button>
        {showAdvancedJSON && (
          <div className="space-y-2">
            <label className="text-xs font-medium mb-1 block">Provider JSON</label>
            <Textarea
              value={providerJSON}
              onChange={(e) => setProviderJSON(e.target.value)}
              rows={16}
              placeholder={`{\n  "type": "openai",\n  "name": "Ollama",\n  "enabled": true,\n  "api_key": "ollama",\n  "base_url": "http://localhost:11434/v1",\n  "models": {}\n}`}
              className="font-mono text-xs"
            />
            <div className="flex justify-end">
              <Button onClick={handleApplyJSON} variant="ghost" size="xs">
                Apply JSON
              </Button>
            </div>
          </div>
        )}
      </div>

      <ConfirmDialog
        open={confirmDeleteOpen}
        onOpenChange={setConfirmDeleteOpen}
        title="Delete provider"
        message={`Delete provider ${provider.id}?`}
        onConfirm={() => onDelete(provider.id)}
      />
    </DetailPanel>
  );
}

// ── NewProviderForm ────────────────────────────────────────────────────────────

interface NewProviderFormProps {
  providerTypes: ProviderType[];
  providerDefaults: Record<string, { base_url: string; name: string }>;
  existingIds: Set<string>;
  onAdd: (type: string, id: string, name: string) => Promise<void>;
  onCancel: () => void;
  showToast: (text: string, kind?: "success" | "error") => void;
}

function NewProviderForm({
  providerTypes,
  providerDefaults,
  existingIds,
  onAdd,
  onCancel,
  showToast,
}: NewProviderFormProps) {
  const { t } = useI18n();
  const [type, setType] = useState(providerTypes[0]?.id || "");
  const [id, setId] = useState("");
  const [name, setName] = useState("");

  useEffect(() => {
    if (providerTypes.length > 0 && !type) {
      setType(providerTypes[0].id);
    }
  }, [providerTypes, type]);

  const handleSubmit = async () => {
    const trimmedId = id.trim();
    if (!type) {
      showToast("Provider type is required", "error");
      return;
    }
    if (!trimmedId) {
      showToast("Provider ID is required", "error");
      return;
    }
    if (existingIds.has(trimmedId)) {
      showToast("Provider ID already exists", "error");
      return;
    }
    await onAdd(type, trimmedId, name.trim() || providerDefaults[type]?.name || trimmedId);
  };

  return (
    <DetailPanel
      onSave={handleSubmit}
      onCancel={onCancel}
      saveLabel="Add provider"
      cancelLabel={t("common.cancel")}
      canSave={!!type && !!id.trim()}
    >
      <DetailPanelHeader
        title="New provider"
        subtitle={
          <p className="text-sm text-muted-foreground">Connect Stella to an LLM API provider.</p>
        }
      />
      <div className="space-y-4">
        <div>
          <label className="text-xs font-medium mb-1 block">Type</label>
          <select
            value={type}
            onChange={(e) => setType(e.target.value)}
            className="h-9 w-full rounded-lg border border-input bg-background px-3 text-sm outline-none sm:h-8"
          >
            <option value="" disabled>
              Select provider type...
            </option>
            {providerTypes.map((pt) => (
              <option key={pt.id} value={pt.id}>
                {pt.name} ({pt.id})
              </option>
            ))}
          </select>
        </div>
        <div>
          <label className="text-xs font-medium mb-1 block">Provider ID</label>
          <Input
            type="text"
            value={id}
            placeholder="e.g. openrouter"
            onChange={(e) => setId((e as React.ChangeEvent<HTMLInputElement>).target.value)}
            nativeInput
            className="font-mono"
          />
        </div>
        <div>
          <label className="text-xs font-medium mb-1 block">Display name</label>
          <Input
            type="text"
            value={name}
            placeholder={providerDefaults[type]?.name || ""}
            onChange={(e) => setName((e as React.ChangeEvent<HTMLInputElement>).target.value)}
            nativeInput
          />
        </div>
      </div>
    </DetailPanel>
  );
}

// ── ProvidersPage ─────────────────────────────────────────────────────────────

export function ProvidersPage() {
  const [providers, setProviders] = useState<Provider[]>([]);
  const [providerModels, setProviderModels] = useState<Record<string, ProviderModel[]>>({});
  const [providerTypes, setProviderTypes] = useState<ProviderType[]>([]);
  const [providerDefaults, setProviderDefaults] = useState<
    Record<string, { base_url: string; name: string }>
  >({});
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [creatingNew, setCreatingNew] = useState(false);
  const { toasts, showToast } = useToast();

  const loadProviderTypes = useCallback(async () => {
    try {
      const { data } = await listProviderTypes({ throwOnError: true });
      const types = (data?.provider_types as ProviderType[]) ?? [];
      const defaults: Record<string, { base_url: string; name: string }> = {};
      for (const t of types) {
        defaults[t.id] = { base_url: t.default_url, name: t.name };
      }
      setProviderDefaults(defaults);
      setProviderTypes(
        [...types]
          .sort((a, b) => (a.name || a.id).localeCompare(b.name || b.id))
          .map((t) => ({ id: t.id, name: t.name || t.id, default_url: t.default_url })),
      );
    } catch (e) {
      console.error("Failed to load provider types:", e);
    }
  }, []);

  const loadProviderModels = useCallback(async (providerID: string) => {
    try {
      const { data } = await listProviderModels({ path: { id: providerID }, throwOnError: true });
      const models = (data?.models as ProviderModel[]) ?? [];
      setProviderModels((prev) => ({ ...prev, [providerID]: models }));
    } catch {
      setProviderModels((prev) => ({ ...prev, [providerID]: [] }));
    }
  }, []);

  const loadProviders = useCallback(async () => {
    try {
      const { data } = await listProviders({ throwOnError: true });
      const list = (data?.providers as Provider[]) ?? [];
      setProviders(
        list.map((p) => ({
          ...p,
          type: p.type || p.id,
          enabled: p.enabled !== false,
          models: p.models || {},
        })),
      );
      await Promise.all(list.map((p) => loadProviderModels(p.id)));
    } catch (e) {
      console.error(e);
    }
  }, [loadProviderModels]);

  useEffect(() => {
    const init = async () => {
      await loadProviderTypes();
      await loadProviders();
    };
    void init();
  }, [loadProviderTypes, loadProviders]);

  // Clear selection if selected provider is removed
  useEffect(() => {
    if (selectedId && !providers.find((p) => p.id === selectedId)) {
      setSelectedId(null);
    }
  }, [providers, selectedId]);

  const handleAddProvider = async (type: string, id: string, name: string) => {
    const d = providerDefaults[type] || {};
    try {
      await createProvider({
        body: {
          id,
          type,
          name: name || d.name || id,
          enabled: false,
          api_key: "",
          base_url: "",
          models: {},
        },
        throwOnError: true,
      });
      await loadProviders();
      setCreatingNew(false);
      setSelectedId(id);
      showToast("Provider added");
    } catch (e) {
      showToast(e instanceof Error ? e.message : String(e), "error");
    }
  };

  const handleSaveProvider = async (p: Provider) => {
    try {
      await updateProvider({
        path: { id: p.id },
        body: {
          id: p.id,
          type: p.type,
          name: p.name,
          enabled: p.enabled,
          api_key: p.api_key,
          base_url: p.base_url,
          models: p.models,
        },
        throwOnError: true,
      });
      await loadProviders();
      showToast("Saved");
    } catch (e) {
      showToast(e instanceof Error ? e.message : String(e), "error");
    }
  };

  const handleDeleteProvider = async (id: string) => {
    try {
      await deleteProvider({ path: { id }, throwOnError: true });
      setSelectedId(null);
      await loadProviders();
      showToast("Deleted");
    } catch (e) {
      showToast(e instanceof Error ? e.message : String(e), "error");
    }
  };

  const handleFetchModels = async (p: Provider) => {
    try {
      const { data } = await fetchProviderModels({
        path: { id: p.id },
        body: { api_key: p.api_key, base_url: p.base_url },
        throwOnError: true,
      });
      const list = (data?.models as ProviderModel[]) ?? [];
      setProviderModels((prev) => ({ ...prev, [p.id]: list }));
      showToast(`${list.length} models available`);
    } catch (e) {
      showToast(e instanceof Error ? e.message : String(e), "error");
    }
  };

  const handleToggleModel = (providerId: string, model: ProviderModel, enabled: boolean) => {
    setProviderModels((prev) => {
      const list = prev[providerId] || [];
      return {
        ...prev,
        [providerId]: list.map((m) =>
          m.id === model.id && m.source === model.source ? { ...m, enabled } : m,
        ),
      };
    });
  };

  const handleRemoveCustomModel = (providerId: string, modelID: string) => {
    setProviderModels((prev) => {
      const list = prev[providerId] || [];
      return {
        ...prev,
        [providerId]: list.filter((m) => !(m.id === modelID && m.source === "custom")),
      };
    });
  };

  const handleAddCustomModel = (providerId: string, form: CustomModelForm) => {
    const modelID = form.id.trim();
    if (!modelID) return;
    setProviderModels((prev) => {
      const list = (prev[providerId] || []).filter(
        (m) =>
          !(
            m.source === "custom" &&
            (m.id === modelID || (form.original_id && m.id === form.original_id))
          ),
      );
      list.push({
        id: modelID,
        name: form.name || modelID,
        enabled: form.enabled,
        source: "custom",
      });
      return { ...prev, [providerId]: list };
    });
  };

  // Flat list sorted by display name
  const sortedProviders = [...providers].sort(
    (a, b) => (a.name || a.id).localeCompare(b.name || b.id) || a.id.localeCompare(b.id),
  );

  const selectedProvider = selectedId ? providers.find((p) => p.id === selectedId) : null;
  const existingIds = new Set(providers.map((p) => p.id));

  // Grouping computation for providers
  const grouped = sortedProviders.reduce<Record<string, Provider[]>>((acc, p) => {
    const type = p.type || "other";
    if (!acc[type]) acc[type] = [];
    acc[type].push(p);
    return acc;
  }, {});

  const groupedPlatforms = Object.entries(grouped)
    .map(([type, platformProviders]) => {
      const typeMeta = providerTypes.find((pt) => pt.id === type);
      return {
        type,
        label: typeMeta?.name || type,
        providers: platformProviders,
      };
    })
    .sort((a, b) => a.label.localeCompare(b.label));

  // Right panel detail
  let detail: React.ReactNode = undefined;
  if (creatingNew) {
    detail = (
      <NewProviderForm
        providerTypes={providerTypes}
        providerDefaults={providerDefaults}
        existingIds={existingIds}
        onAdd={handleAddProvider}
        onCancel={() => setCreatingNew(false)}
        showToast={showToast}
      />
    );
  } else if (selectedProvider) {
    detail = (
      <ProviderDetail
        key={selectedProvider.id}
        provider={selectedProvider}
        models={providerModels[selectedProvider.id] || []}
        providerTypes={providerTypes}
        providerDefaults={providerDefaults}
        onSave={handleSaveProvider}
        onDelete={handleDeleteProvider}
        onFetchModels={handleFetchModels}
        onToggleModel={handleToggleModel}
        onRemoveCustomModel={handleRemoveCustomModel}
        onAddCustomModel={handleAddCustomModel}
        showToast={showToast}
      />
    );
  }

  const hasActiveEditor = creatingNew || !!selectedProvider;

  return (
    <div className="h-full overflow-y-auto bg-background">
      <div className="mx-auto max-w-3xl p-6 sm:p-8 lg:p-10">
        {hasActiveEditor ? (
          <div className="space-y-4">
            <button
              onClick={() => {
                setSelectedId(null);
                setCreatingNew(false);
              }}
              className="group inline-flex items-center gap-1.5 text-xs text-muted-foreground hover:text-foreground transition-colors font-medium cursor-pointer"
            >
              <ArrowLeft className="size-3.5 transition-transform group-hover:-translate-x-0.5" />
              Back to Providers
            </button>
            <div className="bg-card border border-border/40 rounded-2xl overflow-hidden shadow-sm">
              {detail}
            </div>
          </div>
        ) : (
          <div className="space-y-8">
            <SettingsPageHeader
              title="Providers"
              description="Connect and configure LLM API providers."
              action={
                <Button
                  onClick={() => {
                    setCreatingNew(true);
                    setSelectedId(null);
                  }}
                  size="sm"
                  className="rounded-xl"
                >
                  Add provider
                </Button>
              }
            />

            {sortedProviders.length === 0 ? (
              <SettingsEmptyState
                message="No providers configured"
                description="Add your first LLM provider to get started."
                action={
                  <Button
                    onClick={() => {
                      setCreatingNew(true);
                      setSelectedId(null);
                    }}
                    variant="outline"
                    size="sm"
                    className="rounded-xl"
                  >
                    Add provider
                  </Button>
                }
              />
            ) : (
              <div className="space-y-8">
                {groupedPlatforms.map((platform) => (
                  <div key={platform.type} className="space-y-4">
                    <div className="flex items-center gap-2 border-b border-border/40 pb-2">
                      <Cpu className="size-4 shrink-0 text-muted-foreground/80" />
                      <h4 className="text-xs font-semibold text-muted-foreground/85 uppercase tracking-wider">
                        {platform.label}
                      </h4>
                      <Badge variant="secondary" className="text-[10px] py-0 px-1.5 rounded-md">
                        {platform.providers.length}
                      </Badge>
                    </div>
                    <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
                      {platform.providers.map((p) => {
                        const modelCount = (providerModels[p.id] || []).length;
                        return (
                          <div
                            key={p.id}
                            onClick={() => {
                              setSelectedId(p.id);
                              setCreatingNew(false);
                            }}
                            className="group relative flex flex-col justify-between rounded-2xl border border-border/40 bg-card p-5 transition-all hover:border-border/80 hover:shadow-sm cursor-pointer"
                          >
                            <div className="space-y-3">
                              <div className="flex items-center justify-between gap-3">
                                <div className="flex items-center gap-2 min-w-0">
                                  {/* Status dot */}
                                  <span
                                    className={`shrink-0 w-1.5 h-1.5 rounded-full ${
                                      p.enabled ? "bg-green-500" : "bg-muted-foreground/40"
                                    }`}
                                  />
                                  <h3 className="text-sm font-semibold text-foreground/90 truncate">
                                    {p.name || p.id}
                                  </h3>
                                </div>
                                <Badge
                                  variant="outline"
                                  className="font-mono text-[10px] uppercase shrink-0"
                                >
                                  {p.type}
                                </Badge>
                              </div>
                              {p.base_url && (
                                <p className="font-mono text-[10px] text-muted-foreground truncate max-w-full">
                                  {p.base_url}
                                </p>
                              )}
                            </div>
                            <div className="mt-4 flex items-center justify-between">
                              <span className="text-xs text-muted-foreground">
                                {modelCount} {modelCount === 1 ? "model" : "models"} configured
                              </span>
                              <span className="text-xs font-medium text-primary opacity-0 group-hover:opacity-100 transition-opacity">
                                Configure →
                              </span>
                            </div>
                          </div>
                        );
                      })}
                    </div>
                  </div>
                ))}
              </div>
            )}
          </div>
        )}
      </div>
      <ToastContainer messages={toasts} />
    </div>
  );
}

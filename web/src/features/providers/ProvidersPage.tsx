import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
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
import {
  Dialog,
  DialogPopup,
  DialogTitle,
  DialogHeader,
  DialogFooter,
} from "@/components/ui/dialog";
import { useI18n } from "@/lib/i18n";
import { SettingsDetailLayout } from "@/features/settings/SettingsDetailLayout";

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

// ── Toast ─────────────────────────────────────────────────────────────────────

interface ToastMsg {
  id: number;
  text: string;
  kind: "success" | "error";
}

let toastSeq = 0;

function Toast({ messages }: { messages: ToastMsg[] }) {
  if (messages.length === 0) return null;
  return (
    <div className="fixed bottom-4 right-4 z-[9999] flex flex-col gap-2 pointer-events-none">
      {messages.map((m) => (
        <div
          key={m.id}
          className={`px-4 py-2.5 rounded-lg shadow-lg text-sm font-medium pointer-events-auto ${
            m.kind === "error"
              ? "bg-destructive text-destructive-foreground"
              : "bg-success text-success-foreground"
          }`}
        >
          {m.text}
        </div>
      ))}
    </div>
  );
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
      setCustomModelForm(formFromModelConfig(modelID, nextModels[modelID]));
      setShowCustomModelForm(false);
      onAddCustomModel(provider.id, { ...customModelForm, id: modelID });
    } catch (e) {
      showToast(String(e instanceof Error ? e.message : e), "error");
    }
  };

  const handleEditCustomModel = (model: ProviderModel) => {
    const config = (provider.models || {})[model.id];
    setCustomModelForm(formFromModelConfig(model.id, config));
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
    <div className="flex flex-col h-full">
      {/* Scrollable body */}
      <div className="flex-1 overflow-y-auto p-6 space-y-6">
        {/* Panel header */}
        <div className="flex items-start justify-between gap-4">
          <div>
            <h2 className="font-serif text-xl tracking-tight">{provider.name || provider.id}</h2>
            <div className="flex items-center gap-2 mt-1">
              <span className="text-xs font-mono text-muted-foreground">{provider.id}</span>
              <Badge variant="outline" size="sm">
                {provider.type}
              </Badge>
            </div>
          </div>
          <div className="flex items-center gap-3 shrink-0">
            <div className="flex items-center gap-2">
              <Switch
                checked={provider.enabled}
                onCheckedChange={(checked) => updateField("enabled", checked)}
              />
              <span className="text-sm">{t("providers.enabled")}</span>
            </div>
          </div>
        </div>

        {/* Connection section */}
        <div className="space-y-4">
          <p className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
            Connection
          </p>
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
            <p className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
              Models
            </p>
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
                  {customModelForm.original_id
                    ? t("providers.updateModel")
                    : t("providers.addModel")}
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
      </div>

      {/* Sticky footer */}
      <div className="shrink-0 border-t border-border px-6 py-3 flex items-center justify-between gap-3 bg-background">
        <Button onClick={handleSave} variant="default" size="sm">
          {t("common.save")}
        </Button>
        <Button
          onClick={() => setConfirmDeleteOpen(true)}
          variant="ghost"
          size="sm"
          className="text-muted-foreground hover:text-destructive"
        >
          {t("common.delete")}
        </Button>
      </div>

      {/* Confirm delete dialog */}
      <Dialog open={confirmDeleteOpen} onOpenChange={setConfirmDeleteOpen}>
        <DialogPopup showCloseButton={false}>
          <DialogHeader>
            <DialogTitle>Delete provider</DialogTitle>
          </DialogHeader>
          <div className="px-6 pb-2">
            <p className="text-sm">Delete provider {provider.id}?</p>
          </div>
          <DialogFooter>
            <Button onClick={() => setConfirmDeleteOpen(false)} variant="ghost" size="sm">
              {t("common.cancel")}
            </Button>
            <Button
              onClick={() => {
                setConfirmDeleteOpen(false);
                onDelete(provider.id);
              }}
              variant="destructive"
              size="sm"
            >
              {t("common.delete")}
            </Button>
          </DialogFooter>
        </DialogPopup>
      </Dialog>
    </div>
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
    <div className="flex flex-col h-full">
      <div className="flex-1 overflow-y-auto p-6 space-y-6">
        <div>
          <h2 className="font-serif text-xl tracking-tight">New provider</h2>
          <p className="text-sm text-muted-foreground mt-1">
            Connect Stella to an LLM API provider.
          </p>
        </div>
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
      </div>
      <div className="shrink-0 border-t border-border px-6 py-3 flex items-center justify-between gap-3 bg-background">
        <Button onClick={handleSubmit} disabled={!type || !id.trim()} variant="default" size="sm">
          Add provider
        </Button>
        <Button onClick={onCancel} variant="ghost" size="sm">
          {t("common.cancel")}
        </Button>
      </div>
    </div>
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
  const [toasts, setToasts] = useState<ToastMsg[]>([]);

  const showToast = useCallback((text: string, kind: "success" | "error" = "success") => {
    const id = ++toastSeq;
    setToasts((prev) => [...prev, { id, text, kind }]);
    setTimeout(() => setToasts((prev) => prev.filter((t) => t.id !== id)), 3000);
  }, []);

  const loadProviderTypes = useCallback(async () => {
    try {
      const types = (await api<ProviderType[]>("GET", "/api/provider-types")) || [];
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
      const models =
        (await api<ProviderModel[]>("GET", `/api/providers/${providerID}/models`)) || [];
      setProviderModels((prev) => ({ ...prev, [providerID]: models }));
    } catch {
      setProviderModels((prev) => ({ ...prev, [providerID]: [] }));
    }
  }, []);

  const loadProviders = useCallback(async () => {
    try {
      const list = (await api<Provider[]>("GET", "/api/providers")) || [];
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
      await api("POST", "/api/providers", {
        id,
        type,
        name: name || d.name || id,
        enabled: true,
        api_key: "",
        base_url: "",
        models: {},
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
      await api("PUT", `/api/providers/${p.id}`, {
        id: p.id,
        type: p.type,
        name: p.name,
        enabled: p.enabled,
        api_key: p.api_key,
        base_url: p.base_url,
        models: p.models,
      });
      await loadProviders();
      showToast("Saved");
    } catch (e) {
      showToast(e instanceof Error ? e.message : String(e), "error");
    }
  };

  const handleDeleteProvider = async (id: string) => {
    try {
      await api("DELETE", `/api/providers/${id}`);
      setSelectedId(null);
      await loadProviders();
      showToast("Deleted");
    } catch (e) {
      showToast(e instanceof Error ? e.message : String(e), "error");
    }
  };

  const handleFetchModels = async (p: Provider) => {
    try {
      const models = await api<ProviderModel[]>("POST", `/api/providers/${p.id}/models`, {
        api_key: p.api_key,
        base_url: p.base_url,
      });
      const list = models || [];
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

  // Left panel list header
  const listHeader = (
    <div className="flex items-center justify-between px-3 py-3 border-b border-border">
      <span className="text-xs font-medium text-muted-foreground uppercase tracking-wider">
        Providers
      </span>
      <Button
        onClick={() => {
          setCreatingNew(true);
          setSelectedId(null);
        }}
        variant="ghost"
        size="xs"
      >
        New
      </Button>
    </div>
  );

  // Left panel list
  const list = (
    <div>
      {sortedProviders.map((p) => {
        const modelCount = (providerModels[p.id] || []).length;
        const isSelected = !creatingNew && selectedId === p.id;
        return (
          <button
            key={p.id}
            onClick={() => {
              setSelectedId(p.id);
              setCreatingNew(false);
            }}
            className={`w-full text-left px-3 py-2.5 flex items-center gap-2 hover:bg-muted/50 transition-colors ${
              isSelected ? "bg-primary/8" : ""
            }`}
          >
            {/* Status dot */}
            <span
              className={`shrink-0 w-1.5 h-1.5 rounded-full ${
                p.enabled ? "bg-success" : "bg-muted-foreground/40"
              }`}
            />
            <div className="min-w-0 flex-1">
              <p className="text-sm font-medium leading-tight truncate">{p.name || p.id}</p>
              <p className="text-[11px] font-mono text-muted-foreground truncate">{p.id}</p>
            </div>
            {modelCount > 0 && (
              <span className="shrink-0 text-xs text-muted-foreground">{modelCount}</span>
            )}
          </button>
        );
      })}
    </div>
  );

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

  // Empty state
  const emptyState = (
    <p className="text-sm text-muted-foreground">Select a provider or add a new one.</p>
  );

  return (
    // Escape the p-8 px-10 padding from SettingsLayout's outlet wrapper
    <div className="-my-8 -mx-10 h-[calc(100%+4rem)]">
      <SettingsDetailLayout
        listHeader={listHeader}
        list={list}
        detail={detail}
        emptyState={emptyState}
      />
      <Toast messages={toasts} />
    </div>
  );
}

import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import type {
  CustomModelForm,
  ModelConfig,
  Provider,
  ProviderModel,
  ProviderType,
} from "@/lib/types";

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
  if (
    cost.input !== 0 ||
    cost.output !== 0 ||
    cost.cacheRead !== 0 ||
    cost.cacheWrite !== 0
  ) {
    model.cost = cost;
  }
  return model;
}

function formFromModelConfig(
  modelID: string,
  config: ModelConfig | undefined,
): CustomModelForm {
  const form = createCustomModelForm();
  form.original_id = modelID;
  form.id = modelID;
  form.name = config?.name || "";
  form.enabled = config?.enabled !== false;
  form.reasoning = Boolean(config?.reasoning);
  form.input = (config?.input || []).join(", ");
  form.output = (config?.output || []).join(", ");
  form.context_window =
    config?.contextWindow != null ? String(config.contextWindow) : "";
  form.max_tokens = config?.maxTokens != null ? String(config.maxTokens) : "";
  form.cost_input =
    config?.cost?.input != null ? String(config.cost.input) : "";
  form.cost_output =
    config?.cost?.output != null ? String(config.cost.output) : "";
  form.cost_cache_read =
    config?.cost?.cacheRead != null ? String(config.cost.cacheRead) : "";
  form.cost_cache_write =
    config?.cost?.cacheWrite != null ? String(config.cost.cacheWrite) : "";
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

function groupProvidersByType(
  providers: Provider[],
): { type: string; providers: Provider[] }[] {
  const byType = new Map<string, Provider[]>();
  for (const p of providers) {
    if (!byType.has(p.type)) byType.set(p.type, []);
    byType.get(p.type)!.push(p);
  }
  const groups = Array.from(byType.entries()).map(([type, list]) => ({
    type,
    providers: [...list].sort(
      (a, b) =>
        (a.name || a.id).localeCompare(b.name || b.id) ||
        a.id.localeCompare(b.id),
    ),
  }));
  groups.sort((a, b) => a.type.localeCompare(b.type));
  return groups;
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
              ? "bg-error text-error-content"
              : "bg-success text-success-content"
          }`}
        >
          {m.text}
        </div>
      ))}
    </div>
  );
}

// ── ProviderRow ───────────────────────────────────────────────────────────────

interface ProviderRowProps {
  provider: Provider;
  models: ProviderModel[];
  providerTypes: ProviderType[];
  providerDefaults: Record<string, { base_url: string; name: string }>;
  onSave: (p: Provider) => void;
  onDelete: (id: string) => void;
  onFetchModels: (p: Provider) => void;
  onToggleModel: (
    providerId: string,
    model: ProviderModel,
    enabled: boolean,
  ) => void;
  onRemoveCustomModel: (providerId: string, modelID: string) => void;
  onAddCustomModel: (
    providerId: string,
    form: CustomModelForm,
  ) => void;
  showToast: (text: string, kind?: "success" | "error") => void;
}

function ProviderRow({
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
}: ProviderRowProps) {
  const [provider, setProvider] = useState<Provider>(initialProvider);
  const [collapsed, setCollapsed] = useState(false);
  const [showModels, setShowModels] = useState(false);
  const [showCustomModelForm, setShowCustomModelForm] = useState(false);
  const [customModelForm, setCustomModelForm] = useState<CustomModelForm>(
    createCustomModelForm(),
  );
  const [showAdvancedJSON, setShowAdvancedJSON] = useState(false);
  const [providerJSON, setProviderJSON] = useState(() =>
    JSON.stringify(providerJSONValue(initialProvider), null, 2),
  );
  const [fetching, setFetching] = useState(false);
  const [confirmDeleteOpen, setConfirmDeleteOpen] = useState(false);

  // Sync provider state when parent updates (e.g. after save)
  useEffect(() => {
    setProvider(initialProvider);
    setProviderJSON(JSON.stringify(providerJSONValue(initialProvider), null, 2));
  }, [initialProvider]);

  const syncJSON = useCallback(
    (p: Provider) => {
      setProviderJSON(JSON.stringify(providerJSONValue(p), null, 2));
    },
    [],
  );

  const updateField = (
    field: keyof Provider,
    value: Provider[keyof Provider],
  ) => {
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
      type: String(parsed.type || provider.type || "").trim(),
      name:
        String(parsed.name || provider.name || provider.id).trim() ||
        provider.id,
      enabled: parsed.enabled !== false,
      api_key: String(parsed.api_key || ""),
      base_url: String(parsed.base_url || ""),
      models:
        parsed.models &&
        !Array.isArray(parsed.models) &&
        typeof parsed.models === "object"
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
      const nextModels = { ...(provider.models || {}) };
      if (
        customModelForm.original_id &&
        customModelForm.original_id !== modelID
      ) {
        delete nextModels[customModelForm.original_id];
      }
      nextModels[modelID] = modelConfigFromForm({ ...customModelForm, id: modelID });
      const next = { ...provider, models: nextModels };
      setProvider(next);
      syncJSON(next);
      setCustomModelForm(
        formFromModelConfig(modelID, nextModels[modelID]),
      );
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
    setShowModels(true);
  };

  const handleToggleModel = (model: ProviderModel) => {
    const nextModels = { ...(provider.models || {}) };
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
    const next = { ...(provider.models || {}) };
    delete next[modelID];
    const nextProvider = { ...provider, models: next };
    setProvider(nextProvider);
    syncJSON(nextProvider);
    if (
      customModelForm.original_id === modelID ||
      customModelForm.id === modelID
    ) {
      setCustomModelForm(createCustomModelForm());
      setShowCustomModelForm(false);
    }
    onRemoveCustomModel(provider.id, modelID);
  };

  const modelCount = models.length;

  return (
    <div className="py-6">
      {/* Header row */}
      <div className="flex items-center justify-between gap-4 mb-4">
        <button
          onClick={() => setCollapsed((c) => !c)}
          className="flex items-center gap-3 text-left min-w-0 flex-1 cursor-pointer"
        >
          <span className="text-xs text-secondary">{collapsed ? "▸" : "▾"}</span>
          <div className="flex items-baseline gap-3 flex-wrap min-w-0">
            <span className="font-medium text-lg">{provider.name || provider.id}</span>
            <span className="text-xs font-mono text-secondary">{provider.id}</span>
            <span className="badge badge-ghost badge-sm">{provider.type}</span>
            <span
              className={`badge badge-sm ${provider.enabled ? "badge-success" : "badge-ghost"}`}
            >
              {provider.enabled ? "enabled" : "disabled"}
            </span>
          </div>
        </button>
        <div className="flex items-center gap-2 shrink-0">
          <button
            onClick={() => setCollapsed((c) => !c)}
            className="btn btn-ghost btn-xs"
          >
            {collapsed ? "Expand" : "Collapse"}
          </button>
          <button
            onClick={() => setConfirmDeleteOpen(true)}
            className="btn btn-ghost btn-xs text-secondary hover:text-error"
          >
            remove
          </button>
        </div>
      </div>

      {/* Confirm delete dialog */}
      {confirmDeleteOpen && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40">
          <div
            className="card bg-base-100 shadow-xl w-full max-w-sm"
            onClick={(e) => e.stopPropagation()}
          >
            <div className="card-body">
              <p className="text-sm">Delete provider {provider.id}?</p>
              <div className="card-actions justify-end mt-4">
                <button
                  onClick={() => setConfirmDeleteOpen(false)}
                  className="btn btn-ghost btn-sm"
                >
                  Cancel
                </button>
                <button
                  onClick={() => {
                    setConfirmDeleteOpen(false);
                    onDelete(provider.id);
                  }}
                  className="btn btn-error btn-sm"
                >
                  Delete
                </button>
              </div>
            </div>
          </div>
        </div>
      )}

      {!collapsed && (
        <div className="space-y-4">
          {/* Fields grid */}
          <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-5 gap-x-8 gap-y-4 mb-4">
            <div>
              <label className="label text-xs font-medium mb-1">ID</label>
              <input
                type="text"
                value={provider.id}
                disabled
                className="input input-bordered w-full text-sm font-mono opacity-70"
              />
            </div>
            <div>
              <label className="label text-xs font-medium mb-1">Type</label>
              <select
                value={provider.type}
                onChange={(e) => updateField("type", e.target.value)}
                className="select select-bordered w-full text-sm"
              >
                {providerTypes.map((pt) => (
                  <option key={pt.id} value={pt.id}>
                    {pt.name} ({pt.id})
                  </option>
                ))}
              </select>
            </div>
            <div>
              <label className="label text-xs font-medium mb-1">Name</label>
              <input
                type="text"
                value={provider.name}
                placeholder={provider.id}
                onChange={(e) => updateField("name", e.target.value)}
                className="input input-bordered w-full text-sm"
              />
            </div>
            <div>
              <label className="label text-xs font-medium mb-1">API Key</label>
              <input
                type="password"
                value={provider.api_key}
                placeholder="sk-..."
                onChange={(e) => updateField("api_key", e.target.value)}
                className="input input-bordered w-full text-sm font-mono"
              />
            </div>
            <div>
              <label className="label text-xs font-medium mb-1">Base URL</label>
              <input
                type="text"
                value={provider.base_url}
                placeholder={
                  providerDefaults[provider.type]?.base_url || ""
                }
                onChange={(e) => updateField("base_url", e.target.value)}
                className="input input-bordered w-full text-sm font-mono"
              />
            </div>
          </div>

          {/* Custom models section */}
          <div className="mb-4 space-y-4">
            <div className="rounded-xl border border-base-300 bg-base-100 p-4 space-y-4">
              <div className="flex items-start justify-between gap-3 flex-wrap">
                <div>
                  <p className="text-sm font-medium">Custom models</p>
                  <p className="text-xs text-secondary">
                    Add provider-specific models with a guided form. Fetching
                    models only updates discovered models and never overwrites
                    these custom entries.
                  </p>
                </div>
                <button
                  onClick={() => {
                    setCustomModelForm(createCustomModelForm());
                    setShowCustomModelForm(true);
                    setShowModels(true);
                  }}
                  className="btn btn-ghost btn-sm"
                >
                  Add custom model
                </button>
              </div>

              {showCustomModelForm && (
                <div className="rounded-lg border border-base-300 bg-base-200/40 p-4 space-y-4">
                  <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                    <div>
                      <label className="label text-xs font-medium mb-1">
                        Model ID
                      </label>
                      <input
                        type="text"
                        value={customModelForm.id}
                        placeholder="llama3.1:8b"
                        onChange={(e) =>
                          setCustomModelForm((f) => ({
                            ...f,
                            id: e.target.value,
                          }))
                        }
                        className="input input-bordered w-full text-sm font-mono"
                      />
                    </div>
                    <div>
                      <label className="label text-xs font-medium mb-1">
                        Display name
                      </label>
                      <input
                        type="text"
                        value={customModelForm.name}
                        placeholder="Llama 3.1 8B (Local)"
                        onChange={(e) =>
                          setCustomModelForm((f) => ({
                            ...f,
                            name: e.target.value,
                          }))
                        }
                        className="input input-bordered w-full text-sm"
                      />
                    </div>
                    <div>
                      <label className="label text-xs font-medium mb-1">
                        Input
                      </label>
                      <input
                        type="text"
                        value={customModelForm.input}
                        placeholder="text, image"
                        onChange={(e) =>
                          setCustomModelForm((f) => ({
                            ...f,
                            input: e.target.value,
                          }))
                        }
                        className="input input-bordered w-full text-sm"
                      />
                    </div>
                    <div>
                      <label className="label text-xs font-medium mb-1">
                        Output
                      </label>
                      <input
                        type="text"
                        value={customModelForm.output}
                        placeholder="text"
                        onChange={(e) =>
                          setCustomModelForm((f) => ({
                            ...f,
                            output: e.target.value,
                          }))
                        }
                        className="input input-bordered w-full text-sm"
                      />
                    </div>
                    <div>
                      <label className="label text-xs font-medium mb-1">
                        Context window
                      </label>
                      <input
                        type="number"
                        min={0}
                        value={customModelForm.context_window}
                        placeholder="128000"
                        onChange={(e) =>
                          setCustomModelForm((f) => ({
                            ...f,
                            context_window: e.target.value,
                          }))
                        }
                        className="input input-bordered w-full text-sm"
                      />
                    </div>
                    <div>
                      <label className="label text-xs font-medium mb-1">
                        Max tokens
                      </label>
                      <input
                        type="number"
                        min={0}
                        value={customModelForm.max_tokens}
                        placeholder="32000"
                        onChange={(e) =>
                          setCustomModelForm((f) => ({
                            ...f,
                            max_tokens: e.target.value,
                          }))
                        }
                        className="input input-bordered w-full text-sm"
                      />
                    </div>
                  </div>

                  <div className="grid grid-cols-1 md:grid-cols-2 gap-4 items-start">
                    <label className="label cursor-pointer justify-start gap-3 rounded-lg border border-base-300 px-4 py-3">
                      <input
                        type="checkbox"
                        checked={customModelForm.enabled}
                        onChange={(e) =>
                          setCustomModelForm((f) => ({
                            ...f,
                            enabled: e.target.checked,
                          }))
                        }
                        className="toggle toggle-primary toggle-sm"
                      />
                      <div>
                        <p className="text-sm">Enabled</p>
                        <p className="text-xs text-secondary">
                          Persist model availability on this provider instance.
                        </p>
                      </div>
                    </label>
                    <label className="label cursor-pointer justify-start gap-3 rounded-lg border border-base-300 px-4 py-3">
                      <input
                        type="checkbox"
                        checked={customModelForm.reasoning}
                        onChange={(e) =>
                          setCustomModelForm((f) => ({
                            ...f,
                            reasoning: e.target.checked,
                          }))
                        }
                        className="toggle toggle-primary toggle-sm"
                      />
                      <div>
                        <p className="text-sm">Reasoning</p>
                        <p className="text-xs text-secondary">
                          Stores the provider-facing{" "}
                          <span className="font-mono">reasoning</span> flag.
                        </p>
                      </div>
                    </label>
                  </div>

                  <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-4 gap-4">
                    {[
                      {
                        label: "Cost input",
                        field: "cost_input" as const,
                      },
                      {
                        label: "Cost output",
                        field: "cost_output" as const,
                      },
                      {
                        label: "Cost cacheRead",
                        field: "cost_cache_read" as const,
                      },
                      {
                        label: "Cost cacheWrite",
                        field: "cost_cache_write" as const,
                      },
                    ].map(({ label, field }) => (
                      <div key={field}>
                        <label className="label text-xs font-medium mb-1">
                          {label}
                        </label>
                        <input
                          type="number"
                          step="any"
                          value={customModelForm[field]}
                          placeholder="0"
                          onChange={(e) =>
                            setCustomModelForm((f) => ({
                              ...f,
                              [field]: e.target.value,
                            }))
                          }
                          className="input input-bordered w-full text-sm"
                        />
                      </div>
                    ))}
                  </div>

                  <div className="flex items-center gap-3 justify-end">
                    <button
                      onClick={() => {
                        setCustomModelForm(createCustomModelForm());
                        setShowCustomModelForm(false);
                      }}
                      className="btn btn-ghost btn-sm"
                    >
                      Cancel
                    </button>
                    <button
                      onClick={handleSubmitCustomModel}
                      className="btn btn-primary btn-sm"
                    >
                      {customModelForm.original_id ? "Update model" : "Add model"}
                    </button>
                  </div>
                </div>
              )}

              {/* Advanced JSON editor */}
              <div className="border-t border-base-300 pt-4 space-y-3">
                <button
                  onClick={() => {
                    setProviderJSON(
                      JSON.stringify(providerJSONValue(provider), null, 2),
                    );
                    setShowAdvancedJSON((v) => !v);
                  }}
                  className="text-xs font-mono text-secondary hover:text-base-content"
                >
                  {showAdvancedJSON
                    ? "Hide advanced JSON editor"
                    : "Show advanced JSON editor"}
                </button>
                {showAdvancedJSON && (
                  <div className="space-y-2">
                    <label className="label text-xs font-medium mb-1">
                      Provider JSON
                    </label>
                    <textarea
                      value={providerJSON}
                      onChange={(e) => setProviderJSON(e.target.value)}
                      rows={16}
                      placeholder={`{\n  "type": "openai",\n  "name": "Ollama",\n  "enabled": true,\n  "api_key": "ollama",\n  "base_url": "http://localhost:11434/v1",\n  "models": {}\n}`}
                      className="textarea textarea-bordered w-full text-xs font-mono"
                    />
                    <div className="flex justify-end">
                      <button
                        onClick={handleApplyJSON}
                        className="btn btn-ghost btn-xs"
                      >
                        Apply JSON
                      </button>
                    </div>
                  </div>
                )}
              </div>
            </div>
          </div>

          {/* Actions row */}
          <div className="flex items-center gap-4 flex-wrap">
            <label className="label cursor-pointer justify-start gap-2 py-0">
              <input
                type="checkbox"
                checked={provider.enabled}
                onChange={(e) => updateField("enabled", e.target.checked)}
                className="toggle toggle-primary toggle-sm"
              />
              <span className="label-text text-sm">Enabled</span>
            </label>
            <button onClick={handleSave} className="btn btn-primary btn-sm">
              Save
            </button>
            <button
              onClick={handleFetchModels}
              disabled={fetching}
              className="btn btn-ghost btn-sm text-secondary"
            >
              {fetching && (
                <span className="loading loading-spinner loading-xs"></span>
              )}
              {fetching ? "Fetching..." : "Fetch models"}
            </button>
            {modelCount > 0 && (
              <button
                onClick={() => setShowModels((v) => !v)}
                className="btn btn-ghost btn-xs font-mono text-primary"
              >
                {modelCount} models {showModels ? "↑" : "↓"}
              </button>
            )}
          </div>

          {/* Model list */}
          {showModels && (
            <div className="mt-4 pl-4 border-l-2 border-base-300 space-y-3">
              <p className="text-xs text-secondary">
                Toggle models on or off, then save. Custom models can be edited
                with the form or in the advanced JSON editor.
              </p>
              {models.length > 0 ? (
                <div className="max-h-80 overflow-y-auto space-y-2">
                  {models.map((m) => (
                    <div
                      key={`${m.id}:${m.source}`}
                      className="flex items-center justify-between gap-3 rounded-lg border border-base-300 px-3 py-2"
                    >
                      <div className="min-w-0 space-y-1">
                        <div className="flex items-center gap-2 flex-wrap">
                          <span className="font-mono text-sm">{m.id}</span>
                          <span className="badge badge-ghost badge-xs">
                            {m.source}
                          </span>
                          <span
                            className={`badge badge-xs ${m.enabled ? "badge-success" : "badge-ghost"}`}
                          >
                            {m.enabled ? "enabled" : "disabled"}
                          </span>
                        </div>
                        {m.name && m.name !== m.id && (
                          <p className="text-xs text-secondary">{m.name}</p>
                        )}
                      </div>
                      <div className="flex items-center gap-3 shrink-0">
                        <label className="label cursor-pointer justify-start gap-2 py-0">
                          <input
                            type="checkbox"
                            checked={m.enabled}
                            onChange={() => handleToggleModel(m)}
                            className="toggle toggle-primary toggle-sm"
                          />
                          <span className="label-text text-sm">Enabled</span>
                        </label>
                        {m.source === "custom" && (
                          <>
                            <button
                              onClick={() => handleEditCustomModel(m)}
                              className="btn btn-ghost btn-xs"
                            >
                              edit
                            </button>
                            <button
                              onClick={() => handleRemoveCustomModel(m.id)}
                              className="btn btn-ghost btn-xs text-error"
                            >
                              remove
                            </button>
                          </>
                        )}
                      </div>
                    </div>
                  ))}
                </div>
              ) : (
                <div className="text-xs text-secondary py-2">
                  No models yet. Fetch from the provider or add custom models
                  above.
                </div>
              )}
            </div>
          )}
        </div>
      )}
    </div>
  );
}

// ── ProvidersPage ─────────────────────────────────────────────────────────────

export function ProvidersPage() {
  const [providers, setProviders] = useState<Provider[]>([]);
  const [providerModels, setProviderModels] = useState<
    Record<string, ProviderModel[]>
  >({});
  const [providerTypes, setProviderTypes] = useState<ProviderType[]>([]);
  const [providerDefaults, setProviderDefaults] = useState<
    Record<string, { base_url: string; name: string }>
  >({});
  const [newProviderType, setNewProviderType] = useState("");
  const [newProviderID, setNewProviderID] = useState("");
  const [newProviderName, setNewProviderName] = useState("");
  const [toasts, setToasts] = useState<ToastMsg[]>([]);

  const showToast = useCallback(
    (text: string, kind: "success" | "error" = "success") => {
      const id = ++toastSeq;
      setToasts((prev) => [...prev, { id, text, kind }]);
      setTimeout(() => setToasts((prev) => prev.filter((t) => t.id !== id)), 3000);
    },
    [],
  );

  const loadProviderTypes = useCallback(async () => {
    try {
      const types =
        (await api<ProviderType[]>("GET", "/api/provider-types")) || [];
      const defaults: Record<string, { base_url: string; name: string }> = {};
      for (const t of types) {
        defaults[t.id] = { base_url: t.default_url, name: t.name };
      }
      setProviderDefaults(defaults);
      setProviderTypes(
        [...types]
          .sort(
            (a, b) => (a.name || a.id).localeCompare(b.name || b.id),
          )
          .map((t) => ({ id: t.id, name: t.name || t.id, default_url: t.default_url })),
      );
    } catch (e) {
      console.error("Failed to load provider types:", e);
    }
  }, []);

  const loadProviderModels = useCallback(
    async (providerID: string) => {
      try {
        const models =
          (await api<ProviderModel[]>(
            "GET",
            `/api/providers/${providerID}/models`,
          )) || [];
        setProviderModels((prev) => ({ ...prev, [providerID]: models }));
      } catch {
        setProviderModels((prev) => ({ ...prev, [providerID]: [] }));
      }
    },
    [],
  );

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

  // Set default newProviderType when types load
  useEffect(() => {
    if (providerTypes.length > 0 && !newProviderType) {
      setNewProviderType(providerTypes[0].id);
    }
  }, [providerTypes, newProviderType]);

  const handleAddProvider = async () => {
    if (!newProviderType) return;
    const providerID = newProviderID.trim();
    if (!providerID) {
      showToast("Provider ID is required", "error");
      return;
    }
    if (providers.find((p) => p.id === providerID)) {
      showToast("Provider ID already exists", "error");
      return;
    }
    const d = providerDefaults[newProviderType] || {};
    try {
      await api("POST", "/api/providers", {
        id: providerID,
        type: newProviderType,
        name: newProviderName.trim() || d.name || providerID,
        enabled: true,
        api_key: "",
        base_url: "",
        models: {},
      });
      await loadProviders();
      setNewProviderType(providerTypes[0]?.id || "");
      setNewProviderID("");
      setNewProviderName("");
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
      await loadProviders();
      showToast("Deleted");
    } catch (e) {
      showToast(e instanceof Error ? e.message : String(e), "error");
    }
  };

  const handleFetchModels = async (p: Provider) => {
    try {
      const models = await api<ProviderModel[]>(
        "POST",
        `/api/providers/${p.id}/models`,
        { api_key: p.api_key, base_url: p.base_url },
      );
      const list = models || [];
      setProviderModels((prev) => ({ ...prev, [p.id]: list }));
      showToast(`${list.length} models available`);
    } catch (e) {
      showToast(e instanceof Error ? e.message : String(e), "error");
    }
  };

  const handleToggleModel = (
    providerId: string,
    model: ProviderModel,
    enabled: boolean,
  ) => {
    setProviderModels((prev) => {
      const list = prev[providerId] || [];
      return {
        ...prev,
        [providerId]: list.map((m) =>
          m.id === model.id && m.source === model.source
            ? { ...m, enabled }
            : m,
        ),
      };
    });
  };

  const handleRemoveCustomModel = (providerId: string, modelID: string) => {
    setProviderModels((prev) => {
      const list = prev[providerId] || [];
      return {
        ...prev,
        [providerId]: list.filter(
          (m) => !(m.id === modelID && m.source === "custom"),
        ),
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
      list.push({ id: modelID, name: form.name || modelID, enabled: form.enabled, source: "custom" });
      return { ...prev, [providerId]: list };
    });
  };

  const grouped = groupProvidersByType(providers);

  return (
    <div>
      {/* Page header */}
      <div className="mb-8">
        <h1 className="font-serif text-2xl tracking-tight mb-1">
          LLM connections
        </h1>
        <p className="text-sm text-secondary">
          API keys and endpoints for each model provider Anna can use.
        </p>
      </div>

      <div className="border-t border-base-300 pt-8">
        {/* Add provider bar */}
        <div className="grid grid-cols-1 md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_minmax(0,1fr)_auto] gap-3 mb-8">
          <select
            value={newProviderType}
            onChange={(e) => setNewProviderType(e.target.value)}
            className="select select-bordered text-sm"
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
          <input
            value={newProviderID}
            onChange={(e) => setNewProviderID(e.target.value)}
            type="text"
            placeholder="provider id (e.g. openrouter)"
            className="input input-bordered text-sm font-mono"
          />
          <input
            value={newProviderName}
            onChange={(e) => setNewProviderName(e.target.value)}
            type="text"
            placeholder="display name"
            className="input input-bordered text-sm"
          />
          <button
            onClick={handleAddProvider}
            disabled={!newProviderType || !newProviderID}
            className="btn btn-primary btn-sm"
          >
            Add
          </button>
        </div>

        {/* Empty state */}
        {providers.length === 0 && (
          <div className="py-16 text-center">
            <p className="text-sm text-secondary">
              No providers yet. Add one above to connect Anna to an LLM API.
            </p>
          </div>
        )}

        {/* Provider list grouped by type */}
        <div className="space-y-8">
          {grouped.map((group) => (
            <div key={group.type}>
              <div className="flex items-center gap-3 mb-4">
                <h3 className="text-sm font-medium">
                  {providerDefaults[group.type]?.name || group.type}
                </h3>
                <span className="badge badge-ghost badge-sm font-mono">
                  {group.type}
                </span>
                <span className="text-xs text-secondary">
                  {group.providers.length} configured
                </span>
              </div>
              <div className="divide-y divide-base-300 border border-base-300 rounded-xl px-4">
                {group.providers.map((p) => (
                  <ProviderRow
                    key={p.id}
                    provider={p}
                    models={providerModels[p.id] || []}
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
                ))}
              </div>
            </div>
          ))}
        </div>
      </div>

      <Toast messages={toasts} />
    </div>
  );
}

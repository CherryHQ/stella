import type { CustomModelForm, ModelConfig, Provider } from "@/lib/types";

// A new model declares no input modality by default. `input` is no longer
// advisory: an explicit list without "image" now means the model CANNOT see, so
// seeding "text" would silently turn image understanding off for every custom
// model an operator adds. Empty means "undeclared", which fails open. The field
// placeholder ("text, image") carries the hint instead.
export function createCustomModelForm(): CustomModelForm {
  return {
    original_id: "",
    id: "",
    name: "",
    enabled: true,
    reasoning: false,
    input: "",
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

type ProviderJsonValue =
  | string
  | number
  | boolean
  | null
  | ProviderJsonObject
  | ProviderJsonValue[];
type ProviderJsonObject = { readonly [key: string]: ProviderJsonValue };
type ProviderModels = NonNullable<Provider["models"]>;
type ProviderJSON = {
  type: string;
  name: string;
  enabled: boolean;
  api_key: string;
  base_url: string;
  models: ProviderModels;
};

function isString(value: ProviderJsonValue | undefined): value is string {
  return typeof value === "string";
}

function isNumber(value: ProviderJsonValue | undefined): value is number {
  return typeof value === "number";
}

function isBoolean(value: ProviderJsonValue | undefined): value is boolean {
  return typeof value === "boolean";
}

function isProviderJsonObject(value: ProviderJsonValue | undefined): value is ProviderJsonObject {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function textValue(value: ProviderJsonValue | undefined): string {
  if (value === undefined || value === null) return "";
  if (isString(value)) return value;
  if (isNumber(value) || isBoolean(value)) return String(value);
  return "";
}

function numberOrZero(value: string | number): number {
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : 0;
}

export function modelConfigFromForm(form: CustomModelForm): ModelConfig {
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

export function formFromModelConfig(
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
  form.context_window = config?.contextWindow != null ? String(config.contextWindow) : "";
  form.max_tokens = config?.maxTokens != null ? String(config.maxTokens) : "";
  form.cost_input = config?.cost?.input != null ? String(config.cost.input) : "";
  form.cost_output = config?.cost?.output != null ? String(config.cost.output) : "";
  form.cost_cache_read = config?.cost?.cacheRead != null ? String(config.cost.cacheRead) : "";
  form.cost_cache_write = config?.cost?.cacheWrite != null ? String(config.cost.cacheWrite) : "";
  return form;
}

export function providerJSONValue(p: Provider): ProviderJSON {
  return {
    type: p.type,
    name: p.name,
    enabled: p.enabled,
    api_key: p.api_key,
    base_url: p.base_url,
    models: p.models || {},
  };
}

export function parseProviderJSON(raw: string, provider: Provider): Provider {
  const trimmed = raw.trim();
  if (!trimmed) throw new Error("Provider JSON is required");
  let parsed: ProviderJsonValue;
  try {
    // SAFETY: JSON.parse is followed by the object contract check before any fields are read.
    parsed = JSON.parse(trimmed);
  } catch (e) {
    throw new Error("Provider JSON is invalid: " + String(e));
  }
  if (!isProviderJsonObject(parsed)) {
    throw new Error("Provider JSON must be an object");
  }
  // SAFETY: provider model entries use the generated provider model contract at this API boundary.
  const models = isProviderJsonObject(parsed.models) ? (parsed.models as ProviderModels) : {};
  return {
    ...provider,
    type: (textValue(parsed.type) || provider.type).trim(),
    name: (textValue(parsed.name) || provider.name || provider.id).trim() || provider.id,
    enabled: parsed.enabled !== false,
    api_key: textValue(parsed.api_key),
    base_url: textValue(parsed.base_url),
    models,
  };
}

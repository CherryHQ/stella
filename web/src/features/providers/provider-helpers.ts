import type { Provider } from "@/lib/types";

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
  const models = isProviderJsonObject(parsed.models)
    ? (parsed.models as ProviderModels)
    : (provider.models ?? {});
  const apiKey = parsed.api_key === undefined ? provider.api_key : textValue(parsed.api_key);
  return {
    ...provider,
    type: (textValue(parsed.type) || provider.type).trim(),
    name: (textValue(parsed.name) || provider.name || provider.id).trim() || provider.id,
    enabled: parsed.enabled === undefined ? provider.enabled : parsed.enabled !== false,
    api_key: apiKey === "••••" ? provider.api_key : apiKey,
    base_url: parsed.base_url === undefined ? provider.base_url : textValue(parsed.base_url),
    models,
  };
}

// Read/write model for a provider's effective models.
//
// The server merges three layers into one effective model (see
// `internal/model/resolve`): catalog metadata, provider discovery, then the
// provider's own sparse override. The UI has to show all three at once — what
// a field resolves to, where that value came from, and whether an operator
// pinned it — while writing back nothing but the fields the operator touched.
// Every write helper here is field-scoped for that reason: copying an
// inherited catalog value into the provider record would freeze it against
// future catalog updates.
import type {
  CatalogModel,
  ComponentsProviderModelCost,
  ProviderModelOverride,
} from "@/lib/api-client/types.gen";
import type { Provider, ProviderModel } from "@/lib/types";

export type ProviderOverrides = NonNullable<Provider["models"]>;
export type ModelCost = ComponentsProviderModelCost;

/** Override fields the detail view edits one at a time. */
export const OVERRIDE_KEYS = [
  "name",
  "enabled",
  "reasoning",
  "input",
  "output",
  "contextWindow",
  "maxTokens",
] as const;
export type OverrideKey = (typeof OVERRIDE_KEYS)[number];
export type CatalogMatch = string | undefined;
export const CATALOG_MODEL_RESULT_LIMIT = 50;

/** Per-token rates, all quoted per 1M tokens. */
export const COST_KEYS = [
  "input",
  "output",
  "cacheRead",
  "cacheWrite",
  "reasoning",
  "inputAudio",
  "outputAudio",
] as const;
export type CostKey = (typeof COST_KEYS)[number];

export interface OverrideValues {
  name: string;
  enabled: boolean;
  reasoning: boolean;
  input: string[];
  output: string[];
  contextWindow: number;
  maxTokens: number;
}

/**
 * Where a field's effective value comes from.
 * `provider` means the provider listed the model but supplied no metadata for
 * this field, so the runtime falls back to its own default.
 */
export type FieldOrigin = "override" | "catalog" | "provider" | "default";

export type ModelSource = ProviderModel["source"];
export type ModelStatusFilter = "all" | "enabled" | "disabled" | "overridden";
export type ModelCapability = "reasoning" | "tools" | "structured" | "vision" | "audio";

function isSet<T>(value: T | null | undefined): value is T {
  return value !== undefined && value !== null;
}

export function overrideOf(
  overrides: ProviderOverrides | undefined,
  modelID: string,
): ProviderModelOverride | undefined {
  return overrides?.[modelID];
}

export function isOverridden(
  override: ProviderModelOverride | undefined,
  key: OverrideKey,
): boolean {
  return isSet(override?.[key]);
}

export function isCostOverridden(
  override: ProviderModelOverride | undefined,
  key: CostKey,
): boolean {
  return isSet(override?.cost?.[key]);
}

/** How many fields an operator has explicitly pinned on this model. */
export function overrideCount(override: ProviderModelOverride | undefined): number {
  if (!override) return 0;
  let count = isSet(override.catalogModel) ? 1 : 0;
  for (const key of OVERRIDE_KEYS) if (isOverridden(override, key)) count += 1;
  for (const key of COST_KEYS) if (isCostOverridden(override, key)) count += 1;
  if (override.cost?.tiers?.length) count += 1;
  return count;
}

/** Catalog keys line up with override keys except `enabled`, which the catalog never carries. */
function catalogHas(catalog: CatalogModel | undefined, key: OverrideKey): boolean {
  if (!catalog || key === "enabled") return false;
  return isSet(catalog[key]);
}

export function fieldOrigin(
  model: ProviderModel,
  override: ProviderModelOverride | undefined,
  key: OverrideKey,
): FieldOrigin {
  if (isOverridden(override, key)) return "override";
  if (catalogHas(model.catalog, key)) return "catalog";
  return model.source === "fetched" ? "provider" : "default";
}

export function costOrigin(
  model: ProviderModel,
  override: ProviderModelOverride | undefined,
  key: CostKey,
): FieldOrigin {
  if (isCostOverridden(override, key)) return "override";
  if (isSet(model.catalog?.cost?.[key])) return "catalog";
  return model.source === "fetched" ? "provider" : "default";
}

/**
 * The value a field resolves to right now. The local override wins because it
 * may hold an edit the effective-model query has not caught up with yet.
 */
export function effectiveValue<K extends OverrideKey>(
  model: ProviderModel,
  override: ProviderModelOverride | undefined,
  key: K,
): OverrideValues[K] | undefined {
  // ProviderModelOverride, ProviderModel and OverrideValues share this key set
  // and its value types; the override only widens each of them with null, and
  // `enabled` is the one key the effective row carries outside `config`.
  const pinned = override?.[key];
  if (isSet(pinned)) {
    // SAFETY: same key set, same value type; the override only adds null.
    return pinned as OverrideValues[K];
  }
  if (key === "enabled") {
    // Enablement defaults come from the provider's model policy, which the row
    // does not expose separately. The server's effective flag is authoritative.
    // SAFETY: `enabled` has the same boolean value type in both shapes.
    return model.enabled as OverrideValues[K];
  }
  // Do not read `config` here. It already contains persisted overrides, so a
  // local reset would keep showing the stale pinned value until the refetch.
  return inheritedValue(model, key);
}

/**
 * The value a field would resolve to with the operator's pin removed.
 *
 * `config` cannot answer this: it is the server's already-resolved model, so a
 * saved override is baked into it. The catalog entry is the inherited layer
 * exactly — `internal/model/resolve` fills the effective model from the catalog
 * and then applies the override on top, with only the model ID as a fallback
 * name. `enabled` has no inherited value here; it comes from the provider's
 * model policy, not the catalog.
 */
export function inheritedValue<K extends OverrideKey>(
  model: ProviderModel,
  key: K,
): OverrideValues[K] | undefined {
  if (key === "enabled") return undefined;
  if (key === "name") {
    // SAFETY: `name` is a string in both the catalog entry and OverrideValues.
    return (model.catalog?.name || model.id) as OverrideValues[K];
  }
  if (!model.catalog) return undefined;
  // SAFETY: the catalog entry carries every override key except `name` and
  // `enabled` with the same value types, and both return above.
  const value = (model.catalog as Partial<OverrideValues>)[key];
  return isSet(value) ? value : undefined;
}

/** The price table a model inherits before any pinned rate is applied. */
export function inheritedCost(model: ProviderModel): ModelCost | undefined {
  return model.catalog?.cost;
}

export function effectiveCost(
  model: ProviderModel,
  override: ProviderModelOverride | undefined,
): ModelCost | undefined {
  // `config.cost` is already merged with persisted overrides. Starting from
  // the catalog layer makes clearing a pin take effect immediately instead of
  // showing the stale saved rate until the effective-model query refetches.
  const base = inheritedCost(model);
  const pinned = override?.cost;
  if (!base && !pinned) return undefined;
  const merged: ModelCost = { ...base };
  for (const key of COST_KEYS) {
    const value = pinned?.[key];
    if (isSet(value)) merged[key] = value;
  }
  if (pinned?.tiers) merged.tiers = pinned.tiers;
  return merged;
}

function writeOverride(
  overrides: ProviderOverrides | undefined,
  modelID: string,
  next: ProviderModelOverride,
): ProviderOverrides {
  const result: ProviderOverrides = { ...overrides };
  // An override that pins nothing is not an empty object in the provider
  // record — it is no record at all, so the model reverts to pure inheritance.
  if (Object.keys(next).length === 0) delete result[modelID];
  else result[modelID] = next;
  return result;
}

function pruneCost(cost: ModelCost | undefined): ModelCost | undefined {
  if (!cost) return undefined;
  const kept: ModelCost = {};
  for (const key of COST_KEYS) if (isSet(cost[key])) kept[key] = cost[key];
  if (cost.tiers?.length) kept.tiers = cost.tiers;
  return Object.keys(kept).length > 0 ? kept : undefined;
}

/**
 * Pins one field on a model, or clears it when `value` is undefined. Only the
 * named key changes: nothing inherited is copied in, so a catalog refresh keeps
 * moving every field the operator never touched.
 */
export function withFieldOverride<K extends OverrideKey>(
  overrides: ProviderOverrides | undefined,
  modelID: string,
  override: ProviderModelOverride | undefined,
  key: K,
  value: OverrideValues[K] | undefined,
): ProviderOverrides {
  const next: ProviderModelOverride = { ...override };
  if (value === undefined) delete next[key];
  else next[key] = value;
  return writeOverride(overrides, modelID, next);
}

/**
 * Searches the Catalog without handing thousands of rows to the Combobox. The
 * selected row is retained even when it falls outside the first result page so
 * Base UI always receives a value that exists in its current item collection.
 */
export function matchingCatalogModels(
  catalogModels: CatalogModel[],
  query: string,
  selectedModel: string | null | undefined,
): CatalogModel[] {
  const normalized = query.trim().toLowerCase();
  const matches = catalogModels.filter((candidate) => {
    if (!normalized) return true;
    const haystack = [candidate.id, candidate.name, candidate.family]
      .filter(Boolean)
      .join(" ")
      .toLowerCase();
    return normalized.split(/\s+/).every((term) => haystack.includes(term));
  });
  const visible = matches.slice(0, CATALOG_MODEL_RESULT_LIMIT);
  if (!selectedModel) return visible;
  const selected = catalogModels.find((candidate) => candidate.id === selectedModel);
  if (selected && !visible.some((candidate) => candidate.id === selected.id)) {
    visible.push(selected);
  }
  return visible;
}

/** Returns the Catalog metadata currently selected in the unsaved UI state. */
export function selectedCatalogModel(
  model: ProviderModel,
  override: ProviderModelOverride | undefined,
  catalogModels: CatalogModel[],
): CatalogModel | undefined {
  if (override?.catalogModel === "") return undefined;
  if (!override?.catalogModel) return model.catalog;
  return catalogModels.find((candidate) => candidate.id === override.catalogModel);
}

/**
 * Selects a Catalog metadata source. A missing model restores automatic matching
 * for catalog-backed Providers; an empty model explicitly leaves it unmatched.
 */
export function withCatalogMatch(
  overrides: ProviderOverrides | undefined,
  modelID: string,
  override: ProviderModelOverride | undefined,
  catalogModel: CatalogMatch,
): ProviderOverrides {
  const next: ProviderModelOverride = { ...override };
  if (catalogModel === undefined) delete next.catalogModel;
  else next.catalogModel = catalogModel;
  return writeOverride(overrides, modelID, next);
}

/** Pins or clears one per-token rate, leaving every other rate inherited. */
export function withCostOverride(
  overrides: ProviderOverrides | undefined,
  modelID: string,
  override: ProviderModelOverride | undefined,
  key: CostKey,
  value: number | undefined,
): ProviderOverrides {
  const cost: ModelCost = { ...override?.cost };
  if (value === undefined) delete cost[key];
  else cost[key] = value;
  const next: ProviderModelOverride = { ...override };
  const pruned = pruneCost(cost);
  if (pruned) next.cost = pruned;
  else delete next.cost;
  return writeOverride(overrides, modelID, next);
}

/** Drops every pinned field, returning the model to full inheritance. */
export function withoutModelOverride(
  overrides: ProviderOverrides | undefined,
  modelID: string,
): ProviderOverrides {
  const result: ProviderOverrides = { ...overrides };
  delete result[modelID];
  return result;
}

/** Bulk enable/disable writes one `enabled` key per model in a single save. */
export function withEnabledOverrides(
  overrides: ProviderOverrides | undefined,
  models: ProviderModel[],
  enabled: boolean,
): ProviderOverrides {
  let result: ProviderOverrides = { ...overrides };
  for (const model of models) {
    result = withFieldOverride(result, model.id, result[model.id], "enabled", enabled);
  }
  return result;
}

export function capabilitiesOf(
  model: ProviderModel,
  override: ProviderModelOverride | undefined,
): ModelCapability[] {
  const input = effectiveValue(model, override, "input") ?? [];
  const output = effectiveValue(model, override, "output") ?? [];
  const capabilities: ModelCapability[] = [];
  if (effectiveValue(model, override, "reasoning")) capabilities.push("reasoning");
  if (model.catalog?.tool_call) capabilities.push("tools");
  if (model.catalog?.structured_output) capabilities.push("structured");
  if (input.includes("image") || input.includes("video") || model.catalog?.attachment) {
    capabilities.push("vision");
  }
  if (input.includes("audio") || output.includes("audio")) capabilities.push("audio");
  return capabilities;
}

export interface ModelFilters {
  search: string;
  source: ModelSource | "all";
  status: ModelStatusFilter;
}

export function matchesModelFilters(
  model: ProviderModel,
  override: ProviderModelOverride | undefined,
  filters: ModelFilters,
): boolean {
  const needle = filters.search.trim().toLowerCase();
  if (needle) {
    const haystack = [model.id, model.name, model.catalog?.name, model.catalog?.family]
      .filter(Boolean)
      .join(" ")
      .toLowerCase();
    if (!haystack.includes(needle)) return false;
  }
  if (filters.source !== "all" && model.source !== filters.source) return false;
  const enabled = effectiveValue(model, override, "enabled") ?? model.enabled;
  if (filters.status === "enabled" && !enabled) return false;
  if (filters.status === "disabled" && enabled) return false;
  if (filters.status === "overridden" && overrideCount(override) === 0) return false;
  return true;
}

// ── formatting ───────────────────────────────────────────────────────────────

/**
 * 128000 → "128K", 16384 → "16K". Limits are scanned and compared, so they
 * round to the unit an operator quotes rather than to the byte.
 */
export function formatTokenLimit(value: number | undefined): string {
  if (!isSet(value) || !value) return "—";
  if (value >= 1_000_000) return `${Number((value / 1_000_000).toFixed(1))}M`;
  if (value >= 1000) return `${Math.round(value / 1000)}K`;
  return String(value);
}

/** Rates are quoted per 1M tokens; an explicit 0 is free, absent is unknown. */
export function formatRate(value: number | null | undefined): string {
  if (!isSet(value)) return "—";
  const amount = Number(value);
  if (amount === 0) return "$0";
  if (amount < 0.01) return `$${amount.toFixed(4)}`;
  return `$${amount.toFixed(2)}`;
}

export function formatModalities(list: string[] | undefined): string {
  return list && list.length > 0 ? list.join(", ") : "—";
}

export function parseModalities(raw: string): string[] {
  return raw
    .split(",")
    .map((entry) => entry.trim())
    .filter(Boolean);
}

/** Blank clears the pin; a non-numeric draft is rejected by returning null. */
export function parseNumberDraft(raw: string): number | undefined | null {
  const trimmed = raw.trim();
  if (!trimmed) return undefined;
  const parsed = Number(trimmed);
  if (!Number.isFinite(parsed) || parsed < 0) return null;
  return parsed;
}

/** The one-line price summary shown on a collapsed row. */
export function formatPriceSummary(cost: ModelCost | undefined, freeLabel: string): string {
  if (!cost) return "";
  const input = cost.input ?? 0;
  const output = cost.output ?? 0;
  if (!isSet(cost.input) && !isSet(cost.output)) return "";
  if (input === 0 && output === 0) return freeLabel;
  return `${formatRate(input)} / ${formatRate(output)}`;
}

// ── translation keys ─────────────────────────────────────────────────────────
// Literal maps rather than template keys: `t()` is typed against the message
// catalogue, so a key built by interpolation type-checks as a plain string and
// stops catching typos.

export const SOURCE_LABEL_KEYS = {
  catalog: "providers.catalog",
  fetched: "providers.fetched",
  custom: "providers.custom",
} as const satisfies Record<ModelSource, string>;

export const ORIGIN_LABEL_KEYS = {
  override: "providers.origin.override",
  catalog: "providers.origin.catalog",
  provider: "providers.origin.provider",
  default: "providers.origin.default",
} as const satisfies Record<FieldOrigin, string>;

export const CAPABILITY_LABEL_KEYS = {
  reasoning: "providers.capability.reasoning",
  tools: "providers.capability.tools",
  structured: "providers.capability.structured",
  vision: "providers.capability.vision",
  audio: "providers.capability.audio",
} as const satisfies Record<ModelCapability, string>;

export const RATE_LABEL_KEYS = {
  input: "providers.rate.input",
  output: "providers.rate.output",
  cacheRead: "providers.rate.cacheRead",
  cacheWrite: "providers.rate.cacheWrite",
  reasoning: "providers.rate.reasoning",
  inputAudio: "providers.rate.inputAudio",
  outputAudio: "providers.rate.outputAudio",
} as const satisfies Record<CostKey, string>;

import type {
  ManifestPlugin,
  ManifestPluginDefinition,
  ManifestPluginDefinitionField,
  Plugin,
  PluginSchemaField,
  PluginSchemaProperty,
  PluginWithMeta,
} from "@/lib/types";

export function pluginLabel(plugin: Plugin | PluginWithMeta): string {
  return plugin.display_name || plugin.name || plugin.id;
}

export function pluginDescription(plugin: Plugin): string {
  return plugin.description || "";
}

export function pluginToggleURL(plugin: Plugin): string {
  return `/api/plugins/${encodeURIComponent(plugin.kind)}/${encodeURIComponent(plugin.name)}`;
}

export function pluginToggleURLByID(id: string, plugins: Plugin[]): string {
  const p = plugins.find((p) => p.id === id);
  if (p) return pluginToggleURL(p);
  const slash = id.indexOf("/");
  return slash !== -1
    ? `/api/plugins/${encodeURIComponent(id.slice(0, slash))}/${encodeURIComponent(id.slice(slash + 1))}`
    : `/api/plugins/${encodeURIComponent(id)}/${encodeURIComponent(id)}`;
}

export function pluginSchemaPath(plugin: Plugin): string {
  return `/api/plugins/${encodeURIComponent(plugin.kind)}/${encodeURIComponent(plugin.name)}/config-schema`;
}

export function pluginConfigPath(plugin: Plugin): string {
  return `/api/plugins/${encodeURIComponent(plugin.kind)}/${encodeURIComponent(plugin.name)}/config`;
}

export function withManifestMeta(
  plugin: Plugin,
  manifestPlugins: ManifestPlugin[],
): PluginWithMeta {
  const manifest = manifestPlugins.find((m) => m.id === plugin.id) || null;
  return {
    ...plugin,
    enabled: manifest ? manifest.enabled : plugin.enabled,
    _manifest: !!manifest,
    _manifestPlugin: manifest,
  };
}

export function manifestOnlyPlugin(manifest: ManifestPlugin): PluginWithMeta {
  return {
    id: manifest.id,
    kind: manifest.kind,
    name: manifest.name,
    display_name: manifest.display_name,
    description: manifest.description,
    enabled: manifest.enabled,
    config: {},
    capabilities: [],
    has_config: false,
    has_status: false,
    _manifest: true,
    _manifestPlugin: manifest,
  };
}

export function sortPlugins(plugins: PluginWithMeta[]): PluginWithMeta[] {
  return [...plugins].sort(
    (a, b) =>
      (pluginLabel(a) || "").localeCompare(pluginLabel(b) || "") ||
      String(a.id || "").localeCompare(String(b.id || "")),
  );
}

export function semanticPlugins(
  kind: string,
  plugins: Plugin[],
  manifestPlugins: ManifestPlugin[],
): PluginWithMeta[] {
  const byID = new Map<string, PluginWithMeta>();
  for (const plugin of plugins) {
    if (plugin.kind === kind) {
      byID.set(plugin.id, withManifestMeta(plugin, manifestPlugins));
    }
  }
  for (const manifest of manifestPlugins) {
    if (manifest.kind === kind && !byID.has(manifest.id)) {
      byID.set(manifest.id, manifestOnlyPlugin(manifest));
    }
  }
  return sortPlugins(Array.from(byID.values()));
}

export function otherPlugins(
  plugins: Plugin[],
  manifestPlugins: ManifestPlugin[],
): PluginWithMeta[] {
  const known = new Set(["tool", "channel", "hook", "memory", "provider", "sandbox"]);
  const byID = new Map<string, PluginWithMeta>();
  for (const plugin of plugins) {
    if (!known.has(plugin.kind)) {
      byID.set(plugin.id, withManifestMeta(plugin, manifestPlugins));
    }
  }
  for (const manifest of manifestPlugins) {
    if (!known.has(manifest.kind) && !byID.has(manifest.id)) {
      byID.set(manifest.id, manifestOnlyPlugin(manifest));
    }
  }
  return sortPlugins(Array.from(byID.values()));
}

// Schema field helpers
export function pluginFieldType(schema: PluginSchemaProperty): string {
  if (Array.isArray(schema?.type)) {
    return schema.type.find((t) => t !== "null") ?? schema.type[0] ?? "string";
  }
  return schema?.type ?? "string";
}

export function pluginFieldHasEnum(field: PluginSchemaField): boolean {
  return Array.isArray(field.schema?.enum) && field.schema.enum.length > 0;
}

export function pluginFieldIsComplex(field: PluginSchemaField): boolean {
  const type = pluginFieldType(field.schema);
  return type === "object" || type === "array";
}

export function pluginFieldIsSecret(field: PluginSchemaField): boolean {
  return /(token|secret|password|api[_-]?key|encrypt[_-]?key)$/i.test(field.name);
}

export function pluginFieldInputType(field: PluginSchemaField): string {
  const type = pluginFieldType(field.schema);
  if (type === "integer" || type === "number") return "number";
  if (pluginFieldIsSecret(field)) return "password";
  return "text";
}

export function pluginFieldDescription(field: PluginSchemaField): string {
  return field.schema?.description || "";
}

export function pluginFieldText(value: unknown): string {
  if (value === undefined || value === null) return "";
  if (typeof value === "string") return value;
  if (typeof value === "number" || typeof value === "boolean" || typeof value === "bigint") {
    return String(value);
  }
  return "";
}

export function pluginFieldPlaceholder(field: PluginSchemaField): string {
  return pluginFieldText(field.schema?.default);
}

export function pluginFieldRows(field: PluginSchemaField): number {
  return pluginFieldType(field.schema) === "object" ? 8 : 6;
}

export function pluginFieldOptionLabel(option: unknown): string {
  if (option === "") return "(empty)";
  return String(option);
}

export function pluginSchemaFields(
  plugin: Plugin,
  schemas: Record<string, { properties?: Record<string, PluginSchemaProperty> }>,
): PluginSchemaField[] {
  const properties = schemas[plugin.id]?.properties || {};
  return Object.entries(properties).map(([name, schema]) => ({ name, schema: schema || {} }));
}

export function hasGenericConfigEditor(
  plugin: Plugin,
  schemas: Record<string, { properties?: Record<string, PluginSchemaProperty> }>,
): boolean {
  return !!plugin && plugin.has_config && pluginSchemaFields(plugin, schemas).length > 0;
}

export function buildPluginConfigDraft(
  plugin: Plugin,
  config: Record<string, unknown>,
  schemas: Record<string, { properties?: Record<string, PluginSchemaProperty> }>,
): Record<string, unknown> {
  const draft: Record<string, unknown> = {};
  for (const field of pluginSchemaFields(plugin, schemas)) {
    const type = pluginFieldType(field.schema);
    let value = config?.[field.name];
    if (value === undefined && field.schema?.default !== undefined) {
      value = JSON.parse(JSON.stringify(field.schema.default));
    }
    if (type === "object" || type === "array") {
      draft[field.name] = value === undefined ? "" : JSON.stringify(value, null, 2);
      continue;
    }
    if (type === "boolean") {
      draft[field.name] = value === undefined ? false : !!value;
      continue;
    }
    draft[field.name] = value === undefined ? "" : value;
  }
  return draft;
}

export function buildPluginConfigPayload(
  plugin: Plugin,
  draft: Record<string, unknown>,
  rawConfig: Record<string, unknown>,
  schemas: Record<string, { properties?: Record<string, PluginSchemaProperty> }>,
): Record<string, unknown> {
  const next = JSON.parse(JSON.stringify(rawConfig || {})) as Record<string, unknown>;
  for (const field of pluginSchemaFields(plugin, schemas)) {
    const type = pluginFieldType(field.schema);
    const value = draft[field.name];
    if (type === "object" || type === "array") {
      const text = pluginFieldText(value).trim();
      if (!text) {
        delete next[field.name];
        continue;
      }
      try {
        next[field.name] = JSON.parse(text);
      } catch {
        throw new Error(`${field.name} must be valid JSON`);
      }
      continue;
    }
    if (type === "boolean") {
      next[field.name] = !!value;
      continue;
    }
    if (type === "integer" || type === "number") {
      if (value === "" || value === null || value === undefined) {
        delete next[field.name];
        continue;
      }
      const parsed = Number(value);
      if (Number.isNaN(parsed)) throw new Error(`${field.name} must be a number`);
      next[field.name] = type === "integer" ? Math.trunc(parsed) : parsed;
      continue;
    }
    const text = pluginFieldText(value);
    if (text === "" && !field.schema?.enum?.includes("")) {
      delete next[field.name];
      continue;
    }
    next[field.name] = text;
  }
  return next;
}

export function formatTimestamp(value: string): string {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return date.toLocaleString();
}

export type PluginBucket = "integration" | "tool" | "system";

// pluginHasOAuth reports whether a plugin authenticates against an external
// account — either a declared oauth_provider or an oauth-sourced session env.
export function pluginHasOAuth(plugin: PluginWithMeta): boolean {
  const m = plugin._manifestPlugin;
  if (!m) return false;
  if (m.oauth_provider) return true;
  return (m.session_env ?? []).some((e) => (e.source ?? "").startsWith("oauth."));
}

// pluginBucket assigns a plugin to one of three UI sections. An explicit
// manifest `category` wins; otherwise it's derived: OAuth-backed → integration,
// hooks → system, everything else → tool.
export function pluginBucket(plugin: PluginWithMeta): PluginBucket {
  const category = plugin._manifestPlugin?.category;
  if (category === "integration" || category === "tool" || category === "system") {
    return category;
  }
  if (pluginHasOAuth(plugin)) return "integration";
  if (plugin.kind === "hook") return "system";
  return "tool";
}

// pluginIsEssential reports whether disabling the plugin would break the harness
// (e.g. rg/fd back Grep/Glob). The toggle is guarded for these.
export function pluginIsEssential(plugin: PluginWithMeta): boolean {
  return !!plugin._manifestPlugin?.essential;
}

// pluginIsRemovable reports whether a plugin can be deleted: only one an admin
// added, whose whole definition is the override row. A builtin ships with the
// server — the next resolve would bring it back, so disabling is its off switch.
export function pluginIsRemovable(plugin: PluginWithMeta): boolean {
  return !!plugin._manifestPlugin && !plugin._manifestPlugin.builtin;
}

// pluginIsCustomized reports whether a builtin's definition has been edited and
// so no longer follows the one shipped with the server. Only a builtin can be:
// an admin-added plugin has no shipped definition to diverge from.
export function pluginIsCustomized(plugin: PluginWithMeta): boolean {
  const manifest = plugin._manifestPlugin;
  return !!manifest?.builtin && (manifest.overridden_fields?.length ?? 0) > 0;
}

// pluginFieldIsOverridden reports whether one builtin definition field is
// pinned by an admin instead of following the value shipped by the server.
export function pluginFieldIsOverridden(
  plugin: PluginWithMeta,
  field: ManifestPluginDefinitionField,
): boolean {
  const manifest = plugin._manifestPlugin;
  return !!manifest?.builtin && !!manifest.overridden_fields?.includes(field);
}

// manifestPluginDefinitionFields is every field an override may take ownership
// of. The generated definition type below pins this runtime list to the OpenAPI
// schema. `kind` and `essential` are deliberately absent because they belong to
// the server rather than the editable definition.
export const manifestPluginDefinitionFields = [
  "name",
  "display_name",
  "description",
  "category",
  "prompt",
  "binaries",
  "skills",
  "session_env",
  "oauth_provider",
] as const satisfies readonly ManifestPluginDefinitionField[];

// Keep the runtime comparison list exhaustive as the generated definition
// grows; invalid and missing names are both compile errors.
const manifestPluginDefinitionFieldsAreExhaustive: Exclude<
  keyof ManifestPluginDefinition,
  (typeof manifestPluginDefinitionFields)[number]
> extends never
  ? true
  : never = true;
void manifestPluginDefinitionFieldsAreExhaustive;

const manifestPluginDefinitionFieldEnumIsExhaustive: Exclude<
  ManifestPluginDefinitionField,
  (typeof manifestPluginDefinitionFields)[number]
> extends never
  ? true
  : never = true;
void manifestPluginDefinitionFieldEnumIsExhaustive;

function valuesEqual(left: unknown, right: unknown): boolean {
  if (Object.is(left, right)) return true;
  if (Array.isArray(left) || Array.isArray(right)) {
    return (
      Array.isArray(left) &&
      Array.isArray(right) &&
      left.length === right.length &&
      left.every((value, index) => valuesEqual(value, right[index]))
    );
  }
  if (left && right && typeof left === "object" && typeof right === "object") {
    const leftRecord = left as Record<string, unknown>;
    const rightRecord = right as Record<string, unknown>;
    const leftKeys = Object.keys(leftRecord)
      .filter((key) => leftRecord[key] !== undefined)
      .sort();
    const rightKeys = Object.keys(rightRecord)
      .filter((key) => rightRecord[key] !== undefined)
      .sort();
    return (
      leftKeys.length === rightKeys.length &&
      leftKeys.every(
        (key, index) => key === rightKeys[index] && valuesEqual(leftRecord[key], rightRecord[key]),
      )
    );
  }
  return false;
}

// emptyAsAbsent collapses the several ways a definition field can say "nothing
// here" into one. The server drops empty lists and empty strings when it
// serializes a definition, so absent, null, "" and [] all reach it identically —
// and the editor rebuilds `binaries: []` on every render whether or not anyone
// touched it. Without this, opening a form and pressing save would claim
// ownership of fields nobody edited.
function emptyAsAbsent(value: unknown): unknown {
  if (value === null || value === "") return undefined;
  if (Array.isArray(value) && value.length === 0) return undefined;
  return value;
}

// changedManifestPluginFields compares the submitted definition with the
// definition loaded when editing began. It deliberately does not compare with
// the server's builtin defaults: the request declares this edit's ownership.
export function changedManifestPluginFields(
  initial: ManifestPlugin,
  next: ManifestPlugin,
): ManifestPluginDefinitionField[] {
  const initialRecord = initial as unknown as Record<string, unknown>;
  const nextRecord = next as unknown as Record<string, unknown>;
  return manifestPluginDefinitionFields.filter(
    (field) => !valuesEqual(emptyAsAbsent(initialRecord[field]), emptyAsAbsent(nextRecord[field])),
  );
}

// pluginHasBinaries reports whether a manifest plugin installs at least one
// binary — these get the compact version editor in the detail sheet.
export function pluginHasBinaries(plugin: PluginWithMeta): boolean {
  return (plugin._manifestPlugin?.binaries?.length ?? 0) > 0;
}

// deriveToolName extracts a plugin name from a mise tool key by taking the last
// path/backend segment: "claude" → "claude", "github:cli/cli" → "cli",
// "npm:@anthropic-ai/claude-code" → "claude-code", "cargo:fd-find" → "fd-find".
export function deriveToolName(toolKey: string): string {
  const key = toolKey.trim();
  const afterSlash = key.includes("/") ? key.slice(key.lastIndexOf("/") + 1) : key;
  const afterColon = afterSlash.includes(":")
    ? afterSlash.slice(afterSlash.lastIndexOf(":") + 1)
    : afterSlash;
  return afterColon;
}

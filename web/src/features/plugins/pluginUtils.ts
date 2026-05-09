import type {
  ManifestBinary,
  ManifestPlugin,
  ManifestSessionEnv,
  McpServer,
  McpStatus,
  Plugin,
  PluginSchemaField,
  PluginSchemaProperty,
  PluginWithMeta,
} from "@/lib/types";

// Row ID counter (module-level, simple incrementing)
let rowIDCounter = 0;
export function nextRowID(): number {
  rowIDCounter += 1;
  return rowIDCounter;
}

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
  return `/api/plugin-config-schema/${encodeURIComponent(plugin.kind)}/${encodeURIComponent(plugin.name)}`;
}

export function pluginConfigPath(plugin: Plugin): string {
  return `/api/plugin-config/${encodeURIComponent(plugin.kind)}/${encodeURIComponent(plugin.name)}`;
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
  return (
    !!plugin &&
    plugin.id !== "tool/mcp" &&
    plugin.has_config &&
    pluginSchemaFields(plugin, schemas).length > 0
  );
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

// MCP helpers
export function createArgsRows(args: string[]): { id: number; value: string }[] {
  if (!Array.isArray(args) || args.length === 0) {
    return [{ id: nextRowID(), value: "" }];
  }
  return args.map((value) => ({ id: nextRowID(), value: String(value || "") }));
}

export function createKeyValueRows(
  entries: Record<string, string>,
): { id: number; key: string; value: string }[] {
  const pairs = Object.entries(entries || {});
  if (pairs.length === 0) {
    return [{ id: nextRowID(), key: "", value: "" }];
  }
  return pairs.map(([key, value]) => ({ id: nextRowID(), key, value: String(value || "") }));
}

export function normalizeMcpServers(servers: Record<string, unknown>[]): McpServer[] {
  return (servers || []).map((server) => ({
    id: nextRowID(),
    expanded: true,
    name: pluginFieldText(server.name),
    enabled: server.enabled !== false,
    transport: pluginFieldText(server.transport) || "stdio",
    command: pluginFieldText(server.command),
    url: pluginFieldText(server.url),
    timeout_seconds: Number(server.timeout_seconds || 30),
    args: createArgsRows((server.args as string[]) || []),
    env: createKeyValueRows((server.env as Record<string, string>) || {}),
    headers: createKeyValueRows((server.headers as Record<string, string>) || {}),
  }));
}

export function newMcpServer(): McpServer {
  return {
    id: nextRowID(),
    expanded: true,
    name: "",
    enabled: true,
    transport: "stdio",
    command: "",
    url: "",
    timeout_seconds: 30,
    args: createArgsRows([]),
    env: createKeyValueRows({}),
    headers: createKeyValueRows({}),
  };
}

export function argsFromRows(rows: { id: number; value: string }[]): string[] {
  return (rows || []).map((row) => String(row.value || "")).filter((v) => v !== "");
}

export function objectFromRows(
  rows: { id: number; key: string; value: string }[],
): Record<string, string> {
  const result: Record<string, string> = {};
  for (const row of rows || []) {
    const key = (row.key || "").trim();
    if (!key) continue;
    result[key] = String(row.value || "");
  }
  return result;
}

export function snapshotServer(server: McpServer): Record<string, unknown> {
  return {
    name: (server.name || "").trim(),
    enabled: !!server.enabled,
    transport: server.transport,
    command: (server.command || "").trim(),
    url: (server.url || "").trim(),
    timeout_seconds: Number(server.timeout_seconds || 0),
    args: argsFromRows(server.args),
    env: objectFromRows(server.env),
    headers: objectFromRows(server.headers),
  };
}

export function snapshotMcpConfig(servers: McpServer[]): { servers: Record<string, unknown>[] } {
  return { servers: servers.map(snapshotServer) };
}

export function validateKeyValueRows(
  label: string,
  rows: { id: number; key: string; value: string }[],
): string[] {
  const errors: string[] = [];
  const seen = new Set<string>();
  for (const row of rows || []) {
    const key = (row.key || "").trim();
    const value = String(row.value || "");
    if (!key && value === "") continue;
    if (!key) {
      errors.push(`${label} key is required`);
      continue;
    }
    const normalized = key.toLowerCase();
    if (seen.has(normalized)) {
      errors.push(`duplicate ${label} key "${key}"`);
      continue;
    }
    seen.add(normalized);
  }
  return errors;
}

export function validateMcpServers(servers: McpServer[]): {
  global: string[];
  byIndex: string[][];
} {
  const global: string[] = [];
  const byIndex: string[][] = servers.map(() => []);
  const names = new Map<string, number[]>();

  servers.forEach((server, index) => {
    const errors = byIndex[index];
    const name = (server.name || "").trim();
    const transport = server.transport;
    const timeout = Number(server.timeout_seconds);

    if (!name) {
      errors.push("Server name is required");
    } else {
      const normalized = name.toLowerCase();
      if (!names.has(normalized)) names.set(normalized, []);
      names.get(normalized)!.push(index);
    }

    if (transport === "stdio") {
      if (!(server.command || "").trim()) {
        errors.push("Command is required for stdio transport");
      }
    } else if (["sse", "streamable_http", "http"].includes(transport)) {
      if (!(server.url || "").trim()) {
        errors.push(`URL is required for ${transport} transport`);
      }
    } else {
      errors.push(`Unsupported transport "${transport}"`);
    }

    if (Number.isNaN(timeout) || timeout < 0) {
      errors.push("Timeout must be 0 or greater");
    }

    errors.push(...validateKeyValueRows("Environment", server.env));
    errors.push(...validateKeyValueRows("Header", server.headers));
  });

  for (const [name, indexes] of names.entries()) {
    if (indexes.length > 1) {
      global.push(`Duplicate server name "${name}"`);
      indexes.forEach((index) => byIndex[index].push("Server names must be unique"));
    }
  }

  return { global, byIndex };
}

export function usesRemoteTransport(server: McpServer): boolean {
  return ["sse", "streamable_http", "http"].includes(server.transport);
}

export function mcpStatusFor(serverName: string, statuses: McpStatus[]): McpStatus | null {
  const key = (serverName || "").trim().toLowerCase();
  return (
    statuses.find(
      (s) =>
        String(s.name || "")
          .trim()
          .toLowerCase() === key,
    ) ?? null
  );
}

export function mcpStatusTone(serverName: string, statuses: McpStatus[]): string {
  const status = mcpStatusFor(serverName, statuses);
  if (!status) return "outline";
  if (status.state === "running") return "success";
  if (status.state === "suppressed") return "error";
  if (status.state === "backoff") return "warning";
  if (status.state === "starting") return "info";
  return "outline";
}

export function mcpStatusLabel(
  serverName: string,
  serverEnabled: boolean,
  mcpPluginEnabled: boolean,
  statuses: McpStatus[],
): string {
  if (!serverEnabled) return "configured off";
  if (!mcpPluginEnabled) return "plugin off";
  const status = mcpStatusFor(serverName, statuses);
  if (!status) return "not connected";
  return status.state || "unknown";
}

export function formatTimestamp(value: string): string {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return date.toLocaleString();
}

export function manifestInstallSummary(plugin: PluginWithMeta): string {
  const manifest = plugin._manifestPlugin;
  if (!manifest) return "";
  const binaries = (manifest.binaries || []).map((b) => b.name).filter(Boolean);
  if (binaries.length === 0) return "No binaries declared";
  return "Binaries: " + binaries.join(", ");
}

export function pluginMetaBadges(
  plugin: PluginWithMeta,
): { key: string; label: string; variant: string }[] {
  const badges: { key: string; label: string; variant: string }[] = [];
  if (plugin._manifest) badges.push({ key: "manifest", label: "manifest", variant: "default" });
  if (plugin.managed) badges.push({ key: "managed", label: "managed", variant: "default" });
  if (plugin.has_config) badges.push({ key: "config", label: "config", variant: "secondary" });
  if (plugin.has_status) badges.push({ key: "status", label: "status", variant: "secondary" });
  if (plugin.supports_notifications)
    badges.push({ key: "notifications", label: "notifications", variant: "info" });
  const hiddenCapabilities = new Set([plugin.kind, "config", "status"]);
  for (const capability of plugin.capabilities || []) {
    if (hiddenCapabilities.has(capability)) continue;
    badges.push({ key: `capability:${capability}`, label: capability, variant: "outline" });
  }
  return badges;
}

export function sandboxMeta(pluginID: string): {
  recommended: boolean;
  isDefault: boolean;
  features: string[];
  limitations: string[];
} {
  const meta: Record<
    string,
    { recommended: boolean; isDefault: boolean; features: string[]; limitations: string[] }
  > = {
    "sandbox/docker": {
      recommended: true,
      isDefault: false,
      features: [
        "Full container-level process, filesystem, and network isolation",
        "Works on Linux, macOS, and Windows",
        "Per-agent network policy enforcement",
        "Dedicated container process namespace for MCP servers",
      ],
      limitations: ["Requires a running Docker daemon"],
    },
    "sandbox/local": {
      recommended: false,
      isDefault: true,
      features: [
        "No Docker daemon required",
        "Linux: process group kill, rlimits, bwrap filesystem/network isolation",
        "Suitable for CI without Docker or embedded deployments",
      ],
      limitations: [
        "No container-level isolation",
        "macOS: no filesystem or network policy enforcement",
        "Windows: not supported",
        "Linux: bwrap is required; sessions fail closed if unavailable",
      ],
    },
    "sandbox/none": {
      recommended: false,
      isDefault: false,
      features: [
        "No external dependencies — works everywhere",
        "Agent inherits full host environment and permissions",
        "Suitable for trusted workloads or single-user local deployments",
      ],
      limitations: [
        "No isolation of any kind — agent runs as the current user",
        "No filesystem, network, or process restrictions enforced",
        "Not safe for untrusted agents or multi-user environments",
      ],
    },
  };
  return meta[pluginID] || { recommended: false, isDefault: false, features: [], limitations: [] };
}

// Manifest install draft helpers
export interface ManifestInstallDraft {
  id: string;
  kind: string;
  name: string;
  display_name: string;
  description: string;
  enabled: boolean;
  binaries: (ManifestBinary & { id: number })[];
  session_env: (ManifestSessionEnv & { id: number })[];
  oauth_provider: string;
  oauth_provider_config_field: string;
  oauth_provider_choices: string;
}

export function buildManifestInstallDraft(plugin: PluginWithMeta): ManifestInstallDraft {
  const manifest = plugin._manifestPlugin || ({} as ManifestPlugin);
  return {
    id: manifest.id || plugin.id || "",
    kind: manifest.kind || plugin.kind || "tool",
    name: manifest.name || plugin.name || "",
    display_name: manifest.display_name || plugin.display_name || "",
    description: manifest.description || plugin.description || "",
    enabled: manifest.enabled !== false,
    binaries: (manifest.binaries || []).map((b) => ({
      id: nextRowID(),
      name: b.name || "",
      repo: b.repo || "",
      version: b.version || "",
      bin_path: b.bin_path || "",
      exe: b.exe || "",
    })),
    session_env: (manifest.session_env || []).map((e) => ({
      id: nextRowID(),
      env_var: e.env_var || "",
      source: e.source || "static",
      value: e.value || "",
      required: !!e.required,
    })),
    oauth_provider: manifest.oauth_provider || "",
    oauth_provider_config_field: manifest.oauth_provider_config_field || "",
    oauth_provider_choices: (manifest.oauth_provider_choices || []).join(", "),
  };
}

export function buildManifestPluginFromDraft(draft: ManifestInstallDraft): ManifestPlugin {
  const binaries = (draft.binaries || [])
    .map((row) => {
      const binary: ManifestBinary = {
        name: String(row.name || "").trim(),
        repo: String(row.repo || "").trim(),
      };
      if (row.version) binary.version = String(row.version).trim();
      if (row.bin_path) binary.bin_path = String(row.bin_path).trim();
      if (row.exe) binary.exe = String(row.exe).trim();
      return binary;
    })
    .filter((b) => b.name || b.repo);

  const sessionEnv = (draft.session_env || [])
    .map((row) => {
      const env: ManifestSessionEnv = {
        env_var: String(row.env_var || "").trim(),
        source: String(row.source || "").trim(),
      };
      if (row.value) env.value = String(row.value);
      if (row.required) env.required = true;
      return env;
    })
    .filter((e) => e.env_var || e.source);

  const next: ManifestPlugin = {
    id: (draft.id || "").trim(),
    kind: (draft.kind || "").trim(),
    name: (draft.name || "").trim(),
    display_name: (draft.display_name || "").trim(),
    description: (draft.description || "").trim(),
    enabled: !!draft.enabled,
    binaries,
    session_env: sessionEnv,
  };

  if (draft.oauth_provider) next.oauth_provider = draft.oauth_provider.trim();
  if (draft.oauth_provider_config_field)
    next.oauth_provider_config_field = draft.oauth_provider_config_field.trim();

  const choices = String(draft.oauth_provider_choices || "")
    .split(",")
    .map((v) => v.trim())
    .filter(Boolean);
  if (choices.length > 0) next.oauth_provider_choices = choices;

  return next;
}

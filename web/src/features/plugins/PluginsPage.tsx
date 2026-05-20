import React, { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import type {
  ManifestOAuthProvider,
  ManifestPlugin,
  McpServer,
  McpStatus,
  Plugin,
  PluginSchemaProperty,
  PluginWithMeta,
} from "@/lib/types";
import {
  buildManifestInstallDraft,
  buildManifestPluginFromDraft,
  buildPluginConfigDraft,
  buildPluginConfigPayload,
  hasGenericConfigEditor,
  manifestInstallSummary,
  normalizeMcpServers,
  otherPlugins,
  pluginDescription,
  pluginLabel,
  pluginMetaBadges,
  pluginToggleURL,
  pluginToggleURLByID,
  sandboxMeta,
  semanticPlugins,
  snapshotMcpConfig,
} from "./pluginUtils";
import type { ManifestInstallDraft } from "./pluginUtils";
import { GenericConfigEditor } from "./GenericConfigEditor";
import { ManifestInstallEditor } from "./ManifestInstallEditor";
import { McpTab } from "./McpTab";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import { useI18n } from "@/lib/i18n";
import { SettingsDetailLayout } from "@/features/settings/SettingsDetailLayout";

type Tab = "tools" | "mcp" | "channels" | "hooks" | "memory" | "sandbox" | "standalone";

interface Toast {
  id: number;
  message: string;
  type: "success" | "error";
}

let toastCounter = 0;

export function PluginsPage() {
  const { t } = useI18n();
  const [tab, setTab] = useState<Tab>("tools");
  const [plugins, setPlugins] = useState<Plugin[]>([]);
  const [manifestPlugins, setManifestPlugins] = useState<ManifestPlugin[]>([]);
  const [oauthProviders, setOAuthProviders] = useState<ManifestOAuthProvider[]>([]);
  const [schemas, setSchemas] = useState<
    Record<string, { properties?: Record<string, PluginSchemaProperty> }>
  >({});

  // Plugin config state
  const [pluginConfigOpen, setPluginConfigOpen] = useState<Record<string, boolean>>({});
  const [pluginConfigLoading, setPluginConfigLoading] = useState<Record<string, boolean>>({});
  const [pluginConfigSaving, setPluginConfigSaving] = useState<Record<string, boolean>>({});
  const [pluginConfigLoaded, setPluginConfigLoaded] = useState<Record<string, boolean>>({});
  const [pluginConfigRaw, setPluginConfigRaw] = useState<Record<string, Record<string, unknown>>>(
    {},
  );
  const [pluginConfigDrafts, setPluginConfigDrafts] = useState<
    Record<string, Record<string, unknown>>
  >({});

  // Manifest install state
  const [manifestInstallOpen, setManifestInstallOpen] = useState<Record<string, boolean>>({});
  const [manifestInstallDrafts, setManifestInstallDrafts] = useState<
    Record<string, ManifestInstallDraft>
  >({});

  // Add tool form
  const [showAddManifestTool, setShowAddManifestTool] = useState(false);
  const [newManifestTool, setNewManifestTool] = useState({
    id: "tool/",
    name: "",
    display_name: "",
    description: "",
    binary_name: "",
    tool: "",
    version: "",
    bin_path: "",
    bin: "",
  });

  // MCP state
  const [mcpServers, setMcpServers] = useState<McpServer[]>([]);
  const [mcpStatuses, setMcpStatuses] = useState<McpStatus[]>([]);
  const [mcpSaving, setMcpSaving] = useState(false);
  const [mcpSavedSignature, setMcpSavedSignature] = useState('{"servers":[]}');
  const [mcpLastSavedAt, setMcpLastSavedAt] = useState("");

  // Toast
  const [toasts, setToasts] = useState<Toast[]>([]);

  function showToast(message: string, type: "success" | "error" = "success") {
    toastCounter += 1;
    const id = toastCounter;
    setToasts((prev) => [...prev, { id, message, type }]);
    setTimeout(() => setToasts((prev) => prev.filter((t) => t.id !== id)), 4000);
  }

  // Derived plugin lists
  const toolPlugins = semanticPlugins("tool", plugins, manifestPlugins).filter(
    (p) => p.id !== "tool/mcp",
  );
  const hookPlugins = semanticPlugins("hook", plugins, manifestPlugins);
  const memoryPlugins = plugins.filter((p) => p.kind === "memory");
  const validSandboxBackends = new Set(["sandbox/docker", "sandbox/local", "sandbox/none"]);
  const sandboxPlugins = plugins.filter(
    (p) => p.kind === "sandbox" && validSandboxBackends.has(p.id),
  );
  const standalonePlugins = otherPlugins(plugins, manifestPlugins);

  const channelPlugins = plugins.filter((p) => p.kind === "channel");

  const mcpPlugin = plugins.find((p) => p.id === "tool/mcp") || null;
  const mcpPluginEnabled = !!mcpPlugin?.enabled;

  const mcpIsDirty = JSON.stringify(snapshotMcpConfig(mcpServers)) !== mcpSavedSignature;

  // Load plugins
  const loadPlugins = useCallback(async () => {
    try {
      const raw = (await api<Plugin[]>("GET", "/api/plugins")) ?? [];
      const pluginList = raw.map((p) => ({
        ...p,
        capabilities: Array.isArray(p.capabilities) ? p.capabilities : [],
      }));
      setPlugins(pluginList);

      // Load schemas for configurable plugins
      const schemaResults = await Promise.all(
        pluginList
          .filter((p) => p.has_config)
          .map(async (p) => {
            try {
              const schema = await api<{ properties?: Record<string, PluginSchemaProperty> }>(
                "GET",
                `/api/plugin-config-schema/${encodeURIComponent(p.kind)}/${encodeURIComponent(p.name)}`,
              );
              return [p.id, schema || {}] as [
                string,
                { properties?: Record<string, PluginSchemaProperty> },
              ];
            } catch {
              return [p.id, null] as [string, null];
            }
          }),
      );
      const newSchemas = Object.fromEntries(
        schemaResults.filter(
          (entry): entry is [string, { properties?: Record<string, PluginSchemaProperty> }] =>
            !!entry[1],
        ),
      );
      setSchemas(newSchemas);

      // Init MCP servers from plugin config
      const mcp = pluginList.find((p) => p.id === "tool/mcp");
      const servers = normalizeMcpServers(
        (mcp?.config?.servers as Record<string, unknown>[]) || [],
      );
      setMcpServers(servers);
      setMcpSavedSignature(JSON.stringify(snapshotMcpConfig(servers)));

      // Load MCP status
      try {
        const statusResp = await api<{ servers?: McpStatus[] }>(
          "GET",
          "/api/plugin-status/tool/mcp",
        );
        setMcpStatuses(Array.isArray(statusResp?.servers) ? statusResp.servers : []);
      } catch {
        setMcpStatuses([]);
      }
    } catch (e) {
      showToast((e as Error).message, "error");
    }
  }, []);

  const loadManifestPlugins = useCallback(async () => {
    try {
      const res = (await api<{
        plugins: ManifestPlugin[];
        oauth_providers: ManifestOAuthProvider[];
      }>("GET", "/api/manifest-plugins")) ?? { plugins: [], oauth_providers: [] };
      setManifestPlugins(res.plugins);
      setOAuthProviders(res.oauth_providers);
    } catch (e) {
      showToast((e as Error).message, "error");
    }
  }, []);

  async function syncManifest(silent = false) {
    try {
      await api("POST", "/api/manifest-plugins/sync");
      if (!silent) showToast("Manifest sync complete");
    } catch (e) {
      if (!silent) showToast((e as Error).message, "error");
    }
  }

  useEffect(() => {
    void (async () => {
      await loadPlugins();
      await loadManifestPlugins();
      await syncManifest(true);
    })();
  }, [loadPlugins, loadManifestPlugins]);

  function updatePluginEnabled(id: string, enabled: boolean) {
    setPlugins((prev) => prev.map((p) => (p.id === id ? { ...p, enabled } : p)));
  }

  // Plugin toggle
  async function togglePlugin(id: string, enabled: boolean) {
    try {
      updatePluginEnabled(id, enabled);
      const updated = await api<Plugin>("PATCH", pluginToggleURLByID(id, plugins), { enabled });
      updatePluginEnabled(updated.id || id, !!updated.enabled);
      showToast(id + (enabled ? " enabled" : " disabled"));
      void loadPlugins();
    } catch (e) {
      updatePluginEnabled(id, !enabled);
      showToast((e as Error).message, "error");
    }
  }

  async function toggleSandboxPlugin(id: string, enabled: boolean) {
    const previous = new Map(sandboxPlugins.map((p) => [p.id, p.enabled]));
    try {
      if (enabled) {
        for (const other of sandboxPlugins.filter((p) => p.id !== id)) {
          updatePluginEnabled(other.id, false);
        }
        const others = sandboxPlugins.filter((p) => p.id !== id && p.enabled);
        for (const other of others) {
          await api("PATCH", pluginToggleURL(other), { enabled: false });
        }
      }
      updatePluginEnabled(id, enabled);
      const updated = await api<Plugin>("PATCH", pluginToggleURLByID(id, plugins), { enabled });
      updatePluginEnabled(updated.id || id, !!updated.enabled);
      showToast(enabled ? id + " set as active sandbox" : id + " disabled");
      void loadPlugins();
    } catch (e) {
      for (const [pluginID, wasEnabled] of previous) {
        updatePluginEnabled(pluginID, wasEnabled);
      }
      showToast((e as Error).message, "error");
    }
  }

  async function toggleSemanticPlugin(plugin: PluginWithMeta, enabled: boolean) {
    if (plugin._manifest) {
      await toggleManifestPlugin(plugin.id, enabled);
      return;
    }
    await togglePlugin(plugin.id, enabled);
  }

  async function toggleManifestPlugin(id: string, enabled: boolean) {
    const previous = manifestPlugins;
    try {
      const updated = manifestPlugins.map((p) => (p.id === id ? { ...p, enabled } : p));
      setManifestPlugins(updated);
      await api("PUT", "/api/manifest-plugins", { plugins: updated });
      await syncManifest(true);
      await loadManifestPlugins();
      await loadPlugins();
      showToast(id + (enabled ? " enabled" : " disabled"));
    } catch (e) {
      setManifestPlugins(previous);
      showToast((e as Error).message, "error");
    }
  }

  // Plugin config editor
  async function togglePluginConfigEditor(plugin: Plugin) {
    const isOpen = !pluginConfigOpen[plugin.id];
    setPluginConfigOpen((prev) => ({ ...prev, [plugin.id]: isOpen }));
    if (isOpen && !pluginConfigLoaded[plugin.id]) {
      await loadPluginConfig(plugin);
    }
  }

  async function loadPluginConfig(plugin: Plugin, force = false) {
    if (!force && pluginConfigLoaded[plugin.id]) return;
    setPluginConfigLoading((prev) => ({ ...prev, [plugin.id]: true }));
    try {
      const config =
        (await api<Record<string, unknown>>(
          "GET",
          `/api/plugin-config/${encodeURIComponent(plugin.kind)}/${encodeURIComponent(plugin.name)}`,
        )) ?? {};
      setPluginConfigRaw((prev) => ({ ...prev, [plugin.id]: config }));
      setPluginConfigDrafts((prev) => ({
        ...prev,
        [plugin.id]: buildPluginConfigDraft(plugin, config, schemas),
      }));
      setPluginConfigLoaded((prev) => ({ ...prev, [plugin.id]: true }));
    } catch (e) {
      setPluginConfigOpen((prev) => ({ ...prev, [plugin.id]: false }));
      showToast((e as Error).message, "error");
    } finally {
      setPluginConfigLoading((prev) => ({ ...prev, [plugin.id]: false }));
    }
  }

  function resetPluginConfigDraft(plugin: Plugin) {
    setPluginConfigDrafts((prev) => ({
      ...prev,
      [plugin.id]: buildPluginConfigDraft(plugin, pluginConfigRaw[plugin.id] || {}, schemas),
    }));
  }

  async function savePluginConfig(plugin: Plugin) {
    try {
      setPluginConfigSaving((prev) => ({ ...prev, [plugin.id]: true }));
      const config = buildPluginConfigPayload(
        plugin,
        pluginConfigDrafts[plugin.id] || {},
        pluginConfigRaw[plugin.id] || {},
        schemas,
      );
      await api(
        "PUT",
        `/api/plugin-config/${encodeURIComponent(plugin.kind)}/${encodeURIComponent(plugin.name)}`,
        { config },
      );
      setPluginConfigRaw((prev) => ({ ...prev, [plugin.id]: JSON.parse(JSON.stringify(config)) }));
      setPluginConfigDrafts((prev) => ({
        ...prev,
        [plugin.id]: buildPluginConfigDraft(plugin, config, schemas),
      }));
      setPluginConfigLoaded((prev) => ({ ...prev, [plugin.id]: true }));
      await loadPlugins();
      showToast(plugin.id + " config saved");
    } catch (e) {
      showToast((e as Error).message, "error");
    } finally {
      setPluginConfigSaving((prev) => ({ ...prev, [plugin.id]: false }));
    }
  }

  // Manifest install editor
  function toggleManifestInstallEditor(plugin: PluginWithMeta) {
    const isOpen = !manifestInstallOpen[plugin.id];
    setManifestInstallOpen((prev) => ({ ...prev, [plugin.id]: isOpen }));
    if (isOpen && !manifestInstallDrafts[plugin.id]) {
      setManifestInstallDrafts((prev) => ({
        ...prev,
        [plugin.id]: buildManifestInstallDraft(plugin),
      }));
    }
  }

  async function saveManifestInstall(plugin: PluginWithMeta) {
    try {
      const draft = manifestInstallDrafts[plugin.id];
      if (!draft) throw new Error("manifest draft missing");
      const next = buildManifestPluginFromDraft(draft);
      const index = manifestPlugins.findIndex((p) => p.id === plugin.id);
      let updated: ManifestPlugin[];
      if (index >= 0) {
        updated = [...manifestPlugins];
        updated[index] = next;
      } else {
        updated = [...manifestPlugins, next];
      }
      await api("PUT", "/api/manifest-plugins", { plugins: updated });
      await loadManifestPlugins();
      await loadPlugins();
      await syncManifest(true);
      showToast(next.id + " install saved");
    } catch (e) {
      showToast((e as Error).message, "error");
    }
  }

  function resetManifestInstallDraft(plugin: PluginWithMeta) {
    setManifestInstallDrafts((prev) => ({
      ...prev,
      [plugin.id]: buildManifestInstallDraft(plugin),
    }));
  }

  // MCP
  async function saveMcpConfig() {
    try {
      setMcpSaving(true);
      const config = snapshotMcpConfig(mcpServers);
      await api("PUT", "/api/plugin-config/tool/mcp", { config });
      setMcpSavedSignature(JSON.stringify(config));
      setMcpLastSavedAt(new Date().toISOString());
      await loadPlugins();
      showToast("tool/mcp config saved");
    } catch (e) {
      showToast((e as Error).message, "error");
    } finally {
      setMcpSaving(false);
    }
  }

  // Add manifest tool
  function fillNewManifestToolDefaults() {
    const binary = newManifestTool.binary_name.trim();
    if (!binary) return;
    setNewManifestTool((prev) => ({
      ...prev,
      name: prev.name || binary,
      id: prev.id === "tool/" || !prev.id ? "tool/" + binary : prev.id,
      display_name: prev.display_name || binary,
    }));
  }

  async function createManifestTool() {
    try {
      fillNewManifestToolDefaults();
      const draft = newManifestTool;
      const id = (draft.id || "").trim();
      const name = (draft.name || "").trim();
      const binaryName = (draft.binary_name || "").trim();
      const tool = (draft.tool || "").trim();
      if (!id || !id.startsWith("tool/")) throw new Error("Plugin ID must start with tool/");
      if (!name) throw new Error("Name is required");
      if (!binaryName) throw new Error("Binary name is required");
      if (!tool) throw new Error("GitHub repo is required");
      if (manifestPlugins.some((p) => p.id === id)) throw new Error(id + " already exists");

      const binary: Record<string, string> = { name: binaryName, tool };
      if (draft.version) binary.version = draft.version.trim();
      if (draft.bin_path) binary.bin_path = draft.bin_path.trim();
      if (draft.bin) binary.bin = draft.bin.trim();

      const newPlugin: ManifestPlugin = {
        id,
        kind: "tool",
        name,
        display_name: (draft.display_name || "").trim() || name,
        description: (draft.description || "").trim(),
        enabled: true,
        binaries: [binary as unknown as import("@/lib/types").ManifestBinary],
      };
      const updated = [...manifestPlugins, newPlugin];
      await api("PUT", "/api/manifest-plugins", { plugins: updated });
      await loadManifestPlugins();
      await loadPlugins();
      await syncManifest(true);
      setShowAddManifestTool(false);
      setNewManifestTool({
        id: "tool/",
        name: "",
        display_name: "",
        description: "",
        binary_name: "",
        tool: "",
        version: "",
        bin_path: "",
        bin: "",
      });
      showToast(id + " added");
    } catch (e) {
      showToast((e as Error).message, "error");
    }
  }

  const sections: { id: Tab; label: string }[] = [
    { id: "tools", label: t("plugins.tab.tools") },
    { id: "mcp", label: t("plugins.mcp.title") },
    { id: "channels", label: t("plugins.tab.channels") },
    { id: "hooks", label: t("plugins.tab.hooks") },
    { id: "memory", label: t("plugins.tab.memory") },
    { id: "sandbox", label: t("plugins.tab.sandbox") },
    { id: "standalone", label: t("plugins.tab.others") },
  ];

  const listHeader = (
    <div className="px-3 py-3 border-b border-border">
      <span className="text-xs font-medium text-muted-foreground uppercase tracking-wider">
        {t("plugins.title")}
      </span>
    </div>
  );

  const list = (
    <div>
      {sections.map((s) => (
        <button
          key={s.id}
          onClick={() => setTab(s.id)}
          className={`w-full text-left px-3 py-2.5 hover:bg-muted/50 transition-colors ${
            tab === s.id ? "bg-primary/8" : ""
          }`}
        >
          <p className="text-sm font-medium leading-tight">{s.label}</p>
        </button>
      ))}
    </div>
  );

  let detail: React.ReactNode = undefined;

  if (tab === "tools") {
    detail = (
      <div className="p-6">
        <div className="flex items-center justify-between gap-3 mb-4">
          <p className="text-xs text-muted-foreground">
            CLI tools and tool plugins. Manifest-backed tools are installed and synced
            automatically.
          </p>
          <Button
            onClick={() => setShowAddManifestTool(!showAddManifestTool)}
            variant="default"
            size="sm"
          >
            {showAddManifestTool ? "Cancel" : "Add Tool"}
          </Button>
        </div>

        {showAddManifestTool && (
          <div className="rounded-xl border border-border bg-card p-4 mb-4 space-y-4">
            <div className="flex items-center justify-between gap-3">
              <div>
                <p className="text-sm font-medium">Add Tool</p>
                <p className="text-xs text-muted-foreground mt-1">
                  Declare a GitHub release binary. Stella writes it to{" "}
                  <code className="font-mono">$STELLA_HOME/plugins.yaml</code> and syncs
                  automatically.
                </p>
              </div>
              <Button
                onClick={() =>
                  setNewManifestTool({
                    id: "tool/",
                    name: "",
                    display_name: "",
                    description: "",
                    binary_name: "",
                    tool: "",
                    version: "",
                    bin_path: "",
                    bin: "",
                  })
                }
                variant="ghost"
                size="xs"
              >
                Reset
              </Button>
            </div>
            <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-4 gap-4">
              <div className="space-y-1">
                <label className="text-xs font-medium text-muted-foreground">Binary name</label>
                <Input
                  nativeInput
                  value={newManifestTool.binary_name}
                  onChange={(e) =>
                    setNewManifestTool((prev) => ({
                      ...prev,
                      binary_name: (e.target as HTMLInputElement).value,
                    }))
                  }
                  onBlur={fillNewManifestToolDefaults}
                  type="text"
                  placeholder="my-cli"
                  className="font-mono"
                  size="sm"
                />
              </div>
              <div className="space-y-1">
                <label className="text-xs font-medium text-muted-foreground">GitHub repo</label>
                <Input
                  nativeInput
                  value={newManifestTool.tool}
                  onChange={(e) =>
                    setNewManifestTool((prev) => ({
                      ...prev,
                      tool: (e.target as HTMLInputElement).value,
                    }))
                  }
                  type="text"
                  placeholder="owner/repo"
                  className="font-mono"
                  size="sm"
                />
              </div>
              <div className="space-y-1">
                <label className="text-xs font-medium text-muted-foreground">Plugin ID</label>
                <Input
                  nativeInput
                  value={newManifestTool.id}
                  onChange={(e) =>
                    setNewManifestTool((prev) => ({
                      ...prev,
                      id: (e.target as HTMLInputElement).value,
                    }))
                  }
                  type="text"
                  placeholder="tool/my-cli"
                  className="font-mono"
                  size="sm"
                />
              </div>
              <div className="space-y-1">
                <label className="text-xs font-medium text-muted-foreground">Name</label>
                <Input
                  nativeInput
                  value={newManifestTool.name}
                  onChange={(e) =>
                    setNewManifestTool((prev) => ({
                      ...prev,
                      name: (e.target as HTMLInputElement).value,
                    }))
                  }
                  type="text"
                  placeholder="my-cli"
                  className="font-mono"
                  size="sm"
                />
              </div>
              <div className="space-y-1">
                <label className="text-xs font-medium text-muted-foreground">Display name</label>
                <Input
                  nativeInput
                  value={newManifestTool.display_name}
                  onChange={(e) =>
                    setNewManifestTool((prev) => ({
                      ...prev,
                      display_name: (e.target as HTMLInputElement).value,
                    }))
                  }
                  type="text"
                  placeholder="My CLI"
                  size="sm"
                />
              </div>
              <div className="space-y-1">
                <label className="text-xs font-medium text-muted-foreground">Version</label>
                <Input
                  nativeInput
                  value={newManifestTool.version}
                  onChange={(e) =>
                    setNewManifestTool((prev) => ({
                      ...prev,
                      version: (e.target as HTMLInputElement).value,
                    }))
                  }
                  type="text"
                  placeholder="latest or v1.2.3"
                  className="font-mono"
                  size="sm"
                />
              </div>
              <div className="space-y-1">
                <label className="text-xs font-medium text-muted-foreground">Bin path</label>
                <Input
                  nativeInput
                  value={newManifestTool.bin_path}
                  onChange={(e) =>
                    setNewManifestTool((prev) => ({
                      ...prev,
                      bin_path: (e.target as HTMLInputElement).value,
                    }))
                  }
                  type="text"
                  placeholder="bin"
                  className="font-mono"
                  size="sm"
                />
              </div>
              <div className="space-y-1">
                <label className="text-xs font-medium text-muted-foreground">Exe override</label>
                <Input
                  nativeInput
                  value={newManifestTool.bin}
                  onChange={(e) =>
                    setNewManifestTool((prev) => ({
                      ...prev,
                      bin: (e.target as HTMLInputElement).value,
                    }))
                  }
                  type="text"
                  placeholder="archive binary name"
                  className="font-mono"
                  size="sm"
                />
              </div>
              <div className="space-y-1 md:col-span-2 xl:col-span-4">
                <label className="text-xs font-medium text-muted-foreground">Description</label>
                <Input
                  nativeInput
                  value={newManifestTool.description}
                  onChange={(e) =>
                    setNewManifestTool((prev) => ({
                      ...prev,
                      description: (e.target as HTMLInputElement).value,
                    }))
                  }
                  type="text"
                  placeholder="What this CLI does"
                  size="sm"
                />
              </div>
            </div>
            <div className="flex justify-end">
              <Button onClick={createManifestTool} variant="default" size="sm">
                Save and sync
              </Button>
            </div>
          </div>
        )}

        <PluginList
          plugins={toolPlugins}
          schemas={schemas}
          pluginConfigOpen={pluginConfigOpen}
          pluginConfigLoading={pluginConfigLoading}
          pluginConfigSaving={pluginConfigSaving}
          pluginConfigDrafts={pluginConfigDrafts}
          manifestInstallOpen={manifestInstallOpen}
          manifestInstallDrafts={manifestInstallDrafts}
          oauthProviders={oauthProviders}
          onToggle={toggleSemanticPlugin}
          onToggleConfigEditor={togglePluginConfigEditor}
          onDraftChange={(pluginID, field, value) =>
            setPluginConfigDrafts((prev) => ({
              ...prev,
              [pluginID]: { ...prev[pluginID], [field]: value },
            }))
          }
          onSaveConfig={savePluginConfig}
          onResetConfig={resetPluginConfigDraft}
          onToggleManifestEditor={toggleManifestInstallEditor}
          onManifestDraftChange={(pluginID, draft) =>
            setManifestInstallDrafts((prev) => ({ ...prev, [pluginID]: draft }))
          }
          onSaveManifest={saveManifestInstall}
          onResetManifest={resetManifestInstallDraft}
          emptyMessage="No tool plugins registered."
          showManifestEditor
        />
      </div>
    );
  } else if (tab === "mcp") {
    detail = (
      <div className="p-6">
        <McpTab
          mcpServers={mcpServers}
          mcpStatuses={mcpStatuses}
          mcpPluginEnabled={mcpPluginEnabled}
          mcpSaving={mcpSaving}
          mcpLastSavedAt={mcpLastSavedAt}
          mcpIsDirty={mcpIsDirty}
          onServersChange={setMcpServers}
          onToggleMcpPlugin={(enabled) => togglePlugin("tool/mcp", enabled)}
          onSave={saveMcpConfig}
        />
      </div>
    );
  } else if (tab === "channels") {
    detail = (
      <div className="p-6">
        <p className="text-xs text-muted-foreground mb-4">
          Enable messaging platform integrations. Enabled channels appear in Settings → Channels for
          configuration.
        </p>
        <div className="border border-border rounded-lg divide-y divide-border">
          {channelPlugins.map((p) => (
            <div key={p.id} className="flex items-center justify-between gap-4 px-4 py-3">
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <span className="font-medium text-sm">{pluginLabel(p)}</span>
                  <span className="font-mono text-[11px] text-muted-foreground">{p.id}</span>
                  {p.enabled && (
                    <Badge variant="success" size="sm">
                      on
                    </Badge>
                  )}
                </div>
                {pluginDescription(p) && (
                  <p className="text-xs text-muted-foreground mt-1 leading-relaxed">
                    {pluginDescription(p)}
                  </p>
                )}
              </div>
              <Switch
                checked={p.enabled}
                onCheckedChange={(checked) => void togglePlugin(p.id, checked)}
              />
            </div>
          ))}
          {channelPlugins.length === 0 && (
            <div className="px-4 py-8 text-center text-muted-foreground text-sm">
              No channel plugins registered.
            </div>
          )}
        </div>
      </div>
    );
  } else if (tab === "hooks") {
    detail = (
      <div className="p-6">
        <PluginList
          plugins={hookPlugins}
          schemas={schemas}
          pluginConfigOpen={pluginConfigOpen}
          pluginConfigLoading={pluginConfigLoading}
          pluginConfigSaving={pluginConfigSaving}
          pluginConfigDrafts={pluginConfigDrafts}
          manifestInstallOpen={manifestInstallOpen}
          manifestInstallDrafts={manifestInstallDrafts}
          oauthProviders={oauthProviders}
          onToggle={toggleSemanticPlugin}
          onToggleConfigEditor={togglePluginConfigEditor}
          onDraftChange={(pluginID, field, value) =>
            setPluginConfigDrafts((prev) => ({
              ...prev,
              [pluginID]: { ...prev[pluginID], [field]: value },
            }))
          }
          onSaveConfig={savePluginConfig}
          onResetConfig={resetPluginConfigDraft}
          onToggleManifestEditor={toggleManifestInstallEditor}
          onManifestDraftChange={(pluginID, draft) =>
            setManifestInstallDrafts((prev) => ({ ...prev, [pluginID]: draft }))
          }
          onSaveManifest={saveManifestInstall}
          onResetManifest={resetManifestInstallDraft}
          emptyMessage="No hook plugins registered."
          showManifestEditor
        />
      </div>
    );
  } else if (tab === "memory") {
    detail = (
      <div className="p-6">
        <p className="text-xs text-muted-foreground mb-4">
          Only one memory plugin can be active at a time. Changing requires restart.
        </p>
        <div className="border border-border rounded-lg divide-y divide-border">
          {memoryPlugins.map((p) => (
            <div key={p.id} className="flex items-center justify-between gap-4 px-4 py-3">
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <span className="font-medium text-sm">{pluginLabel(p)}</span>
                  <span className="font-mono text-[11px] text-muted-foreground">{p.id}</span>
                  {p.enabled && (
                    <Badge variant="success" size="sm">
                      on
                    </Badge>
                  )}
                </div>
                {pluginDescription(p) && (
                  <p className="text-xs text-muted-foreground mt-1 leading-relaxed">
                    {pluginDescription(p)}
                  </p>
                )}
              </div>
              <Switch
                checked={p.enabled}
                onCheckedChange={(checked) => void togglePlugin(p.id, checked)}
              />
            </div>
          ))}
          {memoryPlugins.length === 0 && (
            <div className="px-4 py-8 text-center text-muted-foreground text-sm">
              No memory plugins registered.
            </div>
          )}
        </div>
      </div>
    );
  } else if (tab === "sandbox") {
    detail = (
      <div className="p-6">
        <p className="text-xs text-muted-foreground mb-4">
          Select which sandbox backend agents use. Only one can be active at a time.
        </p>
        <div className="border border-border rounded-lg divide-y divide-border">
          {sandboxPlugins.map((p) => {
            const meta = sandboxMeta(p.id);
            return (
              <div key={p.id} className={`px-4 py-4${p.enabled ? " bg-muted/50" : ""}`}>
                <div className="flex items-center justify-between gap-4">
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2">
                      <span className="font-medium text-sm">{pluginLabel(p)}</span>
                      {p.enabled && (
                        <Badge variant="success" size="sm">
                          active
                        </Badge>
                      )}
                      {meta.recommended && (
                        <Badge variant="default" size="sm">
                          recommended
                        </Badge>
                      )}
                      {meta.isDefault && (
                        <Badge variant="secondary" size="sm">
                          default
                        </Badge>
                      )}
                    </div>
                    {pluginDescription(p) && (
                      <p className="text-xs text-muted-foreground mt-1 leading-relaxed">
                        {pluginDescription(p)}
                      </p>
                    )}
                  </div>
                  <Switch
                    checked={p.enabled}
                    onCheckedChange={(checked) => void toggleSandboxPlugin(p.id, checked)}
                  />
                </div>
                {(meta.features.length > 0 || meta.limitations.length > 0) && (
                  <div className="mt-3 flex flex-col gap-2 sm:flex-row sm:gap-6">
                    {meta.features.length > 0 && (
                      <div className="flex-1">
                        <p className="text-[11px] font-medium text-success-foreground mb-1">
                          Features
                        </p>
                        <ul className="text-[11px] text-muted-foreground space-y-0.5">
                          {meta.features.map((f) => (
                            <li key={f} className="flex items-start gap-1">
                              <span className="text-success-foreground shrink-0">✓</span>
                              <span>{f}</span>
                            </li>
                          ))}
                        </ul>
                      </div>
                    )}
                    {meta.limitations.length > 0 && (
                      <div className="flex-1">
                        <p className="text-[11px] font-medium text-warning-foreground mb-1">
                          Limitations
                        </p>
                        <ul className="text-[11px] text-muted-foreground space-y-0.5">
                          {meta.limitations.map((l) => (
                            <li key={l} className="flex items-start gap-1">
                              <span className="text-warning-foreground shrink-0">⚠</span>
                              <span>{l}</span>
                            </li>
                          ))}
                        </ul>
                      </div>
                    )}
                  </div>
                )}
              </div>
            );
          })}
          {sandboxPlugins.length === 0 && (
            <div className="px-4 py-8 text-center text-muted-foreground text-sm">
              No sandbox plugins registered.
            </div>
          )}
        </div>
      </div>
    );
  } else if (tab === "standalone") {
    detail = (
      <div className="p-6">
        <p className="text-xs text-muted-foreground mb-4">
          Background services that run independently. Toggling takes effect immediately.
        </p>
        <PluginList
          plugins={standalonePlugins}
          schemas={schemas}
          pluginConfigOpen={pluginConfigOpen}
          pluginConfigLoading={pluginConfigLoading}
          pluginConfigSaving={pluginConfigSaving}
          pluginConfigDrafts={pluginConfigDrafts}
          manifestInstallOpen={manifestInstallOpen}
          manifestInstallDrafts={manifestInstallDrafts}
          oauthProviders={oauthProviders}
          onToggle={toggleSemanticPlugin}
          onToggleConfigEditor={togglePluginConfigEditor}
          onDraftChange={(pluginID, field, value) =>
            setPluginConfigDrafts((prev) => ({
              ...prev,
              [pluginID]: { ...prev[pluginID], [field]: value },
            }))
          }
          onSaveConfig={savePluginConfig}
          onResetConfig={resetPluginConfigDraft}
          onToggleManifestEditor={toggleManifestInstallEditor}
          onManifestDraftChange={(pluginID, draft) =>
            setManifestInstallDrafts((prev) => ({ ...prev, [pluginID]: draft }))
          }
          onSaveManifest={saveManifestInstall}
          onResetManifest={resetManifestInstallDraft}
          emptyMessage="No standalone plugins registered."
        />
      </div>
    );
  }

  return (
    <div className="h-full">
      <SettingsDetailLayout listHeader={listHeader} list={list} detail={detail} />
      {/* Toast notifications */}
      {toasts.length > 0 && (
        <div className="fixed bottom-4 right-4 z-50 space-y-2">
          {toasts.map((toast) => (
            <div
              key={toast.id}
              className={`rounded-lg border px-4 py-3 shadow-lg max-w-sm text-sm ${
                toast.type === "error"
                  ? "border-destructive/40 bg-destructive/10 text-destructive-foreground"
                  : "border-success/40 bg-success/10 text-success-foreground"
              }`}
            >
              <span>{toast.message}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

// Reusable plugin list component
interface PluginListProps {
  plugins: PluginWithMeta[];
  schemas: Record<string, { properties?: Record<string, PluginSchemaProperty> }>;
  pluginConfigOpen: Record<string, boolean>;
  pluginConfigLoading: Record<string, boolean>;
  pluginConfigSaving: Record<string, boolean>;
  pluginConfigDrafts: Record<string, Record<string, unknown>>;
  manifestInstallOpen: Record<string, boolean>;
  manifestInstallDrafts: Record<string, ManifestInstallDraft>;
  oauthProviders: ManifestOAuthProvider[];
  onToggle: (plugin: PluginWithMeta, enabled: boolean) => void;
  onToggleConfigEditor: (plugin: Plugin) => void;
  onDraftChange: (pluginID: string, field: string, value: unknown) => void;
  onSaveConfig: (plugin: Plugin) => void;
  onResetConfig: (plugin: Plugin) => void;
  onToggleManifestEditor: (plugin: PluginWithMeta) => void;
  onManifestDraftChange: (pluginID: string, draft: ManifestInstallDraft) => void;
  onSaveManifest: (plugin: PluginWithMeta) => void;
  onResetManifest: (plugin: PluginWithMeta) => void;
  emptyMessage: string;
  showManifestEditor?: boolean;
}

function PluginList({
  plugins,
  schemas,
  pluginConfigOpen,
  pluginConfigLoading,
  pluginConfigSaving,
  pluginConfigDrafts,
  manifestInstallOpen,
  manifestInstallDrafts,
  oauthProviders,
  onToggle,
  onToggleConfigEditor,
  onDraftChange,
  onSaveConfig,
  onResetConfig,
  onToggleManifestEditor,
  onManifestDraftChange,
  onSaveManifest,
  onResetManifest,
  emptyMessage,
  showManifestEditor = false,
}: PluginListProps) {
  return (
    <div className="border border-border rounded-lg divide-y divide-border">
      {plugins.map((p) => {
        const hasConfig = hasGenericConfigEditor(p, schemas);
        const isConfigOpen = !!pluginConfigOpen[p.id];
        const isManifestOpen = !!manifestInstallOpen[p.id];
        const badges = pluginMetaBadges(p);

        return (
          <div key={p.id}>
            <div className="flex items-center justify-between gap-4 px-4 py-3">
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2 flex-wrap">
                  <span className="font-medium text-sm">{pluginLabel(p)}</span>
                  <span className="font-mono text-[11px] text-muted-foreground">{p.id}</span>
                  {p.enabled && (
                    <Badge variant="success" size="sm">
                      on
                    </Badge>
                  )}
                  {badges.map((badge) => (
                    <Badge
                      key={badge.key}
                      variant={badge.variant as "default" | "outline" | "secondary" | "info"}
                      size="sm"
                    >
                      {badge.label}
                    </Badge>
                  ))}
                </div>
                {pluginDescription(p) && (
                  <p className="text-xs text-muted-foreground mt-1 leading-relaxed">
                    {pluginDescription(p)}
                  </p>
                )}
                {p._manifest && (
                  <p className="text-[11px] text-muted-foreground mt-1 font-mono">
                    {manifestInstallSummary(p)}
                  </p>
                )}
              </div>
              <div className="flex items-center gap-2">
                {hasConfig && (
                  <Button onClick={() => onToggleConfigEditor(p)} variant="ghost" size="xs">
                    {isConfigOpen ? "Hide config" : "Configure"}
                  </Button>
                )}
                {showManifestEditor && p._manifest && (
                  <Button onClick={() => onToggleManifestEditor(p)} variant="ghost" size="xs">
                    {isManifestOpen ? "Hide definition" : "Edit definition"}
                  </Button>
                )}
                <Switch checked={p.enabled} onCheckedChange={(checked) => onToggle(p, checked)} />
              </div>
            </div>

            {hasConfig && isConfigOpen && (
              <GenericConfigEditor
                plugin={p}
                schemas={schemas}
                draft={pluginConfigDrafts[p.id] || {}}
                isLoading={!!pluginConfigLoading[p.id]}
                isSaving={!!pluginConfigSaving[p.id]}
                onDraftChange={(field, value) => onDraftChange(p.id, field, value)}
                onSave={() => onSaveConfig(p)}
                onReset={() => onResetConfig(p)}
              />
            )}

            {showManifestEditor && p._manifest && isManifestOpen && manifestInstallDrafts[p.id] && (
              <ManifestInstallEditor
                draft={manifestInstallDrafts[p.id]}
                oauthProviders={oauthProviders}
                onChange={(draft) => onManifestDraftChange(p.id, draft)}
                onSave={() => onSaveManifest(p)}
                onReset={() => onResetManifest(p)}
              />
            )}
          </div>
        );
      })}
      {plugins.length === 0 && (
        <div className="px-4 py-8 text-center text-muted-foreground text-sm">{emptyMessage}</div>
      )}
    </div>
  );
}

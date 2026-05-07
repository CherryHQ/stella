import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import type { ManifestPlugin, McpServer, McpStatus, Plugin, PluginSchemaProperty, PluginWithMeta } from "@/lib/types";
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

type Tab = "tools" | "mcp" | "channels" | "hooks" | "memory" | "sandbox" | "standalone";

interface Toast {
  id: number;
  message: string;
  type: "success" | "error";
}

let toastCounter = 0;

export function PluginsPage() {
  const [tab, setTab] = useState<Tab>("tools");
  const [plugins, setPlugins] = useState<Plugin[]>([]);
  const [manifestPlugins, setManifestPlugins] = useState<ManifestPlugin[]>([]);
  const [schemas, setSchemas] = useState<Record<string, { properties?: Record<string, PluginSchemaProperty> }>>({});

  // Plugin config state
  const [pluginConfigOpen, setPluginConfigOpen] = useState<Record<string, boolean>>({});
  const [pluginConfigLoading, setPluginConfigLoading] = useState<Record<string, boolean>>({});
  const [pluginConfigSaving, setPluginConfigSaving] = useState<Record<string, boolean>>({});
  const [pluginConfigLoaded, setPluginConfigLoaded] = useState<Record<string, boolean>>({});
  const [pluginConfigRaw, setPluginConfigRaw] = useState<Record<string, Record<string, unknown>>>({});
  const [pluginConfigDrafts, setPluginConfigDrafts] = useState<Record<string, Record<string, unknown>>>({});

  // Manifest install state
  const [manifestInstallOpen, setManifestInstallOpen] = useState<Record<string, boolean>>({});
  const [manifestInstallDrafts, setManifestInstallDrafts] = useState<Record<string, ManifestInstallDraft>>({});

  // Add tool form
  const [showAddManifestTool, setShowAddManifestTool] = useState(false);
  const [newManifestTool, setNewManifestTool] = useState({
    id: "tool/",
    name: "",
    display_name: "",
    description: "",
    binary_name: "",
    repo: "",
    version: "",
    bin_path: "",
    exe: "",
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
  const channelPlugins = semanticPlugins("channel", plugins, manifestPlugins);
  const hookPlugins = semanticPlugins("hook", plugins, manifestPlugins);
  const memoryPlugins = plugins.filter((p) => p.kind === "memory");
  const validSandboxBackends = new Set(["sandbox/docker", "sandbox/local", "sandbox/none"]);
  const sandboxPlugins = plugins.filter((p) => p.kind === "sandbox" && validSandboxBackends.has(p.id));
  const standalonePlugins = otherPlugins(plugins, manifestPlugins);

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
              return [p.id, schema || {}] as [string, { properties?: Record<string, PluginSchemaProperty> }];
            } catch {
              return [p.id, null] as [string, null];
            }
          }),
      );
      const newSchemas = Object.fromEntries(
        schemaResults.filter((entry): entry is [string, { properties?: Record<string, PluginSchemaProperty> }] => !!entry[1]),
      );
      setSchemas(newSchemas);

      // Init MCP servers from plugin config
      const mcp = pluginList.find((p) => p.id === "tool/mcp");
      const servers = normalizeMcpServers(
        ((mcp?.config?.servers as Record<string, unknown>[]) || []),
      );
      setMcpServers(servers);
      setMcpSavedSignature(JSON.stringify(snapshotMcpConfig(servers)));

      // Load MCP status
      try {
        const statusResp = await api<{ servers?: McpStatus[] }>("GET", "/api/plugin-status/tool/mcp");
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
      const list = (await api<ManifestPlugin[]>("GET", "/api/manifest-plugins")) ?? [];
      setManifestPlugins(list);
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

  // Plugin toggle
  async function togglePlugin(id: string, enabled: boolean) {
    try {
      await api("PATCH", pluginToggleURLByID(id, plugins), { enabled });
      await loadPlugins();
      showToast(id + (enabled ? " enabled" : " disabled"));
    } catch (e) {
      showToast((e as Error).message, "error");
    }
  }

  async function toggleSandboxPlugin(id: string, enabled: boolean) {
    try {
      if (enabled) {
        const others = sandboxPlugins.filter((p) => p.id !== id && p.enabled);
        for (const other of others) {
          await api("PATCH", pluginToggleURL(other), { enabled: false });
        }
      }
      await api("PATCH", pluginToggleURLByID(id, plugins), { enabled });
      await loadPlugins();
      showToast(enabled ? id + " set as active sandbox" : id + " disabled");
    } catch (e) {
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
    try {
      const updated = manifestPlugins.map((p) => (p.id === id ? { ...p, enabled } : p));
      await api("PUT", "/api/manifest-plugins", { plugins: updated });
      await loadManifestPlugins();
      await loadPlugins();
      await syncManifest(true);
      showToast(id + (enabled ? " enabled" : " disabled"));
    } catch (e) {
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
      setManifestInstallDrafts((prev) => ({ ...prev, [plugin.id]: buildManifestInstallDraft(plugin) }));
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
    setManifestInstallDrafts((prev) => ({ ...prev, [plugin.id]: buildManifestInstallDraft(plugin) }));
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
      const repo = (draft.repo || "").trim();
      if (!id || !id.startsWith("tool/")) throw new Error("Plugin ID must start with tool/");
      if (!name) throw new Error("Name is required");
      if (!binaryName) throw new Error("Binary name is required");
      if (!repo) throw new Error("GitHub repo is required");
      if (manifestPlugins.some((p) => p.id === id)) throw new Error(id + " already exists");

      const binary: Record<string, string> = { name: binaryName, repo };
      if (draft.version) binary.version = draft.version.trim();
      if (draft.bin_path) binary.bin_path = draft.bin_path.trim();
      if (draft.exe) binary.exe = draft.exe.trim();

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
        repo: "",
        version: "",
        bin_path: "",
        exe: "",
      });
      showToast(id + " added");
    } catch (e) {
      showToast((e as Error).message, "error");
    }
  }

  const tabs: { id: Tab; label: string; count: number; show: boolean }[] = [
    { id: "tools", label: "Tools", count: toolPlugins.length, show: true },
    { id: "mcp", label: "MCP Servers", count: mcpServers.length, show: true },
    { id: "channels", label: "Channels", count: channelPlugins.length, show: true },
    { id: "hooks", label: "Hooks", count: hookPlugins.length, show: true },
    { id: "memory", label: "Memory", count: memoryPlugins.length, show: true },
    { id: "sandbox", label: "Sandbox", count: sandboxPlugins.length, show: true },
    { id: "standalone", label: "Others", count: standalonePlugins.length, show: standalonePlugins.length > 0 },
  ];

  return (
    <div>
      {/* Page header */}
      <div className="mb-6">
        <h1 className="font-serif text-2xl tracking-tight">Plugins</h1>
        <p className="text-sm text-secondary mt-1">
          Manage built-in tools, hooks, channels, memory backends, and standalone services.
        </p>
      </div>

      {/* Tabs */}
      <div className="flex gap-4 border-b border-base-300 mb-6 overflow-x-auto">
        {tabs.filter((t) => t.show).map((t) => (
          <button
            key={t.id}
            onClick={() => setTab(t.id)}
            className={`pb-2 text-sm font-medium transition-colors border-b-2 whitespace-nowrap ${
              tab === t.id
                ? "border-primary text-primary"
                : "border-transparent text-secondary hover:text-base-content"
            }`}
          >
            {t.label}
            <span className="text-xs text-secondary ml-1">({t.count})</span>
          </button>
        ))}
      </div>

      {/* Toast notifications */}
      {toasts.length > 0 && (
        <div className="fixed bottom-4 right-4 z-50 space-y-2">
          {toasts.map((toast) => (
            <div
              key={toast.id}
              className={`alert shadow-lg max-w-sm text-sm ${
                toast.type === "error" ? "alert-error" : "alert-success"
              }`}
            >
              <span>{toast.message}</span>
            </div>
          ))}
        </div>
      )}

      {/* Tools Tab */}
      {tab === "tools" && (
        <div>
          <div className="flex items-center justify-between gap-3 mb-4">
            <p className="text-xs text-secondary">
              CLI tools and tool plugins. Manifest-backed tools are installed and synced
              automatically.
            </p>
            <button
              onClick={() => setShowAddManifestTool(!showAddManifestTool)}
              className="btn btn-primary btn-sm"
            >
              {showAddManifestTool ? "Cancel" : "Add Tool"}
            </button>
          </div>

          {showAddManifestTool && (
            <div className="rounded-xl border border-base-300 bg-base-100 p-4 mb-4 space-y-4">
              <div className="flex items-center justify-between gap-3">
                <div>
                  <p className="text-sm font-medium">Add Tool</p>
                  <p className="text-xs text-secondary mt-1">
                    Declare a GitHub release binary. Anna writes it to{" "}
                    <code className="font-mono">$ANNA_HOME/plugins.yaml</code> and syncs
                    automatically.
                  </p>
                </div>
                <button
                  onClick={() =>
                    setNewManifestTool({
                      id: "tool/",
                      name: "",
                      display_name: "",
                      description: "",
                      binary_name: "",
                      repo: "",
                      version: "",
                      bin_path: "",
                      exe: "",
                    })
                  }
                  className="btn btn-ghost btn-xs"
                >
                  Reset
                </button>
              </div>
              <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-4 gap-4">
                <div className="space-y-1">
                  <label className="text-xs font-medium text-secondary">Binary name</label>
                  <input
                    value={newManifestTool.binary_name}
                    onChange={(e) =>
                      setNewManifestTool((prev) => ({ ...prev, binary_name: e.target.value }))
                    }
                    onBlur={fillNewManifestToolDefaults}
                    type="text"
                    placeholder="my-cli"
                    className="input input-bordered input-sm w-full font-mono"
                  />
                </div>
                <div className="space-y-1">
                  <label className="text-xs font-medium text-secondary">GitHub repo</label>
                  <input
                    value={newManifestTool.repo}
                    onChange={(e) =>
                      setNewManifestTool((prev) => ({ ...prev, repo: e.target.value }))
                    }
                    type="text"
                    placeholder="owner/repo"
                    className="input input-bordered input-sm w-full font-mono"
                  />
                </div>
                <div className="space-y-1">
                  <label className="text-xs font-medium text-secondary">Plugin ID</label>
                  <input
                    value={newManifestTool.id}
                    onChange={(e) =>
                      setNewManifestTool((prev) => ({ ...prev, id: e.target.value }))
                    }
                    type="text"
                    placeholder="tool/my-cli"
                    className="input input-bordered input-sm w-full font-mono"
                  />
                </div>
                <div className="space-y-1">
                  <label className="text-xs font-medium text-secondary">Name</label>
                  <input
                    value={newManifestTool.name}
                    onChange={(e) =>
                      setNewManifestTool((prev) => ({ ...prev, name: e.target.value }))
                    }
                    type="text"
                    placeholder="my-cli"
                    className="input input-bordered input-sm w-full font-mono"
                  />
                </div>
                <div className="space-y-1">
                  <label className="text-xs font-medium text-secondary">Display name</label>
                  <input
                    value={newManifestTool.display_name}
                    onChange={(e) =>
                      setNewManifestTool((prev) => ({ ...prev, display_name: e.target.value }))
                    }
                    type="text"
                    placeholder="My CLI"
                    className="input input-bordered input-sm w-full"
                  />
                </div>
                <div className="space-y-1">
                  <label className="text-xs font-medium text-secondary">Version</label>
                  <input
                    value={newManifestTool.version}
                    onChange={(e) =>
                      setNewManifestTool((prev) => ({ ...prev, version: e.target.value }))
                    }
                    type="text"
                    placeholder="latest or v1.2.3"
                    className="input input-bordered input-sm w-full font-mono"
                  />
                </div>
                <div className="space-y-1">
                  <label className="text-xs font-medium text-secondary">Bin path</label>
                  <input
                    value={newManifestTool.bin_path}
                    onChange={(e) =>
                      setNewManifestTool((prev) => ({ ...prev, bin_path: e.target.value }))
                    }
                    type="text"
                    placeholder="bin"
                    className="input input-bordered input-sm w-full font-mono"
                  />
                </div>
                <div className="space-y-1">
                  <label className="text-xs font-medium text-secondary">Exe override</label>
                  <input
                    value={newManifestTool.exe}
                    onChange={(e) =>
                      setNewManifestTool((prev) => ({ ...prev, exe: e.target.value }))
                    }
                    type="text"
                    placeholder="archive binary name"
                    className="input input-bordered input-sm w-full font-mono"
                  />
                </div>
                <div className="space-y-1 md:col-span-2 xl:col-span-4">
                  <label className="text-xs font-medium text-secondary">Description</label>
                  <input
                    value={newManifestTool.description}
                    onChange={(e) =>
                      setNewManifestTool((prev) => ({ ...prev, description: e.target.value }))
                    }
                    type="text"
                    placeholder="What this CLI does"
                    className="input input-bordered input-sm w-full"
                  />
                </div>
              </div>
              <div className="flex justify-end">
                <button onClick={createManifestTool} className="btn btn-primary btn-sm">
                  Save and sync
                </button>
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
      )}

      {/* MCP Tab */}
      {tab === "mcp" && (
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
      )}

      {/* Channels Tab */}
      {tab === "channels" && (
        <div>
          <p className="text-xs text-secondary mb-4">
            Enable platform types here. Configure bot instances on the{" "}
            <a href="/channels" className="link link-primary">
              Channels
            </a>{" "}
            page.
          </p>
          <PluginList
            plugins={channelPlugins}
            schemas={schemas}
            pluginConfigOpen={pluginConfigOpen}
            pluginConfigLoading={pluginConfigLoading}
            pluginConfigSaving={pluginConfigSaving}
            pluginConfigDrafts={pluginConfigDrafts}
            manifestInstallOpen={manifestInstallOpen}
            manifestInstallDrafts={manifestInstallDrafts}
            onToggle={(p, e) => togglePlugin(p.id, e)}
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
            emptyMessage="No channel plugins registered."
          />
        </div>
      )}

      {/* Hooks Tab */}
      {tab === "hooks" && (
        <PluginList
          plugins={hookPlugins}
          schemas={schemas}
          pluginConfigOpen={pluginConfigOpen}
          pluginConfigLoading={pluginConfigLoading}
          pluginConfigSaving={pluginConfigSaving}
          pluginConfigDrafts={pluginConfigDrafts}
          manifestInstallOpen={manifestInstallOpen}
          manifestInstallDrafts={manifestInstallDrafts}
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
      )}

      {/* Memory Tab */}
      {tab === "memory" && (
        <div>
          <p className="text-xs text-secondary mb-4">
            Only one memory plugin can be active at a time. Changing requires restart.
          </p>
          <div className="border border-base-300 rounded-lg divide-y divide-base-300">
            {memoryPlugins.map((p) => (
              <div key={p.id} className="flex items-center justify-between gap-4 px-4 py-3">
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2">
                    <span className="font-medium text-sm">{pluginLabel(p)}</span>
                    <span className="font-mono text-[11px] text-secondary">{p.id}</span>
                    {p.enabled && <span className="badge badge-success badge-xs">on</span>}
                  </div>
                  {pluginDescription(p) && (
                    <p className="text-xs text-secondary mt-1 leading-relaxed">
                      {pluginDescription(p)}
                    </p>
                  )}
                </div>
                <input
                  type="checkbox"
                  checked={p.enabled}
                  onChange={(e) => void togglePlugin(p.id, e.target.checked)}
                  className="toggle toggle-primary toggle-sm"
                />
              </div>
            ))}
            {memoryPlugins.length === 0 && (
              <div className="px-4 py-8 text-center text-secondary text-sm">
                No memory plugins registered.
              </div>
            )}
          </div>
        </div>
      )}

      {/* Sandbox Tab */}
      {tab === "sandbox" && (
        <div>
          <p className="text-xs text-secondary mb-4">
            Select which sandbox backend agents use. Only one can be active at a time.
          </p>
          <div className="border border-base-300 rounded-lg divide-y divide-base-300">
            {sandboxPlugins.map((p) => {
              const meta = sandboxMeta(p.id);
              return (
                <div key={p.id} className={`px-4 py-4${p.enabled ? " bg-base-200/50" : ""}`}>
                  <div className="flex items-center justify-between gap-4">
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-2">
                        <span className="font-medium text-sm">{pluginLabel(p)}</span>
                        {p.enabled && <span className="badge badge-success badge-xs">active</span>}
                        {meta.recommended && (
                          <span className="badge badge-primary badge-xs">recommended</span>
                        )}
                        {meta.isDefault && (
                          <span className="badge badge-neutral badge-xs">default</span>
                        )}
                      </div>
                      {pluginDescription(p) && (
                        <p className="text-xs text-secondary mt-1 leading-relaxed">
                          {pluginDescription(p)}
                        </p>
                      )}
                    </div>
                    <input
                      type="checkbox"
                      checked={p.enabled}
                      onChange={(e) => void toggleSandboxPlugin(p.id, e.target.checked)}
                      className="toggle toggle-primary toggle-sm"
                    />
                  </div>
                  {(meta.features.length > 0 || meta.limitations.length > 0) && (
                    <div className="mt-3 flex flex-col gap-2 sm:flex-row sm:gap-6">
                      {meta.features.length > 0 && (
                        <div className="flex-1">
                          <p className="text-[11px] font-medium text-success mb-1">Features</p>
                          <ul className="text-[11px] text-secondary space-y-0.5">
                            {meta.features.map((f) => (
                              <li key={f} className="flex items-start gap-1">
                                <span className="text-success shrink-0">✓</span>
                                <span>{f}</span>
                              </li>
                            ))}
                          </ul>
                        </div>
                      )}
                      {meta.limitations.length > 0 && (
                        <div className="flex-1">
                          <p className="text-[11px] font-medium text-warning mb-1">Limitations</p>
                          <ul className="text-[11px] text-secondary space-y-0.5">
                            {meta.limitations.map((l) => (
                              <li key={l} className="flex items-start gap-1">
                                <span className="text-warning shrink-0">⚠</span>
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
              <div className="px-4 py-8 text-center text-secondary text-sm">
                No sandbox plugins registered.
              </div>
            )}
          </div>
        </div>
      )}

      {/* Standalone Tab */}
      {tab === "standalone" && (
        <div>
          <p className="text-xs text-secondary mb-4">
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
            onToggle={(p, e) => togglePlugin(p.id, e)}
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
    <div className="border border-base-300 rounded-lg divide-y divide-base-300">
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
                  <span className="font-mono text-[11px] text-secondary">{p.id}</span>
                  {p.enabled && <span className="badge badge-success badge-xs">on</span>}
                  {badges.map((badge) => (
                    <span key={badge.key} className={`badge badge-xs ${badge.className}`}>
                      {badge.label}
                    </span>
                  ))}
                </div>
                {pluginDescription(p) && (
                  <p className="text-xs text-secondary mt-1 leading-relaxed">
                    {pluginDescription(p)}
                  </p>
                )}
                {p._manifest && (
                  <p className="text-[11px] text-secondary mt-1 font-mono">
                    {manifestInstallSummary(p)}
                  </p>
                )}
              </div>
              <div className="flex items-center gap-2">
                {hasConfig && (
                  <button
                    onClick={() => onToggleConfigEditor(p)}
                    className="btn btn-ghost btn-xs"
                  >
                    {isConfigOpen ? "Hide config" : "Configure"}
                  </button>
                )}
                {showManifestEditor && p._manifest && (
                  <button
                    onClick={() => onToggleManifestEditor(p)}
                    className="btn btn-ghost btn-xs"
                  >
                    {isManifestOpen ? "Hide definition" : "Edit definition"}
                  </button>
                )}
                <input
                  type="checkbox"
                  checked={p.enabled}
                  onChange={(e) => onToggle(p, e.target.checked)}
                  className="toggle toggle-primary toggle-sm"
                />
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
                onChange={(draft) => onManifestDraftChange(p.id, draft)}
                onSave={() => onSaveManifest(p)}
                onReset={() => onResetManifest(p)}
              />
            )}
          </div>
        );
      })}
      {plugins.length === 0 && (
        <div className="px-4 py-8 text-center text-secondary text-sm">{emptyMessage}</div>
      )}
    </div>
  );
}

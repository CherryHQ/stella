import { useCallback, useEffect, useState } from "react";
import {
  getPluginConfig,
  getPluginConfigSchema,
  listManifestPlugins,
  listPlugins,
  saveManifestPlugins,
  syncManifestPlugins,
  togglePlugin as togglePluginRequest,
  updatePluginConfig,
} from "@/lib/api-client/sdk.gen";
import type { ManifestPluginsResponse, SaveManifestPluginsData } from "@/lib/api-client/types.gen";
import type {
  ManifestBinary,
  ManifestOAuthProvider,
  ManifestPlugin,
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
  otherPlugins,
  pluginDescription,
  pluginLabel,
  pluginMetaBadges,
  semanticPlugins,
} from "./pluginUtils";
import type { ManifestInstallDraft } from "./pluginUtils";
import { GenericConfigEditor } from "./GenericConfigEditor";
import { ManifestInstallEditor } from "./ManifestInstallEditor";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import { useI18n } from "@/lib/i18n";
import { useToast, ToastContainer } from "@/hooks/use-toast";
import { SettingsPageHeader } from "@/features/settings/SettingsPageHeader";
import { Wrench, Webhook, Blocks, Plus } from "lucide-react";

function manifestPluginsBody(plugins: ManifestPlugin[]): SaveManifestPluginsData["body"] {
  return { plugins: plugins.map((plugin) => ({ ...plugin })) };
}

export function PluginsPage() {
  const { t } = useI18n();
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

  const { toasts, showToast } = useToast(4000);

  // Derived plugin lists
  const toolPlugins = semanticPlugins("tool", plugins, manifestPlugins);
  const hookPlugins = semanticPlugins("hook", plugins, manifestPlugins);
  const standalonePlugins = otherPlugins(plugins, manifestPlugins);

  // Load plugins
  const loadPlugins = useCallback(async () => {
    try {
      const { data } = await listPlugins({ throwOnError: true });
      const raw = (data?.plugins as Plugin[]) ?? [];
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
              const { data: schema } = await getPluginConfigSchema({
                path: { kind: p.kind, name: p.name },
                throwOnError: true,
              });
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
    } catch (e) {
      showToast((e as Error).message, "error");
    }
  }, []);

  const loadManifestPlugins = useCallback(async () => {
    try {
      const { data } = await listManifestPlugins({ throwOnError: true });
      const manifest = data as ManifestPluginsResponse;
      setManifestPlugins((manifest.plugins as unknown as ManifestPlugin[]) ?? []);
      setOAuthProviders((manifest.oauth_providers as ManifestOAuthProvider[]) ?? []);
    } catch (e) {
      showToast((e as Error).message, "error");
    }
  }, []);

  async function syncManifest(silent = false) {
    try {
      await syncManifestPlugins({ throwOnError: true });
      if (!silent) showToast("Manifest sync complete");
    } catch (e) {
      if (!silent) showToast((e as Error).message, "error");
    }
  }

  useEffect(() => {
    void (async () => {
      await loadPlugins();
      await loadManifestPlugins();
    })();
  }, [loadPlugins, loadManifestPlugins]);

  function updatePluginEnabled(id: string, enabled: boolean) {
    setPlugins((prev) => prev.map((p) => (p.id === id ? { ...p, enabled } : p)));
  }

  function pluginPathByID(id: string, pluginList: Plugin[]) {
    const plugin = pluginList.find((p) => p.id === id);
    if (plugin) return { kind: plugin.kind, name: plugin.name };
    const slash = id.indexOf("/");
    return slash !== -1
      ? { kind: id.slice(0, slash), name: id.slice(slash + 1) }
      : { kind: id, name: id };
  }

  // Plugin toggle
  async function togglePlugin(id: string, enabled: boolean) {
    try {
      updatePluginEnabled(id, enabled);
      const { data } = await togglePluginRequest({
        path: pluginPathByID(id, plugins),
        body: { enabled },
        throwOnError: true,
      });
      const updated = data as Plugin;
      updatePluginEnabled(updated.id || id, !!updated.enabled);
      showToast(id + (enabled ? " enabled" : " disabled"));
      void loadPlugins();
    } catch (e) {
      updatePluginEnabled(id, !enabled);
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
      await saveManifestPlugins({ body: manifestPluginsBody(updated), throwOnError: true });
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
      const { data } = await getPluginConfig({
        path: { kind: plugin.kind, name: plugin.name },
        throwOnError: true,
      });
      const config = (data ?? {}) as Record<string, unknown>;
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
      await updatePluginConfig({
        path: { kind: plugin.kind, name: plugin.name },
        body: { config },
        throwOnError: true,
      });
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
      await saveManifestPlugins({ body: manifestPluginsBody(updated), throwOnError: true });
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
        binaries: [binary as unknown as ManifestBinary],
      };
      const updated = [...manifestPlugins, newPlugin];
      await saveManifestPlugins({ body: manifestPluginsBody(updated), throwOnError: true });
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

  const pluginListProps = {
    schemas,
    pluginConfigOpen,
    pluginConfigLoading,
    pluginConfigSaving,
    pluginConfigDrafts,
    manifestInstallOpen,
    manifestInstallDrafts,
    oauthProviders,
    onToggle: toggleSemanticPlugin,
    onToggleConfigEditor: togglePluginConfigEditor,
    onDraftChange: (pluginID: string, field: string, value: unknown) =>
      setPluginConfigDrafts((prev) => ({
        ...prev,
        [pluginID]: { ...prev[pluginID], [field]: value },
      })),
    onSaveConfig: savePluginConfig,
    onResetConfig: resetPluginConfigDraft,
    onToggleManifestEditor: toggleManifestInstallEditor,
    onManifestDraftChange: (pluginID: string, draft: ManifestInstallDraft) =>
      setManifestInstallDrafts((prev) => ({ ...prev, [pluginID]: draft })),
    onSaveManifest: saveManifestInstall,
    onResetManifest: resetManifestInstallDraft,
  };

  return (
    <div className="h-full overflow-y-auto bg-background">
      <div className="mx-auto max-w-3xl p-6 sm:p-8 lg:p-10 space-y-8">
        <SettingsPageHeader
          title={t("settings.nav.plugins")}
          description="Manage CLI tools, hooks, and background services."
        />
        {/* Tools */}
        <section className="space-y-4">
          <div className="flex items-center justify-between gap-3 border-b border-border pb-2">
            <div className="flex items-center gap-2">
              <Wrench className="size-4 shrink-0 text-muted-foreground" />
              <h4 className="font-mono text-[9px] text-muted-foreground">
                {t("plugins.tab.tools")}
              </h4>
              <Badge variant="secondary" className="text-[10px] py-0 px-1.5 rounded-md">
                {toolPlugins.length}
              </Badge>
            </div>
            <Button
              onClick={() => setShowAddManifestTool(!showAddManifestTool)}
              variant={showAddManifestTool ? "outline" : "premium-outline"}
              size="xs"
              className="group h-7 flex items-center gap-1 cursor-pointer duration-120"
            >
              {showAddManifestTool ? (
                "Cancel"
              ) : (
                <>
                  <Plus className="size-3.5 group-hover:rotate-90 transition-transform duration-120" />
                  Add Tool
                </>
              )}
            </Button>
          </div>
          <p className="text-xs text-muted-foreground -mt-2">
            CLI tools and tool plugins. Manifest-backed tools are installed and synced
            automatically.
          </p>

          {showAddManifestTool && (
            <div className="rounded-xl border border-border bg-card p-5 mb-6 space-y-5">
              <div className="flex items-center justify-between gap-3 border-b border-border pb-3">
                <div>
                  <p className="text-sm font-semibold font-sans tracking-tight">Add Tool</p>
                  <p className="text-xs text-muted-foreground mt-0.5">
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
                  className="rounded-lg h-7.5 cursor-pointer duration-120"
                >
                  Reset
                </Button>
              </div>
              <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                <div className="space-y-1.5">
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
                    className="font-mono text-sm"
                    size="sm"
                  />
                </div>
                <div className="space-y-1.5">
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
                    className="font-mono text-sm"
                    size="sm"
                  />
                </div>
                <div className="space-y-1.5">
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
                    className="font-mono text-sm"
                    size="sm"
                  />
                </div>
                <div className="space-y-1.5">
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
                    className="font-mono text-sm"
                    size="sm"
                  />
                </div>
                <div className="space-y-1.5">
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
                    className="text-sm"
                  />
                </div>
                <div className="space-y-1.5">
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
                    className="font-mono text-sm"
                    size="sm"
                  />
                </div>
                <div className="space-y-1.5">
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
                    className="font-mono text-sm"
                    size="sm"
                  />
                </div>
                <div className="space-y-1.5">
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
                    className="font-mono text-sm"
                    size="sm"
                  />
                </div>
                <div className="space-y-1.5 md:col-span-2">
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
                    className="text-sm"
                  />
                </div>
              </div>
              <div className="flex justify-end pt-2 border-t border-border">
                <Button
                  onClick={createManifestTool}
                  variant="default"
                  size="sm"
                  className="rounded-lg cursor-pointer duration-120"
                >
                  Save and sync
                </Button>
              </div>
            </div>
          )}

          <PluginList
            {...pluginListProps}
            plugins={toolPlugins}
            emptyMessage="No tool plugins registered."
            showManifestEditor
          />
        </section>

        {/* Hooks */}
        <section className="space-y-4">
          <div className="flex items-center gap-2 border-b border-border pb-2">
            <Webhook className="size-4 shrink-0 text-muted-foreground" />
            <h4 className="font-mono text-[9px] text-muted-foreground">{t("plugins.tab.hooks")}</h4>
            <Badge variant="secondary" className="text-[10px] py-0 px-1.5 rounded-md">
              {hookPlugins.length}
            </Badge>
          </div>
          <PluginList
            {...pluginListProps}
            plugins={hookPlugins}
            emptyMessage="No hook plugins registered."
            showManifestEditor
          />
        </section>

        {/* Others */}
        {standalonePlugins.length > 0 && (
          <section className="space-y-4">
            <div className="flex items-center gap-2 border-b border-border pb-2">
              <Blocks className="size-4 shrink-0 text-muted-foreground" />
              <h4 className="font-mono text-[9px] text-muted-foreground">
                {t("plugins.tab.others")}
              </h4>
              <Badge variant="secondary" className="text-[10px] py-0 px-1.5 rounded-md">
                {standalonePlugins.length}
              </Badge>
            </div>
            <p className="text-xs text-muted-foreground -mt-2">
              Background services that run independently.
            </p>
            <PluginList
              {...pluginListProps}
              plugins={standalonePlugins}
              emptyMessage="No standalone plugins registered."
            />
          </section>
        )}
      </div>
      <ToastContainer messages={toasts} />
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
    <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
      {plugins.map((p) => {
        const hasConfig = hasGenericConfigEditor(p, schemas);
        const isConfigOpen = !!pluginConfigOpen[p.id];
        const isManifestOpen = !!manifestInstallOpen[p.id];
        const badges = pluginMetaBadges(p);
        const isOpen = isConfigOpen || isManifestOpen;

        return (
          <div
            key={p.id}
            className={`flex flex-col rounded-xl border bg-card transition-colors duration-120 ${
              p.enabled ? "border-primary/40 bg-primary/[0.02]" : "border-border"
            } overflow-hidden ${isOpen ? "sm:col-span-2" : ""}`}
          >
            <div className="flex items-center justify-between gap-4 px-5 py-4">
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2 flex-wrap">
                  <span className="font-semibold text-sm">{pluginLabel(p)}</span>
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
                  <p className="text-xs text-muted-foreground mt-1.5 leading-relaxed">
                    {pluginDescription(p)}
                  </p>
                )}
                {p._manifest && (
                  <p className="text-[11px] text-muted-foreground mt-1 font-mono">
                    {manifestInstallSummary(p)}
                  </p>
                )}
              </div>
              <div className="flex items-center gap-2.5 shrink-0">
                {hasConfig && (
                  <Button
                    onClick={() => onToggleConfigEditor(p)}
                    variant={isConfigOpen ? "default" : "ghost"}
                    size="xs"
                    className="rounded-lg h-7.5 cursor-pointer duration-120"
                  >
                    {isConfigOpen ? "Hide config" : "Configure"}
                  </Button>
                )}
                {showManifestEditor && p._manifest && (
                  <Button
                    onClick={() => onToggleManifestEditor(p)}
                    variant={isManifestOpen ? "default" : "ghost"}
                    size="xs"
                    className="rounded-lg h-7.5 cursor-pointer duration-120"
                  >
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
        <div className="text-center text-muted-foreground text-sm py-8 border border-dashed border-border rounded-xl bg-card sm:col-span-2">
          {emptyMessage}
        </div>
      )}
    </div>
  );
}

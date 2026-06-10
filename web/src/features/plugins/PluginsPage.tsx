import { useCallback, useEffect, useState } from "react";
import { useNavigate, useParams } from "@tanstack/react-router";
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
  isSimpleCliTool,
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
import { CliToolAddForm, CliToolEditor } from "./CliToolPanel";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import { useI18n } from "@/lib/i18n";
import { useToast, ToastContainer } from "@/hooks/use-toast";
import { SettingsDetailLayout } from "@/features/settings/SettingsDetailLayout";
import { SettingsEmptyState } from "@/features/settings/SettingsEmptyState";
import {
  SettingsListHeader,
  SettingsListItem,
  SettingsListBody,
} from "@/features/settings/SettingsListPanel";
import { DetailPanel, DetailPanelHeader } from "@/features/settings/SettingsDetailPanel";
import { Plus, Wrench, Webhook, Blocks } from "lucide-react";

function manifestPluginsBody(plugins: ManifestPlugin[]): SaveManifestPluginsData["body"] {
  return { plugins: plugins.map((plugin) => ({ ...plugin })) };
}

export function PluginsPage() {
  const { t } = useI18n();
  const navigate = useNavigate();
  const params = useParams({ strict: false }) as { pluginId?: string };
  const pluginId = params.pluginId;

  const [plugins, setPlugins] = useState<Plugin[]>([]);
  const [manifestPlugins, setManifestPlugins] = useState<ManifestPlugin[]>([]);
  const [oauthProviders, setOAuthProviders] = useState<ManifestOAuthProvider[]>([]);
  const [schemas, setSchemas] = useState<
    Record<string, { properties?: Record<string, PluginSchemaProperty> }>
  >({});

  const [pluginConfigLoading, setPluginConfigLoading] = useState<Record<string, boolean>>({});
  const [pluginConfigSaving, setPluginConfigSaving] = useState<Record<string, boolean>>({});
  const [pluginConfigLoaded, setPluginConfigLoaded] = useState<Record<string, boolean>>({});
  const [pluginConfigRaw, setPluginConfigRaw] = useState<Record<string, Record<string, unknown>>>(
    {},
  );
  const [pluginConfigDrafts, setPluginConfigDrafts] = useState<
    Record<string, Record<string, unknown>>
  >({});

  const [manifestInstallDrafts, setManifestInstallDrafts] = useState<
    Record<string, ManifestInstallDraft>
  >({});

  const { toasts, showToast } = useToast(4000);

  const toolPlugins = semanticPlugins("tool", plugins, manifestPlugins);
  const hookPlugins = semanticPlugins("hook", plugins, manifestPlugins);
  const standalonePlugins = otherPlugins(plugins, manifestPlugins);
  const allPlugins = [...toolPlugins, ...hookPlugins, ...standalonePlugins];

  const selectedPlugin =
    pluginId && pluginId !== "new" ? allPlugins.find((p) => p.name === pluginId) : undefined;
  const isCreating = pluginId === "new";

  const loadPlugins = useCallback(async () => {
    try {
      const { data } = await listPlugins({ throwOnError: true });
      const raw = (data?.plugins as Plugin[]) ?? [];
      const pluginList = raw.map((p) => ({
        ...p,
        capabilities: Array.isArray(p.capabilities) ? p.capabilities : [],
      }));
      setPlugins(pluginList);

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

  // Load config for selected plugin
  useEffect(() => {
    if (selectedPlugin && selectedPlugin.has_config && !pluginConfigLoaded[selectedPlugin.id]) {
      void loadPluginConfig(selectedPlugin);
    }
    if (selectedPlugin?._manifest && !manifestInstallDrafts[selectedPlugin.id]) {
      setManifestInstallDrafts((prev) => ({
        ...prev,
        [selectedPlugin.id]: buildManifestInstallDraft(selectedPlugin),
      }));
    }
  }, [selectedPlugin?.id]);

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

  // upsertManifestPlugin replaces (or appends) one manifest plugin, then persists,
  // reloads, and syncs. Preserves every other plugin's definition verbatim.
  async function upsertManifestPlugin(next: ManifestPlugin, successMsg: string) {
    const index = manifestPlugins.findIndex((p) => p.id === next.id);
    const updated =
      index >= 0
        ? manifestPlugins.map((p, i) => (i === index ? next : p))
        : [...manifestPlugins, next];
    await saveManifestPlugins({ body: manifestPluginsBody(updated), throwOnError: true });
    await loadManifestPlugins();
    await loadPlugins();
    await syncManifest(true);
    showToast(successMsg);
  }

  async function createCliTool(params: {
    toolKey: string;
    name: string;
    displayName: string;
    version: string;
  }) {
    const id = "tool/" + params.name;
    if (manifestPlugins.some((p) => p.id === id)) {
      showToast(id + " already exists", "error");
      return;
    }
    const binary: ManifestBinary = { name: params.name, tool: params.toolKey };
    if (params.version) binary.version = params.version;
    const next: ManifestPlugin = {
      id,
      kind: "tool",
      name: params.name,
      display_name: params.displayName || params.name,
      description: "",
      enabled: true,
      binaries: [binary],
    };
    try {
      await upsertManifestPlugin(next, id + " added");
      void navigate({ to: "/settings/plugins/$pluginId", params: { pluginId: params.name } });
    } catch (e) {
      showToast((e as Error).message, "error");
    }
  }

  // --- Render ---

  function pluginKindIcon(kind: string) {
    switch (kind) {
      case "tool":
        return <Wrench className="size-3.5 text-muted-foreground" />;
      case "hook":
        return <Webhook className="size-3.5 text-muted-foreground" />;
      default:
        return <Blocks className="size-3.5 text-muted-foreground" />;
    }
  }

  function pluginKindLabel(kind: string) {
    switch (kind) {
      case "tool":
        return t("plugins.tab.tools");
      case "hook":
        return t("plugins.tab.hooks");
      default:
        return t("plugins.tab.others");
    }
  }

  const groups = [
    { kind: "tool", plugins: toolPlugins },
    { kind: "hook", plugins: hookPlugins },
    ...(standalonePlugins.length > 0 ? [{ kind: "other", plugins: standalonePlugins }] : []),
  ];

  let detail: React.ReactNode = undefined;

  if (isCreating) {
    detail = (
      <CliToolAddForm
        existingIds={manifestPlugins.map((p) => p.id)}
        onCreate={createCliTool}
        onCancel={() => void navigate({ to: "/settings/plugins" })}
      />
    );
  } else if (selectedPlugin) {
    const p = selectedPlugin;
    const hasConfig = hasGenericConfigEditor(p, schemas);
    const badges = pluginMetaBadges(p);

    detail = (
      <DetailPanel>
        <DetailPanelHeader
          title={pluginLabel(p)}
          subtitle={
            <div className="flex items-center gap-1.5 flex-wrap">
              <span className="font-mono text-xs text-muted-foreground">{p.id}</span>
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
          }
          action={
            <Switch
              checked={p.enabled}
              onCheckedChange={(checked) => void toggleSemanticPlugin(p, checked)}
            />
          }
        />

        {pluginDescription(p) && (
          <p className="text-sm text-muted-foreground leading-relaxed">{pluginDescription(p)}</p>
        )}

        {p._manifest && (
          <p className="text-xs text-muted-foreground font-mono">{manifestInstallSummary(p)}</p>
        )}

        {hasConfig && (
          <div className="border-t border-border pt-4 -mx-6 px-0">
            <GenericConfigEditor
              plugin={p}
              schemas={schemas}
              draft={pluginConfigDrafts[p.id] || {}}
              isLoading={!!pluginConfigLoading[p.id]}
              isSaving={!!pluginConfigSaving[p.id]}
              onDraftChange={(field, value) =>
                setPluginConfigDrafts((prev) => ({
                  ...prev,
                  [p.id]: { ...prev[p.id], [field]: value },
                }))
              }
              onSave={() => savePluginConfig(p)}
              onReset={() => resetPluginConfigDraft(p)}
            />
          </div>
        )}

        {p._manifest && isSimpleCliTool(p) && (
          <div className="border-t border-border pt-4 -mx-6 px-0">
            <CliToolEditor
              plugin={p}
              onSave={(next) => upsertManifestPlugin(next, next.id + " updated")}
              showToast={showToast}
            />
          </div>
        )}

        {p._manifest && !isSimpleCliTool(p) && manifestInstallDrafts[p.id] && (
          <div className="border-t border-border pt-4 -mx-6 px-0">
            <ManifestInstallEditor
              draft={manifestInstallDrafts[p.id]}
              oauthProviders={oauthProviders}
              onChange={(draft) => setManifestInstallDrafts((prev) => ({ ...prev, [p.id]: draft }))}
              onSave={() => saveManifestInstall(p)}
              onReset={() => resetManifestInstallDraft(p)}
            />
          </div>
        )}
      </DetailPanel>
    );
  }

  return (
    <>
      <SettingsDetailLayout
        list={
          <>
            <SettingsListHeader
              title={t("settings.nav.plugins")}
              action={
                <Button
                  onClick={() =>
                    void navigate({
                      to: "/settings/plugins/$pluginId",
                      params: { pluginId: "new" },
                    })
                  }
                  variant="ghost"
                  size="icon-sm"
                >
                  <Plus className="size-4" />
                </Button>
              }
            />
            <SettingsListBody>
              {groups.map((group) => (
                <div key={group.kind} className="space-y-0.5">
                  <div className="flex items-center gap-2 px-3 py-1.5">
                    {pluginKindIcon(group.kind)}
                    <span className="text-xs font-medium text-muted-foreground">
                      {pluginKindLabel(group.kind)}
                    </span>
                    <Badge variant="secondary" size="sm">
                      {group.plugins.length}
                    </Badge>
                  </div>
                  {group.plugins.map((p) => (
                    <SettingsListItem
                      key={p.id}
                      active={pluginId === p.name}
                      onClick={() =>
                        void navigate({
                          to: "/settings/plugins/$pluginId",
                          params: { pluginId: p.name },
                        })
                      }
                    >
                      <div className="flex items-center gap-2">
                        <span
                          className={`shrink-0 size-1.5 rounded-full ${p.enabled ? "bg-green-500" : "bg-muted-foreground"}`}
                        />
                        <span className="text-sm truncate">{pluginLabel(p)}</span>
                      </div>
                    </SettingsListItem>
                  ))}
                </div>
              ))}
            </SettingsListBody>
          </>
        }
        detail={detail}
        emptyState={
          <SettingsEmptyState
            message={t("plugins.noPlugins") ?? "No plugin selected"}
            description={t("plugins.noPluginsDesc") ?? "Select a plugin to view its configuration."}
          />
        }
        onBack={() => void navigate({ to: "/settings/plugins" })}
      />
      <ToastContainer messages={toasts} />
    </>
  );
}

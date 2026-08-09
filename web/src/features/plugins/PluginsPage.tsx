import { useCallback, useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import {
  deleteManifestPlugin,
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
  buildPluginConfigDraft,
  buildPluginConfigPayload,
  hasGenericConfigEditor,
  otherPlugins,
  pluginBucket,
  pluginDescription,
  pluginHasBinaries,
  pluginIsEssential,
  pluginIsRemovable,
  pluginLabel,
  semanticPlugins,
} from "./pluginUtils";
import { GenericConfigEditor } from "./GenericConfigEditor";
import { CliToolAddForm, CliToolEditor } from "./CliToolPanel";
import { PluginSection, bucketIcon } from "./PluginGrid";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import { useI18n } from "@/lib/i18n";
import { useToast } from "@/hooks/use-toast";
import { DetailPanel, DetailPanelHeader } from "@/features/settings/SettingsDetailPanel";
import { SettingsGridPage, SettingsDetailSheet } from "@/features/settings/SettingsCardGrid";
import { ConfirmDialog } from "@/features/settings/ConfirmDialog";
import { meQueryOptions } from "@/lib/queries/me";
import { MCPServersPanel } from "@/features/mcp/MCPServersPage";
import { Plus } from "lucide-react";

function manifestPluginsBody(plugins: ManifestPlugin[]): SaveManifestPluginsData["body"] {
  return { plugins: plugins.map((plugin) => ({ ...plugin })) };
}

export function PluginsPage() {
  const { t } = useI18n();
  const { data: me } = useQuery(meQueryOptions);
  const isAdmin = me?.is_admin ?? false;
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

  // The delete confirmation is an overlay and the detail renders inside a Sheet,
  // so the page owns it — nesting overlays is a bug (`web-ui.md`).
  const [pendingDelete, setPendingDelete] = useState<PluginWithMeta | null>(null);

  const { showToast } = useToast(4000);

  const toolPlugins = semanticPlugins("tool", plugins, manifestPlugins);
  const hookPlugins = semanticPlugins("hook", plugins, manifestPlugins);
  const standalonePlugins = otherPlugins(plugins, manifestPlugins);
  const allPlugins = [...toolPlugins, ...hookPlugins, ...standalonePlugins];

  const integrationPlugins = allPlugins.filter((p) => pluginBucket(p) === "integration");
  const capabilityPlugins = allPlugins.filter((p) => pluginBucket(p) === "tool");
  const systemPlugins = allPlugins.filter((p) => pluginBucket(p) === "system");

  const selectedPlugin =
    pluginId && pluginId !== "new" ? allPlugins.find((p) => p.name === pluginId) : undefined;
  const isCreating = pluginId === "new";
  const sheetOpen = isCreating || !!selectedPlugin;

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
    if (!isAdmin) return;
    void (async () => {
      await loadPlugins();
      await loadManifestPlugins();
    })();
  }, [isAdmin, loadPlugins, loadManifestPlugins]);

  // Load config for selected plugin
  useEffect(() => {
    if (selectedPlugin && selectedPlugin.has_config && !pluginConfigLoaded[selectedPlugin.id]) {
      void loadPluginConfig(selectedPlugin);
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

  // removeManifestPlugin drops an admin-added plugin. Only a custom plugin can
  // go: a builtin's definition ships with the server, so the UI offers "disable"
  // for those and the API refuses the delete outright.
  async function removeManifestPlugin(plugin: PluginWithMeta) {
    try {
      await deleteManifestPlugin({
        path: { kind: plugin.kind, name: plugin.name },
        throwOnError: true,
      });
      await loadManifestPlugins();
      await loadPlugins();
      await syncManifest(true);
      showToast(plugin.id + " removed");
      void navigate({ to: "/settings/plugins" });
    } catch (e) {
      showToast((e as Error).message, "error");
    }
  }

  // --- Render ---

  function closeSheet() {
    void navigate({ to: "/settings/plugins" });
  }

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
    const essential = pluginIsEssential(p);
    const oauthProvider = p._manifestPlugin?.oauth_provider;

    detail = (
      <DetailPanel>
        <DetailPanelHeader
          title={pluginLabel(p)}
          subtitle={
            <div className="flex items-center gap-1.5 flex-wrap">
              <span className="font-mono text-xs text-muted-foreground">{p.id}</span>
              {essential && (
                <Badge variant="secondary" size="sm">
                  core
                </Badge>
              )}
              {oauthProvider && (
                <Badge variant="outline" size="sm">
                  {oauthProvider}
                </Badge>
              )}
            </div>
          }
        />

        <div className="flex items-center justify-between gap-3 rounded-lg border border-border px-4 py-2.5">
          <span className="text-sm font-medium">
            {p.enabled ? t("plugins.enabled") : t("plugins.disabled")}
          </span>
          <Switch
            checked={p.enabled}
            disabled={essential}
            onCheckedChange={(checked) => void toggleSemanticPlugin(p, checked)}
          />
        </div>

        {pluginDescription(p) && (
          <p className="text-sm text-muted-foreground leading-relaxed">{pluginDescription(p)}</p>
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

        {p._manifest && pluginHasBinaries(p) && (
          <div className="border-t border-border pt-4 -mx-6 px-0">
            <CliToolEditor
              // The editor holds an unsaved draft in local state; without a key
              // per plugin, switching plugins would carry the previous one's
              // draft into the new form.
              key={p.id}
              plugin={p}
              oauthProviders={oauthProviders}
              onSave={(next) => upsertManifestPlugin(next, next.id + " updated")}
              showToast={showToast}
            />
          </div>
        )}

        {/* Removal is the counterpart of the add form: a plugin an admin added
            can be taken back out. A builtin has no such row to drop — the
            enable switch above is its off. */}
        {pluginIsRemovable(p) && (
          <div className="border-t border-border pt-4 flex items-center justify-between gap-3">
            <span className="text-xs text-muted-foreground">{t("plugins.removeDesc")}</span>
            <Button
              onClick={() => setPendingDelete(p)}
              variant="ghost"
              size="sm"
              className="text-destructive-foreground hover:bg-destructive/10 shrink-0"
            >
              {t("common.remove")}
            </Button>
          </div>
        )}
      </DetailPanel>
    );
  }

  return (
    <>
      <SettingsGridPage
        title={t(isAdmin ? "plugins.title" : "mcp.title")}
        action={
          isAdmin ? (
            <Button
              render={<Link to="/settings/plugins/$pluginId" params={{ pluginId: "new" }} />}
              variant="outline"
              size="sm"
            >
              <Plus className="size-4" />
              {t("plugins.addTool")}
            </Button>
          ) : null
        }
      >
        <MCPServersPanel embedded />
        {isAdmin && (
          <>
            <PluginSection
              icon={bucketIcon.integration}
              title={t("plugins.bucket.integrations")}
              description={t("plugins.bucket.integrationsDesc")}
              plugins={integrationPlugins}
              activeName={selectedPlugin?.name}
              onToggle={(p, enabled) => void toggleSemanticPlugin(p, enabled)}
            />
            <PluginSection
              icon={bucketIcon.tool}
              title={t("plugins.bucket.tools")}
              description={t("plugins.bucket.toolsDesc")}
              plugins={capabilityPlugins}
              activeName={selectedPlugin?.name}
              onToggle={(p, enabled) => void toggleSemanticPlugin(p, enabled)}
            />
            <PluginSection
              icon={bucketIcon.system}
              title={t("plugins.bucket.system")}
              description={t("plugins.bucket.systemDesc")}
              plugins={systemPlugins}
              activeName={selectedPlugin?.name}
              onToggle={(p, enabled) => void toggleSemanticPlugin(p, enabled)}
            />
          </>
        )}
      </SettingsGridPage>

      <SettingsDetailSheet open={sheetOpen} onClose={closeSheet}>
        {detail}
      </SettingsDetailSheet>

      <ConfirmDialog
        open={!!pendingDelete}
        onOpenChange={(open) => !open && setPendingDelete(null)}
        title={t("plugins.removePlugin")}
        message={pendingDelete ? t("plugins.removePluginMsg", { id: pendingDelete.id }) : ""}
        onConfirm={() => {
          if (pendingDelete) void removeManifestPlugin(pendingDelete);
          setPendingDelete(null);
        }}
      />
    </>
  );
}

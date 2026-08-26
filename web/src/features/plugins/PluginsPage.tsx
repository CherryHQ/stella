import { useCallback, useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import {
  deleteManifestPlugin,
  getPluginConfig,
  getPluginConfigSchema,
  listManifestPlugins,
  listPlugins,
  resetManifestPlugin,
  saveManifestPluginDefinition,
  setManifestPluginEnabled,
  syncManifestPlugins,
  togglePlugin as togglePluginRequest,
  updatePluginConfig,
} from "@/lib/api-client/sdk.gen";
import type { ManifestPluginsResponse } from "@/lib/api-client/types.gen";
import type {
  ManifestBinary,
  ManifestOAuthProvider,
  ManifestPlugin,
  ManifestPluginDefinitionField,
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
  pluginFieldIsOverridden,
  pluginIsCustomized,
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
import { errorMessage } from "@/lib/utils";
import { useToast } from "@/hooks/use-toast";
import { DetailPanel, DetailPanelHeader } from "@/features/settings/SettingsDetailPanel";
import { SettingsGridPage, SettingsDetailSheet } from "@/features/settings/SettingsCardGrid";
import { ConfirmDialog } from "@/features/settings/ConfirmDialog";
import { Plus } from "lucide-react";

export function AdminPluginsPage() {
  const { t } = useI18n();
  const navigate = useNavigate();
  const listRoute = "/admin/integrations/plugins" as const;
  const detailRoute = "/admin/integrations/plugins/$pluginId" as const;
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
  const [resettingManifestField, setResettingManifestField] = useState<string | null>(null);

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
      showToast(errorMessage(e), "error");
    }
  }, []);

  const loadManifestPlugins = useCallback(async () => {
    try {
      const { data } = await listManifestPlugins({ throwOnError: true });
      // SAFETY: listManifestPlugins returns a ManifestPluginsResponse whose
      // plugins field is ComponentsManifestPlugin[], i.e. ManifestPlugin[].
      const manifest = data as ManifestPluginsResponse;
      setManifestPlugins(manifest.plugins ?? []);
      setOAuthProviders((manifest.oauth_providers as ManifestOAuthProvider[]) ?? []);
    } catch (e) {
      showToast(errorMessage(e), "error");
    }
  }, []);

  async function syncManifest(silent = false) {
    try {
      await syncManifestPlugins({ throwOnError: true });
      if (!silent) showToast("Manifest sync complete");
    } catch (e) {
      if (!silent) showToast(errorMessage(e), "error");
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
  }, [selectedPlugin?.id]);

  function updatePluginEnabled(id: string, enabled: boolean) {
    setPlugins((prev) => prev.map((p) => (p.id === id ? { ...p, enabled } : p)));
  }

  // manifestPluginPath addresses a manifest plugin by its stable ID, not by its
  // name. `name` is an editable definition field and is allowed to differ from
  // the ID's suffix, so routing by name could miss the plugin and create a
  // second one beside it on a write.
  function manifestPluginPath(id: string) {
    const slash = id.indexOf("/");
    return slash !== -1
      ? { kind: id.slice(0, slash), name: id.slice(slash + 1) }
      : { kind: id, name: id };
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
      showToast(errorMessage(e), "error");
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
    const target = manifestPlugins.find((plugin) => plugin.id === id);
    if (!target) {
      showToast(id + " not found", "error");
      return;
    }
    try {
      const updated = manifestPlugins.map((p) => (p.id === id ? { ...p, enabled } : p));
      setManifestPlugins(updated);
      await setManifestPluginEnabled({
        path: manifestPluginPath(target.id),
        body: { enabled },
        throwOnError: true,
      });
      await syncManifest(true);
      await loadManifestPlugins();
      await loadPlugins();
      showToast(id + (enabled ? " enabled" : " disabled"));
    } catch (e) {
      setManifestPlugins(previous);
      showToast(errorMessage(e), "error");
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
      showToast(errorMessage(e), "error");
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
      showToast(errorMessage(e), "error");
    } finally {
      setPluginConfigSaving((prev) => ({ ...prev, [plugin.id]: false }));
    }
  }

  // Save one definition and explicitly declare only the fields this edit takes
  // ownership of. Existing field ownership is retained by the backend.
  async function upsertManifestPlugin(
    next: ManifestPlugin,
    fields: ManifestPluginDefinitionField[],
    successMsg: string,
  ) {
    const { builtin, overridden_fields: _overriddenFields, ...plugin } = next;
    const replacement = {
      ...plugin,
      category: plugin.category ?? "",
      essential: plugin.essential ?? false,
      prompt: plugin.prompt ?? "",
      binaries: plugin.binaries ?? [],
      skills: plugin.skills ?? [],
      session_env: plugin.session_env ?? [],
      oauth_provider: plugin.oauth_provider ?? "",
    };
    await saveManifestPluginDefinition({
      path: manifestPluginPath(next.id),
      body: builtin ? { plugin, fields } : { plugin: replacement },
      throwOnError: true,
    });
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
      await upsertManifestPlugin(next, [], id + " added");
      void navigate({ to: detailRoute, params: { pluginId: params.name } });
    } catch (e) {
      showToast(errorMessage(e), "error");
    }
  }

  // resetManifestPluginDefinition drops a builtin's customization so its
  // definition follows the server again. The enable switch is untouched: this
  // says "stop diverging", not "turn off".
  async function resetManifestPluginDefinition(plugin: PluginWithMeta, field?: string) {
    if (field) setResettingManifestField(field);
    try {
      await resetManifestPlugin({
        path: manifestPluginPath(plugin.id),
        ...(field ? { body: { field } } : {}),
        throwOnError: true,
      });
      await loadManifestPlugins();
      await loadPlugins();
      await syncManifest(true);
      showToast(t(field ? "plugins.resetFieldDone" : "plugins.resetDone"));
    } catch (e) {
      showToast(errorMessage(e), "error");
    } finally {
      if (field) setResettingManifestField(null);
    }
  }

  // removeManifestPlugin drops an admin-added plugin. Only a custom plugin can
  // go: a builtin's definition ships with the server, so the UI offers "disable"
  // for those and the API refuses the delete outright.
  async function removeManifestPlugin(plugin: PluginWithMeta) {
    try {
      await deleteManifestPlugin({
        path: manifestPluginPath(plugin.id),
        throwOnError: true,
      });
      await loadManifestPlugins();
      await loadPlugins();
      await syncManifest(true);
      showToast(plugin.id + " removed");
      void navigate({ to: listRoute });
    } catch (e) {
      showToast(errorMessage(e), "error");
    }
  }

  // --- Render ---

  function closeSheet() {
    void navigate({ to: listRoute });
  }

  let detail: React.ReactNode = undefined;

  if (isCreating) {
    detail = (
      <CliToolAddForm
        existingIds={manifestPlugins.map((p) => p.id)}
        onCreate={createCliTool}
        onCancel={() => void navigate({ to: listRoute })}
      />
    );
  } else if (selectedPlugin) {
    const p = selectedPlugin;
    const hasConfig = hasGenericConfigEditor(p, schemas);
    const essential = pluginIsEssential(p);
    const oauthProvider = p._manifestPlugin?.oauth_provider;
    const customized = pluginIsCustomized(p);
    const additionalOverriddenFields = (p._manifestPlugin?.overridden_fields ?? []).filter(
      (field) => !["binaries", "session_env", "oauth_provider"].includes(field),
    );

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
              {customized && (
                <Badge variant="outline" size="sm">
                  {t("plugins.customized")}
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

        {p._manifest &&
          (pluginHasBinaries(p) ||
            (["binaries", "session_env", "oauth_provider"] as const).some((field) =>
              pluginFieldIsOverridden(p, field),
            )) && (
            <div className="border-t border-border pt-4 -mx-6 px-0">
              <CliToolEditor
                // The editor holds an unsaved draft in local state; without a key
                // per plugin, switching plugins would carry the previous one's
                // draft into the new form.
                key={`${p.id}:${p._manifestPlugin?.overridden_fields?.join(",") ?? ""}`}
                plugin={p}
                oauthProviders={oauthProviders}
                onSave={(next, fields) => upsertManifestPlugin(next, fields, next.id + " updated")}
                onResetField={(field) => resetManifestPluginDefinition(p, field)}
                resettingField={resettingManifestField}
                showToast={showToast}
              />
            </div>
          )}

        {additionalOverriddenFields.length > 0 && (
          <div className="border-t border-border pt-4 space-y-2">
            <p className="text-xs font-semibold text-muted-foreground">
              {t("plugins.otherOverriddenFields")}
            </p>
            {additionalOverriddenFields.map((field) => (
              <div key={field} className="flex items-center justify-between gap-2">
                <div className="flex items-center gap-2">
                  <span className="font-mono text-xs">{field}</span>
                  <Badge variant="outline" size="sm">
                    {t("plugins.overriddenField")}
                  </Badge>
                </div>
                <Button
                  onClick={() => void resetManifestPluginDefinition(p, field)}
                  loading={resettingManifestField === field}
                  disabled={resettingManifestField !== null}
                  variant="ghost"
                  size="xs"
                >
                  {t("plugins.resetField")}
                </Button>
              </div>
            ))}
          </div>
        )}

        {/* An edited builtin stops following the server for the fields that were
            edited. This is the way back: drop the customization, keep the
            enable switch. */}
        {customized && (
          <div className="border-t border-border pt-4 flex items-center justify-between gap-3">
            <span className="text-xs text-muted-foreground">{t("plugins.resetDesc")}</span>
            <Button
              onClick={() => void resetManifestPluginDefinition(p)}
              disabled={resettingManifestField !== null}
              variant="ghost"
              size="sm"
              className="shrink-0"
            >
              {t("plugins.resetToDefault")}
            </Button>
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
        title={t("plugins.title")}
        action={
          <Button
            render={<Link to={detailRoute} params={{ pluginId: "new" }} />}
            variant="outline"
            size="sm"
          >
            <Plus className="size-4" />
            {t("plugins.addTool")}
          </Button>
        }
      >
        <PluginSection
          icon={bucketIcon.integration}
          title={t("plugins.bucket.integrations")}
          description={t("plugins.bucket.integrationsDesc")}
          plugins={integrationPlugins}
          activeName={selectedPlugin?.name}
          detailRoute={detailRoute}
          onToggle={(p, enabled) => void toggleSemanticPlugin(p, enabled)}
        />
        <PluginSection
          icon={bucketIcon.tool}
          title={t("plugins.bucket.tools")}
          description={t("plugins.bucket.toolsDesc")}
          plugins={capabilityPlugins}
          activeName={selectedPlugin?.name}
          detailRoute={detailRoute}
          onToggle={(p, enabled) => void toggleSemanticPlugin(p, enabled)}
        />
        <PluginSection
          icon={bucketIcon.system}
          title={t("plugins.bucket.system")}
          description={t("plugins.bucket.systemDesc")}
          plugins={systemPlugins}
          activeName={selectedPlugin?.name}
          detailRoute={detailRoute}
          onToggle={(p, enabled) => void toggleSemanticPlugin(p, enabled)}
        />
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

import { useMemo, useState } from "react";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  copyPlugin,
  createPlugin,
  deletePlugin,
  deletePluginFile,
  getPluginFile,
  updatePlugin,
  updatePluginFile,
} from "@/lib/api-client/sdk.gen";
import type {
  ComponentsCopyPluginRequest,
  ComponentsCreatePluginRequest,
  PluginResource,
  ResourceFileInfo,
} from "@/lib/api-client/types.gen";
import { scopedPluginsQueryOptions } from "@/lib/queries/plugins";
import { agentsQueryOptions, allAgentsAdminQueryOptions } from "@/lib/queries/agents";
import {
  isAgentManagedScope,
  scopesForBand,
  type ManagedScope,
  type ScopeBand,
  isManagedScope,
} from "@/lib/scope-band";
import { ErrorState } from "@/components/RouteFallback";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Field, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectItem,
  SelectPopup,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";
import { DetailPanel, DetailPanelHeader } from "@/features/settings/SettingsDetailPanel";
import { useToast } from "@/hooks/use-toast";
import { useI18n } from "@/lib/i18n";
import { apiErrorCode, apiErrorMessage } from "@/lib/api-error";
import { FileCode2, FilePlus2, FileText, RefreshCw, Trash2, Upload, X } from "lucide-react";

export type Translate = ReturnType<typeof useI18n>["t"];

const oauthClientInitializationMessage =
  "administrator must initialize this connection before users can authorize their own accounts";

type PluginError = Error | { error?: { code?: number; message?: string } };

export function pluginErrorMessage(error: PluginError, t: Translate): string {
  const message = apiErrorMessage(error, t("common.error"));
  if (apiErrorCode(error) === 409 && message === oauthClientInitializationMessage) {
    return t("plugins.oauthAdminInitializationRequired");
  }
  return message;
}

export function configHasMcpOAuth(resource: Pick<PluginResource, "resource_summary">): boolean {
  return resource.resource_summary.mcp_servers.some((server) => server.auth_type === "oauth");
}

function scopeLabel(scope: ManagedScope, t: Translate): string {
  const labels = {
    user: t("plugins.scope.user"),
    user_agent: t("plugins.scope.user_agent"),
    system: t("plugins.scope.system"),
    system_agent: t("plugins.scope.system_agent"),
  };
  return labels[scope];
}

function statusVariant(
  value: boolean,
  forbidden = false,
): "success" | "warning" | "destructive" | "secondary" {
  if (forbidden) return "destructive";
  return value ? "success" : "warning";
}

function toBase64(value: string): string {
  return btoa(unescape(encodeURIComponent(value)));
}

function fromBase64(value: string): string {
  try {
    return decodeURIComponent(escape(atob(value)));
  } catch {
    return atob(value);
  }
}

function ScopePicker({
  band,
  scope,
  agentId,
  agents,
  onScope,
  onAgent,
}: {
  band: ScopeBand;
  scope: ManagedScope;
  agentId: string;
  agents: Array<{ id?: string; name?: string }>;
  onScope: (scope: ManagedScope) => void;
  onAgent: (id: string) => void;
}) {
  const { t } = useI18n();
  const scopes = scopesForBand(band);
  return (
    <div className="flex flex-wrap items-end gap-2">
      <Field className="min-w-40">
        <FieldLabel>{t("plugins.scopeLabel")}</FieldLabel>
        <Select
          value={scope}
          onValueChange={(value) => value && isManagedScope(value) && onScope(value)}
        >
          <SelectTrigger>
            <SelectValue>
              {(value) => scopeLabel(value && isManagedScope(value) ? value : scope, t)}
            </SelectValue>
          </SelectTrigger>
          <SelectPopup>
            {scopes.map((value) => (
              <SelectItem key={value} value={value}>
                {scopeLabel(value, t)}
              </SelectItem>
            ))}
          </SelectPopup>
        </Select>
      </Field>
      {isAgentManagedScope(scope) && (
        <Field className="min-w-52">
          <FieldLabel>{t("plugins.agent")}</FieldLabel>
          <Select
            value={agentId || "__none"}
            onValueChange={(value) => value && onAgent(value === "__none" ? "" : value)}
          >
            <SelectTrigger>
              <SelectValue placeholder={t("plugins.selectAgent")} />
            </SelectTrigger>
            <SelectPopup>
              <SelectItem value="__none">{t("plugins.selectAgent")}</SelectItem>
              {agents.map((agent) =>
                agent.id ? (
                  <SelectItem key={agent.id} value={agent.id}>
                    {agent.name || agent.id}
                  </SelectItem>
                ) : null,
              )}
            </SelectPopup>
          </Select>
        </Field>
      )}
    </div>
  );
}

function DiagnosticList({ resource }: { resource: PluginResource }) {
  const { t } = useI18n();
  if (resource.diagnostics.length === 0) return null;
  return (
    <div className="space-y-2">
      <p className="text-xs font-semibold text-muted-foreground">{t("plugins.diagnostics")}</p>
      {resource.diagnostics.map((diagnostic, index) => (
        <div
          key={`${diagnostic.code}-${index}`}
          className="rounded-md border border-border p-2 text-xs"
        >
          <div className="flex flex-wrap gap-2">
            <Badge
              variant={
                diagnostic.severity === "error"
                  ? "destructive"
                  : diagnostic.severity === "warning"
                    ? "warning"
                    : "info"
              }
              size="sm"
            >
              {diagnostic.severity}
            </Badge>
            <span className="font-mono">{diagnostic.code}</span>
            {diagnostic.path && (
              <span className="font-mono text-muted-foreground">{diagnostic.path}</span>
            )}
          </div>
          <p className="mt-1 text-muted-foreground">{diagnostic.message}</p>
        </div>
      ))}
    </div>
  );
}

function ResourceSummary({ resource }: { resource: PluginResource }) {
  const { t } = useI18n();
  return (
    <div className="space-y-2">
      <p className="text-xs font-semibold text-muted-foreground">{t("plugins.resources")}</p>
      <div className="flex flex-wrap gap-2">
        {resource.resource_summary.binaries.map((item) => (
          <Badge key={`bin:${item.name}`} variant="outline" size="sm">
            {item.name} {item.version}
          </Badge>
        ))}
        {resource.resource_summary.skills.map((item) => (
          <Badge key={`skill:${item.name}`} variant="outline" size="sm">
            {item.name}
          </Badge>
        ))}
        {resource.resource_summary.mcp_servers.map((item) => (
          <Badge key={`mcp:${item.server_key}`} variant="outline" size="sm">
            MCP:{item.server_key}
          </Badge>
        ))}
        {resource.resource_summary.session_env.map((item) => (
          <Badge key={`env:${item.env_var}`} variant="outline" size="sm">
            {item.env_var}
          </Badge>
        ))}
        {resource.resource_summary.oauth_provider_configured && (
          <Badge variant="info" size="sm">
            OAuth
          </Badge>
        )}
      </div>
    </div>
  );
}

function FileEditor({
  resource,
  file,
  onSaved,
  onDeleted,
}: {
  resource: PluginResource;
  file: ResourceFileInfo | null;
  onSaved: () => void;
  onDeleted: () => void;
}) {
  const { t } = useI18n();
  const { showToast } = useToast();
  const [path, setPath] = useState(file?.path ?? "");
  const [content, setContent] = useState("");
  const [executable, setExecutable] = useState(file?.is_executable ?? false);
  const [loadedDigest, setLoadedDigest] = useState("");
  const [loading, setLoading] = useState(false);
  const read = async () => {
    if (!path.trim()) return;
    setLoading(true);
    try {
      const { data } = await getPluginFile({
        path: { plugin_id: resource.id },
        query: { path: path.trim() },
        throwOnError: true,
      });
      if (!data) throw new Error(t("plugins.fileSelectRequired"));
      setContent(fromBase64(data.content_base64));
      setExecutable(data.is_executable);
      setLoadedDigest(data.resource_digest);
    } catch (error) {
      showToast(apiErrorMessage(error, t("common.error")), "error");
    } finally {
      setLoading(false);
    }
  };
  const save = useMutation({
    mutationFn: async () => {
      const target = path.trim();
      if (!target || !loadedDigest) throw new Error(t("plugins.fileSelectRequired"));
      return (
        await updatePluginFile({
          path: { plugin_id: resource.id },
          body: {
            path: target,
            content_base64: toBase64(content),
            is_executable: executable,
            expected_digest: loadedDigest,
          },
          throwOnError: true,
        })
      ).data;
    },
    onSuccess: () => {
      showToast(t("plugins.fileSaved"), "success");
      onSaved();
    },
    onError: (error) => showToast(pluginErrorMessage(error, t), "error"),
  });
  const remove = useMutation({
    mutationFn: async () => {
      if (!path.trim() || !loadedDigest) throw new Error(t("plugins.fileSelectRequired"));
      await deletePluginFile({
        path: { plugin_id: resource.id },
        query: { path: path.trim(), expected_digest: loadedDigest },
        throwOnError: true,
      });
    },
    onSuccess: () => {
      showToast(t("plugins.fileDeleted"), "success");
      setPath("");
      setContent("");
      setLoadedDigest("");
      onDeleted();
    },
    onError: (error) => showToast(pluginErrorMessage(error, t), "error"),
  });
  return (
    <div className="space-y-3 rounded-lg border border-border p-3">
      <div className="flex flex-wrap items-end gap-2">
        <Field className="min-w-64 flex-1">
          <FieldLabel>{t("plugins.filePath")}</FieldLabel>
          <Input
            value={path}
            onChange={(event) => {
              setPath(event.target.value);
              setLoadedDigest("");
            }}
            placeholder="manifest.yaml"
            nativeInput
          />
        </Field>
        <Button
          variant="outline"
          onClick={() => void read()}
          disabled={loading || resource.is_read_only || !path.trim()}
          loading={loading}
        >
          {t("plugins.readFile")}
        </Button>
      </div>
      <textarea
        value={content}
        onChange={(event) => setContent(event.target.value)}
        className="min-h-56 w-full rounded-md border border-border bg-background p-3 font-mono text-xs"
        aria-label={t("plugins.fileContent")}
        disabled={!loadedDigest || resource.is_read_only}
      />
      <div className="flex flex-wrap items-center justify-between gap-2">
        <label className="flex items-center gap-2 text-xs text-muted-foreground">
          <input
            type="checkbox"
            checked={executable}
            onChange={(event) => setExecutable(event.target.checked)}
            disabled={!loadedDigest || resource.is_read_only}
          />
          {t("plugins.executable")}
        </label>
        <div className="flex gap-2">
          <Button
            variant="ghost"
            onClick={() => remove.mutate()}
            disabled={!loadedDigest || resource.is_read_only || remove.isPending}
            loading={remove.isPending}
          >
            <Trash2 size={16} />
            {t("common.delete")}
          </Button>
          <Button
            onClick={() => save.mutate()}
            disabled={!loadedDigest || resource.is_read_only || save.isPending}
            loading={save.isPending}
          >
            {t("common.save")}
          </Button>
        </div>
      </div>
    </div>
  );
}

function PluginDetail({
  resource,
  band,
  agents,
  onRefresh,
  onClose,
}: {
  resource: PluginResource;
  band: ScopeBand;
  agents: Array<{ id?: string; name?: string }>;
  onRefresh: () => void;
  onClose: () => void;
}) {
  const { t } = useI18n();
  const { showToast } = useToast();
  const queryClient = useQueryClient();
  const [file, setFile] = useState<ResourceFileInfo | null>(null);
  const [newPath, setNewPath] = useState("");
  const [newContent, setNewContent] = useState("");
  const [newExecutable, setNewExecutable] = useState(false);
  const [copyScope, setCopyScope] = useState<ManagedScope>(scopesForBand(band)[0]);
  const [copyAgent, setCopyAgent] = useState("");
  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: ["plugins"] });
    onRefresh();
  };
  const toggle = useMutation({
    mutationFn: (enabled: boolean) =>
      updatePlugin({
        path: { plugin_id: resource.id },
        body: {
          is_enabled: enabled,
          expected_settings_digest: resource.settings_digest,
        },
        throwOnError: true,
      }),
    onSuccess: invalidate,
    onError: (error) => showToast(pluginErrorMessage(error, t), "error"),
  });
  const remove = useMutation({
    mutationFn: async () =>
      deletePlugin({
        path: { plugin_id: resource.id },
        query: { expected_digest: resource.content_digest },
        throwOnError: true,
      }),
    onSuccess: () => {
      showToast(t("plugins.deleted"), "success");
      onClose();
      invalidate();
    },
    onError: (error) => showToast(pluginErrorMessage(error, t), "error"),
  });
  const copy = useMutation({
    mutationFn: () => {
      const body: ComponentsCopyPluginRequest = { scope: copyScope };
      if (copyAgent) body.agent_id = copyAgent;
      return copyPlugin({
        path: { plugin_id: resource.id },
        body,
        throwOnError: true,
      });
    },
    onSuccess: () => {
      showToast(t("plugins.copied"), "success");
      invalidate();
    },
    onError: (error) => showToast(pluginErrorMessage(error, t), "error"),
  });
  const createFile = useMutation({
    mutationFn: () =>
      updatePluginFile({
        path: { plugin_id: resource.id },
        body: {
          path: newPath.trim(),
          content_base64: toBase64(newContent),
          is_executable: newExecutable,
          expected_digest: resource.content_digest,
        },
        throwOnError: true,
      }),
    onSuccess: () => {
      showToast(t("plugins.fileSaved"), "success");
      setNewPath("");
      setNewContent("");
      invalidate();
    },
    onError: (error) => showToast(pluginErrorMessage(error, t), "error"),
  });
  return (
    <DetailPanel>
      <DetailPanelHeader
        title={resource.display_name}
        action={
          <Button variant="ghost" size="icon-sm" aria-label={t("common.close")} onClick={onClose}>
            <X size={16} />
          </Button>
        }
      />
      <div className="space-y-4 overflow-y-auto p-4">
        <div className="flex flex-wrap items-center gap-2">
          <Badge variant="outline">{scopeLabel(resource.scope, t)}</Badge>
          <Badge variant={statusVariant(resource.is_enabled, resource.is_forbidden)}>
            {resource.is_forbidden
              ? t("plugins.forbidden")
              : resource.is_enabled
                ? t("plugins.enabled")
                : t("plugins.disabled")}
          </Badge>
          {resource.is_overridden && <Badge variant="warning">{t("plugins.overridden")}</Badge>}
          {resource.is_read_only && <Badge variant="secondary">{t("plugins.readOnly")}</Badge>}
        </div>
        <div className="space-y-1 text-xs text-muted-foreground">
          <p>
            {resource.name} · v{resource.version}
          </p>
          <p className="break-all font-mono">{resource.content_digest}</p>
        </div>
        <p className="text-sm text-muted-foreground">{resource.description}</p>
        <DiagnosticList resource={resource} />
        <ResourceSummary resource={resource} />
        <div className="space-y-2">
          <p className="text-xs font-semibold text-muted-foreground">{t("plugins.files")}</p>
          {resource.files.map((item) => (
            <button
              type="button"
              key={item.path}
              onClick={() => setFile(item)}
              className="flex w-full items-center gap-2 rounded-md border border-border p-2 text-left text-xs hover:bg-muted"
            >
              <FileText size={16} />
              <span className="min-w-0 flex-1 truncate font-mono">{item.path}</span>
              <span className="text-muted-foreground">{item.size} B</span>
              {item.is_executable && (
                <Badge variant="outline" size="sm">
                  exec
                </Badge>
              )}
            </button>
          ))}
        </div>
        <FileEditor resource={resource} file={file} onSaved={invalidate} onDeleted={invalidate} />
        {!resource.is_read_only && (
          <div className="space-y-2 rounded-lg border border-border p-3">
            <p className="text-xs font-semibold text-muted-foreground">{t("plugins.createFile")}</p>
            <Input
              value={newPath}
              onChange={(event) => setNewPath(event.target.value)}
              placeholder="skills/example/SKILL.md"
              nativeInput
            />
            <textarea
              value={newContent}
              onChange={(event) => setNewContent(event.target.value)}
              className="min-h-32 w-full rounded-md border border-border bg-background p-3 font-mono text-xs"
              aria-label={t("plugins.fileContent")}
            />
            <label className="flex items-center gap-2 text-xs text-muted-foreground">
              <input
                type="checkbox"
                checked={newExecutable}
                onChange={(event) => setNewExecutable(event.target.checked)}
              />
              {t("plugins.executable")}
            </label>
            <Button
              onClick={() => createFile.mutate()}
              disabled={!newPath.trim() || createFile.isPending}
              loading={createFile.isPending}
            >
              <FilePlus2 size={16} />
              {t("plugins.createFile")}
            </Button>
          </div>
        )}
        <div className="space-y-2 rounded-lg border border-border p-3">
          <p className="text-xs font-semibold text-muted-foreground">{t("plugins.copy")}</p>
          <ScopePicker
            band={band}
            scope={copyScope}
            agentId={copyAgent}
            agents={agents}
            onScope={setCopyScope}
            onAgent={setCopyAgent}
          />
          <Button
            onClick={() => copy.mutate()}
            disabled={
              resource.is_read_only ||
              (isAgentManagedScope(copyScope) && !copyAgent) ||
              copy.isPending
            }
            loading={copy.isPending}
          >
            {t("plugins.copy")}
          </Button>
        </div>
        <div className="flex flex-wrap justify-end gap-2">
          <Switch
            checked={resource.is_enabled}
            disabled={resource.is_read_only || resource.is_forbidden || toggle.isPending}
            onCheckedChange={(value) => toggle.mutate(value)}
            aria-label={t("plugins.enabled")}
          />
          <Button
            variant="destructive"
            onClick={() => remove.mutate()}
            disabled={resource.is_read_only || remove.isPending}
            loading={remove.isPending}
          >
            <Trash2 size={16} />
            {t("common.delete")}
          </Button>
        </div>
      </div>
    </DetailPanel>
  );
}

function CreatePlugin({
  band,
  agents,
  onDone,
}: {
  band: ScopeBand;
  agents: Array<{ id?: string; name?: string }>;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const { showToast } = useToast();
  const [name, setName] = useState("");
  const [path, setPath] = useState("manifest.yaml");
  const [content, setContent] = useState("");
  const [scope, setScope] = useState<ManagedScope>(scopesForBand(band)[0]);
  const [agentId, setAgentId] = useState("");
  const create = useMutation({
    mutationFn: () => {
      if (isAgentManagedScope(scope) && !agentId) {
        throw new Error(t("plugins.selectAgent"));
      }
      const files = {
        [path.trim()]: {
          content_base64: toBase64(content),
          is_executable: false,
        },
      } satisfies ComponentsCreatePluginRequest["files"];
      const body: ComponentsCreatePluginRequest = {
        name: name.trim(),
        scope,
        files,
      };
      if (agentId) body.agent_id = agentId;
      return createPlugin({
        body,
        throwOnError: true,
      });
    },
    onSuccess: () => {
      showToast(t("plugins.created"), "success");
      onDone();
    },
    onError: (error) => showToast(pluginErrorMessage(error, t), "error"),
  });
  return (
    <div className="space-y-3 rounded-lg border border-border p-4">
      <div className="flex items-center gap-2">
        <FileCode2 size={18} />
        <p className="text-sm font-semibold">{t("plugins.create")}</p>
      </div>
      <Input
        value={name}
        onChange={(event) => setName(event.target.value)}
        placeholder="my-plugin"
        nativeInput
      />
      <ScopePicker
        band={band}
        scope={scope}
        agentId={agentId}
        agents={agents}
        onScope={setScope}
        onAgent={setAgentId}
      />
      <Input
        value={path}
        onChange={(event) => setPath(event.target.value)}
        placeholder="manifest.yaml"
        nativeInput
      />
      <textarea
        value={content}
        onChange={(event) => setContent(event.target.value)}
        className="min-h-40 w-full rounded-md border border-border bg-background p-3 font-mono text-xs"
        aria-label={t("plugins.fileContent")}
      />
      <div className="flex justify-end">
        <Button
          onClick={() => create.mutate()}
          disabled={!name.trim() || !path.trim() || create.isPending}
          loading={create.isPending}
        >
          <Upload size={16} />
          {t("common.create")}
        </Button>
      </div>
    </div>
  );
}

export function UnifiedPluginsPage({ scopeBand = "system" }: { scopeBand?: ScopeBand }) {
  const { t } = useI18n();
  const navigate = useNavigate();
  // SAFETY: both plugin routes expose the same optional `pluginId` path parameter.
  const params = useParams({ strict: false }) as { pluginId?: string };
  const queryClient = useQueryClient();
  const agentsQuery = useQuery(
    scopeBand === "system" ? allAgentsAdminQueryOptions(true) : agentsQueryOptions,
  );
  const agents = agentsQuery.data ?? [];
  const [agentId, setAgentId] = useState("");
  const [scope, setScope] = useState<ManagedScope>(scopesForBand(scopeBand)[0]);
  const pluginsQuery = useQuery(scopedPluginsQueryOptions(scopeBand, agentId || undefined));
  const plugins = pluginsQuery.data ?? [];
  const selected = useMemo(
    () => plugins.find((item) => item.id === params.pluginId),
    [plugins, params.pluginId],
  );
  const close = () =>
    void navigate({
      to: scopeBand === "system" ? "/admin/integrations/plugins" : "/settings/plugins",
    });
  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ["plugins"] });
  };
  if (pluginsQuery.isPending)
    return (
      <div className="flex flex-1 items-center justify-center">
        <Spinner />
      </div>
    );
  if (pluginsQuery.isError)
    return (
      <ErrorState
        title={t("plugins.loadFailed")}
        description={pluginErrorMessage(pluginsQuery.error, t)}
        onRetry={() => void pluginsQuery.refetch()}
      />
    );
  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-y-auto p-4 sm:p-8">
      <div className="mx-auto flex w-full max-w-6xl flex-col gap-4">
        <div className="flex flex-wrap items-end justify-between gap-3">
          <div>
            <h1 className="text-lg font-semibold">{t("plugins.title")}</h1>
            <p className="text-sm text-muted-foreground">{t("plugins.rawResourceHelp")}</p>
          </div>
          <Button variant="outline" onClick={refresh}>
            <RefreshCw size={16} />
            {t("plugins.refresh")}
          </Button>
        </div>
        <ScopePicker
          band={scopeBand}
          scope={scope}
          agentId={agentId}
          agents={agents}
          onScope={setScope}
          onAgent={setAgentId}
        />
        <CreatePlugin band={scopeBand} agents={agents} onDone={refresh} />
        <div className="grid gap-3 md:grid-cols-2">
          {plugins.length === 0 ? (
            <ErrorState title={t("plugins.noPlugins")} description={t("plugins.noPluginsDesc")} />
          ) : (
            plugins.map((plugin) => (
              <Card key={plugin.id} className="flex flex-col gap-3 p-4">
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0">
                    <Link
                      to={
                        scopeBand === "system"
                          ? "/admin/integrations/plugins/$pluginId"
                          : "/settings/plugins/$pluginId"
                      }
                      params={{ pluginId: plugin.id }}
                      className="font-medium hover:underline"
                    >
                      {plugin.display_name}
                    </Link>
                    <p className="mt-1 truncate font-mono text-xs text-muted-foreground">
                      {plugin.name} · {plugin.version}
                    </p>
                  </div>
                  <div className="flex flex-wrap justify-end gap-1.5">
                    <Badge variant="outline" size="sm">
                      {scopeLabel(plugin.scope, t)}
                    </Badge>
                    <Badge
                      variant={statusVariant(plugin.is_enabled, plugin.is_forbidden)}
                      size="sm"
                    >
                      {plugin.is_forbidden
                        ? t("plugins.forbidden")
                        : plugin.is_enabled
                          ? t("plugins.enabled")
                          : t("plugins.disabled")}
                    </Badge>
                    {plugin.is_overridden && (
                      <Badge variant="warning" size="sm">
                        {t("plugins.overridden")}
                      </Badge>
                    )}
                  </div>
                </div>
                <p className="line-clamp-2 text-sm text-muted-foreground">{plugin.description}</p>
                <div className="flex flex-wrap gap-1.5">
                  {plugin.diagnostics.some((item) => item.severity === "error") && (
                    <Badge variant="destructive" size="sm">
                      {t("plugins.invalid")}
                    </Badge>
                  )}
                  <Badge variant="secondary" size="sm">
                    {plugin.files.length} {t("plugins.files")}
                  </Badge>
                </div>
              </Card>
            ))
          )}
        </div>
        {selected && (
          <PluginDetail
            resource={selected}
            band={scopeBand}
            agents={agents}
            onRefresh={refresh}
            onClose={close}
          />
        )}
      </div>
    </div>
  );
}

export function PersonalUnifiedPluginsPage() {
  return <UnifiedPluginsPage scopeBand="personal" />;
}

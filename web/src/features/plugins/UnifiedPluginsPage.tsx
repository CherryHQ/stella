import { useMemo, useState } from "react";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  createPlugin,
  createPluginConfig,
  createMcpServer,
  deletePluginConfig,
  deletePlugin,
  deleteMcpServer,
  probeMcpServer,
  resetPluginConfig,
  disconnectMcpServerOAuth,
  startMcpServerOAuth,
  updateMcpServer,
  updatePluginConfig,
} from "@/lib/api-client/sdk.gen";
import type {
  ComponentsCreatePluginRequestWritable,
  ComponentsPluginConfigInputWritable,
  ComponentsUpdatePluginConfigRequestWritable,
  PluginConfig,
  PluginDefinition,
} from "@/lib/api-client";
import {
  pluginConfigsQueryOptions,
  pluginsQueryOptions,
  type PluginScope,
} from "@/lib/queries/plugins";
import { agentsQueryOptions, allAgentsAdminQueryOptions } from "@/lib/queries/agents";
import { isAgentManagedScope, scopesForBand, type ScopeBand } from "@/lib/scope-band";
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
import {
  SettingsCardSection,
  SettingsDetailSheet,
  SettingsGridPage,
} from "@/features/settings/SettingsCardGrid";
import { ConfirmDialog } from "@/features/settings/ConfirmDialog";
import { DetailPanel, DetailPanelHeader } from "@/features/settings/SettingsDetailPanel";
import { useToast } from "@/hooks/use-toast";
import { useI18n } from "@/lib/i18n";
import { apiErrorCode, apiErrorMessage } from "@/lib/api-error";
import {
  PluginConfigEditor,
  type PluginConfigCredentials,
  type PluginConfigPayload,
} from "@/features/plugins/PluginConfigEditor";
import { McpInstallSheet } from "@/features/mcp/McpInstallSheet";
import {
  McpServerFields,
  type McpAuthType,
  type McpTransport,
} from "@/features/mcp/McpServerFields";
import { Package, Plus, RotateCcw, Trash2 } from "lucide-react";

export type Translate = ReturnType<typeof useI18n>["t"];

const oauthClientInitializationMessage =
  "administrator must initialize this connection before users can authorize their own accounts";

export function pluginErrorMessage(error: unknown, t: Translate): string {
  const message = apiErrorMessage(error, t("common.error"));
  if (apiErrorCode(error) === 409 && message === oauthClientInitializationMessage) {
    return t("plugins.oauthAdminInitializationRequired");
  }
  return message;
}

function scopeLabel(scope: PluginScope, t: Translate): string {
  return t(`plugins.scope.${scope}`);
}

export function configHasMcpOAuth(config: Pick<PluginConfig, "resource_summary">): boolean {
  return config.resource_summary.mcp_servers.some((server) => server.auth_type === "oauth");
}

function oauthChildID(config: PluginConfig, serverKey?: string): string {
  const children = config.resource_summary.mcp_servers.filter(
    (server) => server.auth_type === "oauth",
  );
  const selected = serverKey
    ? children.find((server) => server.server_key === serverKey)
    : children.length === 1
      ? children[0]
      : undefined;
  if (!selected?.child_id) throw new Error("select one MCP OAuth server before connecting");
  return selected.child_id;
}

function BackendSummary({ config, t }: { config: PluginConfig; t: Translate }) {
  const summary = config.resource_summary;
  const servers = summary.mcp_servers;
  const binaries = summary.binaries.map((binary) => `${binary.name} ${binary.version}`.trim());
  const skills = summary.skills.map((skill) => (
    <Badge key={skill.name} variant="outline" size="sm">
      {skill.name}
    </Badge>
  ));
  if (
    servers.length === 0 &&
    binaries.length === 0 &&
    skills.length === 0 &&
    summary.session_env.length === 0 &&
    !summary.oauth_provider_configured
  )
    return null;
  return (
    <div className="space-y-1">
      {servers.length > 0 && (
        <div className="flex flex-wrap gap-1.5">
          {servers.map((server) => (
            <span key={server.server_key} className="flex gap-1.5">
              <Badge variant="outline" size="sm">
                {server.server_key}
              </Badge>
              {server.transport && (
                <Badge variant="outline" size="sm">
                  {server.transport}
                </Badge>
              )}
              {server.auth_type && (
                <Badge variant="outline" size="sm">
                  {server.auth_type}
                </Badge>
              )}
              {server.credential_mode && (
                <Badge variant="outline" size="sm">
                  {server.credential_mode}
                </Badge>
              )}
              <Badge variant={server.endpoint_configured ? "success" : "warning"} size="sm">
                {t(
                  server.endpoint_configured
                    ? "plugins.summary.endpointReady"
                    : "plugins.summary.endpointMissing",
                )}
              </Badge>
              {server.auth_type === "bearer" && (
                <Badge variant={server.bearer_configured ? "success" : "warning"} size="sm">
                  {t(
                    server.bearer_configured
                      ? "plugins.summary.bearerReady"
                      : "plugins.summary.bearerMissing",
                  )}
                </Badge>
              )}
              {server.auth_type === "oauth" && (
                <>
                  <Badge
                    variant={server.oauth_client_id_configured ? "success" : "warning"}
                    size="sm"
                  >
                    {t(
                      server.oauth_client_id_configured
                        ? "plugins.summary.oauthClientReady"
                        : "plugins.summary.oauthClientMissing",
                    )}
                  </Badge>
                  <Badge
                    variant={server.oauth_client_secret_configured ? "success" : "warning"}
                    size="sm"
                  >
                    {t(
                      server.oauth_client_secret_configured
                        ? "plugins.summary.oauthClientSecretReady"
                        : "plugins.summary.oauthClientSecretMissing",
                    )}
                  </Badge>
                </>
              )}
            </span>
          ))}
        </div>
      )}
      {binaries.length > 0 && (
        <p className="text-xs text-muted-foreground">{binaries.join(", ")}</p>
      )}
      {skills.length > 0 && <div className="flex flex-wrap gap-1.5">{skills}</div>}
      {summary.session_env.length > 0 && (
        <p className="text-xs text-muted-foreground">
          {t("plugins.summary.env", { count: summary.session_env.length })}
        </p>
      )}
      {summary.oauth_provider_configured && (
        <Badge variant="info" size="sm">
          {t("plugins.summary.oauthConfigured")}
        </Badge>
      )}
    </div>
  );
}

function ConfigRow({
  config,
  onEnabled,
  onInherit,
  onEdit,
  onAddChild,
  onProbe,
  onDeleteChild,
  onReset,
  onDelete,
  onOAuthConnect,
  onOAuthDisconnect,
  busy,
  t,
}: {
  config: PluginConfig;
  onEnabled: (enabled: boolean) => void;
  onInherit: () => void;
  onEdit: (serverKey?: string) => void;
  onAddChild?: () => void;
  onProbe?: (serverKey: string) => void;
  onDeleteChild?: (serverKey: string) => void;
  onReset?: () => void;
  onDelete?: () => void;
  onOAuthConnect?: (serverKey?: string) => void;
  onOAuthDisconnect?: (serverKey?: string) => void;
  busy: boolean;
  t: Translate;
}) {
  const children = config.resource_summary.mcp_servers;
  const enabledLabel =
    config.is_enabled === null
      ? t("plugins.inherited")
      : config.is_enabled
        ? t("plugins.enabled")
        : t("plugins.disabled");
  return (
    <Card className="gap-3 p-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-sm font-medium">{scopeLabel(config.scope, t)}</span>
            <Badge
              variant={
                config.is_enabled === null ? "secondary" : config.is_enabled ? "success" : "warning"
              }
              size="sm"
            >
              {enabledLabel}
            </Badge>
          </div>
          <p className="text-xs text-muted-foreground">
            {config.agent_id ? `${t("plugins.agent")}: ${config.agent_id}` : t("plugins.allAgents")}
          </p>
          <BackendSummary config={config} t={t} />
          {children.length > 0 && (
            <div className="space-y-1.5 pt-1">
              {children.map((child) => (
                <div
                  key={child.server_key}
                  className="flex flex-wrap items-center gap-1.5 rounded-md border border-border/60 px-2 py-1"
                >
                  <span className="mr-auto text-xs font-medium">{child.server_key}</span>
                  <Button
                    variant="ghost"
                    size="xs"
                    onClick={() => onEdit(child.server_key)}
                    disabled={busy}
                  >
                    {t("common.edit")}
                  </Button>
                  {onProbe && child.child_id && (
                    <Button
                      variant="ghost"
                      size="xs"
                      onClick={() => onProbe(child.server_key)}
                      disabled={busy}
                    >
                      {t("mcp.server.probe")}
                    </Button>
                  )}
                  {child.auth_type === "oauth" && onOAuthConnect && (
                    <Button
                      variant="ghost"
                      size="xs"
                      onClick={() => onOAuthConnect(child.server_key)}
                      disabled={busy}
                    >
                      {t("plugins.oauthAuthorize")}
                    </Button>
                  )}
                  {child.auth_type === "oauth" && onOAuthDisconnect && (
                    <Button
                      variant="ghost"
                      size="xs"
                      onClick={() => onOAuthDisconnect(child.server_key)}
                      disabled={busy}
                    >
                      {t("plugins.oauthDisconnect")}
                    </Button>
                  )}
                  {onDeleteChild && child.child_id && (
                    <Button
                      variant="ghost"
                      size="xs"
                      onClick={() => onDeleteChild(child.server_key)}
                      disabled={busy}
                    >
                      {t("common.delete")}
                    </Button>
                  )}
                </div>
              ))}
            </div>
          )}
        </div>
        <div className="flex items-center gap-2">
          {config.resource_summary.binaries.length > 0 && (
            <Button variant="ghost" size="xs" onClick={() => onEdit()} disabled={busy}>
              {t("common.edit")}
            </Button>
          )}
          {onAddChild && (
            <Button variant="outline" size="xs" onClick={onAddChild} disabled={busy}>
              {t("plugins.addMcpServer")}
            </Button>
          )}
          <Switch
            checked={config.is_enabled === true}
            disabled={busy}
            onCheckedChange={onEnabled}
            aria-label={enabledLabel}
          />
          <Button
            variant="ghost"
            size="xs"
            onClick={onInherit}
            disabled={busy || config.is_enabled === null}
          >
            {t("plugins.inherit")}
          </Button>
          {onReset && (
            <Button variant="ghost" size="xs" onClick={onReset} disabled={busy}>
              <RotateCcw className="size-3.5" />
              {t("plugins.resetConfig")}
            </Button>
          )}
          {onDelete && (
            <Button variant="ghost" size="xs" onClick={onDelete} disabled={busy}>
              <Trash2 className="size-3.5" />
              {t("common.delete")}
            </Button>
          )}
        </div>
      </div>
    </Card>
  );
}

export function UnifiedPluginsPage({ scopeBand = "system" }: { scopeBand?: ScopeBand }) {
  const { t } = useI18n();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { showToast } = useToast(4000);
  const params = useParams({ strict: false }) as { pluginId?: string };
  const pluginsQuery = useQuery(pluginsQueryOptions);
  const agentsQuery = useQuery(
    scopeBand === "system" ? allAgentsAdminQueryOptions(true) : agentsQueryOptions,
  );
  const agents = agentsQuery.data ?? [];
  const [selectedAgentID, setSelectedAgentID] = useState("");
  const visibleScopes = scopesForBand(scopeBand) as readonly PluginScope[];
  const plugins = pluginsQuery.data ?? [];
  const selectedPlugin = useMemo(
    () => plugins.find((plugin) => plugin.id === params.pluginId),
    [plugins, params.pluginId],
  );
  const selectedPluginID = selectedPlugin?.id;
  const closeDetail = () =>
    void navigate({
      to: scopeBand === "system" ? "/admin/integrations/plugins" : "/settings/plugins",
    });
  const configQueries = useQueries({
    queries: selectedPluginID
      ? visibleScopes.map((scope) =>
          pluginConfigsQueryOptions(
            selectedPluginID,
            scope,
            isAgentManagedScope(scope) ? selectedAgentID || undefined : undefined,
          ),
        )
      : [],
  });
  const [pendingDelete, setPendingDelete] = useState<PluginConfig | null>(null);
  const [pendingChildDelete, setPendingChildDelete] = useState<{
    config: PluginConfig;
    serverKey: string;
  } | null>(null);
  const [pendingPluginDelete, setPendingPluginDelete] = useState<PluginDefinition | null>(null);
  const [editingConfig, setEditingConfig] = useState<{
    config: PluginConfig;
    serverKey?: string;
  } | null>(null);
  const [addingChildConfig, setAddingChildConfig] = useState<PluginConfig | null>(null);
  const [newChildKey, setNewChildKey] = useState("");
  const [newChildURL, setNewChildURL] = useState("");
  const [newChildTransport, setNewChildTransport] = useState<McpTransport>("streamable_http");
  const [newChildAuthType, setNewChildAuthType] = useState<McpAuthType>("none");
  const [newChildCredentialMode, setNewChildCredentialMode] = useState<"shared" | "per_user">(
    "shared",
  );
  const [newChildToken, setNewChildToken] = useState("");
  const [newChildOAuthClientID, setNewChildOAuthClientID] = useState("");
  const [newChildOAuthSecret, setNewChildOAuthSecret] = useState("");
  const [newMcpOpen, setNewMcpOpen] = useState(false);
  const [registryOpen, setRegistryOpen] = useState(false);
  const [newMcpName, setNewMcpName] = useState("");
  const [newMcpURL, setNewMcpURL] = useState("");
  const [newMcpID, setNewMcpID] = useState("");
  const [newMcpDescription, setNewMcpDescription] = useState("");
  const [newMcpScope, setNewMcpScope] = useState<PluginScope>(visibleScopes[0]);
  const [newScope, setNewScope] = useState<PluginScope>(visibleScopes[0]);
  const closeNewMcp = () => {
    setNewMcpOpen(false);
    setNewMcpURL("");
  };
  const closeAddChild = () => {
    setAddingChildConfig(null);
    setNewChildKey("");
    setNewChildURL("");
    setNewChildToken("");
    setNewChildOAuthClientID("");
    setNewChildOAuthSecret("");
  };
  const invalidate = () => {
    if (selectedPluginID)
      void queryClient.invalidateQueries({
        queryKey: ["plugin-configs", selectedPluginID],
      });
    void queryClient.invalidateQueries({ queryKey: ["plugins"] });
  };
  const configMutation = useMutation({
    mutationFn: async (input: { config: PluginConfig; enabled: boolean | null }) => {
      if (!selectedPluginID) throw new Error(t("plugins.noSelection"));
      const { data } = await updatePluginConfig({
        path: { plugin_id: selectedPluginID, config_id: input.config.id },
        body: {
          expected_revision: input.config.revision,
          is_enabled: input.enabled,
        },
        throwOnError: true,
      });
      return data;
    },
    onSuccess: () => {
      invalidate();
      showToast(t("plugins.configUpdated"));
    },
    onError: (error) => showToast(pluginErrorMessage(error, t), "error"),
  });
  const resetMutation = useMutation({
    mutationFn: async (config: PluginConfig) => {
      if (!selectedPluginID) throw new Error(t("plugins.noSelection"));
      const { data } = await resetPluginConfig({
        path: { plugin_id: selectedPluginID, config_id: config.id },
        body: { expected_revision: config.revision },
        throwOnError: true,
      });
      return data;
    },
    onSuccess: () => {
      invalidate();
      showToast(t("plugins.configReset"));
    },
    onError: (error) => showToast(pluginErrorMessage(error, t), "error"),
  });
  const deleteMutation = useMutation({
    mutationFn: async (config: PluginConfig) => {
      if (!selectedPluginID) throw new Error(t("plugins.noSelection"));
      await deletePluginConfig({
        path: { plugin_id: selectedPluginID, config_id: config.id },
        query: { expected_revision: config.revision },
        throwOnError: true,
      });
    },
    onSuccess: () => {
      invalidate();
      showToast(t("plugins.configDeleted"));
    },
    onError: (error) => showToast(pluginErrorMessage(error, t), "error"),
  });
  const createMutation = useMutation({
    mutationFn: async () => {
      if (!selectedPluginID) throw new Error(t("plugins.noSelection"));
      const { data } = await createPluginConfig({
        path: { plugin_id: selectedPluginID },
        body: {
          scope: newScope,
          ...(isAgentManagedScope(newScope) && selectedAgentID
            ? { agent_id: selectedAgentID }
            : {}),
          is_enabled: false,
        },
        throwOnError: true,
      });
      return data;
    },
    onSuccess: () => {
      invalidate();
      showToast(t("plugins.configCreated"));
    },
    onError: (error) => showToast(pluginErrorMessage(error, t), "error"),
  });
  const editMutation = useMutation({
    mutationFn: async (input: {
      config: PluginConfig;
      serverKey?: string;
      payload: PluginConfigPayload;
      credentials: PluginConfigCredentials;
    }) => {
      if (!selectedPluginID) throw new Error(t("plugins.noSelection"));
      if (input.serverKey !== undefined) {
        const child = input.config.resource_summary.mcp_servers.find(
          (server) => server.server_key === input.serverKey,
        );
        if (!child?.child_id) throw new Error("select one MCP server before editing");
        const childID = child.child_id;
        if (!childID) throw new Error("MCP child server was not returned");
        const payload = input.payload.config ?? {};
        const { data } = await updateMcpServer({
          path: { id: childID },
          body: {
            expected_parent_revision: input.config.revision,
            url: typeof payload.url === "string" ? payload.url : undefined,
            transport: payload.transport as "streamable_http" | "sse" | undefined,
            auth_type: payload.auth_type as "none" | "bearer" | "oauth" | undefined,
            credential_mode: payload.credential_mode as "shared" | "per_user" | undefined,
            credentials: Object.keys(input.credentials).length > 0 ? input.credentials : undefined,
          },
          throwOnError: true,
        });
        return data;
      }
      const body: ComponentsUpdatePluginConfigRequestWritable = {
        expected_revision: input.config.revision,
      };
      if (input.payload.config) body.config = input.payload.config;
      if (input.payload.binary_versions) body.binary_versions = input.payload.binary_versions;
      if (Object.keys(input.credentials).length > 0) body.credentials = input.credentials;
      const { data } = await updatePluginConfig({
        path: { plugin_id: selectedPluginID, config_id: input.config.id },
        body,
        throwOnError: true,
      });
      return data;
    },
    onSuccess: () => {
      invalidate();
      setEditingConfig(null);
      showToast(t("plugins.configUpdated"));
    },
    onError: (error) => showToast(pluginErrorMessage(error, t), "error"),
  });
  const createChildMutation = useMutation({
    mutationFn: async () => {
      if (!addingChildConfig) throw new Error(t("plugins.noSelection"));
      const key = newChildKey.trim();
      const url = newChildURL.trim();
      if (!key || !url) throw new Error(t("plugins.mcpChildRequired"));
      const credentials: Record<string, string> = {};
      if (newChildAuthType === "bearer" && newChildToken.trim())
        credentials.token = newChildToken.trim();
      if (newChildAuthType === "oauth") {
        if (newChildOAuthClientID.trim())
          credentials.oauth_client_id = newChildOAuthClientID.trim();
        if (newChildOAuthSecret) credentials.oauth_client_secret = newChildOAuthSecret;
      }
      return createMcpServer({
        body: {
          parent_config_id: addingChildConfig.id,
          server_key: key,
          expected_parent_revision: addingChildConfig.revision ?? 1,
          url,
          transport: newChildTransport,
          auth_type: newChildAuthType,
          credential_mode: newChildCredentialMode,
          credentials: Object.keys(credentials).length > 0 ? credentials : undefined,
        },
        throwOnError: true,
      });
    },
    onSuccess: () => {
      invalidate();
      closeAddChild();
      showToast(t("plugins.mcpChildCreated"));
    },
    onError: (error) => showToast(pluginErrorMessage(error, t), "error"),
  });
  const probeChildMutation = useMutation({
    mutationFn: async (input: { config: PluginConfig; serverKey: string }) => {
      const child = input.config.resource_summary.mcp_servers.find(
        (server) => server.server_key === input.serverKey,
      );
      if (!child?.child_id) throw new Error(t("plugins.noSelection"));
      return probeMcpServer({ path: { id: child.child_id }, throwOnError: true });
    },
    onSuccess: () => {
      invalidate();
      showToast(t("mcp.server.probed", { time: new Date().toISOString() }));
    },
    onError: (error) => showToast(pluginErrorMessage(error, t), "error"),
  });
  const deleteChildMutation = useMutation({
    mutationFn: async (input: { config: PluginConfig; serverKey: string }) => {
      const child = input.config.resource_summary.mcp_servers.find(
        (server) => server.server_key === input.serverKey,
      );
      if (!child?.child_id) throw new Error(t("plugins.noSelection"));
      await deleteMcpServer({
        path: { id: child.child_id },
        query: { expected_parent_revision: input.config.revision ?? 1 },
        throwOnError: true,
      });
    },
    onSuccess: () => {
      invalidate();
      showToast(t("plugins.configUpdated"));
    },
    onError: (error) => showToast(pluginErrorMessage(error, t), "error"),
  });
  const oauthStartMutation = useMutation({
    mutationFn: async (input: { config: PluginConfig; serverKey?: string }) => {
      const { data } = await startMcpServerOAuth({
        path: { id: oauthChildID(input.config, input.serverKey) },
        throwOnError: true,
      });
      return data;
    },
    onSuccess: (data) => {
      if (data?.authorization_url) window.location.href = data.authorization_url;
    },
    onError: (error) => showToast(pluginErrorMessage(error, t), "error"),
  });
  const oauthDisconnectMutation = useMutation({
    mutationFn: async (input: { config: PluginConfig; serverKey?: string }) => {
      await disconnectMcpServerOAuth({
        path: { id: oauthChildID(input.config, input.serverKey) },
        throwOnError: true,
      });
    },
    onSuccess: () => {
      invalidate();
      showToast(t("plugins.oauthDisconnected"));
    },
    onError: (error) => showToast(pluginErrorMessage(error, t), "error"),
  });
  const createMcpMutation = useMutation({
    mutationFn: async (input: {
      payload: PluginConfigPayload;
      credentials: PluginConfigCredentials;
    }) => {
      const displayName = newMcpName.trim();
      const pluginID = newMcpID.trim();
      if (!displayName || !pluginID) throw new Error(t("plugins.mcpIdentityRequired"));
      const initialConfig: ComponentsPluginConfigInputWritable = {
        scope: newMcpScope,
        is_enabled: false,
        config: input.payload.config,
      };
      if (isAgentManagedScope(newMcpScope) && selectedAgentID) {
        initialConfig.agent_id = selectedAgentID;
      }
      if (Object.keys(input.credentials).length > 0) {
        initialConfig.credentials = input.credentials;
      }
      const body: ComponentsCreatePluginRequestWritable = {
        display_name: displayName,
        name: pluginID,
        definition_spec: newMcpDescription.trim() ? { description: newMcpDescription.trim() } : {},
        initial_config: initialConfig,
      };
      const { data } = await createPlugin({
        body,
        throwOnError: true,
      });
      return data;
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["plugins"] });
      closeNewMcp();
      setNewMcpName("");
      setNewMcpID("");
      setNewMcpDescription("");
      showToast(t("plugins.created"));
    },
    onError: (error) => showToast(pluginErrorMessage(error, t), "error"),
  });
  const definitionDeleteMutation = useMutation({
    mutationFn: async (plugin: PluginDefinition) => {
      await deletePlugin({
        path: { plugin_id: plugin.id },
        query: { expected_revision: plugin.revision },
        throwOnError: true,
      });
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["plugins"] });
      showToast(t("plugins.deleted"));
      closeDetail();
    },
    onError: (error) => showToast(pluginErrorMessage(error, t), "error"),
  });
  const groups = useMemo(() => [{ title: t("plugins.title"), items: plugins }], [plugins, t]);
  if (pluginsQuery.isPending)
    return (
      <div className="flex h-full items-center justify-center">
        <Spinner className="size-5 text-muted-foreground" />
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
  const detail = selectedPlugin ? (
    <DetailPanel>
      <DetailPanelHeader
        title={selectedPlugin.display_name}
        subtitle={
          <div className="flex flex-wrap items-center gap-1.5">
            {selectedPlugin.is_builtin && (
              <Badge variant="secondary" size="sm">
                {t("plugins.builtin")}
              </Badge>
            )}
          </div>
        }
      />
      {typeof selectedPlugin.spec.description === "string" && selectedPlugin.spec.description && (
        <p className="text-sm text-muted-foreground">{selectedPlugin.spec.description}</p>
      )}
      <Field>
        <FieldLabel>{t("plugins.agent")}</FieldLabel>
        <Select
          value={selectedAgentID || "__none"}
          onValueChange={(value) => setSelectedAgentID(value === "__none" || !value ? "" : value)}
        >
          <SelectTrigger>
            <SelectValue placeholder={t("plugins.selectAgent")} />
          </SelectTrigger>
          <SelectPopup>
            <SelectItem value="__none">{t("plugins.selectAgent")}</SelectItem>
            {agents.map((agent) => (
              <SelectItem key={agent.id} value={agent.id}>
                {agent.name}
              </SelectItem>
            ))}
          </SelectPopup>
        </Select>
      </Field>
      <div className="space-y-2">
        <p className="text-xs font-semibold text-muted-foreground">{t("plugins.configuration")}</p>
        {visibleScopes.map((scope, index) => {
          const query = configQueries[index];
          const configs = query.data ?? [];
          return (
            <section key={scope} className="space-y-2">
              <div className="flex items-center justify-between gap-2">
                <div className="flex items-center gap-2">
                  <h3 className="text-sm font-medium">{scopeLabel(scope, t)}</h3>
                  <Badge variant="secondary" size="sm">
                    {configs.length}
                  </Badge>
                </div>
                {query.isFetching && <Spinner className="size-4 text-muted-foreground" />}
              </div>
              {isAgentManagedScope(scope) && !selectedAgentID ? (
                <p className="text-xs text-muted-foreground">{t("plugins.selectAgent")}</p>
              ) : query.isError ? (
                <p className="text-xs text-destructive-foreground">
                  {t("plugins.scopeUnavailable")}
                </p>
              ) : configs.length === 0 ? (
                <p className="text-xs text-muted-foreground">{t("plugins.noScopeConfig")}</p>
              ) : (
                configs.map((config) => (
                  <ConfigRow
                    key={config.id}
                    config={config}
                    busy={
                      configMutation.isPending ||
                      resetMutation.isPending ||
                      deleteMutation.isPending ||
                      probeChildMutation.isPending ||
                      deleteChildMutation.isPending ||
                      oauthStartMutation.isPending ||
                      oauthDisconnectMutation.isPending
                    }
                    onEnabled={(enabled) => configMutation.mutate({ config, enabled })}
                    onInherit={() => configMutation.mutate({ config, enabled: null })}
                    onEdit={(serverKey) => setEditingConfig({ config, serverKey })}
                    onAddChild={
                      selectedPlugin.spec.origin === "remote_mcp"
                        ? () => setAddingChildConfig(config)
                        : undefined
                    }
                    onProbe={
                      config.resource_summary.mcp_servers.length > 0
                        ? (serverKey) => probeChildMutation.mutate({ config, serverKey })
                        : undefined
                    }
                    onDeleteChild={
                      selectedPlugin.spec.origin === "remote_mcp" &&
                      config.resource_summary.mcp_servers.length > 0
                        ? (serverKey) => setPendingChildDelete({ config, serverKey })
                        : undefined
                    }
                    onReset={
                      selectedPlugin.is_builtin && scope === "system"
                        ? () => resetMutation.mutate(config)
                        : undefined
                    }
                    onDelete={
                      selectedPlugin.is_builtin && scope === "system"
                        ? undefined
                        : () => setPendingDelete(config)
                    }
                    onOAuthConnect={
                      configHasMcpOAuth(config)
                        ? (serverKey) => oauthStartMutation.mutate({ config, serverKey })
                        : undefined
                    }
                    onOAuthDisconnect={
                      configHasMcpOAuth(config)
                        ? (serverKey) => oauthDisconnectMutation.mutate({ config, serverKey })
                        : undefined
                    }
                    t={t}
                  />
                ))
              )}
            </section>
          );
        })}
      </div>
      <div className="space-y-3 border-t border-border pt-4">
        <p className="text-xs font-semibold text-muted-foreground">{t("plugins.addScopeConfig")}</p>
        <div className="grid gap-3 sm:grid-cols-2">
          <Field>
            <FieldLabel>{t("plugins.scopeLabel")}</FieldLabel>
            <Select value={newScope} onValueChange={(value) => setNewScope(value as PluginScope)}>
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectPopup>
                {visibleScopes.map((scope) => (
                  <SelectItem key={scope} value={scope}>
                    {scopeLabel(scope, t)}
                  </SelectItem>
                ))}
              </SelectPopup>
            </Select>
          </Field>
          {(newScope === "system_agent" || newScope === "user_agent") && (
            <Field>
              <FieldLabel>{t("plugins.agent")}</FieldLabel>
              <Select
                value={selectedAgentID || "__none"}
                onValueChange={(value) =>
                  setSelectedAgentID(value === "__none" || !value ? "" : value)
                }
              >
                <SelectTrigger>
                  <SelectValue placeholder={t("plugins.selectAgent")} />
                </SelectTrigger>
                <SelectPopup>
                  <SelectItem value="__none">{t("plugins.selectAgent")}</SelectItem>
                  {agents.map((agent) => (
                    <SelectItem key={agent.id} value={agent.id}>
                      {agent.name}
                    </SelectItem>
                  ))}
                </SelectPopup>
              </Select>
            </Field>
          )}
        </div>
        <Button
          variant="outline"
          size="sm"
          loading={createMutation.isPending}
          disabled={(newScope === "system_agent" || newScope === "user_agent") && !selectedAgentID}
          onClick={() => createMutation.mutate()}
        >
          {t("plugins.addScopeConfig")}
        </Button>
      </div>
      {!selectedPlugin.is_builtin && (
        <div className="border-t border-border pt-4">
          <Button
            variant="destructive"
            size="sm"
            loading={definitionDeleteMutation.isPending}
            onClick={() => setPendingPluginDelete(selectedPlugin)}
          >
            <Trash2 className="size-3.5" />
            {t("common.delete")}
          </Button>
        </div>
      )}
    </DetailPanel>
  ) : null;
  const newMcpPlugin = {
    display_name: newMcpName || t("plugins.newMcp"),
    resource_summary: {
      binaries: [],
      skills: [],
      session_env: [],
      oauth_provider_configured: false,
      mcp_servers: [
        {
          server_key: "main",
          endpoint_configured: false,
          bearer_configured: false,
          oauth_client_id_configured: false,
          oauth_client_secret_configured: false,
        },
      ],
    },
  };
  return (
    <>
      <SettingsGridPage
        title={t("plugins.title")}
        action={
          <div className="flex gap-2">
            <Button size="sm" variant="outline" onClick={() => setRegistryOpen(true)}>
              {t("mcp.market.title")}
            </Button>
            <Button size="sm" variant="outline" onClick={() => setNewMcpOpen(true)}>
              <Plus className="size-3.5" />
              {t("plugins.addMcp")}
            </Button>
          </div>
        }
      >
        {plugins.length === 0 ? (
          <ErrorState title={t("plugins.noPlugins")} description={t("plugins.noPluginsDesc")} />
        ) : (
          groups.map(
            (group) =>
              group.items.length > 0 && (
                <SettingsCardSection
                  key={group.title}
                  title={group.title}
                  count={group.items.length}
                >
                  {group.items.map((plugin) => (
                    <Card
                      key={plugin.id}
                      render={
                        <Link
                          to={
                            scopeBand === "system"
                              ? "/admin/integrations/plugins/$pluginId"
                              : "/settings/plugins/$pluginId"
                          }
                          params={{ pluginId: plugin.id }}
                        />
                      }
                      className="gap-3 p-4 transition-colors hover:border-ring/40"
                    >
                      <div className="flex items-start gap-3">
                        <span className="grid size-9 shrink-0 place-items-center rounded-lg border border-border bg-muted text-muted-foreground">
                          <Package className="size-4" />
                        </span>
                        <div className="min-w-0 flex-1 space-y-1">
                          <div className="flex flex-wrap items-center gap-1.5">
                            <span className="truncate text-sm font-medium">
                              {plugin.display_name}
                            </span>
                            {plugin.is_builtin && (
                              <Badge variant="secondary" size="sm">
                                {t("plugins.builtin")}
                              </Badge>
                            )}
                          </div>
                          <p className="text-xs text-muted-foreground">
                            {typeof plugin.spec.description === "string" && plugin.spec.description
                              ? plugin.spec.description
                              : t("plugins.noDescription")}
                          </p>
                        </div>
                      </div>
                    </Card>
                  ))}
                </SettingsCardSection>
              ),
          )
        )}
      </SettingsGridPage>
      <McpInstallSheet
        open={registryOpen}
        onOpenChange={setRegistryOpen}
        notify={showToast}
        defaultScope={scopeBand === "system" ? "system" : "user"}
        isAdmin={scopeBand === "system"}
        agentId={selectedAgentID || undefined}
        onRequestManual={({ name, pluginID, url }) => {
          setNewMcpName(name);
          setNewMcpID(pluginID);
          setNewMcpURL(url);
          setNewMcpOpen(true);
        }}
      />
      <SettingsDetailSheet open={detail !== null} onClose={closeDetail}>
        {detail}
      </SettingsDetailSheet>
      <SettingsDetailSheet open={newMcpOpen} onClose={closeNewMcp}>
        <DetailPanel>
          <DetailPanelHeader title={t("plugins.addMcp")} />
          <div className="space-y-4">
            <Field>
              <FieldLabel>{t("plugins.mcpName")}</FieldLabel>
              <Input
                value={newMcpName}
                onChange={(event) => setNewMcpName(event.target.value)}
                placeholder={t("plugins.mcpNamePlaceholder")}
                nativeInput
              />
            </Field>
            <Field>
              <FieldLabel>{t("plugins.mcpID")}</FieldLabel>
              <Input
                value={newMcpID}
                onChange={(event) => setNewMcpID(event.target.value)}
                placeholder={t("plugins.mcpIDPlaceholder")}
                nativeInput
              />
            </Field>
            <Field>
              <FieldLabel>{t("plugins.mcpDescription")}</FieldLabel>
              <Input
                value={newMcpDescription}
                onChange={(event) => setNewMcpDescription(event.target.value)}
                placeholder={t("plugins.mcpDescriptionPlaceholder")}
                nativeInput
              />
            </Field>
            <Field>
              <FieldLabel>{t("plugins.scopeLabel")}</FieldLabel>
              <Select
                value={newMcpScope}
                onValueChange={(value) => setNewMcpScope(value as PluginScope)}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectPopup>
                  {visibleScopes.map((scope) => (
                    <SelectItem key={scope} value={scope}>
                      {scopeLabel(scope, t)}
                    </SelectItem>
                  ))}
                </SelectPopup>
              </Select>
            </Field>
            {(newMcpScope === "system_agent" || newMcpScope === "user_agent") && (
              <Field>
                <FieldLabel>{t("plugins.agent")}</FieldLabel>
                <Select
                  value={selectedAgentID || "__none"}
                  onValueChange={(value) =>
                    setSelectedAgentID(value === "__none" || !value ? "" : value)
                  }
                >
                  <SelectTrigger>
                    <SelectValue placeholder={t("plugins.selectAgent")} />
                  </SelectTrigger>
                  <SelectPopup>
                    <SelectItem value="__none">{t("plugins.selectAgent")}</SelectItem>
                    {agents.map((agent) => (
                      <SelectItem key={agent.id} value={agent.id}>
                        {agent.name}
                      </SelectItem>
                    ))}
                  </SelectPopup>
                </Select>
              </Field>
            )}
            <PluginConfigEditor
              plugin={newMcpPlugin}
              initialMcpUrl={newMcpURL}
              mcpServerKey="main"
              onSave={(payload, credentials) => {
                const url = payload.config?.url;
                if (typeof url !== "string" || !url.trim()) {
                  showToast(t("plugins.mcpEndpointRequired"), "error");
                  return;
                }
                createMcpMutation.mutate({ payload, credentials });
              }}
              onCancel={closeNewMcp}
              busy={createMcpMutation.isPending}
            />
          </div>
        </DetailPanel>
      </SettingsDetailSheet>
      <SettingsDetailSheet open={editingConfig !== null} onClose={() => setEditingConfig(null)}>
        {editingConfig && selectedPlugin && (
          <DetailPanel>
            <DetailPanelHeader
              title={t("plugins.editConfiguration")}
              subtitle={scopeLabel(editingConfig.config.scope, t)}
            />
            <PluginConfigEditor
              plugin={selectedPlugin}
              config={editingConfig.config}
              mcpServerKey={editingConfig.serverKey}
              onSave={(payload, credentials) => {
                editMutation.mutate({
                  config: editingConfig.config,
                  serverKey: editingConfig.serverKey,
                  payload,
                  credentials,
                });
              }}
              onCancel={() => setEditingConfig(null)}
              busy={editMutation.isPending}
            />
          </DetailPanel>
        )}
      </SettingsDetailSheet>
      <SettingsDetailSheet open={addingChildConfig !== null} onClose={closeAddChild}>
        <DetailPanel>
          <DetailPanelHeader title={t("plugins.addMcpServer")} />
          <div className="space-y-4">
            <McpServerFields
              name={newChildKey}
              onNameChange={setNewChildKey}
              url={newChildURL}
              onUrlChange={setNewChildURL}
              transport={newChildTransport}
              onTransportChange={setNewChildTransport}
              authType={newChildAuthType}
              onAuthTypeChange={setNewChildAuthType}
              token={newChildToken}
              onTokenChange={setNewChildToken}
              editing={false}
              oauthClientId={newChildOAuthClientID}
              onOauthClientIdChange={setNewChildOAuthClientID}
              oauthClientSecret={newChildOAuthSecret}
              onOauthClientSecretChange={setNewChildOAuthSecret}
              credentialMode={newChildCredentialMode}
              onCredentialModeChange={setNewChildCredentialMode}
              showCredentialMode
            />
            <div className="flex justify-end gap-2">
              <Button
                variant="ghost"
                size="sm"
                onClick={closeAddChild}
                disabled={createChildMutation.isPending}
              >
                {t("common.cancel")}
              </Button>
              <Button
                size="sm"
                onClick={() => createChildMutation.mutate()}
                loading={createChildMutation.isPending}
              >
                {t("common.save")}
              </Button>
            </div>
          </div>
        </DetailPanel>
      </SettingsDetailSheet>
      <ConfirmDialog
        open={pendingChildDelete !== null}
        onOpenChange={(open) => !open && setPendingChildDelete(null)}
        title={t("plugins.deleteMcpServer")}
        message={pendingChildDelete ? pendingChildDelete.serverKey : ""}
        onConfirm={() => {
          if (pendingChildDelete) deleteChildMutation.mutate(pendingChildDelete);
          setPendingChildDelete(null);
        }}
      />
      <ConfirmDialog
        open={pendingDelete !== null}
        onOpenChange={(open) => !open && setPendingDelete(null)}
        title={t("plugins.deleteConfig")}
        message={
          pendingDelete
            ? t("plugins.deleteConfigMsg", {
                scope: scopeLabel(pendingDelete.scope, t),
              })
            : ""
        }
        onConfirm={() => {
          if (pendingDelete) deleteMutation.mutate(pendingDelete);
          setPendingDelete(null);
        }}
      />
      <ConfirmDialog
        open={pendingPluginDelete !== null}
        onOpenChange={(open) => !open && setPendingPluginDelete(null)}
        title={t("plugins.deleteConfirm")}
        message={
          pendingPluginDelete
            ? t("plugins.deleteConfirmDesc", {
                name: pendingPluginDelete.display_name,
              })
            : ""
        }
        onConfirm={() => {
          if (pendingPluginDelete) definitionDeleteMutation.mutate(pendingPluginDelete);
          setPendingPluginDelete(null);
        }}
      />
    </>
  );
}

export function PersonalUnifiedPluginsPage() {
  return <UnifiedPluginsPage scopeBand="personal" />;
}

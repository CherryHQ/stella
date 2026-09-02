import { apiErrorCode } from "@/lib/api-error";
import { useCallback, useEffect, useMemo, useState } from "react";
import { PlugZap, Plus } from "lucide-react";
import {
  createScopedMcpServer,
  deleteScopedMcpServer as deleteScopedMcpServerRequest,
  disconnectMcpoAuth,
  listAgents,
  listScopedMcpServers,
  startMcpoAuth,
  updateScopedMcpServer,
} from "@/lib/api-client/sdk.gen";
import { McpInstallSheet } from "@/features/mcp/McpInstallSheet";
import { McpServerDrawer } from "@/features/mcp/McpServerDrawer";
import type { McpServer } from "@/lib/api-client/types.gen";
import type { Agent } from "@/lib/types";
import { useI18n } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/messages";
import { useToast } from "@/hooks/use-toast";
import {
  AlertDialog,
  AlertDialogClose,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogPopup,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field";
import { Switch } from "@/components/ui/switch";
import {
  Select,
  SelectItem,
  SelectPopup,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  McpServerFields,
  transportLabel,
  type McpAuthType,
  type McpTransport,
} from "@/features/mcp/McpServerFields";
import { SettingsEmptyState } from "@/features/settings/SettingsEmptyState";
import { ErrorState } from "@/components/RouteFallback";
import {
  SettingsCard,
  SettingsCardSection,
  SettingsDetailSheet,
  SettingsGridPage,
} from "@/features/settings/SettingsCardGrid";
import { DetailPanel, DetailPanelHeader } from "@/features/settings/SettingsDetailPanel";
import {
  isAgentManagedScope,
  scopeForRange,
  scopeQueriesForBand,
  scopesForBand,
  type ScopeBand,
} from "@/lib/scope-band";

type MCPScope = McpServer["scope"];

// A 409 from PATCH/DELETE means the registration changed elsewhere; the local
// copy is stale, so reload instead of retrying blind.
function isConflictStatus<TError>(error: TError): boolean {
  return apiErrorCode(error) === 409;
}
type MCPTransport = McpTransport;
type MCPAuthType = McpAuthType;

type ScopeRange = "all" | "specific";

const SCOPE_ORDER: MCPScope[] = ["user", "user_agent", "system", "system_agent"];

const SCOPE_LABEL_KEY = {
  user: "mcp.scope.user.label",
  user_agent: "mcp.scope.userAgent.label",
  system: "mcp.scope.system.label",
  system_agent: "mcp.scope.systemAgent.label",
} satisfies Record<MCPScope, MessageKey>;

function isAgentScope(scope: MCPScope) {
  return isAgentManagedScope(scope);
}

export function MCPServersPanel({
  embedded = false,
  scopeBand,
}: {
  embedded?: boolean;
  scopeBand: ScopeBand;
}) {
  const { t } = useI18n();
  // SAFETY: scopesForBand returns ManagedScope, the same literal union as MCPScope.
  const managedScopes = scopesForBand(scopeBand) as readonly MCPScope[];
  const { showToast } = useToast();

  const [servers, setServers] = useState<McpServer[]>([]);
  const [agents, setAgents] = useState<Agent[]>([]);
  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState(false);
  const [saving, setSaving] = useState(false);
  const [installOpen, setInstallOpen] = useState(false);
  const [drawerServer, setDrawerServer] = useState<McpServer | null>(null);
  const [confirmDelete, setConfirmDelete] = useState<McpServer | null>(null);
  const [sheetOpen, setSheetOpen] = useState(false);
  const [editingServer, setEditingServer] = useState<McpServer | null>(null);

  const [formRange, setFormRange] = useState<ScopeRange>("all");
  const [formAgentID, setFormAgentID] = useState("");
  const [name, setName] = useState("");
  const [url, setURL] = useState("");
  const [transport, setTransport] = useState<MCPTransport>("streamable_http");
  const [authType, setAuthType] = useState<MCPAuthType>("none");
  const [token, setToken] = useState("");
  const [oauthClientId, setOauthClientId] = useState("");
  const [oauthClientSecret, setOauthClientSecret] = useState("");
  const [credentialMode, setCredentialMode] = useState<"shared" | "per_user">("shared");

  // The OAuth callback lands on this page with a fixed-enum result; surface it
  // once and scrub the URL so a refresh doesn't re-toast.
  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    const connected = params.get("connected");
    const oauthError = params.get("oauth_error");
    if (!connected && !oauthError) return;
    if (connected) {
      showToast(t("mcp.oauthSuccess"));
    } else {
      // SAFETY: the callback only ever writes the fixed error enum into the URL.
      const key = `mcp.oauthError.${oauthError}` as MessageKey;
      showToast(t(key), "error");
    }
    window.history.replaceState(null, "", window.location.pathname);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const agentName = useCallback(
    (id?: string | null) => (id && agents.find((agent) => agent.id === id)?.name) || id || "",
    [agents],
  );

  // These used to swallow into `[]`, so an unreachable server rendered as
  // "no MCP servers configured" — indistinguishable from a clean install.
  // Failures now surface through `loadError`.
  const fetchScope = useCallback(async (scope: MCPScope, agentID?: string) => {
    const { data } = await listScopedMcpServers({
      query: { scope, agent_id: agentID },
      throwOnError: true,
    });
    return data?.servers ?? [];
  }, []);

  const loadAgents = useCallback(async () => {
    const { data } = await listAgents({ query: { include_all: true }, throwOnError: true });
    // SAFETY: listAgents returns Agent items under data.agents.
    const list = (data?.agents as Agent[]) ?? [];
    setAgents(list);
    return list;
  }, []);

  const loadServers = useCallback(
    async (agentList: Agent[]) => {
      setLoading(true);
      try {
        const jobs = scopeQueriesForBand(
          scopeBand,
          agentList.map((agent) => agent.id),
        ).map(({ scope, agentID }) =>
          // SAFETY: scopeQueriesForBand emits ManagedScope, the same literal union as MCPScope.
          fetchScope(scope as MCPScope, agentID),
        );
        const results = await Promise.all(jobs);
        setServers(results.flat());
      } finally {
        setLoading(false);
      }
    },
    [fetchScope, scopeBand],
  );

  const reloadScope = useCallback(
    async (scope: MCPScope, agentID?: string) => {
      const fetched = await fetchScope(scope, agentID);
      setServers((prev) => [
        ...prev.filter(
          (server) => !(server.scope === scope && (agentID ? server.agent_id === agentID : true)),
        ),
        ...fetched,
      ]);
    },
    [fetchScope],
  );

  const init = useCallback(async () => {
    setLoadError(false);
    try {
      const agentList = await loadAgents();
      await loadServers(agentList);
    } catch {
      setAgents([]);
      setServers([]);
      setLoadError(true);
    }
  }, [loadAgents, loadServers]);

  useEffect(() => {
    void init();
  }, [init]);

  const openAddSheet = useCallback(() => {
    setEditingServer(null);
    setFormRange("all");
    setFormAgentID("");
    setName("");
    setURL("");
    setTransport("streamable_http");
    setAuthType("none");
    setToken("");
    setSheetOpen(true);
  }, []);

  const openEditSheet = useCallback((server: McpServer) => {
    setEditingServer(server);
    setFormRange(isAgentScope(server.scope) ? "specific" : "all");
    setFormAgentID(server.agent_id ?? "");
    setName(server.name);
    setURL(server.url);
    setTransport(server.transport);
    setAuthType(server.auth_type);
    setToken("");
    setSheetOpen(true);
  }, []);

  const saveServer = useCallback(async () => {
    // SAFETY: scopeForRange returns ManagedScope, the same literal union as MCPScope.
    const scope = scopeForRange(scopeBand, formRange === "specific") as MCPScope;
    const agentScoped = isAgentScope(scope);
    if (!name.trim()) {
      showToast(t("mcp.nameRequired"), "error");
      return;
    }
    if (!url.trim()) {
      showToast(t("mcp.urlRequired"), "error");
      return;
    }
    if (agentScoped && !formAgentID) {
      showToast(t("mcp.scope.agentMissing"), "error");
      return;
    }
    if (authType === "bearer" && !editingServer && !token.trim()) {
      showToast(t("mcp.tokenRequired"), "error");
      return;
    }

    setSaving(true);
    try {
      if (editingServer) {
        await updateScopedMcpServer({
          path: { id: editingServer.id },
          query: {
            scope: editingServer.scope,
            agent_id: isAgentScope(editingServer.scope) ? editingServer.agent_id : undefined,
          },
          body: {
            scope,
            agent_id: agentScoped ? formAgentID : undefined,
            name: name.trim(),
            url: url.trim(),
            transport,
            auth_type: authType,
            token: authType === "bearer" && token.trim() ? token : undefined,
            oauth_client_id: authType === "oauth" ? oauthClientId.trim() : undefined,
            oauth_client_secret:
              authType === "oauth" && oauthClientSecret.trim()
                ? oauthClientSecret.trim()
                : undefined,
            credential_mode: authType === "oauth" ? credentialMode : undefined,
          },
          throwOnError: true,
        });
        showToast(t("mcp.updated"));
        await reloadScope(
          editingServer.scope,
          isAgentScope(editingServer.scope) ? editingServer.agent_id : undefined,
        );
      } else {
        await createScopedMcpServer({
          body: {
            scope,
            agent_id: agentScoped ? formAgentID : undefined,
            name: name.trim(),
            url: url.trim(),
            transport,
            auth_type: authType,
            token: authType === "bearer" ? token : undefined,
            oauth_client_id:
              authType === "oauth" && oauthClientId.trim() ? oauthClientId.trim() : undefined,
            oauth_client_secret:
              authType === "oauth" && oauthClientSecret.trim()
                ? oauthClientSecret.trim()
                : undefined,
            credential_mode: authType === "oauth" ? credentialMode : undefined,
          },
          throwOnError: true,
        });
        showToast(t("mcp.created"));
      }
      setSheetOpen(false);
      await reloadScope(scope, agentScoped ? formAgentID : undefined);
    } catch (e) {
      showToast(e instanceof Error ? e.message : t("mcp.saveFailed"), "error");
    } finally {
      setSaving(false);
    }
  }, [
    authType,
    editingServer,
    formAgentID,
    formRange,
    scopeBand,
    name,
    reloadScope,
    showToast,
    t,
    token,
    transport,
    url,
  ]);

  const toggleServer = useCallback(
    async (server: McpServer, enabled: boolean) => {
      try {
        await updateScopedMcpServer({
          path: { id: server.id },
          query: {
            scope: server.scope,
            agent_id: isAgentScope(server.scope) ? server.agent_id : undefined,
          },
          headers: server.version ? { "If-Match": server.version } : undefined,
          body: { enabled },
          throwOnError: true,
        });
        await reloadScope(server.scope, isAgentScope(server.scope) ? server.agent_id : undefined);
      } catch (e) {
        showToast(
          isConflictStatus(e)
            ? t("mcp.server.changed")
            : e instanceof Error
              ? e.message
              : t("mcp.saveFailed"),
          "error",
        );
        await reloadScope(server.scope, isAgentScope(server.scope) ? server.agent_id : undefined);
      }
    },
    [reloadScope, showToast, t],
  );

  const deleteServer = useCallback(
    async (server: McpServer) => {
      try {
        await deleteScopedMcpServerRequest({
          path: { id: server.id },
          query: {
            scope: server.scope,
            agent_id: isAgentScope(server.scope) ? server.agent_id : undefined,
          },
          headers: server.version ? { "If-Match": server.version } : undefined,
          throwOnError: true,
        });
        showToast(t("mcp.deleted"));
        if (editingServer?.id === server.id) {
          setSheetOpen(false);
          setEditingServer(null);
        }
        await reloadScope(server.scope, isAgentScope(server.scope) ? server.agent_id : undefined);
      } catch (e) {
        showToast(e instanceof Error ? e.message : t("mcp.deleteFailed"), "error");
      }
    },
    [editingServer?.id, reloadScope, showToast, t],
  );

  const connectServer = useCallback(
    async (server: McpServer) => {
      try {
        const { data } = await startMcpoAuth({
          path: { id: server.id },
          query: {
            scope: server.scope,
            agent_id: isAgentScope(server.scope) ? server.agent_id : undefined,
          },
          throwOnError: true,
        });
        if (data?.authorization_url) {
          // The authorization URL belongs to the external authorization server;
          // navigate the whole tab so its callback returns to Stella.
          window.location.href = data.authorization_url;
        }
      } catch (e) {
        showToast(e instanceof Error ? e.message : t("mcp.connectFailed"), "error");
      }
    },
    [showToast, t],
  );

  const disconnectServer = useCallback(
    async (server: McpServer) => {
      try {
        await disconnectMcpoAuth({
          path: { id: server.id },
          query: {
            scope: server.scope,
            agent_id: isAgentScope(server.scope) ? server.agent_id : undefined,
          },
          throwOnError: true,
        });
        showToast(t("mcp.oauth.notConnected"));
        await reloadScope(server.scope, isAgentScope(server.scope) ? server.agent_id : undefined);
      } catch (e) {
        showToast(e instanceof Error ? e.message : t("mcp.disconnectFailed"), "error");
      }
    },
    [reloadScope, showToast, t],
  );

  const sortedServers = useMemo(
    () =>
      [...servers]
        .filter((server) => managedScopes.includes(server.scope))
        .sort((a, b) => SCOPE_ORDER.indexOf(a.scope) - SCOPE_ORDER.indexOf(b.scope)),
    [managedScopes, servers],
  );

  // SAFETY: scopeForRange returns ManagedScope, the same literal union as MCPScope.
  const formScope = scopeForRange(scopeBand, formRange === "specific") as MCPScope;
  // SAFETY: the scope Select offers the managed scopes as options; membership is re-checked in the handler.
  const onSelectScope = (value: string | null) => {
    if (!value) return;
    // SAFETY: the scope Select's value is one of the managed scopes; membership is checked next.
    const scope = value as MCPScope;
    if (!managedScopes.includes(scope)) return;
    setFormRange(isAgentScope(scope) ? "specific" : "all");
  };
  // SAFETY: the scope options are MCPScope keys rendered back through SCOPE_LABEL_KEY.
  const renderScopeLabel = (value: string) => t(SCOPE_LABEL_KEY[(value as MCPScope) || formScope]);
  // SAFETY: the agent Select offers agent-id options as strings; null clears the field.
  const onSelectFormAgent = (value: string | null) =>
    setFormAgentID((value as string | null) ?? "");

  const addPanel = (
    <DetailPanel
      onCancel={() => setSheetOpen(false)}
      onDelete={editingServer ? () => setConfirmDelete(editingServer) : undefined}
      onSave={saveServer}
      saveLabel={editingServer ? t("common.save") : t("mcp.add")}
      cancelLabel={t("common.cancel")}
      isSaving={saving}
      canSave={!saving}
    >
      <DetailPanelHeader
        title={editingServer ? t("mcp.editTitle") : t("mcp.addTitle")}
        subtitle={editingServer ? t("mcp.editDescription") : t("mcp.addDescription")}
      />

      <div className="space-y-4">
        <Field>
          <FieldLabel>{t("mcp.scope")}</FieldLabel>
          <Select value={formScope} onValueChange={onSelectScope}>
            <SelectTrigger>
              <SelectValue>{renderScopeLabel}</SelectValue>
            </SelectTrigger>
            <SelectPopup>
              {SCOPE_ORDER.filter((scope) => managedScopes.includes(scope)).map((scope) => (
                <SelectItem key={scope} value={scope}>
                  {t(SCOPE_LABEL_KEY[scope])}
                </SelectItem>
              ))}
            </SelectPopup>
          </Select>
          <FieldDescription>{t("mcp.scope.description")}</FieldDescription>
        </Field>

        {isAgentScope(formScope) && (
          <Field>
            <FieldLabel>{t("mcp.agent")}</FieldLabel>
            <Select value={formAgentID || null} onValueChange={onSelectFormAgent}>
              <SelectTrigger>
                <SelectValue placeholder={t("mcp.scope.selectAgent")}>
                  {(value) => (value ? agentName(value) : null)}
                </SelectValue>
              </SelectTrigger>
              <SelectPopup>
                {agents.map((agent) => (
                  <SelectItem key={agent.id} value={agent.id}>
                    {agent.name || agent.id}
                  </SelectItem>
                ))}
              </SelectPopup>
            </Select>
          </Field>
        )}

        <McpServerFields
          name={name}
          onNameChange={setName}
          url={url}
          onUrlChange={setURL}
          transport={transport}
          onTransportChange={setTransport}
          authType={authType}
          onAuthTypeChange={setAuthType}
          token={token}
          onTokenChange={setToken}
          editing={!!editingServer}
          oauthClientId={oauthClientId}
          onOauthClientIdChange={setOauthClientId}
          oauthClientSecret={oauthClientSecret}
          onOauthClientSecretChange={setOauthClientSecret}
          credentialMode={credentialMode}
          onCredentialModeChange={setCredentialMode}
          showCredentialMode={scopeBand === "system"}
        />
      </div>
    </DetailPanel>
  );

  const content = loading ? (
    <p className="text-sm text-muted-foreground">{t("common.loading")}</p>
  ) : loadError ? (
    <ErrorState
      title={t("route.error.title")}
      description={t("route.loadFailed")}
      onRetry={() => void init()}
    />
  ) : sortedServers.length === 0 ? (
    <SettingsEmptyState
      icon={<PlugZap className="size-5" />}
      message={t("mcp.empty")}
      description={t("mcp.empty.description")}
      action={<Button onClick={openAddSheet}>{t("mcp.add")}</Button>}
    />
  ) : (
    <SettingsCardSection
      icon={<PlugZap className="size-4" />}
      title={t(scopeBand === "system" ? "admin.resources.mcp.title" : "mcp.title")}
      description={t("mcp.sectionDescription")}
      count={sortedServers.length}
    >
      {sortedServers.map((server) => (
        <SettingsCard
          key={server.id}
          icon={<PlugZap className="size-4" />}
          title={server.name}
          badge={
            <Badge variant="secondary" size="sm">
              {t(SCOPE_LABEL_KEY[server.scope])}
            </Badge>
          }
          description={server.url}
          action={
            <Switch
              checked={server.enabled}
              onCheckedChange={(checked) => void toggleServer(server, checked)}
            />
          }
          onClick={() => setDrawerServer(server)}
          footer={
            <>
              <Badge variant="outline" size="sm">
                {transportLabel(server.transport)}
              </Badge>
              <Badge variant="secondary" size="sm">
                {server.auth_type === "bearer"
                  ? t("mcp.auth.bearer")
                  : server.auth_type === "oauth"
                    ? t("mcp.auth.oauth")
                    : t("mcp.auth.none")}
              </Badge>
              {server.auth_type === "oauth" && server.oauth && (
                <Badge variant="outline" size="sm">
                  {server.oauth.connected
                    ? t("mcp.oauth.connected")
                    : server.oauth.client_registered
                      ? t("mcp.oauth.needsReconnect")
                      : t("mcp.oauth.notConnected")}
                </Badge>
              )}
              {isAgentScope(server.scope) && server.agent_id && (
                <span className="truncate text-xs text-muted-foreground">
                  {agentName(server.agent_id)}
                </span>
              )}
              {server.auth_type === "oauth" && (
                <Button
                  variant="outline"
                  size="sm"
                  onClick={(e) => {
                    e.stopPropagation();
                    void (server.oauth?.connected
                      ? disconnectServer(server)
                      : connectServer(server));
                  }}
                >
                  {server.oauth?.connected
                    ? t("mcp.disconnect")
                    : server.oauth?.client_registered
                      ? t("mcp.reconnect")
                      : t("mcp.connect")}
                </Button>
              )}
            </>
          }
        />
      ))}
    </SettingsCardSection>
  );

  const action = (
    <Button size="sm" onClick={() => setInstallOpen(true)}>
      <Plus className="size-4" />
      {t("mcp.add")}
    </Button>
  );

  return (
    <>
      {embedded ? (
        <div className="space-y-3">
          <div className="flex justify-end">{action}</div>
          {content}
        </div>
      ) : (
        <SettingsGridPage
          title={t(scopeBand === "system" ? "admin.resources.mcp.title" : "mcp.title")}
          action={action}
        >
          {content}
        </SettingsGridPage>
      )}

      <SettingsDetailSheet open={sheetOpen} onClose={() => setSheetOpen(false)}>
        {addPanel}
      </SettingsDetailSheet>
      <McpInstallSheet
        open={installOpen}
        onOpenChange={setInstallOpen}
        notify={showToast}
        defaultScope={scopeBand === "system" ? "system" : "user"}
        isAdmin={scopeBand === "system"}
        manual={
          <div className="space-y-4">
            <Button onClick={openAddSheet}>
              <Plus className="size-4" />
              {t("mcp.market.openManual")}
            </Button>
          </div>
        }
        onRequestManual={(prefill) => {
          setInstallOpen(false);
          openAddSheet();
          setName(prefill.name);
          setURL(prefill.url);
        }}
      />
      <McpServerDrawer
        server={drawerServer}
        open={!!drawerServer}
        onOpenChange={(next) => !next && setDrawerServer(null)}
        onConnect={(srv) => void connectServer(srv)}
        onDisconnect={(srv) => void disconnectServer(srv)}
        onEdit={(srv) => {
          setDrawerServer(null);
          openEditSheet(srv);
        }}
        onDelete={(srv) => setConfirmDelete(srv)}
        notify={showToast}
      />
      <AlertDialog open={!!confirmDelete} onOpenChange={(next) => !next && setConfirmDelete(null)}>
        <AlertDialogPopup>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("mcp.deleteTitle")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("mcp.deleteConfirm", { name: confirmDelete?.name ?? "" })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogClose render={<Button variant="ghost" />}>
              {t("common.cancel")}
            </AlertDialogClose>
            <Button
              variant="destructive"
              onClick={() => {
                const target = confirmDelete;
                setConfirmDelete(null);
                if (target) void deleteServer(target);
              }}
            >
              {t("common.delete")}
            </Button>
          </AlertDialogFooter>
        </AlertDialogPopup>
      </AlertDialog>
    </>
  );
}

export function MCPServersPage({ scopeBand }: { scopeBand: ScopeBand }) {
  return <MCPServersPanel scopeBand={scopeBand} />;
}

export function PersonalMCPPage() {
  return <MCPServersPage scopeBand="personal" />;
}

export function GlobalMCPPage() {
  return <MCPServersPage scopeBand="system" />;
}

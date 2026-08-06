import { useCallback, useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { PlugZap, Plus } from "lucide-react";
import {
  createScopedMcpServer,
  deleteScopedMcpServer as deleteScopedMcpServerRequest,
  listAgents,
  listScopedMcpServers,
  updateScopedMcpServer,
} from "@/lib/api-client/sdk.gen";
import type { McpServer } from "@/lib/api-client/types.gen";
import type { Agent } from "@/lib/types";
import { meQueryOptions } from "@/lib/queries/me";
import { useI18n } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/messages";
import { useToast } from "@/hooks/use-toast";
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

type MCPScope = McpServer["scope"];
type MCPTransport = McpTransport;
type MCPAuthType = McpAuthType;

type ScopeOwner = "me" | "global";
type ScopeRange = "all" | "specific";

const SCOPE_ORDER: MCPScope[] = ["user", "user_agent", "system", "system_agent"];

const SCOPE_LABEL_KEY: Record<MCPScope, MessageKey> = {
  user: "mcp.scope.user.label",
  user_agent: "mcp.scope.userAgent.label",
  system: "mcp.scope.system.label",
  system_agent: "mcp.scope.systemAgent.label",
};

function isAgentScope(scope: MCPScope) {
  return scope === "user_agent" || scope === "system_agent";
}

function toScope(owner: ScopeOwner, range: ScopeRange): MCPScope {
  if (range === "specific") return owner === "global" ? "system_agent" : "user_agent";
  return owner === "global" ? "system" : "user";
}

export function MCPServersPanel({ embedded = false }: { embedded?: boolean }) {
  const { t } = useI18n();
  const { data: me } = useQuery(meQueryOptions);
  const isAdmin = me?.is_admin ?? false;
  const { showToast } = useToast();

  const [servers, setServers] = useState<McpServer[]>([]);
  const [agents, setAgents] = useState<Agent[]>([]);
  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState(false);
  const [saving, setSaving] = useState(false);
  const [sheetOpen, setSheetOpen] = useState(false);
  const [editingServer, setEditingServer] = useState<McpServer | null>(null);

  const [formOwner, setFormOwner] = useState<ScopeOwner>("me");
  const [formRange, setFormRange] = useState<ScopeRange>("all");
  const [formAgentID, setFormAgentID] = useState("");
  const [name, setName] = useState("");
  const [url, setURL] = useState("");
  const [transport, setTransport] = useState<MCPTransport>("streamable_http");
  const [authType, setAuthType] = useState<MCPAuthType>("none");
  const [token, setToken] = useState("");

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
    const list = (data?.agents as Agent[]) ?? [];
    setAgents(list);
    return list;
  }, []);

  const loadServers = useCallback(
    async (agentList: Agent[]) => {
      setLoading(true);
      try {
        const jobs: Promise<McpServer[]>[] = [fetchScope("user")];
        if (isAdmin) jobs.push(fetchScope("system"));
        for (const agent of agentList) {
          jobs.push(fetchScope("user_agent", agent.id));
          if (isAdmin) jobs.push(fetchScope("system_agent", agent.id));
        }
        const results = await Promise.all(jobs);
        setServers(results.flat());
      } finally {
        setLoading(false);
      }
    },
    [fetchScope, isAdmin],
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
    setFormOwner("me");
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
    setFormOwner(server.scope === "system" || server.scope === "system_agent" ? "global" : "me");
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
    const scope = toScope(formOwner, formRange);
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
    formOwner,
    formRange,
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
          body: { enabled },
          throwOnError: true,
        });
        await reloadScope(server.scope, isAgentScope(server.scope) ? server.agent_id : undefined);
      } catch (e) {
        showToast(e instanceof Error ? e.message : t("mcp.saveFailed"), "error");
      }
    },
    [reloadScope, showToast, t],
  );

  const deleteServer = useCallback(
    async (server: McpServer) => {
      if (!window.confirm(t("mcp.deleteConfirm", { name: server.name }))) return;
      try {
        await deleteScopedMcpServerRequest({
          path: { id: server.id },
          query: {
            scope: server.scope,
            agent_id: isAgentScope(server.scope) ? server.agent_id : undefined,
          },
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

  const sortedServers = useMemo(
    () => [...servers].sort((a, b) => SCOPE_ORDER.indexOf(a.scope) - SCOPE_ORDER.indexOf(b.scope)),
    [servers],
  );

  const formScope = toScope(formOwner, formRange);

  const addPanel = (
    <DetailPanel
      onCancel={() => setSheetOpen(false)}
      onDelete={editingServer ? () => void deleteServer(editingServer) : undefined}
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
          <Select
            value={formScope}
            onValueChange={(value) => {
              const scope = value as MCPScope;
              setFormOwner(scope === "system" || scope === "system_agent" ? "global" : "me");
              setFormRange(isAgentScope(scope) ? "specific" : "all");
            }}
          >
            <SelectTrigger>
              <SelectValue>
                {(value) => t(SCOPE_LABEL_KEY[(value as MCPScope) || formScope])}
              </SelectValue>
            </SelectTrigger>
            <SelectPopup>
              {SCOPE_ORDER.filter(
                (scope) => isAdmin || (scope !== "system" && scope !== "system_agent"),
              ).map((scope) => (
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
            <Select
              value={formAgentID || null}
              onValueChange={(value) => setFormAgentID((value as string | null) ?? "")}
            >
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
      title={t("mcp.title")}
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
          onClick={() => openEditSheet(server)}
          footer={
            <>
              <Badge variant="outline" size="sm">
                {transportLabel(server.transport)}
              </Badge>
              <Badge variant="secondary" size="sm">
                {server.auth_type === "bearer" ? t("mcp.auth.bearer") : t("mcp.auth.none")}
              </Badge>
              {isAgentScope(server.scope) && server.agent_id && (
                <span className="truncate text-xs text-muted-foreground">
                  {agentName(server.agent_id)}
                </span>
              )}
            </>
          }
        />
      ))}
    </SettingsCardSection>
  );

  const action = (
    <Button size="sm" onClick={openAddSheet}>
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
        <SettingsGridPage title={t("mcp.title")} action={action}>
          {content}
        </SettingsGridPage>
      )}

      <SettingsDetailSheet open={sheetOpen} onClose={() => setSheetOpen(false)}>
        {addPanel}
      </SettingsDetailSheet>
    </>
  );
}

export function MCPServersPage() {
  return <MCPServersPanel />;
}

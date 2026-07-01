import { useCallback, useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { PlugZap, Plus } from "lucide-react";
import {
  createScopedMcpServer,
  deleteScopedMcpServer as deleteScopedMcpServerRequest,
  listAgents,
  listScopedMcpServers,
} from "@/lib/api-client/sdk.gen";
import type { McpServer } from "@/lib/api-client/types.gen";
import type { Agent } from "@/lib/types";
import { meQueryOptions } from "@/lib/queries/me";
import { useI18n } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/messages";
import { useToast, ToastContainer } from "@/hooks/use-toast";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectItem,
  SelectPopup,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { SettingsEmptyState } from "@/features/settings/SettingsEmptyState";
import {
  SettingsDetailSheet,
  SettingsGridPage,
  SettingsList,
  SettingsRow,
  SettingsSection,
} from "@/features/settings/SettingsCardGrid";
import { DetailPanel, DetailPanelHeader } from "@/features/settings/SettingsDetailPanel";

type MCPScope = McpServer["scope"];
type MCPTransport = McpServer["transport"];
type MCPAuthType = McpServer["auth_type"];

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

function transportLabel(transport: MCPTransport) {
  return transport === "streamable_http" ? "Streamable HTTP" : "SSE";
}

export function MCPServersPage() {
  const { t } = useI18n();
  const { data: me } = useQuery(meQueryOptions);
  const isAdmin = me?.is_admin ?? false;
  const { toasts, showToast } = useToast();

  const [servers, setServers] = useState<McpServer[]>([]);
  const [agents, setAgents] = useState<Agent[]>([]);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [addSheetOpen, setAddSheetOpen] = useState(false);

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

  const fetchScope = useCallback(async (scope: MCPScope, agentID?: string) => {
    try {
      const { data } = await listScopedMcpServers({
        query: { scope, agent_id: agentID },
        throwOnError: true,
      });
      return data?.servers ?? [];
    } catch {
      return [];
    }
  }, []);

  const loadAgents = useCallback(async () => {
    try {
      const { data } = await listAgents({ query: { include_all: true }, throwOnError: true });
      const list = (data?.agents as Agent[]) ?? [];
      setAgents(list);
      return list;
    } catch {
      setAgents([]);
      return [];
    }
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

  useEffect(() => {
    const init = async () => {
      const agentList = await loadAgents();
      await loadServers(agentList);
    };
    void init();
  }, [loadAgents, loadServers]);

  const openAddSheet = useCallback(() => {
    setFormOwner("me");
    setFormRange("all");
    setFormAgentID("");
    setName("");
    setURL("");
    setTransport("streamable_http");
    setAuthType("none");
    setToken("");
    setAddSheetOpen(true);
  }, []);

  const createServer = useCallback(async () => {
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
    if (authType === "bearer" && !token.trim()) {
      showToast(t("mcp.tokenRequired"), "error");
      return;
    }

    setSaving(true);
    try {
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
      setAddSheetOpen(false);
      await reloadScope(scope, agentScoped ? formAgentID : undefined);
    } catch (e) {
      showToast(e instanceof Error ? e.message : t("mcp.createFailed"), "error");
    } finally {
      setSaving(false);
    }
  }, [
    authType,
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
        await reloadScope(server.scope, isAgentScope(server.scope) ? server.agent_id : undefined);
      } catch (e) {
        showToast(e instanceof Error ? e.message : t("mcp.deleteFailed"), "error");
      }
    },
    [reloadScope, showToast, t],
  );

  const grouped = useMemo(
    () =>
      SCOPE_ORDER.map((scope) => ({
        scope,
        items: servers.filter((server) => server.scope === scope),
      })).filter((group) => group.items.length > 0),
    [servers],
  );

  const formScope = toScope(formOwner, formRange);

  const addPanel = (
    <DetailPanel
      onCancel={() => setAddSheetOpen(false)}
      onSave={createServer}
      saveLabel={t("mcp.add")}
      cancelLabel={t("common.cancel")}
      isSaving={saving}
      canSave={!saving}
    >
      <DetailPanelHeader title={t("mcp.addTitle")} subtitle={t("mcp.addDescription")} />

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

        <Field>
          <FieldLabel>{t("mcp.name")}</FieldLabel>
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="github"
            nativeInput
          />
          <FieldDescription>{t("mcp.name.description")}</FieldDescription>
        </Field>

        <Field>
          <FieldLabel>{t("mcp.url")}</FieldLabel>
          <Input
            value={url}
            onChange={(e) => setURL(e.target.value)}
            placeholder="https://mcp.example.com/mcp"
            nativeInput
          />
        </Field>

        <Field>
          <FieldLabel>{t("mcp.transport")}</FieldLabel>
          <Select value={transport} onValueChange={(value) => setTransport(value as MCPTransport)}>
            <SelectTrigger>
              <SelectValue>
                {(value) => transportLabel((value as MCPTransport) || transport)}
              </SelectValue>
            </SelectTrigger>
            <SelectPopup>
              <SelectItem value="streamable_http">Streamable HTTP</SelectItem>
              <SelectItem value="sse">SSE</SelectItem>
            </SelectPopup>
          </Select>
        </Field>

        <Field>
          <FieldLabel>{t("mcp.auth")}</FieldLabel>
          <Select value={authType} onValueChange={(value) => setAuthType(value as MCPAuthType)}>
            <SelectTrigger>
              <SelectValue>
                {(value) => (value === "bearer" ? t("mcp.auth.bearer") : t("mcp.auth.none"))}
              </SelectValue>
            </SelectTrigger>
            <SelectPopup>
              <SelectItem value="none">{t("mcp.auth.none")}</SelectItem>
              <SelectItem value="bearer">{t("mcp.auth.bearer")}</SelectItem>
            </SelectPopup>
          </Select>
        </Field>

        {authType === "bearer" && (
          <Field>
            <FieldLabel>{t("mcp.token")}</FieldLabel>
            <Input
              type="password"
              value={token}
              onChange={(e) => setToken(e.target.value)}
              autoComplete="off"
              nativeInput
            />
            <FieldDescription>{t("mcp.token.description")}</FieldDescription>
          </Field>
        )}
      </div>
    </DetailPanel>
  );

  return (
    <>
      <SettingsGridPage
        title={t("mcp.title")}
        action={
          <Button size="sm" onClick={openAddSheet}>
            <Plus className="size-4" />
            {t("mcp.add")}
          </Button>
        }
      >
        {loading ? (
          <p className="text-sm text-muted-foreground">{t("common.loading")}</p>
        ) : grouped.length === 0 ? (
          <SettingsEmptyState
            icon={<PlugZap className="size-5" />}
            message={t("mcp.empty")}
            description={t("mcp.empty.description")}
            action={<Button onClick={openAddSheet}>{t("mcp.add")}</Button>}
          />
        ) : (
          grouped.map((group) => (
            <SettingsSection
              key={group.scope}
              title={t(SCOPE_LABEL_KEY[group.scope])}
              count={group.items.length}
            >
              <SettingsList>
                {group.items.map((server) => (
                  <SettingsRow
                    key={server.id}
                    icon={<PlugZap className="size-4" />}
                    title={server.name}
                    subtitle={server.url}
                    chip={
                      isAgentScope(server.scope) && server.agent_id ? (
                        <Badge variant="secondary" size="sm">
                          {agentName(server.agent_id)}
                        </Badge>
                      ) : undefined
                    }
                    status={
                      <div className="flex items-center gap-1.5">
                        <Badge variant="outline" size="sm">
                          {transportLabel(server.transport)}
                        </Badge>
                        <Badge variant="secondary" size="sm">
                          {server.auth_type === "bearer"
                            ? t("mcp.auth.bearer")
                            : t("mcp.auth.none")}
                        </Badge>
                      </div>
                    }
                    menu={[
                      {
                        label: t("common.delete"),
                        destructive: true,
                        onClick: () => void deleteServer(server),
                      },
                    ]}
                  />
                ))}
              </SettingsList>
            </SettingsSection>
          ))
        )}
      </SettingsGridPage>

      <SettingsDetailSheet open={addSheetOpen} onClose={() => setAddSheetOpen(false)}>
        {addPanel}
      </SettingsDetailSheet>
      <ToastContainer messages={toasts} />
    </>
  );
}

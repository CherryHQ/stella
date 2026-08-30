import { useMemo, useState } from "react";
import { Link } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronRight, Lock, MoreHorizontal, Plus } from "lucide-react";
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
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Collapsible, CollapsiblePanel, CollapsibleTrigger } from "@/components/ui/collapsible";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuTrigger,
} from "@/components/ui/menu";
import { Switch } from "@/components/ui/switch";
import { Tooltip, TooltipPopup, TooltipTrigger } from "@/components/ui/tooltip";
import { updateAgentTool } from "@/lib/api-client";
import { deleteScopedMcpServer } from "@/lib/api-client/sdk.gen";
import type { McpServer } from "@/lib/api-client/types.gen";
import { apiErrorMessage } from "@/lib/api-error";
import { agentToolsOptions } from "@/lib/queries/agents";
import { agentMcpServersOptions, MCP_SCOPE_PRECEDENCE } from "@/lib/queries/mcp";
import { meQueryOptions } from "@/lib/queries/me";
import { SCOPE_LABEL_KEY } from "@/lib/skill-scope";
import type { MessageKey } from "@/lib/i18n/messages";
import type { Tool } from "@/lib/types";
import { useToast } from "@/hooks/use-toast";
import { useI18n } from "@/lib/i18n";
import { AgentMcpServerSheet } from "./AgentMcpServerSheet";
import { ProfilePanelSection, ProfileSectionMessage } from "./ProfilePanelSection";

const SOURCE_LABEL_KEY = {
  core: "agents.tools.source.core",
  builtin: "agents.tools.source.builtin",
  plugin: "agents.tools.source.plugin",
} as const;

const SYSTEM_FAMILY_ORDER = [
  "agent_management",
  "knowledge_and_skills",
  "models_and_deployment",
  "extensions_and_connections",
] as const;

const SYSTEM_FAMILY_LABEL_KEY = {
  agent_management: "agents.tools.system.family.agentManagement",
  knowledge_and_skills: "agents.tools.system.family.knowledgeAndSkills",
  models_and_deployment: "agents.tools.system.family.modelsAndDeployment",
  extensions_and_connections: "agents.tools.system.family.extensionsAndConnections",
} as const satisfies Record<(typeof SYSTEM_FAMILY_ORDER)[number], MessageKey>;

const REGULAR_FAMILY_ORDER = [
  "goal",
  "scheduler",
  "workflow",
  "oauth",
  "email",
  "share",
  "vault",
  "recally",
  "session",
  "skill",
  "library",
  "memory",
  "core_tools",
  "plugin_tools",
  "other_tools",
] as const;

const REGULAR_FAMILY_LABEL_KEY = {
  goal: "agents.tools.family.goal",
  scheduler: "agents.tools.family.scheduler",
  workflow: "agents.tools.family.workflow",
  oauth: "agents.tools.family.oauth",
  email: "agents.tools.family.email",
  share: "agents.tools.family.share",
  vault: "agents.tools.family.vault",
  recally: "agents.tools.family.recally",
  session: "agents.tools.family.session",
  skill: "agents.tools.family.skill",
  library: "agents.tools.family.library",
  memory: "agents.tools.family.memory",
  core_tools: "agents.tools.family.coreTools",
  plugin_tools: "agents.tools.family.pluginTools",
  other_tools: "agents.tools.family.otherTools",
} as const satisfies Record<(typeof REGULAR_FAMILY_ORDER)[number], MessageKey>;

type ToolOverrideScope = "user" | "user_agent" | "system" | "system_agent";
type SystemFamily = (typeof SYSTEM_FAMILY_ORDER)[number];
type RegularToolFamily = (typeof REGULAR_FAMILY_ORDER)[number];

const WIDER_SCOPES: ToolOverrideScope[] = ["user", "system_agent", "system"];
const ADMIN_SCOPES = new Set<string>(["system", "system_agent"]);
const EMAIL_CONFIG_REQUIRED = "email_config_required";

type FamilyState =
  | { kind: "email_config_required"; enabledCount: number; overrideCount: number }
  | { kind: "all_enabled"; enabledCount: number; overrideCount: number }
  | { kind: "partially_enabled"; enabledCount: number; overrideCount: number }
  | { kind: "all_disabled"; enabledCount: number; overrideCount: number }
  | { kind: "system_managed"; enabledCount: number; overrideCount: number };

interface McpRow {
  name: string;
  url: string;
  server?: McpServer;
}

interface Props {
  agentId: string;
  canEdit: boolean;
}

function isSystemSettingsTool(tool: Tool): tool is Tool & { family: SystemFamily } {
  return (
    tool.control === "system" &&
    tool.policy_reason === "settings_policy" &&
    tool.family != null &&
    // SAFETY: Settings policy remains a closed display-family list even though
    // AgentTool.family now also carries open-ended toolmeta families.
    SYSTEM_FAMILY_ORDER.includes(tool.family as SystemFamily)
  );
}

function sourceLabel(source: string): MessageKey {
  // SAFETY: source is untrusted API data, and a missing map entry deliberately
  // renders the generic Unknown label rather than claiming a different source.
  return SOURCE_LABEL_KEY[source as keyof typeof SOURCE_LABEL_KEY] ?? "agents.tools.source.unknown";
}

function regularFamily(tool: Tool): RegularToolFamily {
  // SAFETY: family is untrusted API data; membership in the label map narrows
  // it to the only display families this client translates explicitly.
  const family = tool.family as RegularToolFamily | undefined;
  if (family && family in REGULAR_FAMILY_LABEL_KEY) return family;
  // A new generated family or an untrusted plugin value must not turn into a
  // raw backend identifier in the UI. The backend groups known plugin tools
  // under plugin_tools; this catches future or malformed values safely.
  return "other_tools";
}

// groupedRegularTools is the Profile's single family-navigation boundary: source
// stays on each row as metadata and cannot create a second top-level section.
export function groupedRegularTools(
  tools: Tool[],
): Array<{ family: RegularToolFamily; tools: Tool[] }> {
  const members = new Map<RegularToolFamily, Tool[]>();
  for (const tool of tools) {
    const family = regularFamily(tool);
    const group = members.get(family) ?? [];
    group.push(tool);
    members.set(family, group);
  }
  return REGULAR_FAMILY_ORDER.filter((family) => members.has(family)).map((family) => ({
    family,
    tools: (members.get(family) ?? []).sort((a, b) => a.name.localeCompare(b.name)),
  }));
}

function familyState(tools: Tool[]): FamilyState {
  const overrides = tools.filter((tool) => tool.control === "override" && tool.enabled != null);
  const enabledCount = overrides.filter((tool) => tool.enabled).length;
  if (
    tools.length > 0 &&
    tools.every(
      (tool) =>
        tool.control === "system" &&
        tool.policy_reason === "runtime_unavailable" &&
        tool.availability_reason === EMAIL_CONFIG_REQUIRED,
    )
  ) {
    return { kind: "email_config_required", enabledCount, overrideCount: overrides.length };
  }
  if (overrides.length === 0) {
    return { kind: "system_managed", enabledCount, overrideCount: 0 };
  }
  if (enabledCount === overrides.length) {
    return { kind: "all_enabled", enabledCount, overrideCount: overrides.length };
  }
  if (enabledCount === 0) {
    return { kind: "all_disabled", enabledCount, overrideCount: overrides.length };
  }
  return { kind: "partially_enabled", enabledCount, overrideCount: overrides.length };
}

function originLabel(origin: string): MessageKey {
  if (origin === "default") return "agents.tools.origin.default";
  // SAFETY: origin is untrusted API data, and an unknown scope must render as
  // Unknown rather than the default origin.
  return SCOPE_LABEL_KEY[origin as keyof typeof SCOPE_LABEL_KEY] ?? "agents.tools.origin.unknown";
}

/**
 * The profile is a control surface, not a runtime preview. System Settings are
 * described from server policy, while only rows marked `override` can write a
 * tool override that the runner will honor.
 */
export function AgentToolsPanel({ agentId, canEdit }: Props) {
  const { t } = useI18n();
  const { showToast } = useToast();
  const queryClient = useQueryClient();
  const { data: me } = useQuery(meQueryOptions);
  const isAdmin = me?.is_admin ?? false;
  const query = useQuery(agentToolsOptions(agentId));
  const [sheetOpen, setSheetOpen] = useState(false);
  const [editingServer, setEditingServer] = useState<McpServer | null>(null);
  const [formSeq, setFormSeq] = useState(0);
  const [pendingDelete, setPendingDelete] = useState<McpServer | null>(null);
  const mcpQuery = useQuery(agentMcpServersOptions(agentId, isAdmin));

  const mcpRows = useMemo<McpRow[]>(() => {
    // A registration is identified by id and scope, not by tool name. Keeping
    // shadowed registrations visible is the only honest management surface:
    // a disabled higher-scope server must not masquerade as an enabled lower
    // scope server that the runtime resolved by name.
    const readableServers = [...(mcpQuery.data ?? [])].sort((a, b) => {
      const nameOrder = a.name.localeCompare(b.name);
      if (nameOrder !== 0) return nameOrder;
      return MCP_SCOPE_PRECEDENCE.indexOf(a.scope) - MCP_SCOPE_PRECEDENCE.indexOf(b.scope);
    });
    const readableNames = new Set(readableServers.map((server) => server.name));
    const readable = readableServers.map((server) => ({
      name: server.name,
      url: server.url,
      server,
    }));
    const unreadable = (query.data ?? [])
      .filter((tool) => tool.source === "mcp" && !readableNames.has(tool.name))
      .map((tool) => ({ name: tool.name, url: tool.description }))
      .sort((a, b) => a.name.localeCompare(b.name));
    return [...readable, ...unreadable];
  }, [mcpQuery.data, query.data]);

  const invalidateMcp = async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ["agent-tools", agentId] }),
      queryClient.invalidateQueries({ queryKey: ["agent-mcp-servers", agentId] }),
    ]);
  };

  const removeServer = useMutation({
    mutationFn: (server: McpServer) =>
      deleteScopedMcpServer({
        path: { id: server.id },
        query: {
          scope: server.scope,
          agent_id:
            server.scope === "user_agent" || server.scope === "system_agent"
              ? server.agent_id
              : undefined,
        },
        throwOnError: true,
      }),
    onSuccess: async () => {
      showToast(t("mcp.deleted"), "success");
      await invalidateMcp();
    },
    onError: (error) => showToast(apiErrorMessage(error, t("mcp.deleteFailed")), "error"),
  });

  const openServerSheet = (server: McpServer | null) => {
    setEditingServer(server);
    setFormSeq((n) => n + 1);
    setSheetOpen(true);
  };

  const mutation = useMutation({
    mutationFn: ({
      tool,
      enabled,
      scope,
    }: {
      tool: Tool;
      enabled: boolean;
      scope: ToolOverrideScope;
    }) =>
      updateAgentTool({
        path: { id: agentId, toolName: tool.name },
        body: { enabled, scope },
        throwOnError: true,
      }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["agent-tools", agentId] });
    },
    onError: () => showToast(t("agents.tools.updateFailed"), "error"),
  });

  if (!agentId) {
    return <ProfileSectionMessage>{t("agents.tools.createFirst")}</ProfileSectionMessage>;
  }
  if (query.isLoading) {
    return <ProfileSectionMessage>{t("agents.tools.loading")}</ProfileSectionMessage>;
  }
  if (query.isError) {
    return <ProfileSectionMessage>{t("agents.tools.loadFailed")}</ProfileSectionMessage>;
  }

  const catalog = query.data ?? [];
  const systemSettings = catalog.filter(isSystemSettingsTool);
  const tools = catalog.filter((tool) => tool.source !== "mcp" && !isSystemSettingsTool(tool));
  const toolFamilies = groupedRegularTools(tools);

  return (
    <div className="flex flex-col gap-6">
      {canEdit && (
        <AgentMcpServerSheet
          agentId={agentId}
          isAdmin={isAdmin}
          open={sheetOpen}
          server={editingServer}
          formKey={formSeq}
          onOpenChange={setSheetOpen}
          notify={showToast}
        />
      )}
      <AlertDialog open={!!pendingDelete} onOpenChange={(open) => !open && setPendingDelete(null)}>
        <AlertDialogPopup>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("mcp.deleteTitle")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("mcp.deleteConfirm", { name: pendingDelete?.name ?? "" })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogClose render={<Button variant="ghost" />}>
              {t("common.cancel")}
            </AlertDialogClose>
            <Button
              variant="destructive"
              onClick={() => {
                const target = pendingDelete;
                setPendingDelete(null);
                if (target) removeServer.mutate(target);
              }}
            >
              {t("common.delete")}
            </Button>
          </AlertDialogFooter>
        </AlertDialogPopup>
      </AlertDialog>

      <SystemSettingsSection tools={systemSettings} />

      <ProfilePanelSection
        title={t("agents.tools.title")}
        count={tools.length}
        description={t("agents.tools.description")}
      >
        {toolFamilies.length === 0 ? (
          <ProfileSectionMessage>{t("agents.tools.empty")}</ProfileSectionMessage>
        ) : (
          <div className="flex flex-col gap-3">
            {toolFamilies.map(({ family, tools: members }, index) => (
              <RegularToolFamilyCard
                key={family}
                family={family}
                tools={members}
                defaultOpen={index === 0}
                canEdit={canEdit}
                isAdmin={isAdmin}
                busyToolName={mutation.isPending ? (mutation.variables?.tool.name ?? null) : null}
                onToggle={(tool, enabled, scope) => mutation.mutate({ tool, enabled, scope })}
              />
            ))}
          </div>
        )}
      </ProfilePanelSection>

      <ProfilePanelSection
        title={t("agents.tools.mcpServers")}
        count={mcpRows.length}
        description={t("agents.tools.mcpDescription")}
        action={
          canEdit && (
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label={t("mcp.addTitle")}
              title={t("mcp.addTitle")}
              onClick={() => openServerSheet(null)}
            >
              <Plus />
            </Button>
          )
        }
      >
        <div className="flex flex-col gap-2">
          {mcpRows.length === 0 ? (
            <ProfileSectionMessage>{t("mcp.empty")}</ProfileSectionMessage>
          ) : (
            mcpRows.map((row) => (
              <McpServerRow
                key={`mcp:${row.server?.id ?? row.name}`}
                row={row}
                canEdit={canEdit}
                busy={removeServer.isPending && removeServer.variables?.name === row.name}
                onEdit={openServerSheet}
                onDelete={setPendingDelete}
              />
            ))
          )}
        </div>
      </ProfilePanelSection>
    </div>
  );
}

export function SystemSettingsSection({
  tools,
}: {
  tools: Array<Tool & { family: SystemFamily }>;
}) {
  const { t } = useI18n();
  return (
    <ProfilePanelSection
      title={t("agents.tools.system.title")}
      description={t("agents.tools.system.description")}
    >
      <div className="flex flex-wrap gap-2">
        <Badge variant="outline">{t("agents.tools.system.badge.stellaOnly")}</Badge>
        <Badge variant="outline">{t("agents.tools.system.badge.foregroundOnly")}</Badge>
        <Badge variant="outline">{t("agents.tools.system.badge.writeChecks")}</Badge>
        <Badge variant="outline">{t("agents.tools.system.badge.credentials")}</Badge>
      </div>
      <p className="text-xs text-muted-foreground">{t("agents.tools.system.policy")}</p>
      {tools.length === 0 ? (
        <ProfileSectionMessage>{t("agents.tools.system.empty")}</ProfileSectionMessage>
      ) : (
        <div className="flex flex-col gap-2">
          {SYSTEM_FAMILY_ORDER.map((family, index) => {
            const members = tools.filter((tool) => tool.family === family);
            if (members.length === 0) return null;
            return (
              <ProfilePanelSection
                key={family}
                collapsible
                defaultOpen={index === 0}
                title={t(SYSTEM_FAMILY_LABEL_KEY[family])}
                count={members.length}
              >
                <div className="flex flex-col gap-2">
                  {members.map((tool) => (
                    <SettingsActionRow key={tool.name} tool={tool} />
                  ))}
                </div>
              </ProfilePanelSection>
            );
          })}
        </div>
      )}
    </ProfilePanelSection>
  );
}

export function RegularToolFamilyCard({
  family,
  tools,
  defaultOpen,
  canEdit,
  isAdmin,
  busyToolName,
  onToggle,
}: {
  family: RegularToolFamily;
  tools: Tool[];
  defaultOpen: boolean;
  canEdit: boolean;
  isAdmin: boolean;
  busyToolName: string | null;
  onToggle: (tool: Tool, enabled: boolean, scope: ToolOverrideScope) => void;
}) {
  const { t } = useI18n();
  const state = familyState(tools);
  const emailConfigRequired = state.kind === "email_config_required";
  const stateLabel =
    state.kind === "email_config_required"
      ? t("agents.tools.family.emailSetupRequired")
      : state.kind === "all_enabled"
        ? t("agents.tools.family.allEnabled")
        : state.kind === "partially_enabled"
          ? t("agents.tools.family.enabledCount", { count: state.enabledCount })
          : state.kind === "all_disabled"
            ? t("agents.tools.family.allDisabled")
            : t("agents.tools.systemManaged");
  const stateVariant =
    state.kind === "email_config_required"
      ? "warning"
      : state.kind === "all_enabled"
        ? "success"
        : "outline";

  return (
    <Collapsible defaultOpen={defaultOpen || emailConfigRequired} render={<Card />}>
      <CardHeader>
        <h3 className="flex min-w-0">
          <CollapsibleTrigger className="group flex min-w-0 flex-1 items-center gap-2 text-left">
            <ChevronRight className="size-4 shrink-0 text-muted-foreground transition-transform duration-150 ease-out group-data-[panel-open]:rotate-90" />
            <CardTitle render={<span className="truncate" />}>
              {t(REGULAR_FAMILY_LABEL_KEY[family])}
            </CardTitle>
          </CollapsibleTrigger>
        </h3>
        <CardAction>
          <div className="flex items-center gap-1">
            <Badge variant="outline">
              {t("agents.tools.family.actionCount", { count: tools.length })}
            </Badge>
            <Badge variant={stateVariant}>{stateLabel}</Badge>
          </div>
        </CardAction>
        {emailConfigRequired && (
          <CardDescription render={<div className="flex flex-wrap items-center gap-2" />}>
            <span>{t("agents.tools.family.emailConfigRequired")}</span>
            <Button variant="link" size="xs" render={<Link to="/settings/credentials" />}>
              {t("agents.tools.family.configureEmail")}
            </Button>
          </CardDescription>
        )}
      </CardHeader>
      <CollapsiblePanel render={<CardContent />}>
        <div className="flex flex-col gap-2">
          {tools.map((tool) => (
            <ToolRow
              key={`${tool.source}:${tool.name}`}
              tool={tool}
              canEdit={canEdit}
              isAdmin={isAdmin}
              busy={busyToolName === tool.name}
              compactRuntimeStatus={emailConfigRequired}
              onToggle={(enabled, scope) => onToggle(tool, enabled, scope)}
            />
          ))}
        </div>
      </CollapsiblePanel>
    </Collapsible>
  );
}

function SettingsActionRow({ tool }: { tool: Tool }) {
  const { t } = useI18n();
  return (
    <Card>
      <CardContent className="flex min-w-0 flex-col gap-1">
        <div className="flex min-w-0 items-center gap-2">
          <span className="truncate font-mono text-sm font-semibold text-foreground">
            {tool.name}
          </span>
          {tool.admin_required && (
            <Badge variant="outline">{t("agents.tools.system.adminRequired")}</Badge>
          )}
          <Badge variant="outline">{t(sourceLabel(tool.source))}</Badge>
        </div>
        <p className="text-xs text-muted-foreground">{tool.description}</p>
      </CardContent>
    </Card>
  );
}

function McpServerRow({
  row,
  canEdit,
  busy,
  onEdit,
  onDelete,
}: {
  row: McpRow;
  canEdit: boolean;
  busy: boolean;
  onEdit: (server: McpServer) => void;
  onDelete: (server: McpServer) => void;
}) {
  const { t } = useI18n();
  const server = row.server;
  return (
    <Card>
      <CardContent className="flex items-start justify-between gap-3">
        <div className="flex min-w-0 flex-col gap-1">
          <div className="flex min-w-0 items-center gap-2">
            <span className="truncate font-mono text-sm font-semibold text-foreground">
              {row.name}
            </span>
            {server && <Badge variant="outline">{t(SCOPE_LABEL_KEY[server.scope])}</Badge>}
            {server && !server.enabled && (
              <Badge variant="outline">{t("agents.tools.disabled")}</Badge>
            )}
          </div>
          <p className="truncate text-xs text-muted-foreground">{row.url}</p>
        </div>
        {canEdit && (
          <div className="flex shrink-0 items-center gap-1">
            {server ? (
              <DropdownMenu>
                <DropdownMenuTrigger
                  render={
                    <Button
                      variant="ghost"
                      size="icon-xs"
                      disabled={busy}
                      aria-label={t("common.actions")}
                    />
                  }
                >
                  <MoreHorizontal />
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end" sideOffset={6}>
                  <DropdownMenuLabel>{row.name}</DropdownMenuLabel>
                  <DropdownMenuItem onClick={() => onEdit(server)}>
                    {t("common.edit")}
                  </DropdownMenuItem>
                  <DropdownMenuItem onClick={() => onDelete(server)}>
                    {t("common.delete")}
                  </DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
            ) : (
              <Tooltip>
                <TooltipTrigger
                  render={
                    <span
                      tabIndex={0}
                      role="note"
                      className="flex size-8 shrink-0 items-center justify-center text-muted-foreground"
                      aria-label={t("agents.tools.mcpReadOnly")}
                    />
                  }
                >
                  <Lock size={16} />
                </TooltipTrigger>
                <TooltipPopup side="top" className="max-w-56">
                  {t("agents.tools.mcpReadOnly")}
                </TooltipPopup>
              </Tooltip>
            )}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

export function ToolRow({
  tool,
  canEdit,
  isAdmin,
  busy,
  compactRuntimeStatus = false,
  onToggle,
}: {
  tool: Tool;
  canEdit: boolean;
  isAdmin: boolean;
  busy: boolean;
  compactRuntimeStatus?: boolean;
  onToggle: (enabled: boolean, scope: ToolOverrideScope) => void;
}) {
  const { t } = useI18n();
  const overridable = tool.control === "override" && tool.enabled != null && tool.origin != null;
  const enabled = tool.enabled ?? false;
  const origin = tool.origin ?? "default";
  const adminLocked = overridable && !enabled && ADMIN_SCOPES.has(origin) && !isAdmin;
  const scopes = WIDER_SCOPES.filter((scope) => isAdmin || !ADMIN_SCOPES.has(scope));

  return (
    <Card>
      <CardContent className="flex items-start justify-between gap-3">
        <div className="flex min-w-0 flex-col gap-1">
          <div className="flex min-w-0 items-center gap-2">
            <span className="truncate font-mono text-sm font-semibold text-foreground">
              {tool.name}
            </span>
            {overridable ? (
              <>
                <Badge variant={enabled ? "success" : "outline"}>
                  {enabled ? t("agents.tools.enabled") : t("agents.tools.disabled")}
                </Badge>
                <Badge variant="outline">{t(originLabel(origin))}</Badge>
              </>
            ) : (
              !compactRuntimeStatus && (
                <Badge variant="outline">{t("agents.tools.systemManaged")}</Badge>
              )
            )}
            <Badge variant="outline">{t(sourceLabel(tool.source))}</Badge>
          </div>
          <p className="text-xs text-muted-foreground">{tool.description}</p>
          {!overridable && tool.policy_reason === "core_sandbox" && (
            <p className="text-xs text-muted-foreground">{t("agents.tools.locked.core")}</p>
          )}
          {!overridable &&
            !compactRuntimeStatus &&
            tool.policy_reason === "runtime_unavailable" && (
              <p className="text-xs text-muted-foreground">{t("agents.tools.runtimeManaged")}</p>
            )}
        </div>
        {canEdit && overridable && (
          <div className="flex shrink-0 items-center gap-1">
            {adminLocked ? (
              <Tooltip>
                <TooltipTrigger render={<span className="inline-flex" />}>
                  <Switch checked={false} disabled />
                </TooltipTrigger>
                <TooltipPopup>{t("agents.tools.adminDisabled")}</TooltipPopup>
              </Tooltip>
            ) : (
              <>
                <Switch
                  checked={enabled}
                  disabled={busy}
                  onCheckedChange={(checked) => onToggle(!!checked, "user_agent")}
                />
                <DropdownMenu>
                  <DropdownMenuTrigger
                    render={
                      <Button
                        variant="ghost"
                        size="icon-xs"
                        disabled={busy}
                        aria-label={t("agents.tools.moreScopes")}
                      />
                    }
                  >
                    <MoreHorizontal />
                  </DropdownMenuTrigger>
                  <DropdownMenuContent align="end" sideOffset={6}>
                    <DropdownMenuLabel>
                      {enabled ? t("agents.tools.applyDisable") : t("agents.tools.applyEnable")}
                    </DropdownMenuLabel>
                    {scopes.map((scope) => (
                      <DropdownMenuItem key={scope} onClick={() => onToggle(!enabled, scope)}>
                        {t(SCOPE_LABEL_KEY[scope])}
                      </DropdownMenuItem>
                    ))}
                  </DropdownMenuContent>
                </DropdownMenu>
              </>
            )}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Lock, MoreHorizontal, Plus } from "lucide-react";
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
import type { Tool } from "@/lib/types";
import { useToast } from "@/hooks/use-toast";
import { useI18n } from "@/lib/i18n";
import { AgentMcpServerSheet } from "./AgentMcpServerSheet";
import { ProfilePanelSection, ProfileSectionMessage } from "./ProfilePanelSection";

const SOURCE_ORDER: Record<string, number> = { core: 0, builtin: 1, plugin: 2, mcp: 3 };
const SOURCE_LABEL_KEY = {
  core: "agents.tools.source.core",
  builtin: "agents.tools.source.builtin",
  plugin: "agents.tools.source.plugin",
  mcp: "agents.tools.source.mcp",
} as const;
type ToolOverrideScope = "user" | "user_agent" | "system" | "system_agent";

/**
 * Scopes the row's ⋯ menu can write, widest intent first. `user_agent` is absent
 * on purpose: it is what the row Switch already writes.
 */
const WIDER_SCOPES: ToolOverrideScope[] = ["user", "system_agent", "system"];
const ADMIN_SCOPES = new Set<string>(["system", "system_agent"]);

/**
 * One row of the MCP section. `server` is the registration the viewer may read
 * and therefore manage; without it the row is one the agent resolved but the
 * viewer cannot address, and stays read-only.
 */
interface McpRow {
  name: string;
  url: string;
  server?: McpServer;
}

interface Props {
  agentId: string;
  /**
   * Whether the viewer may change the enabled state. Everyone can read the
   * catalog; only an admin or the agent's creator may write an override, so a
   * read-only viewer never gets a switch that would 403.
   */
  canEdit: boolean;
}

/**
 * The agent's runtime tool catalog, grouped by source. Shared by the agent
 * editor's tools tab and the profile's tools tab so both read the same list and
 * write through the same override mutation.
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
  // Bumped on every open so the sheet's draft is a fresh mount, never the
  // leftovers of the last server that was edited.
  const [formSeq, setFormSeq] = useState(0);
  const [pendingDelete, setPendingDelete] = useState<McpServer | null>(null);
  const mcpQuery = useQuery(agentMcpServersOptions(agentId, isAdmin));

  // The MCP section is driven by the registrations, not by the tool payload:
  // the payload only carries servers that are `enabled`, so deriving rows from
  // it would make a server vanish the moment it was switched off, with no way
  // back. Registrations the viewer cannot read (admin scopes for a non-admin)
  // exist only in the payload, so those rows are folded back in as read-only.
  //
  // Name is the only identity the two lists share — the payload has no id and
  // no scope — and one name can be registered in several scopes, so the
  // readable side is deduped with the backend's own precedence first.
  const mcpRows = useMemo<McpRow[]>(() => {
    const byName = new Map<string, McpServer>();
    for (const server of mcpQuery.data ?? []) {
      const current = byName.get(server.name);
      if (
        !current ||
        MCP_SCOPE_PRECEDENCE.indexOf(server.scope) < MCP_SCOPE_PRECEDENCE.indexOf(current.scope)
      ) {
        byName.set(server.name, server);
      }
    }
    const byNameAsc = (a: { name: string }, b: { name: string }) => a.name.localeCompare(b.name);
    const readable: McpRow[] = [...byName.values()]
      .sort(byNameAsc)
      .map((server) => ({ name: server.name, url: server.url, server }));
    const unreadable: McpRow[] = (query.data ?? [])
      .filter((tool) => tool.source === "mcp" && !byName.has(tool.name))
      .map((tool) => ({ name: tool.name, url: tool.description }))
      .sort(byNameAsc);
    // Manageable rows first, then the locked ones; both alphabetical, so the
    // order never depends on which query settled first.
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

  // MCP is rendered from `mcpRows`, not from the payload, so it is dropped from
  // the grouped tools here and appended as its own section below.
  const tools = [...(query.data ?? [])]
    .filter((tool) => tool.source !== "mcp")
    .sort((a, b) => {
      const diff = (SOURCE_ORDER[a.source ?? ""] ?? 9) - (SOURCE_ORDER[b.source ?? ""] ?? 9);
      return diff !== 0 ? diff : a.name.localeCompare(b.name);
    });
  const groups = tools.reduce<Record<string, Tool[]>>((acc, tool) => {
    const source = tool.source ?? "builtin";
    acc[source] = [...(acc[source] ?? []), tool];
    return acc;
  }, {});
  // The MCP section is the only place this agent's servers can be added, so an
  // editor sees it even when there is nothing registered yet — otherwise the ＋
  // would appear only once a server already existed.
  const showMcp = canEdit || mcpRows.length > 0;

  return (
    <div className="flex flex-col gap-6">
      <ProfilePanelSection
        title={t("agents.tools.title")}
        description={t("agents.tools.description")}
      />
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
      {/* The confirmation lives here, not in the sheet: an overlay nested inside
          a Sheet is a bug (see web-ui.md). */}
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
      {tools.length === 0 && !showMcp ? (
        <ProfileSectionMessage>{t("agents.tools.empty")}</ProfileSectionMessage>
      ) : (
        <div className="flex flex-col gap-6">
          {/* MCP leads: it is the only group the user configures rather than
              toggles, so it sits above the built-in catalog. */}
          {showMcp && (
            <ProfilePanelSection
              title={t(SOURCE_LABEL_KEY.mcp)}
              count={mcpRows.length}
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
                      key={`mcp:${row.name}`}
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
          )}
          {Object.entries(groups).map(([source, items]) => (
            <ProfilePanelSection
              key={source}
              title={t(
                SOURCE_LABEL_KEY[source as keyof typeof SOURCE_LABEL_KEY] ??
                  "agents.tools.source.builtin",
              )}
              count={items.length}
            >
              <div className="flex flex-col gap-2">
                {items.map((tool) => (
                  <ToolRow
                    key={`${tool.source}:${tool.name}`}
                    tool={tool}
                    canEdit={canEdit}
                    isAdmin={isAdmin}
                    busy={mutation.isPending && mutation.variables?.tool.name === tool.name}
                    onToggle={(enabled, scope) => mutation.mutate({ tool, enabled, scope })}
                  />
                ))}
              </div>
            </ProfilePanelSection>
          ))}
        </div>
      )}
    </div>
  );
}

/**
 * One MCP server registration. A row carrying a `server` is one the viewer may
 * address through the MCP API, so it is manageable; without it the agent
 * resolved the registration but the viewer cannot read it — a system-scope
 * server for a non-admin — so the row shows the same lock a read-only skill does.
 */
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
    <div className="flex items-start justify-between gap-3 rounded-lg border border-border p-3">
      <div className="flex min-w-0 flex-col gap-1">
        <div className="flex min-w-0 items-center gap-2">
          <span className="truncate font-mono text-sm font-semibold text-foreground">
            {row.name}
          </span>
          {server && <Badge variant="outline">{t(SCOPE_LABEL_KEY[server.scope])}</Badge>}
          {/* A disabled registration feeds the agent nothing, and the tool
              payload drops it entirely — say so rather than let the row look
              live. */}
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
                  // Focusable on purpose: the lock is the only explanation the
                  // row carries, so it must be reachable by keyboard.
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
    </div>
  );
}

function ToolRow({
  tool,
  canEdit,
  isAdmin,
  busy,
  onToggle,
}: {
  tool: Tool;
  canEdit: boolean;
  isAdmin: boolean;
  busy: boolean;
  onToggle: (enabled: boolean, scope: ToolOverrideScope) => void;
}) {
  const { t } = useI18n();
  const togglable = tool.source === "builtin" || tool.source === "plugin";
  // The backend resolves the winning layer per viewer and reports it as `origin`.
  // An admin-scope "off" beats any user-layer row, so a non-admin's own toggle
  // would silently do nothing — say so instead of offering a dead control.
  const adminLocked = !tool.enabled && ADMIN_SCOPES.has(tool.origin) && !isAdmin;
  const scopes = WIDER_SCOPES.filter((scope) => isAdmin || !ADMIN_SCOPES.has(scope));
  return (
    <div className="flex items-start justify-between gap-3 rounded-lg border border-border p-3">
      <div className="flex min-w-0 flex-col gap-1">
        <div className="flex min-w-0 items-center gap-2">
          <span className="truncate font-mono text-sm font-semibold text-foreground">
            {tool.name}
          </span>
          <Badge variant={tool.enabled ? "success" : "outline"}>
            {tool.enabled ? t("agents.tools.enabled") : t("agents.tools.disabled")}
          </Badge>
          {/* Which layer decided the state above, in the same scope vocabulary
              the ⋯ menu writes with. */}
          <Badge variant="outline">
            {t(
              SCOPE_LABEL_KEY[tool.origin as keyof typeof SCOPE_LABEL_KEY] ??
                "agents.tools.origin.default",
            )}
          </Badge>
        </div>
        <p className="text-xs text-muted-foreground">{tool.description}</p>
        {canEdit && !togglable && (
          // MCP rows never reach here — they render as manageable server rows —
          // so the only non-togglable source left is the core sandbox set.
          <p className="text-xs text-muted-foreground">{t("agents.tools.locked.core")}</p>
        )}
      </div>
      {canEdit && togglable && (
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
                checked={tool.enabled}
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
                    {tool.enabled ? t("agents.tools.applyDisable") : t("agents.tools.applyEnable")}
                  </DropdownMenuLabel>
                  {scopes.map((scope) => (
                    <DropdownMenuItem key={scope} onClick={() => onToggle(!tool.enabled, scope)}>
                      {t(SCOPE_LABEL_KEY[scope])}
                    </DropdownMenuItem>
                  ))}
                </DropdownMenuContent>
              </DropdownMenu>
            </>
          )}
        </div>
      )}
    </div>
  );
}

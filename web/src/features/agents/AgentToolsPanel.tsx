import { Link } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { MoreHorizontal } from "lucide-react";
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
import { agentToolsOptions } from "@/lib/queries/agents";
import { meQueryOptions } from "@/lib/queries/me";
import { SCOPE_LABEL_KEY } from "@/lib/skill-scope";
import type { Tool } from "@/lib/types";
import { ToastContainer, useToast } from "@/hooks/use-toast";
import { useI18n } from "@/lib/i18n";
import { ProfilePanelSection, ProfileSectionMessage } from "./ProfilePanelSection";

const SOURCE_ORDER: Record<string, number> = { core: 0, builtin: 1, plugin: 2, mcp: 3 };
const SOURCE_LABEL_KEY = {
  core: "agents.tools.source.core",
  builtin: "agents.tools.source.builtin",
  plugin: "agents.tools.source.plugin",
  mcp: "agents.tools.source.mcp",
} as const;
const LOCKED_LABEL_KEY = {
  core: "agents.tools.locked.core",
  mcp: "agents.tools.locked.mcp",
} as const;

type ToolOverrideScope = "user" | "user_agent" | "system" | "system_agent";

/**
 * Scopes the row's ⋯ menu can write, widest intent first. `user_agent` is absent
 * on purpose: it is what the row Switch already writes.
 */
const WIDER_SCOPES: ToolOverrideScope[] = ["user", "system_agent", "system"];
const ADMIN_SCOPES = new Set<string>(["system", "system_agent"]);

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
  const { toasts, showToast } = useToast();
  const queryClient = useQueryClient();
  const { data: me } = useQuery(meQueryOptions);
  const isAdmin = me?.is_admin ?? false;
  const query = useQuery(agentToolsOptions(agentId));
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

  const tools = [...(query.data ?? [])].sort((a, b) => {
    const diff = (SOURCE_ORDER[a.source ?? ""] ?? 9) - (SOURCE_ORDER[b.source ?? ""] ?? 9);
    return diff !== 0 ? diff : a.name.localeCompare(b.name);
  });
  const groups = tools.reduce<Record<string, Tool[]>>((acc, tool) => {
    const source = tool.source ?? "builtin";
    acc[source] = [...(acc[source] ?? []), tool];
    return acc;
  }, {});

  return (
    <div className="flex flex-col gap-6">
      <ToastContainer messages={toasts} />
      <ProfilePanelSection
        title={t("agents.tools.title")}
        description={t("agents.tools.description")}
      />
      {tools.length === 0 ? (
        <ProfileSectionMessage>{t("agents.tools.empty")}</ProfileSectionMessage>
      ) : (
        <div className="flex flex-col gap-6">
          {Object.entries(groups).map(([source, items]) => (
            <ProfilePanelSection
              key={source}
              title={t(
                SOURCE_LABEL_KEY[source as keyof typeof SOURCE_LABEL_KEY] ??
                  "agents.tools.source.builtin",
              )}
              count={items.length}
              action={
                source === "mcp" &&
                canEdit && (
                  <Button variant="ghost" size="sm" render={<Link to="/settings/plugins" />}>
                    {t("agents.tools.manageMcp")}
                  </Button>
                )
              }
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
          <p className="text-xs text-muted-foreground">
            {t(
              LOCKED_LABEL_KEY[tool.source as keyof typeof LOCKED_LABEL_KEY] ??
                "agents.tools.locked.core",
            )}
          </p>
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

import { useState } from "react";
import { Link } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectItem,
  SelectPopup,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { updateAgentTool } from "@/lib/api-client";
import { agentToolsOptions } from "@/lib/queries/agents";
import { meQueryOptions } from "@/lib/queries/me";
import type { Tool } from "@/lib/types";
import { ToastContainer, useToast } from "@/hooks/use-toast";
import { useI18n } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/messages";
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

const TOOL_SCOPE_ORDER: ToolOverrideScope[] = ["user", "user_agent", "system", "system_agent"];
const TOOL_SCOPE_LABEL_KEY: Record<ToolOverrideScope, MessageKey> = {
  user: "agents.tools.scope.user",
  user_agent: "agents.tools.scope.userAgent",
  system: "agents.tools.scope.system",
  system_agent: "agents.tools.scope.systemAgent",
};

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
  const [selectedScope, setSelectedScope] = useState<ToolOverrideScope>("user_agent");
  const { data: me } = useQuery(meQueryOptions);
  const isAdmin = me?.is_admin ?? false;
  const query = useQuery(agentToolsOptions(agentId));
  const mutation = useMutation({
    mutationFn: ({ tool, enabled }: { tool: Tool; enabled: boolean }) =>
      updateAgentTool({
        path: { id: agentId, toolName: tool.name },
        body: { enabled, scope: selectedScope },
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

  const scopeOptions = TOOL_SCOPE_ORDER.filter(
    (scope) => isAdmin || (scope !== "system" && scope !== "system_agent"),
  );
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
      {/* The write scope is an attribute of the whole catalog, so it sits in the
          panel heading's action slot rather than in a form block of its own. */}
      <ProfilePanelSection
        title={t("agents.tools.title")}
        description={
          canEdit
            ? `${t("agents.tools.description")} ${t("agents.tools.scope.description")}`
            : t("agents.tools.description")
        }
        action={
          canEdit && (
            <Select
              value={selectedScope}
              onValueChange={(value) => setSelectedScope(value as ToolOverrideScope)}
            >
              <SelectTrigger size="sm" aria-label={t("agents.tools.scope.label")}>
                <SelectValue>
                  {(value) =>
                    t(TOOL_SCOPE_LABEL_KEY[(value as ToolOverrideScope) || selectedScope])
                  }
                </SelectValue>
              </SelectTrigger>
              <SelectPopup>
                {scopeOptions.map((scope) => (
                  <SelectItem key={scope} value={scope}>
                    {t(TOOL_SCOPE_LABEL_KEY[scope])}
                  </SelectItem>
                ))}
              </SelectPopup>
            </Select>
          )
        }
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
                    busy={mutation.isPending && mutation.variables?.tool.name === tool.name}
                    onToggle={(enabled) => mutation.mutate({ tool, enabled })}
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
  busy,
  onToggle,
}: {
  tool: Tool;
  canEdit: boolean;
  busy: boolean;
  onToggle: (enabled: boolean) => void;
}) {
  const { t } = useI18n();
  const togglable = tool.source === "builtin" || tool.source === "plugin";
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
          <Badge variant="outline">{tool.origin}</Badge>
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
        <Switch
          checked={tool.enabled}
          disabled={busy}
          onCheckedChange={(checked) => onToggle(!!checked)}
        />
      )}
    </div>
  );
}

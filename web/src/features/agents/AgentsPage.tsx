import { useState } from "react";
import { useLoaderData, useNavigate, useParams, useRouter } from "@tanstack/react-router";
import type { AgentDetail } from "@/lib/types";
import type { AgentsSettingsLoaderData } from "@/lib/queries/agent-settings";
import { canEditAgent } from "./agent-detail-state";
import { AgentDetailPanel } from "./AgentDetailPanel";
import { SettingsCard, SettingsGridPage } from "@/features/settings/SettingsCardGrid";
import { Button } from "@/components/ui/button";
import { Bot, Plus } from "lucide-react";
import { useI18n } from "@/lib/i18n";

/**
 * The agent fleet: a grid of every agent, plus creation. Editing one agent is
 * {@link AgentDetailPanel}'s job — the same panel the agent's profile page
 * embeds — so both surfaces stay behaviorally identical.
 */
export function AgentsPage() {
  const navigate = useNavigate();
  const router = useRouter();
  const params = useParams({ strict: false }) as { agentId?: string; tab?: string };
  const routeAgentId = params.agentId ?? "";
  const routeTab = params.tab ?? "config";
  const data = useLoaderData({ strict: false }) as AgentsSettingsLoaderData | undefined;
  const [creating, setCreating] = useState(false);
  const { t } = useI18n();

  const editing = routeAgentId && data?.selectedAgent ? routeAgentId : "";
  const showPanel = !!data && (!!editing || creating);

  const modelLabel = (value: string) =>
    data?.cachedModels.find((m) => m.value === value)?.label ?? value;

  if (showPanel) {
    return (
      <AgentDetailPanel
        key={editing || "new"}
        data={data}
        agentId={editing}
        activeTab={routeTab}
        onTabChange={(tab) => {
          if (!editing) return;
          void navigate({
            to: "/settings/agents/$agentId/$tab",
            params: { agentId: editing, tab },
          });
        }}
        onClose={() => {
          setCreating(false);
          void navigate({ to: "/settings/agents" });
        }}
        onSaved={(agentId) => {
          void router.invalidate();
          if (!editing) {
            setCreating(false);
            void navigate({
              to: "/settings/agents/$agentId/$tab",
              params: { agentId, tab: "config" },
            });
          }
        }}
        onDeleted={() => {
          setCreating(false);
          void navigate({ to: "/settings/agents" });
          void router.invalidate();
        }}
      />
    );
  }

  return (
    <SettingsGridPage
      title={t("settings.nav.agents")}
      action={
        <Button onClick={() => setCreating(true)} variant="outline" size="sm">
          <Plus className="size-4" />
          {t("agents.form.newAgent")}
        </Button>
      }
    >
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {(data?.agents ?? []).map((a: AgentDetail) => {
          const canEdit = canEditAgent(a, data?.isAdmin ?? false, data?.currentUserId ?? "");
          return (
            <SettingsCard
              key={a.id}
              icon={<Bot className="size-4" />}
              title={a.name || a.id}
              active={routeAgentId === a.id}
              to={canEdit ? "/settings/agents/$agentId/$tab" : undefined}
              params={canEdit ? { agentId: a.id, tab: "config" } : undefined}
              footer={
                <>
                  <span
                    className={`size-1.5 shrink-0 rounded-full ${
                      a.enabled ? "bg-chart-3" : "bg-muted-foreground"
                    }`}
                  />
                  <span className="truncate font-mono text-xs text-muted-foreground">
                    {a.model ? modelLabel(a.model) : "—"}
                  </span>
                </>
              }
            />
          );
        })}
      </div>
    </SettingsGridPage>
  );
}

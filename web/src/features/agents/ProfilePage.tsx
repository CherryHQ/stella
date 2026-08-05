import { useCallback, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate, useParams, useSearch } from "@tanstack/react-router";
import {
  Brain,
  ChevronRight,
  ListTodo,
  MessageCircle,
  Puzzle,
  SlidersHorizontal,
  Wrench,
} from "lucide-react";
import { useI18n } from "@/lib/i18n";
import { getAgentColor } from "@/lib/agent-colors";
import { agentsQueryOptions, agentSkillsOptions, agentToolsOptions } from "@/lib/queries/agents";
import { agentSettingsQueryOptions } from "@/lib/queries/agent-settings";
import { goalCountsOptions } from "@/lib/queries/goals";
import { agentMemoryOptions } from "@/lib/queries/memories";
import { meQueryOptions } from "@/lib/queries/me";
import { modelsQueryOptions } from "@/lib/queries/models";
import { agentProjectsOptions } from "@/lib/queries/projects";
import { formatTime } from "@/lib/time";
import type { MemorySearch, ProfileTab } from "@/lib/route-search";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Tabs, TabsList, TabsPanel, TabsTab } from "@/components/ui/tabs";
import { Spinner } from "@/components/ui/spinner";
import { canEditAgent } from "./agent-detail-state";
import { AgentChannelsPanel } from "./AgentChannelsPanel";
import { AgentDetailPanel } from "./AgentDetailPanel";
import { AgentToolsPanel } from "./AgentToolsPanel";
import { ProfileSkillsTab } from "./ProfileSkillsTab";
import { SoulSection } from "@/features/memories/SoulSection";
import { ProfileSection } from "@/features/memories/ProfileSection";
import { KnowledgeSection } from "@/features/memories/KnowledgeSection";
import { ConstraintsSection } from "@/features/memories/ConstraintsSection";
import { ChangelogSection } from "@/features/memories/ChangelogSection";

/**
 * Skills keep their own full-page management surface (deep-linked from the
 * skills tab) and tools now have a profile tab of their own, so the embedded
 * editor drops both rather than showing them twice.
 */
const PROFILE_HIDDEN_CONFIG_TABS: readonly string[] = ["skills", "tools"];

/**
 * "Who this agent is" as one entity card: a persistent identity header over a
 * tab strip. Overview lands first; memory, skills and tools are open to every
 * viewer; configuration exists only for whoever may change it — it carries the
 * system prompt, so an unauthorized viewer must not even fetch it.
 *
 * Renders for both an agent and a project inside it. Memory is agent-scoped in
 * the API either way, so a project only drops the tabs that are agent-only.
 */
export function ProfilePage() {
  const { t } = useI18n();
  const navigate = useNavigate();
  const { agentId, projectId } = useParams({ strict: false }) as {
    agentId: string;
    projectId?: string;
  };
  const search = useSearch({ strict: false }) as MemorySearch;
  const knowledgeState = search.knowledge === "removed" ? "removed" : "active";

  const { data: agents = [] } = useQuery(agentsQueryOptions);
  const { data: me } = useQuery(meQueryOptions);
  const { data: models = [] } = useQuery(modelsQueryOptions);
  const { data: memory } = useQuery(agentMemoryOptions(agentId));
  const { data: projects = [] } = useQuery(agentProjectsOptions(agentId));

  const agentIndex = agents.findIndex((agent) => agent.id === agentId);
  const agent = agentIndex >= 0 ? agents[agentIndex] : undefined;
  const project = projects.find((candidate) => candidate.id === projectId);
  const title = project?.name ?? agent?.name ?? "";

  // Projects inherit their agent's configuration and tool catalog; both belong
  // to the agent's own profile, so those tabs only appear there.
  const canConfigure =
    !projectId && !!agent && canEditAgent(agent, me?.is_admin ?? false, me?.id ?? "");

  const tabs: ProfileTab[] = [
    "overview",
    "memory",
    "skills",
    ...(projectId ? [] : (["tools", "channels"] as const)),
    ...(canConfigure ? (["config"] as const) : []),
  ];
  // Derived, never stored: an unavailable tab (a config link opened by someone
  // who may not configure) falls back to the landing tab instead of 403-ing.
  const tab: ProfileTab = tabs.includes(search.tab ?? "overview")
    ? (search.tab ?? "overview")
    : "overview";

  // Only fetch the editor bootstrap (system prompt included) once the viewer
  // passed the permission check *and* actually opened the configuration tab.
  const settings = useQuery(agentSettingsQueryOptions(agentId, canConfigure && tab === "config"));

  const updateSearch = useCallback(
    (patch: Partial<MemorySearch>) => {
      const next: MemorySearch = { ...search, ...patch };
      if (projectId) {
        void navigate({
          to: "/agents/$agentId/projects/$projectId/profile",
          params: { agentId, projectId },
          search: next,
        });
      } else {
        void navigate({
          to: "/agents/$agentId/profile",
          params: { agentId },
          search: next,
        });
      }
    },
    [agentId, projectId, navigate, search],
  );

  const selectTab = useCallback(
    (next: ProfileTab) => {
      // Overview is the canonical bare URL, so it clears the param instead of
      // writing "?tab=overview".
      updateSearch({ tab: next === "overview" ? undefined : next });
    },
    [updateSearch],
  );

  const modelLabel = (value?: string) => {
    if (!value) return "—";
    const match = models.find((m) => `${m.provider}/${m.model}` === value);
    return match ? `${match.provider_name || match.provider}/${match.model}` : value;
  };

  const TAB_LABEL: Record<ProfileTab, string> = {
    overview: t("profile.overview"),
    memory: t("profile.memory"),
    skills: t("profile.skills"),
    tools: t("profile.tools"),
    channels: t("profile.channels"),
    config: t("profile.configuration"),
  };

  return (
    <div className="flex-1 overflow-y-auto">
      <div className="mx-auto flex max-w-3xl flex-col gap-6 p-6 sm:p-8 lg:p-10">
        <header className="flex items-center gap-3">
          <span
            className="grid size-10 place-items-center rounded-full text-sm font-semibold text-primary-foreground"
            style={{ background: getAgentColor(agentId, agentIndex >= 0 ? agentIndex : undefined) }}
          >
            {title[0]?.toUpperCase()}
          </span>
          <div className="flex min-w-0 flex-col gap-0.5">
            <h1 className="truncate text-xl font-semibold">{title}</h1>
            <div className="flex min-w-0 flex-wrap items-center gap-2">
              {agent && (
                <Badge variant={agent.enabled ? "success" : "outline"} size="sm">
                  {agent.enabled ? t("profile.aboutEnabled") : t("profile.aboutDisabled")}
                </Badge>
              )}
              <span className="truncate text-xs text-muted-foreground">
                {modelLabel(agent?.model)}
              </span>
            </div>
          </div>
        </header>

        <Tabs value={tab} onValueChange={(value) => selectTab(value as ProfileTab)}>
          <TabsList variant="underline">
            {tabs.map((value) => (
              <TabsTab key={value} value={value}>
                {TAB_LABEL[value]}
              </TabsTab>
            ))}
          </TabsList>

          <TabsPanel value="overview" className="pt-4">
            <OverviewTab
              agentId={agentId}
              projectId={projectId}
              facts={
                agent && !projectId
                  ? [
                      { label: t("profile.aboutModel"), value: modelLabel(agent.model) },
                      {
                        label: t("profile.aboutScope"),
                        value:
                          agent.scope === "system"
                            ? t("profile.aboutScopeSystem")
                            : t("profile.aboutScopeRestricted"),
                      },
                      {
                        label: t("profile.aboutStatus"),
                        value: agent.enabled
                          ? t("profile.aboutEnabled")
                          : t("profile.aboutDisabled"),
                      },
                      ...(agent.last_active
                        ? [
                            {
                              label: t("profile.aboutLastActive"),
                              value: formatTime(agent.last_active),
                            },
                          ]
                        : []),
                    ]
                  : []
              }
              memoryUpdatedAt={memory?.updated_at}
              canConfigure={canConfigure}
              onSelectTab={selectTab}
            />
          </TabsPanel>

          <TabsPanel value="memory" className="flex flex-col pt-4">
            <SoulSection agentId={agentId} soul={memory?.soul ?? ""} />
            <ProfileSection
              agentId={agentId}
              content={memory?.content ?? ""}
              updatedAt={memory?.updated_at ?? ""}
            />
            <KnowledgeSection
              agentId={agentId}
              state={knowledgeState}
              onStateChange={(state) =>
                // Keep the selected lifecycle view shareable and history-aware.
                updateSearch({ knowledge: state === "removed" ? "removed" : undefined })
              }
            />
            <ConstraintsSection agentId={agentId} />
            <ChangelogSection agentId={agentId} />
          </TabsPanel>

          <TabsPanel value="skills" className="pt-4">
            <ProfileSkillsTab agentId={agentId} projectId={projectId} />
          </TabsPanel>

          {!projectId && (
            <TabsPanel value="tools" className="pt-4">
              <AgentToolsPanel agentId={agentId} canEdit={canConfigure} />
            </TabsPanel>
          )}

          {!projectId && (
            <TabsPanel value="channels" className="pt-4">
              <AgentChannelsPanel agentId={agentId} />
            </TabsPanel>
          )}

          {canConfigure && (
            <TabsPanel value="config" className="pt-4">
              {settings.isPending ? (
                <div className="flex justify-center p-6">
                  <Spinner className="size-5" />
                </div>
              ) : settings.data ? (
                <AgentDetailPanel
                  key={agentId}
                  layout="embedded"
                  hiddenTabs={PROFILE_HIDDEN_CONFIG_TABS}
                  data={settings.data}
                  agentId={agentId}
                  onDeleted={() => void navigate({ to: "/agents" })}
                />
              ) : (
                <div className="flex flex-col items-start gap-2">
                  <p className="text-sm text-muted-foreground">
                    {t("common.error")}
                    {settings.error?.message ? `: ${settings.error.message}` : ""}
                  </p>
                  <Button variant="outline" size="sm" onClick={() => void settings.refetch()}>
                    {t("common.retry")}
                  </Button>
                </div>
              )}
            </TabsPanel>
          )}
        </Tabs>
      </div>
    </div>
  );
}

interface Fact {
  label: string;
  value: string;
}

/**
 * The landing tab: what this agent is, plus one card per sibling tab so the
 * rest of the card is one click away. Every stat comes from a query the page
 * already needs or shares with another page — no stat gets its own API.
 */
function OverviewTab({
  agentId,
  projectId,
  facts,
  memoryUpdatedAt,
  canConfigure,
  onSelectTab,
}: {
  agentId: string;
  projectId?: string;
  facts: Fact[];
  memoryUpdatedAt?: string;
  canConfigure: boolean;
  onSelectTab: (tab: ProfileTab) => void;
}) {
  const { t } = useI18n();
  const { data: skills = [] } = useQuery(agentSkillsOptions(agentId));
  const { data: tools = [] } = useQuery({
    ...agentToolsOptions(agentId),
    enabled: !!agentId && !projectId,
  });
  const { data: goalCounts } = useQuery({
    ...goalCountsOptions(agentId),
    enabled: !!agentId && !projectId,
  });
  const enabledTools = tools.filter((tool) => tool.enabled).length;

  return (
    <div className="flex flex-col gap-8">
      {facts.length > 0 && (
        <section className="flex flex-col gap-3">
          <h2 className="text-lg font-semibold">{t("profile.about")}</h2>
          <dl className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            {facts.map((fact) => (
              <div key={fact.label} className="flex min-w-0 flex-col gap-0.5">
                <dt className="text-xs text-muted-foreground">{fact.label}</dt>
                <dd className="truncate text-sm">{fact.value}</dd>
              </div>
            ))}
          </dl>
        </section>
      )}

      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <SummaryCard
          icon={<Brain size={16} />}
          title={t("profile.memory")}
          detail={
            memoryUpdatedAt
              ? t("profile.memoryUpdated", { time: formatTime(memoryUpdatedAt) })
              : t("profile.memoryEmpty")
          }
          onClick={() => onSelectTab("memory")}
        />
        <SummaryCard
          icon={<Puzzle size={16} />}
          title={t("profile.skills")}
          detail={t("profile.skillCount", { count: skills.length })}
          onClick={() => onSelectTab("skills")}
        />
        {!projectId && (
          <SummaryCard
            icon={<Wrench size={16} />}
            title={t("profile.tools")}
            detail={t("profile.toolCount", { enabled: enabledTools, total: tools.length })}
            onClick={() => onSelectTab("tools")}
          />
        )}
        {!projectId && (
          <SummaryCard
            icon={<MessageCircle size={16} />}
            title={t("profile.channels")}
            detail={t("profile.channelsDesc")}
            onClick={() => onSelectTab("channels")}
          />
        )}
        {!projectId && (
          <Link to="/agents/$agentId/goals" params={{ agentId }} className={SUMMARY_CARD_CLS}>
            <SummaryCardBody
              icon={<ListTodo size={16} />}
              title={t("sidebar.goals")}
              detail={t("profile.goalCount", { count: goalCounts?.active ?? 0 })}
            />
          </Link>
        )}
        {canConfigure && (
          <SummaryCard
            icon={<SlidersHorizontal size={16} />}
            title={t("profile.configuration")}
            detail={t("profile.configurationDesc")}
            onClick={() => onSelectTab("config")}
          />
        )}
      </div>
    </div>
  );
}

// Shared between the tab cards (buttons — a sibling tab is a search-param
// change) and the goals card (a real Link to another route).
const SUMMARY_CARD_CLS =
  "flex min-w-0 cursor-pointer items-center gap-3 rounded-lg border border-border p-3 text-left transition-colors hover:bg-muted/40";

function SummaryCardBody({
  icon,
  title,
  detail,
}: {
  icon: ReactNode;
  title: string;
  detail: string;
}) {
  return (
    <>
      <span className="grid size-8 shrink-0 place-items-center rounded-lg bg-muted text-muted-foreground">
        {icon}
      </span>
      <span className="flex min-w-0 flex-1 flex-col gap-0.5">
        <span className="truncate text-sm font-medium">{title}</span>
        <span className="truncate text-xs text-muted-foreground">{detail}</span>
      </span>
      <ChevronRight size={16} className="shrink-0 text-muted-foreground" />
    </>
  );
}

function SummaryCard({
  icon,
  title,
  detail,
  onClick,
}: {
  icon: ReactNode;
  title: string;
  detail: string;
  onClick: () => void;
}) {
  return (
    <button type="button" onClick={onClick} className={SUMMARY_CARD_CLS}>
      <SummaryCardBody icon={icon} title={title} detail={detail} />
    </button>
  );
}

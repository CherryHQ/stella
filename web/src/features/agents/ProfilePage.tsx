import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { Puzzle } from "lucide-react";
import { useI18n } from "@/lib/i18n";
import { getAgentColor } from "@/lib/agent-colors";
import { agentsQueryOptions, agentSkillsOptions } from "@/lib/queries/agents";
import { agentSettingsQueryOptions } from "@/lib/queries/agent-settings";
import { agentMemoryOptions } from "@/lib/queries/memories";
import { meQueryOptions } from "@/lib/queries/me";
import { modelsQueryOptions } from "@/lib/queries/models";
import { agentProjectsOptions } from "@/lib/queries/projects";
import { formatTime } from "@/lib/time";
import type { AgentConfigTab, MemorySearch } from "@/lib/route-search";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Spinner } from "@/components/ui/spinner";
import { canEditAgent } from "./agent-detail-state";
import { AgentDetailPanel } from "./AgentDetailPanel";
import { SoulSection } from "@/features/memories/SoulSection";
import { ProfileSection } from "@/features/memories/ProfileSection";
import { KnowledgeSection } from "@/features/memories/KnowledgeSection";
import { ConstraintsSection } from "@/features/memories/ConstraintsSection";
import { ChangelogSection } from "@/features/memories/ChangelogSection";

/** How many skills the profile lists before deferring to the skills page. */
const SKILL_PREVIEW_LIMIT = 8;

/** Skills get their own profile section and page, so the editor drops that tab. */
const PROFILE_HIDDEN_CONFIG_TABS = ["skills"] as const;

/**
 * "Who this agent is" on one page: identity, facts, memory, skills — and, for
 * whoever may change it, the agent's full configuration. The configuration
 * section is absent rather than disabled for everyone else: it carries the
 * system prompt, so an unauthorized viewer must not even fetch it.
 *
 * Renders for both the agent and a project inside it; memory is agent-scoped in
 * the API either way, so only the identity header, facts, and links differ.
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
  const configTab: AgentConfigTab = search.ctab ?? "config";

  const { data: agents = [] } = useQuery(agentsQueryOptions);
  const { data: me } = useQuery(meQueryOptions);
  const { data: models = [] } = useQuery(modelsQueryOptions);
  const { data: memory } = useQuery(agentMemoryOptions(agentId));
  const { data: skills = [] } = useQuery(agentSkillsOptions(agentId));
  const { data: projects = [] } = useQuery(agentProjectsOptions(agentId));

  const agentIndex = agents.findIndex((agent) => agent.id === agentId);
  const agent = agentIndex >= 0 ? agents[agentIndex] : undefined;
  const project = projects.find((candidate) => candidate.id === projectId);
  const title = project?.name ?? agent?.name ?? "";

  // Projects inherit their agent's configuration; editing it belongs to the
  // agent's own profile, so the section only appears there.
  const canConfigure =
    !projectId && !!agent && canEditAgent(agent, me?.is_admin ?? false, me?.id ?? "");
  const settings = useQuery(agentSettingsQueryOptions(agentId, canConfigure));

  const modelLabel = (value?: string) => {
    if (!value) return "—";
    const match = models.find((m) => `${m.provider}/${m.model}` === value);
    return match ? `${match.provider_name || match.provider}/${match.model}` : value;
  };

  const facts = agent
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
          value: agent.enabled ? t("profile.aboutEnabled") : t("profile.aboutDisabled"),
        },
        ...(agent.last_active
          ? [{ label: t("profile.aboutLastActive"), value: formatTime(agent.last_active) }]
          : []),
      ]
    : [];

  return (
    <div className="flex-1 overflow-y-auto">
      <div className="mx-auto max-w-3xl p-6 sm:p-8 lg:p-10">
        <header className="mb-8 flex items-center gap-3">
          <span
            className="grid size-10 place-items-center rounded-full text-sm font-semibold text-primary-foreground"
            style={{ background: getAgentColor(agentId, agentIndex >= 0 ? agentIndex : undefined) }}
          >
            {title[0]?.toUpperCase()}
          </span>
          <div className="flex min-w-0 flex-col">
            <h1 className="truncate text-xl font-semibold">{title}</h1>
            <p className="text-xs text-muted-foreground">{t("profile.title")}</p>
          </div>
        </header>

        {!projectId && facts.length > 0 && (
          <section className="mb-10 flex flex-col gap-3">
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

        {canConfigure && (
          <section className="mb-10 flex flex-col gap-3">
            <div className="flex flex-col gap-1">
              <h2 className="text-lg font-semibold">{t("profile.configuration")}</h2>
              <p className="text-sm text-muted-foreground">{t("profile.configurationDesc")}</p>
            </div>
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
                activeTab={configTab}
                onTabChange={(tab) => {
                  void navigate({
                    to: "/agents/$agentId/profile",
                    params: { agentId },
                    search: (prev: MemorySearch) => ({ ...prev, ctab: tab as AgentConfigTab }),
                  });
                }}
                onDeleted={() => void navigate({ to: "/agents" })}
              />
            ) : (
              <p className="text-sm text-muted-foreground">{t("common.error")}</p>
            )}
          </section>
        )}

        <SoulSection agentId={agentId} soul={memory?.soul ?? ""} />
        <ProfileSection
          agentId={agentId}
          content={memory?.content ?? ""}
          updatedAt={memory?.updated_at ?? ""}
        />
        <KnowledgeSection
          agentId={agentId}
          state={knowledgeState}
          onStateChange={(state) => {
            // Keep the selected lifecycle view shareable and browser-history aware.
            const nextSearch = {
              knowledge: state === "removed" ? ("removed" as const) : undefined,
            };
            if (projectId) {
              void navigate({
                to: "/agents/$agentId/projects/$projectId/profile",
                params: { agentId, projectId },
                search: nextSearch,
              });
            } else {
              void navigate({
                to: "/agents/$agentId/profile",
                params: { agentId },
                search: (prev: MemorySearch) => ({ ...prev, ...nextSearch }),
              });
            }
          }}
        />
        <ConstraintsSection agentId={agentId} />

        <section className="mt-10 flex flex-col gap-3">
          <div className="flex items-center justify-between gap-2">
            <h2 className="text-lg font-semibold">{t("profile.skills")}</h2>
            <Button
              variant="outline"
              size="sm"
              render={
                projectId ? (
                  <Link
                    to="/agents/$agentId/projects/$projectId/skills"
                    params={{ agentId, projectId }}
                  />
                ) : (
                  <Link to="/agents/$agentId/skills" params={{ agentId }} />
                )
              }
            >
              <Puzzle />
              {t("profile.manageSkills")}
            </Button>
          </div>
          {skills.length === 0 ? (
            <p className="text-sm text-muted-foreground">{t("profile.noSkills")}</p>
          ) : (
            <ul className="flex flex-col gap-2">
              {skills.slice(0, SKILL_PREVIEW_LIMIT).map((skill) => (
                <li
                  key={skill.id}
                  className="flex min-w-0 items-center gap-3 rounded-lg border border-border p-3"
                >
                  <div className="flex min-w-0 flex-1 flex-col gap-0.5">
                    <span className="truncate text-sm font-medium">{skill.name}</span>
                    {skill.description && (
                      <span className="truncate text-xs text-muted-foreground">
                        {skill.description}
                      </span>
                    )}
                  </div>
                  <Badge variant="secondary" size="sm">
                    {skill.scope}
                  </Badge>
                </li>
              ))}
            </ul>
          )}
        </section>

        <ChangelogSection agentId={agentId} />
      </div>
    </div>
  );
}

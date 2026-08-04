import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { Puzzle } from "lucide-react";
import { useI18n } from "@/lib/i18n";
import { getAgentColor } from "@/lib/agent-colors";
import { agentsQueryOptions, agentSkillsOptions } from "@/lib/queries/agents";
import { agentMemoryOptions } from "@/lib/queries/memories";
import { agentProjectsOptions } from "@/lib/queries/projects";
import type { MemorySearch } from "@/lib/route-search";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { SoulSection } from "@/features/memories/SoulSection";
import { ProfileSection } from "@/features/memories/ProfileSection";
import { KnowledgeSection } from "@/features/memories/KnowledgeSection";
import { ConstraintsSection } from "@/features/memories/ConstraintsSection";
import { ChangelogSection } from "@/features/memories/ChangelogSection";

/** How many skills the profile lists before deferring to the skills page. */
const SKILL_PREVIEW_LIMIT = 8;

/**
 * "Who this agent is" on one page: identity, memory, and skills. Replaces the
 * old memory/skills facet tabs — the conversation is the default view now, and
 * everything about the agent itself lives one click behind its name.
 *
 * Renders for both the agent and a project inside it; memory is agent-scoped in
 * the API either way, so only the identity header and links differ.
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
  const { data: memory } = useQuery(agentMemoryOptions(agentId));
  const { data: skills = [] } = useQuery(agentSkillsOptions(agentId));
  const { data: projects = [] } = useQuery(agentProjectsOptions(agentId));

  const agentIndex = agents.findIndex((agent) => agent.id === agentId);
  const agent = agentIndex >= 0 ? agents[agentIndex] : undefined;
  const project = projects.find((candidate) => candidate.id === projectId);
  const title = project?.name ?? agent?.name ?? "";

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
                search: nextSearch,
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

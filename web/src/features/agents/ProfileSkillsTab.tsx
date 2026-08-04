import { useMemo } from "react";
import { Link } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { Lock, Puzzle, SquarePen } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipPopup, TooltipTrigger } from "@/components/ui/tooltip";
import { agentSkillsOptions } from "@/lib/queries/agents";
import { meQueryOptions } from "@/lib/queries/me";
import {
  isSkillReadOnly,
  SCOPE_DESC_KEY,
  SCOPE_LABEL_KEY,
  type SkillScope,
} from "@/lib/skill-scope";
import { useI18n } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/messages";
import type { Skill } from "@/lib/types";

type GroupKey = "mine" | "project" | "system";

const GROUP_LABEL_KEY: Record<GroupKey, MessageKey> = {
  mine: "profile.skillsGroupMine",
  project: "profile.skillsGroupProject",
  system: "profile.skillsGroupSystem",
};

// Every scope lands in exactly one group; anything the client doesn't recognise
// falls back to "system" so a new backend scope can never silently vanish.
function groupOf(scope: string): GroupKey {
  if (scope === "user" || scope === "user_agent") return "mine";
  if (scope === "project") return "project";
  return "system";
}

const GROUP_ORDER: GroupKey[] = ["mine", "project", "system"];

/**
 * Every skill this agent can reach, grouped by who owns it. The trailing action
 * mirrors the backend write rules (`isSkillReadOnly`) so the list never offers
 * an edit that would 403 — read-only rows explain themselves through the
 * scope's own description instead.
 */
export function ProfileSkillsTab({ agentId, projectId }: { agentId: string; projectId?: string }) {
  const { t } = useI18n();
  const { data: me } = useQuery(meQueryOptions);
  const { data: skills = [], isPending } = useQuery(agentSkillsOptions(agentId));
  const isAdmin = me?.is_admin ?? false;

  const groups = useMemo(() => {
    const buckets: Record<GroupKey, Skill[]> = { mine: [], project: [], system: [] };
    for (const skill of skills) buckets[groupOf(skill.scope)].push(skill);
    return buckets;
  }, [skills]);

  return (
    <div className="flex flex-col gap-6">
      <div className="flex items-center justify-between gap-2">
        <p className="text-sm text-muted-foreground">{t("profile.skillsDesc")}</p>
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
          {t("profile.manageAllSkills")}
        </Button>
      </div>

      {isPending ? (
        <p className="text-sm text-muted-foreground">{t("common.loading")}</p>
      ) : skills.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t("profile.noSkills")}</p>
      ) : (
        GROUP_ORDER.filter((key) => groups[key].length > 0).map((key) => (
          <section key={key} className="flex flex-col gap-2">
            <h3 className="flex items-center gap-2 text-sm font-semibold">
              {t(GROUP_LABEL_KEY[key])}
              <span className="text-xs font-normal text-muted-foreground">
                {groups[key].length}
              </span>
            </h3>
            <ul className="flex flex-col gap-2">
              {groups[key].map((skill) => (
                <SkillRow
                  key={`${skill.scope}:${skill.id}`}
                  skill={skill}
                  agentId={agentId}
                  projectId={projectId}
                  readOnly={isSkillReadOnly(skill.scope, isAdmin)}
                />
              ))}
            </ul>
          </section>
        ))
      )}
    </div>
  );
}

function SkillRow({
  skill,
  agentId,
  projectId,
  readOnly,
}: {
  skill: Skill;
  agentId: string;
  projectId?: string;
  readOnly: boolean;
}) {
  const { t } = useI18n();
  const scope = skill.scope as SkillScope;
  const labelKey = SCOPE_LABEL_KEY[scope];
  const descKey = SCOPE_DESC_KEY[scope];
  const sel = `${skill.scope}:${skill.id}`;

  return (
    <li className="flex min-w-0 items-center gap-3 rounded-lg border border-border p-3">
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <span className="truncate text-sm font-medium">{skill.name}</span>
        {skill.description && (
          <span className="truncate text-xs text-muted-foreground">{skill.description}</span>
        )}
      </div>
      {labelKey &&
        (descKey ? (
          <Tooltip>
            <TooltipTrigger render={<Badge variant="secondary" size="sm" />}>
              {t(labelKey)}
            </TooltipTrigger>
            <TooltipPopup side="top" className="max-w-56">
              {t(descKey)}
            </TooltipPopup>
          </Tooltip>
        ) : (
          <Badge variant="secondary" size="sm">
            {t(labelKey)}
          </Badge>
        ))}
      {readOnly ? (
        <Tooltip>
          <TooltipTrigger
            render={
              // Focusable on purpose: the lock is the only explanation a
              // read-only row carries, so it must be reachable by keyboard.
              <span
                tabIndex={0}
                role="note"
                className="flex size-8 shrink-0 items-center justify-center text-muted-foreground"
                aria-label={t("profile.skillReadOnly")}
              />
            }
          >
            <Lock size={16} />
          </TooltipTrigger>
          <TooltipPopup side="top" className="max-w-56">
            {descKey ? t(descKey) : t("profile.skillReadOnly")}
          </TooltipPopup>
        </Tooltip>
      ) : (
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label={t("profile.editSkill")}
          render={
            projectId ? (
              <Link
                to="/agents/$agentId/projects/$projectId/skills"
                params={{ agentId, projectId }}
                search={{ sel }}
              />
            ) : (
              <Link to="/agents/$agentId/skills" params={{ agentId }} search={{ sel }} />
            )
          }
        >
          <SquarePen />
        </Button>
      )}
    </li>
  );
}

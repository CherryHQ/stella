import { useQuery } from "@tanstack/react-query";
import { Sheet, SheetPopup } from "@/components/ui/sheet";
import { SkillInspectorPanel, type SkillNotify } from "@/features/skills/SkillInspectorPanel";
import { projectSessionsQueryOptions } from "@/lib/queries/sessions";
import type { SkillScope } from "@/lib/skill-scope";
import type { Skill } from "@/lib/types";

/**
 * The profile's skill inspector: one of the agent's skills opened from its row.
 *
 * The sheet itself only resolves the project session a filesystem-backed skill
 * needs and hosts {@link SkillInspectorPanel}, which owns everything a skill can
 * be — read, edited, upgraded or deleted — so the profile is the only surface a
 * skill is managed from.
 */
export function ProfileSkillDetailSheet({
  agentId,
  projectId,
  skill,
  notify,
  onClose,
}: {
  agentId: string;
  projectId?: string;
  skill: Skill | null;
  notify: SkillNotify;
  onClose: () => void;
}) {
  const scope = skill?.scope as SkillScope | undefined;

  // Project skills are filesystem-backed: the API resolves their authorized
  // project root from a session that belongs to the current project.
  const projectSessions = useQuery({
    ...projectSessionsQueryOptions(agentId, projectId ?? ""),
    enabled: !!projectId && scope === "project",
  });
  const sessionId = projectId
    ? (projectSessions.data?.find((session) => session.kind === "main")?.id ??
      projectSessions.data?.[0]?.id)
    : undefined;

  return (
    <Sheet open={!!skill} onOpenChange={(open) => !open && onClose()}>
      <SheetPopup
        side="right"
        showCloseButton={false}
        className="w-full sm:w-[560px] sm:max-w-[560px]"
      >
        {skill && (
          // Remount per skill so the panel's draft fields start from the new one.
          <SkillInspectorPanel
            key={`${skill.scope}:${skill.id}`}
            agentId={agentId}
            sessionId={sessionId}
            skill={skill}
            notify={notify}
            onClose={onClose}
          />
        )}
      </SheetPopup>
    </Sheet>
  );
}

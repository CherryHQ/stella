import { useEffect, useState } from "react";
import { Link } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { ChevronLeft, ChevronRight, FileText, SquarePen, X } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Sheet, SheetPopup } from "@/components/ui/sheet";
import { Spinner } from "@/components/ui/spinner";
import { Tooltip, TooltipPopup, TooltipTrigger } from "@/components/ui/tooltip";
import { SkillFilePreview } from "@/features/sessions/SkillFilePreview";
import { getAgentSkill, getAgentSkillFile } from "@/lib/api-client/sdk.gen";
import { projectSessionsQueryOptions } from "@/lib/queries/sessions";
import { meQueryOptions } from "@/lib/queries/me";
import {
  isSkillReadOnly,
  SCOPE_DESC_KEY,
  SCOPE_LABEL_KEY,
  type SkillScope,
} from "@/lib/skill-scope";
import { useI18n } from "@/lib/i18n";
import type { Skill } from "@/lib/types";

/**
 * Read-only detail for one of the agent's skills, opened from its profile row.
 *
 * The profile owns *which* skills this agent has and how they behave; authoring
 * their content stays on the skills page, so this sheet previews files and hands
 * off editing through a deep link rather than duplicating the editor.
 *
 * Query keys mirror the skills page exactly so both surfaces share one cache
 * entry per skill and per file.
 */
export function ProfileSkillDetailSheet({
  agentId,
  projectId,
  skill,
  onClose,
}: {
  agentId: string;
  projectId?: string;
  skill: Skill | null;
  onClose: () => void;
}) {
  const { t } = useI18n();
  const [viewer, setViewer] = useState<string | null>(null);
  const { data: me } = useQuery(meQueryOptions);
  const scope = skill?.scope as SkillScope | undefined;
  // Same rule the list uses: a scope the backend would reject never gets an
  // edit affordance, read-only or not.
  const canEdit = !!skill && !isSkillReadOnly(skill.scope, me?.is_admin ?? false);

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
  const scoped = scope === "project";
  const ready = !scoped || !!sessionId;

  const detail = useQuery({
    queryKey: ["agent-skill", agentId, sessionId ?? "", skill?.scope ?? "", skill?.id ?? ""],
    queryFn: async () =>
      (
        await getAgentSkill({
          path: { id: agentId, skillId: skill!.id },
          query: {
            scope: skill!.scope as SkillScope,
            ...(sessionId ? { session_id: sessionId } : {}),
          },
          throwOnError: true,
        })
      ).data as Skill,
    enabled: !!skill && ready,
  });

  // A fresh selection always starts on the file list, never on the previous
  // skill's open file.
  useEffect(() => setViewer(null), [skill]);

  const files = detail.data?.files ?? skill?.files ?? [];
  const sel = skill ? `${skill.scope}:${skill.id}` : "";
  const editLink = projectId ? (
    <Link
      to="/agents/$agentId/projects/$projectId/skills"
      params={{ agentId, projectId }}
      search={{ sel }}
    />
  ) : (
    <Link to="/agents/$agentId/skills" params={{ agentId }} search={{ sel }} />
  );

  return (
    <Sheet open={!!skill} onOpenChange={(open) => !open && onClose()}>
      <SheetPopup
        side="right"
        showCloseButton={false}
        className="w-full sm:w-[560px] sm:max-w-[560px]"
      >
        {skill && (
          <div className="flex h-full min-h-0 flex-col">
            <div className="flex items-start gap-3 border-b border-border p-5">
              <div className="min-w-0 flex-1">
                <h2 className="truncate font-mono text-base font-semibold">{skill.name}</h2>
                {scope && SCOPE_LABEL_KEY[scope] && (
                  <div className="mt-2">
                    <Tooltip>
                      <TooltipTrigger render={<Badge variant="secondary" size="sm" />}>
                        {t(SCOPE_LABEL_KEY[scope])}
                      </TooltipTrigger>
                      <TooltipPopup side="bottom" className="max-w-56">
                        {t(SCOPE_DESC_KEY[scope])}
                      </TooltipPopup>
                    </Tooltip>
                  </div>
                )}
              </div>
              <Button
                size="icon-sm"
                variant="ghost"
                aria-label={t("common.close")}
                onClick={onClose}
              >
                <X />
              </Button>
            </div>

            {viewer ? (
              <SkillFileBody
                agentId={agentId}
                sessionId={sessionId}
                skill={skill}
                path={viewer}
                onBack={() => setViewer(null)}
              />
            ) : (
              <>
                <div className="min-h-0 flex-1 space-y-5 overflow-y-auto p-5">
                  {skill.description && (
                    <p className="text-sm text-muted-foreground">{skill.description}</p>
                  )}
                  <section className="space-y-2">
                    <p className="text-xs font-semibold text-muted-foreground">
                      {t("profile.skillFiles")}
                    </p>
                    {!ready ? (
                      <p className="text-sm text-muted-foreground">
                        {projectSessions.isPending
                          ? t("common.loading")
                          : t("profile.skillFilesUnavailable")}
                      </p>
                    ) : detail.isPending && files.length === 0 ? (
                      <div className="flex justify-center py-6">
                        <Spinner className="size-5" />
                      </div>
                    ) : files.length === 0 ? (
                      <p className="text-sm text-muted-foreground">{t("profile.skillNoFiles")}</p>
                    ) : (
                      <div className="divide-y divide-border overflow-hidden rounded-lg border border-border">
                        {files.map((file) => (
                          <button
                            key={file}
                            type="button"
                            onClick={() => setViewer(file)}
                            className="flex w-full items-center gap-2 p-3 text-left font-mono text-sm hover:bg-muted"
                          >
                            <FileText className="size-4 shrink-0 text-muted-foreground" />
                            <span className="truncate">{file}</span>
                            <ChevronRight className="ml-auto size-4 shrink-0 text-muted-foreground" />
                          </button>
                        ))}
                      </div>
                    )}
                  </section>
                </div>
                {canEdit && (
                  <div className="flex shrink-0 items-center justify-end border-t border-border p-3">
                    <Button variant="ghost" size="sm" render={editLink}>
                      <SquarePen />
                      {t("profile.skillEditContent")}
                    </Button>
                  </div>
                )}
              </>
            )}
          </div>
        )}
      </SheetPopup>
    </Sheet>
  );
}

/**
 * Drilling into a file swaps the sheet body rather than stacking a dialog over
 * it — same idiom the skills page uses for its drawer.
 */
function SkillFileBody({
  agentId,
  sessionId,
  skill,
  path,
  onBack,
}: {
  agentId: string;
  sessionId?: string;
  skill: Skill;
  path: string;
  onBack: () => void;
}) {
  const { t } = useI18n();
  const file = useQuery({
    queryKey: ["agent-skill-file", agentId, sessionId ?? "", skill.scope, skill.id, path],
    queryFn: async () =>
      (
        await getAgentSkillFile({
          path: { id: agentId, skillId: skill.id },
          query: {
            scope: skill.scope as SkillScope,
            path,
            ...(sessionId ? { session_id: sessionId } : {}),
          },
          throwOnError: true,
        })
      ).data,
    enabled: skill.scope !== "project" || !!sessionId,
  });

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex shrink-0 items-center gap-2 border-b border-border px-3 py-2">
        <Button variant="ghost" size="icon-sm" aria-label={t("common.back")} onClick={onBack}>
          <ChevronLeft />
        </Button>
        <span className="truncate font-mono text-xs text-muted-foreground">{path}</span>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto p-5">
        {file.isPending ? (
          <div className="flex justify-center py-6">
            <Spinner className="size-5" />
          </div>
        ) : (
          <SkillFilePreview
            path={path}
            content={file.data?.content ?? ""}
            encoding={file.data?.encoding}
            emptyText={t("profile.skillNoFiles")}
          />
        )}
      </div>
    </div>
  );
}

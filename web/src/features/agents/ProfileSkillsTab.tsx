import { useMemo, useState } from "react";
import { Link } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Lock, Plus, Puzzle, SquarePen, Trash2 } from "lucide-react";
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
import { Switch } from "@/components/ui/switch";
import { Tooltip, TooltipPopup, TooltipTrigger } from "@/components/ui/tooltip";
import { deleteAgentSkill, updateAgentSkill } from "@/lib/api-client/sdk.gen";
import { agentSkillsOptions } from "@/lib/queries/agents";
import { meQueryOptions } from "@/lib/queries/me";
import {
  isSkillReadOnly,
  SCOPE_DESC_KEY,
  SCOPE_LABEL_KEY,
  type SkillScope,
} from "@/lib/skill-scope";
import { ToastContainer, useToast } from "@/hooks/use-toast";
import { useI18n } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/messages";
import type { Skill } from "@/lib/types";
import { ProfilePanelSection, ProfileSectionMessage } from "./ProfilePanelSection";

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
 * Every skill this agent can reach, grouped by who owns it, and manageable in
 * place: the row's switch is the model-invocation gate the skill actually
 * carries, and the trailing actions mirror the backend write rules
 * (`isSkillReadOnly`) so the list never offers an edit that would 403 —
 * read-only rows explain themselves through the scope's own description
 * instead. Installing a skill stays on the full skills page, which owns the
 * market and upload surfaces.
 */
export function ProfileSkillsTab({ agentId, projectId }: { agentId: string; projectId?: string }) {
  const { t } = useI18n();
  const { toasts, showToast } = useToast();
  const queryClient = useQueryClient();
  const { data: me } = useQuery(meQueryOptions);
  const { data: skills = [], isPending } = useQuery(agentSkillsOptions(agentId));
  const isAdmin = me?.is_admin ?? false;

  const invalidate = () =>
    queryClient.invalidateQueries({ queryKey: ["agent-skills", agentId] }).then(() => {
      void queryClient.invalidateQueries({ queryKey: ["agent-skills-management", agentId] });
    });

  // The skill list has no `enabled` flag: what a row toggles is
  // `disable_model_invocation`, i.e. whether the model may call the skill.
  const toggle = useMutation({
    mutationFn: ({ skill, invocable }: { skill: Skill; invocable: boolean }) =>
      updateAgentSkill({
        path: { id: agentId, skillId: skill.id },
        query: { scope: skill.scope as SkillScope },
        body: { disable_model_invocation: !invocable },
        throwOnError: true,
      }),
    onSuccess: invalidate,
    onError: () => showToast(t("profile.skillUpdateFailed"), "error"),
  });

  const remove = useMutation({
    mutationFn: (skill: Skill) =>
      deleteAgentSkill({
        path: { id: agentId, skillId: skill.id },
        query: { scope: skill.scope as SkillScope },
        throwOnError: true,
      }),
    onSuccess: invalidate,
    onError: () => showToast(t("profile.skillDeleteFailed"), "error"),
  });

  const groups = useMemo(() => {
    const buckets: Record<GroupKey, Skill[]> = { mine: [], project: [], system: [] };
    for (const skill of skills) buckets[groupOf(skill.scope)].push(skill);
    return buckets;
  }, [skills]);

  // The skills page owns install (market + upload); both entry points here just
  // point at it rather than rebuilding that surface inside a tab.
  const skillsPageLink = () =>
    projectId ? (
      <Link to="/agents/$agentId/projects/$projectId/skills" params={{ agentId, projectId }} />
    ) : (
      <Link to="/agents/$agentId/skills" params={{ agentId }} />
    );

  return (
    <div className="flex flex-col gap-6">
      <ToastContainer messages={toasts} />
      <div className="flex items-center justify-between gap-2">
        <p className="min-w-0 text-sm text-muted-foreground">{t("profile.skillsDesc")}</p>
        <div className="flex shrink-0 items-center gap-1">
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label={t("profile.addSkill")}
            title={t("profile.addSkill")}
            render={skillsPageLink()}
          >
            <Plus />
          </Button>
          <Button variant="ghost" size="sm" render={skillsPageLink()}>
            <Puzzle />
            {t("profile.manageAllSkills")}
          </Button>
        </div>
      </div>

      {isPending ? (
        <ProfileSectionMessage>{t("common.loading")}</ProfileSectionMessage>
      ) : skills.length === 0 ? (
        <ProfileSectionMessage>{t("profile.noSkills")}</ProfileSectionMessage>
      ) : (
        GROUP_ORDER.filter((key) => groups[key].length > 0).map((key) => (
          <ProfilePanelSection key={key} title={t(GROUP_LABEL_KEY[key])} count={groups[key].length}>
            <ul className="flex flex-col gap-2">
              {groups[key].map((skill) => (
                <SkillRow
                  key={`${skill.scope}:${skill.id}`}
                  skill={skill}
                  agentId={agentId}
                  projectId={projectId}
                  readOnly={isSkillReadOnly(skill.scope, isAdmin)}
                  busy={
                    (toggle.isPending && toggle.variables?.skill.id === skill.id) ||
                    (remove.isPending && remove.variables?.id === skill.id)
                  }
                  onToggle={(invocable) => toggle.mutate({ skill, invocable })}
                  onDelete={() => remove.mutate(skill)}
                />
              ))}
            </ul>
          </ProfilePanelSection>
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
  busy,
  onToggle,
  onDelete,
}: {
  skill: Skill;
  agentId: string;
  projectId?: string;
  readOnly: boolean;
  busy: boolean;
  onToggle: (invocable: boolean) => void;
  onDelete: () => void;
}) {
  const { t } = useI18n();
  const [confirmOpen, setConfirmOpen] = useState(false);
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
      {/* Read-only scopes still show their state — a filesystem skill carries the
          flag in its frontmatter — but the switch cannot be moved from here. */}
      <Switch
        checked={!skill.disable_model_invocation}
        disabled={readOnly || busy}
        aria-label={t("profile.skillModelInvocation")}
        title={t("profile.skillModelInvocation")}
        onCheckedChange={(checked) => onToggle(!!checked)}
      />
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
        <>
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
          <Button
            variant="ghost"
            size="icon-sm"
            disabled={busy}
            aria-label={t("profile.deleteSkill")}
            onClick={() => setConfirmOpen(true)}
          >
            <Trash2 />
          </Button>
          <AlertDialog open={confirmOpen} onOpenChange={setConfirmOpen}>
            <AlertDialogPopup>
              <AlertDialogHeader>
                <AlertDialogTitle>{t("profile.deleteSkillConfirm")}</AlertDialogTitle>
                <AlertDialogDescription>
                  {t("profile.deleteSkillConfirmDesc", { name: skill.name })}
                </AlertDialogDescription>
              </AlertDialogHeader>
              <AlertDialogFooter>
                <AlertDialogClose render={<Button variant="ghost" />}>
                  {t("common.cancel")}
                </AlertDialogClose>
                <Button
                  variant="destructive"
                  onClick={() => {
                    setConfirmOpen(false);
                    onDelete();
                  }}
                >
                  {t("profile.deleteSkill")}
                </Button>
              </AlertDialogFooter>
            </AlertDialogPopup>
          </AlertDialog>
        </>
      )}
    </li>
  );
}

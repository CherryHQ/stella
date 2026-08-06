import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Lock, Plus, Trash2 } from "lucide-react";
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
import { SkillInstallSheet } from "@/features/skills/SkillInstallSheet";
import { deleteAgentSkill, updateAgentSkill } from "@/lib/api-client/sdk.gen";
import { agentSkillsOptions } from "@/lib/queries/agents";
import { meQueryOptions } from "@/lib/queries/me";
import {
  isSkillReadOnly,
  SCOPE_DESC_KEY,
  SCOPE_LABEL_KEY,
  type SkillScope,
} from "@/lib/skill-scope";
import { useToast } from "@/hooks/use-toast";
import { useI18n } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/messages";
import type { Skill } from "@/lib/types";
import { ProfilePanelSection, ProfileSectionMessage } from "./ProfilePanelSection";
import { ProfileSkillDetailSheet } from "./ProfileSkillDetailSheet";

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
 * Every skill this agent can reach, grouped by who owns it, and fully managed
 * here: the row's switch is the model-invocation gate the skill actually
 * carries, the trailing actions mirror the backend write rules
 * (`isSkillReadOnly`) so the list never offers an edit that would 403, the
 * detail sheet edits a skill's metadata and file contents, and the install
 * sheet is the single place a new skill is added.
 *
 * Ceiling: the list is the non-paginated skill set with no search box, so a very
 * large install count renders one long page. Upgrade trigger — if agents
 * routinely carry more skills than fit a scroll, add client-side filtering over
 * this list, and only then reach for the paginated query.
 */
export function ProfileSkillsTab({ agentId, projectId }: { agentId: string; projectId?: string }) {
  const { t } = useI18n();
  const { showToast } = useToast();
  const queryClient = useQueryClient();
  const [installOpen, setInstallOpen] = useState(false);
  const [selected, setSelected] = useState<Skill | null>(null);
  const { data: me } = useQuery(meQueryOptions);
  const { data: skills = [], isPending } = useQuery(agentSkillsOptions(agentId));
  const isAdmin = me?.is_admin ?? false;

  const invalidate = () => queryClient.invalidateQueries({ queryKey: ["agent-skills", agentId] });

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

  return (
    <div className="flex flex-col gap-6">
      <div className="flex items-center justify-between gap-2">
        <p className="min-w-0 text-sm text-muted-foreground">{t("profile.skillsDesc")}</p>
        <Button
          variant="ghost"
          size="icon-sm"
          className="shrink-0"
          aria-label={t("profile.addSkill")}
          title={t("profile.addSkill")}
          onClick={() => setInstallOpen(true)}
        >
          <Plus />
        </Button>
      </div>

      <SkillInstallSheet
        agentId={agentId}
        open={installOpen}
        onOpenChange={setInstallOpen}
        notify={showToast}
      />

      <ProfileSkillDetailSheet
        agentId={agentId}
        projectId={projectId}
        skill={selected}
        notify={showToast}
        onClose={() => setSelected(null)}
      />

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
                  readOnly={isSkillReadOnly(skill.scope, isAdmin)}
                  busy={
                    (toggle.isPending && toggle.variables?.skill.id === skill.id) ||
                    (remove.isPending && remove.variables?.id === skill.id)
                  }
                  onOpen={() => setSelected(skill)}
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
  readOnly,
  busy,
  onOpen,
  onToggle,
  onDelete,
}: {
  skill: Skill;
  readOnly: boolean;
  busy: boolean;
  onOpen: () => void;
  onToggle: (invocable: boolean) => void;
  onDelete: () => void;
}) {
  const { t } = useI18n();
  const [confirmOpen, setConfirmOpen] = useState(false);
  const scope = skill.scope as SkillScope;
  const labelKey = SCOPE_LABEL_KEY[scope];
  const descKey = SCOPE_DESC_KEY[scope];

  return (
    <li className="flex min-w-0 items-center gap-3 rounded-lg border border-border p-3">
      {/* Only the text block opens the detail sheet: the switch, the delete
          button and the scope tooltip stay siblings, so no interactive control
          is ever nested inside another one. */}
      <button
        type="button"
        onClick={onOpen}
        aria-label={t("profile.openSkill")}
        className="flex min-w-0 flex-1 cursor-pointer flex-col gap-0.5 text-left"
      >
        <span className="truncate text-sm font-medium">{skill.name}</span>
        {skill.description && (
          <span className="truncate text-xs text-muted-foreground">{skill.description}</span>
        )}
      </button>
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

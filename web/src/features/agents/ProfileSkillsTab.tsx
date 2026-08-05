import { useMemo, useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Lock, Plus, Store, Trash2, Upload, Wand2 } from "lucide-react";
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
import {
  Dialog,
  DialogClose,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogPopup,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/menu";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Tooltip, TooltipPopup, TooltipTrigger } from "@/components/ui/tooltip";
import { deleteAgentSkill, updateAgentSkill, uploadAgentSkill } from "@/lib/api-client/sdk.gen";
import { apiErrorMessage } from "@/lib/api-error";
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
 * Every skill this agent can reach, grouped by who owns it, and manageable in
 * place: the row's switch is the model-invocation gate the skill actually
 * carries, and the trailing actions mirror the backend write rules
 * (`isSkillReadOnly`) so the list never offers an edit that would 403 —
 * read-only rows explain themselves through the scope's own description
 * instead. Adding a skill is an action reachable from here — the market and the
 * content editor stay on the skills page, which this tab links into.
 */
export function ProfileSkillsTab({ agentId, projectId }: { agentId: string; projectId?: string }) {
  const { t } = useI18n();
  const { toasts, showToast } = useToast();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const [uploadOpen, setUploadOpen] = useState(false);
  const [selected, setSelected] = useState<Skill | null>(null);
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

  const upload = useMutation({
    mutationFn: (file: File) =>
      // Uploads land in the user's own profile scope: the agent-wide and
      // admin scopes need the picker the skills page already owns.
      uploadAgentSkill({
        path: { id: agentId },
        body: { file, scope: "user_agent" },
        throwOnError: true,
      }),
    onSuccess: () => {
      setUploadOpen(false);
      showToast(t("profile.skillUploaded"), "success");
      void invalidate();
    },
    onError: (error) => showToast(apiErrorMessage(error, t("profile.skillUploadFailed")), "error"),
  });

  const groups = useMemo(() => {
    const buckets: Record<GroupKey, Skill[]> = { mine: [], project: [], system: [] };
    for (const skill of skills) buckets[groupOf(skill.scope)].push(skill);
    return buckets;
  }, [skills]);

  // Browsing surfaces (create-from-source, market) stay on the skills page,
  // which drives both from its `new` / `source` search params.
  const goToSkillsPage = (search: { new?: boolean; source?: "market" }) =>
    projectId
      ? navigate({
          to: "/agents/$agentId/projects/$projectId/skills",
          params: { agentId, projectId },
          search,
        })
      : navigate({ to: "/agents/$agentId/skills", params: { agentId }, search });

  return (
    <div className="flex flex-col gap-6">
      <ToastContainer messages={toasts} />
      <div className="flex items-center justify-between gap-2">
        <p className="min-w-0 text-sm text-muted-foreground">{t("profile.skillsDesc")}</p>
        <DropdownMenu>
          <DropdownMenuTrigger
            render={
              <Button
                variant="ghost"
                size="icon-sm"
                className="shrink-0"
                aria-label={t("profile.addSkill")}
                title={t("profile.addSkill")}
              />
            }
          >
            <Plus />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" sideOffset={6}>
            <DropdownMenuItem onClick={() => void goToSkillsPage({ new: true })}>
              <Wand2 />
              {t("profile.skillMenuNew")}
            </DropdownMenuItem>
            <DropdownMenuItem onClick={() => setUploadOpen(true)}>
              <Upload />
              {t("profile.skillMenuUpload")}
            </DropdownMenuItem>
            <DropdownMenuItem onClick={() => void goToSkillsPage({ source: "market" })}>
              <Store />
              {t("profile.skillMenuMarket")}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>

      <UploadSkillDialog
        open={uploadOpen}
        busy={upload.isPending}
        onOpenChange={setUploadOpen}
        onUpload={(file) => upload.mutate(file)}
      />

      <ProfileSkillDetailSheet
        agentId={agentId}
        projectId={projectId}
        skill={selected}
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

/**
 * Upload is the one install path with nothing to browse, so it stays in the
 * profile instead of bouncing the user to the skills page for a file picker.
 */
function UploadSkillDialog({
  open,
  busy,
  onOpenChange,
  onUpload,
}: {
  open: boolean;
  busy: boolean;
  onOpenChange: (open: boolean) => void;
  onUpload: (file: File) => void;
}) {
  const { t } = useI18n();
  const [file, setFile] = useState<File | null>(null);

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next) setFile(null);
        onOpenChange(next);
      }}
    >
      <DialogPopup className="max-w-md">
        <DialogHeader>
          <DialogTitle>{t("profile.uploadSkillTitle")}</DialogTitle>
          <DialogDescription>{t("profile.uploadSkillDesc")}</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-2 px-6">
          <Input
            nativeInput
            type="file"
            accept=".zip"
            aria-label={t("profile.uploadSkillTitle")}
            onChange={(e) => setFile((e.target as HTMLInputElement).files?.[0] ?? null)}
          />
          <p className="text-xs text-muted-foreground">{t("profile.uploadSkillTarget")}</p>
        </div>
        <DialogFooter>
          <DialogClose render={<Button variant="ghost" />}>{t("common.cancel")}</DialogClose>
          <Button disabled={!file || busy} loading={busy} onClick={() => file && onUpload(file)}>
            <Upload />
            {t("profile.uploadSkillAction")}
          </Button>
        </DialogFooter>
      </DialogPopup>
    </Dialog>
  );
}

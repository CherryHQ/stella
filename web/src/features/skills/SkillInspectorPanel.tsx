import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ChevronLeft,
  ChevronRight,
  Clock,
  Copy,
  FileText,
  GitBranch,
  Lock,
  RefreshCw,
  Trash2,
  X,
} from "lucide-react";
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
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { Tooltip, TooltipPopup, TooltipTrigger } from "@/components/ui/tooltip";
import { SkillFilePreview } from "@/features/sessions/SkillFilePreview";
import { SkillGlyph } from "@/features/skills/SkillGlyph";
import {
  deleteAgentSkill,
  getAgentSkill,
  getAgentSkillFile,
  updateAgentSkill,
  upgradeAgentSkill,
} from "@/lib/api-client/sdk.gen";
import { apiErrorMessage } from "@/lib/api-error";
import { meQueryOptions } from "@/lib/queries/me";
import { useI18n } from "@/lib/i18n";
import {
  isSkillReadOnly,
  SCOPE_DESC_KEY,
  SCOPE_LABEL_KEY,
  type SkillScope,
} from "@/lib/skill-scope";
import { formatTime } from "@/lib/time";
import type { Skill } from "@/lib/types";

export type SkillNotify = (text: string, kind?: "success" | "error") => void;

// A skill can be re-fetched from its source when it was installed from a remote
// (git/github/URL) — clawhub pins and on-disk project skills have no moving ref
// to check, so the upgrade affordance only applies to remote sources.
export function isUpdatableSource(source?: string): boolean {
  return (
    !!source && !source.startsWith("clawhub:") && !source.startsWith("/") && !source.startsWith(".")
  );
}

export function skillSourceMessageKey(skill: Skill) {
  if (skill.scope === "system") return "sessions.skillsList.builtin" as const;
  if (skill.created_by === "reflect") return "sessions.skillsList.generated" as const;
  return "sessions.skillsList.manualMaintenance" as const;
}

/**
 * The full inspector for one installed skill: metadata, editable description /
 * version / model-invocation gate, per-file view with inline editing, upgrade
 * check and delete. It is the sheet body wherever a skill is opened, so the
 * profile owns both *which* skills an agent has and what is inside them.
 *
 * Write affordances mirror the backend rules (`isSkillReadOnly`): a read-only
 * scope renders the same content with a lock note and no mutation controls.
 *
 * Query keys are shared with every other skill surface so one cache entry backs
 * a skill and one backs each of its files.
 */
export function SkillInspectorPanel({
  agentId,
  sessionId,
  skill,
  notify,
  onClose,
}: {
  agentId: string;
  /** Project skills are filesystem-backed; the API resolves their root from a project session. */
  sessionId?: string;
  skill: Skill;
  notify: SkillNotify;
  /** Dismisses the whole surface — used after a delete or a manual conversion. */
  onClose: () => void;
}) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { data: me } = useQuery(meQueryOptions);
  const [description, setDescription] = useState(skill.description ?? "");
  const [version, setVersion] = useState(skill.version ?? "");
  const [modelEnabled, setModelEnabled] = useState(!skill.disable_model_invocation);
  const [viewer, setViewer] = useState<string | null>(null);
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [upgrading, setUpgrading] = useState(false);
  const [convertToManual, setConvertToManual] = useState(false);
  const readOnly = isSkillReadOnly(skill.scope, !!me?.is_admin);
  const canUpgrade = !readOnly && isUpdatableSource(skill.source);
  const scoped = skill.scope === "project";
  const ready = !scoped || !!sessionId;
  const detail = useQuery({
    queryKey: ["agent-skill", agentId, sessionId ?? "", skill.scope, skill.id],
    queryFn: async () =>
      (
        await getAgentSkill({
          path: { id: agentId, skillId: skill.id },
          query: {
            scope: skill.scope as SkillScope,
            ...(sessionId ? { session_id: sessionId } : {}),
          },
          throwOnError: true,
        })
      ).data as Skill,
    enabled: ready,
  });
  const files = detail.data?.files ?? skill.files ?? [];

  useEffect(() => {
    setDescription(skill.description ?? "");
    setVersion(skill.version ?? "");
    setModelEnabled(!skill.disable_model_invocation);
    setConvertToManual(false);
    setViewer(null);
  }, [skill]);

  function invalidateSkillQueries() {
    void queryClient.invalidateQueries({ queryKey: ["agent-skills", agentId] });
    void queryClient.invalidateQueries({
      queryKey: ["agent-skill", agentId, sessionId ?? "", skill.scope, skill.id],
    });
  }

  async function save() {
    // Keep the conversion decision stable while local form state is reset after saving.
    const shouldConvertToManual = convertToManual;
    try {
      await updateAgentSkill({
        path: { id: agentId, skillId: skill.id },
        query: {
          scope: skill.scope as SkillScope,
          ...(sessionId ? { session_id: sessionId } : {}),
        },
        body: {
          expected_digest: detail.data?.content_digest ?? skill.content_digest,
          description,
          disable_model_invocation: !modelEnabled,
          version,
          ...(shouldConvertToManual ? { convert_to_manual: true } : {}),
        },
        throwOnError: true,
      });
      notify(t("sessions.skillsList.saved"), "success");
      invalidateSkillQueries();
      if (shouldConvertToManual) onClose();
    } catch (error) {
      notify(apiErrorMessage(error, t("common.error")), "error");
    }
  }

  async function upgrade() {
    setUpgrading(true);
    try {
      const res = await upgradeAgentSkill({
        path: { id: agentId, skillId: skill.id },
        query: {
          scope: skill.scope as SkillScope,
          expected_digest: (detail.data?.content_digest ?? skill.content_digest)!,
        },
        throwOnError: true,
      });
      if (res.data?.updated) {
        notify(
          t("sessions.skillsList.upgradeDone", { version: res.data.version ?? "" }),
          "success",
        );
        invalidateSkillQueries();
      } else {
        notify(t("sessions.skillsList.upgradeUpToDate"), "success");
      }
    } catch (error) {
      notify(apiErrorMessage(error, t("common.error")), "error");
    } finally {
      setUpgrading(false);
    }
  }

  async function remove() {
    try {
      await deleteAgentSkill({
        path: { id: agentId, skillId: skill.id },
        query: {
          scope: skill.scope as SkillScope,
          ...(sessionId ? { session_id: sessionId } : {}),
          ...(skill.content_digest ? { expected_digest: skill.content_digest } : {}),
        },
        throwOnError: true,
      });
      notify(t("sessions.skillsList.deletedSuccess"), "success");
      // Awaited so the list has dropped the skill before the sheet closes.
      await queryClient.invalidateQueries({ queryKey: ["agent-skills", agentId] });
      onClose();
    } catch (error) {
      notify(apiErrorMessage(error, t("common.error")), "error");
    } finally {
      setConfirmOpen(false);
    }
  }

  // Drilling into a file swaps the whole panel to a file view rather than
  // stacking a centered dialog over the sheet (the old behaviour read as broken).
  if (viewer) {
    return (
      <SkillFileView
        agentId={agentId}
        sessionId={sessionId}
        skill={skill}
        path={viewer}
        readOnly={readOnly}
        notify={notify}
        onBack={() => setViewer(null)}
        onClose={onClose}
      />
    );
  }

  return (
    <div className="flex h-full min-h-0 flex-1 flex-col">
      <div className="flex items-start gap-3 border-b p-5">
        <SkillGlyph className="size-11 rounded-lg" />
        <div className="min-w-0 flex-1">
          <h2 className="truncate font-mono text-base font-semibold">{skill.name}</h2>
          <div className="mt-2 flex flex-wrap items-center gap-2">
            <Tooltip>
              <TooltipTrigger render={<Badge variant="secondary" size="sm" />}>
                {t(SCOPE_LABEL_KEY[skill.scope as SkillScope])}
              </TooltipTrigger>
              <TooltipPopup side="bottom" className="max-w-56">
                {t(SCOPE_DESC_KEY[skill.scope as SkillScope])}
              </TooltipPopup>
            </Tooltip>
            <Badge variant="outline" size="sm">
              {skill.disable_model_invocation
                ? t("sessions.skillsList.manual")
                : t("sessions.skillsList.auto")}
            </Badge>
            <Badge variant="outline" size="sm">
              {t(skillSourceMessageKey(skill))}
            </Badge>
          </div>
          <div className="mt-2 flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
            {skill.scope !== "system" && (
              <span className="inline-flex items-center gap-1">
                <Clock className="size-4" />
                {formatTime(skill.updated_at)}
              </span>
            )}
            {skill.source && (
              <span className="inline-flex items-center gap-1 font-mono">
                <GitBranch className="size-4" />
                {skill.source}
                {skill.version && (
                  <Badge variant="outline" size="sm">
                    {skill.version}
                  </Badge>
                )}
              </span>
            )}
          </div>
        </div>
        <Button size="icon-sm" variant="ghost" aria-label={t("common.close")} onClick={onClose}>
          <X size={16} />
        </Button>
      </div>

      <div className="min-h-0 flex-1 space-y-6 overflow-y-auto p-5">
        <section className="space-y-2">
          <Label>{t("sessions.skillsList.description")}</Label>
          {readOnly ? (
            <p className="text-sm text-muted-foreground">
              {skill.description || t("sessions.skillsList.emptyFile")}
            </p>
          ) : (
            <Textarea
              value={description}
              onChange={(e) => setDescription((e.target as HTMLTextAreaElement).value)}
              className="min-h-20"
            />
          )}
        </section>

        <section className="space-y-2">
          <Label>
            {t("sessions.skillsList.files")} · {files.length}
          </Label>
          {!ready ? (
            <p className="text-sm text-muted-foreground">{t("profile.skillFilesUnavailable")}</p>
          ) : detail.isPending && files.length === 0 ? (
            <div className="flex justify-center py-6">
              <Spinner className="size-5" />
            </div>
          ) : files.length === 0 ? (
            <p className="text-sm text-muted-foreground">{t("profile.skillNoFiles")}</p>
          ) : (
            <div className="divide-y divide-border overflow-hidden rounded-lg border">
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

        {!readOnly && (
          <section className="space-y-4">
            <div className="space-y-2">
              <Label>{t("sessions.skillsList.versionLabel")}</Label>
              <Input
                value={version}
                onChange={(e) => setVersion((e.target as HTMLInputElement).value)}
                className="font-mono"
              />
              <p className="text-xs text-muted-foreground">
                {t("sessions.skillsList.versionHint")}
              </p>
            </div>
            <div className="flex items-start justify-between gap-4">
              <div className="space-y-0.5">
                <Label>{t("sessions.skillsList.modelInvocation")}</Label>
                <p className="text-xs text-muted-foreground">
                  {t("sessions.skillsList.modelInvocationHint")}
                </p>
              </div>
              <Switch checked={modelEnabled} onCheckedChange={setModelEnabled} />
            </div>
            {skill.created_by === "reflect" && (
              <div className="space-y-2 rounded-lg border p-3">
                <div className="flex items-start justify-between gap-4">
                  <div className="space-y-0.5">
                    <Label>{t("sessions.skillsList.convertToManual")}</Label>
                    <p className="text-xs text-muted-foreground">
                      {t("sessions.skillsList.convertToManualHint")}
                    </p>
                  </div>
                  <Switch checked={convertToManual} onCheckedChange={setConvertToManual} />
                </div>
                {convertToManual && (
                  <p className="text-xs text-destructive-foreground">
                    {t("sessions.skillsList.convertToManualWarning")}
                  </p>
                )}
              </div>
            )}
          </section>
        )}
      </div>

      {readOnly ? (
        <div className="flex items-center gap-2 border-t p-4 text-sm text-muted-foreground">
          <Lock size={16} /> {t("sessions.skillsList.readonlyNote")}
        </div>
      ) : (
        <div className="flex items-center gap-2 border-t p-4">
          <Button variant="destructive-outline" onClick={() => setConfirmOpen(true)}>
            <Trash2 size={16} />
            {t("sessions.skillsList.deleteSkill")}
          </Button>
          <div className="ml-auto flex items-center gap-2">
            {canUpgrade && (
              <Button variant="outline" loading={upgrading} onClick={() => void upgrade()}>
                <RefreshCw size={16} />
                {t("sessions.skillsList.upgradeCheck")}
              </Button>
            )}
            <Button onClick={() => void save()}>{t("common.save")}</Button>
          </div>
        </div>
      )}

      <AlertDialog open={confirmOpen} onOpenChange={setConfirmOpen}>
        <AlertDialogPopup>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("sessions.skillsList.deleteConfirm")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("sessions.skillsList.deleteConfirmDesc", { name: skill.name })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogClose render={<Button variant="ghost" />}>
              {t("common.cancel")}
            </AlertDialogClose>
            <Button variant="destructive" onClick={() => void remove()}>
              {t("sessions.skillsList.deleteSkill")}
            </Button>
          </AlertDialogFooter>
        </AlertDialogPopup>
      </AlertDialog>
    </div>
  );
}

// Inline file view: replaces the panel body so reading/editing a skill file
// stays on the same surface instead of opening a separate centered dialog.
function SkillFileView({
  agentId,
  sessionId,
  skill,
  path,
  readOnly,
  notify,
  onBack,
  onClose,
}: {
  agentId: string;
  sessionId?: string;
  skill: Skill;
  path: string;
  readOnly: boolean;
  notify: SkillNotify;
  onBack: () => void;
  onClose: () => void;
}) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState("");
  const [convertToManual, setConvertToManual] = useState(false);
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
  const content = editing ? draft : (file.data?.content ?? "");
  // Binary files travel base64-encoded and are view-only: saving the transport
  // form back through the JSON files map would corrupt them.
  const binaryFile = file.data?.encoding === "base64";
  useEffect(() => {
    if (file.data?.content != null) setDraft(file.data.content);
  }, [file.data?.content]);

  async function save() {
    // Hard gate at the mutation boundary: never write a file whose content was
    // not successfully loaded (a failed fetch would overwrite it with an empty
    // draft) and never write a binary file's base64 transport form back.
    if (!file.isSuccess || binaryFile) {
      notify(t("common.error"), "error");
      return;
    }
    // Keep the conversion decision stable while local editor state is reset after saving.
    const shouldConvertToManual = convertToManual;
    try {
      await updateAgentSkill({
        path: { id: agentId, skillId: skill.id },
        query: {
          scope: skill.scope as SkillScope,
          ...(sessionId ? { session_id: sessionId } : {}),
        },
        body: {
          expected_digest: skill.content_digest,
          files: { [path]: draft },
          ...(shouldConvertToManual ? { convert_to_manual: true } : {}),
        },
        throwOnError: true,
      });
      setEditing(false);
      setConvertToManual(false);
      notify(t("sessions.skillsList.saved"), "success");
      void queryClient.invalidateQueries({
        queryKey: ["agent-skill-file", agentId, sessionId ?? "", skill.scope, skill.id, path],
      });
      void queryClient.invalidateQueries({ queryKey: ["agent-skills", agentId] });
      if (shouldConvertToManual) onClose();
    } catch (error) {
      notify(apiErrorMessage(error, t("common.error")), "error");
    }
  }

  return (
    <div className="flex h-full min-h-0 flex-1 flex-col">
      <div className="flex items-center gap-2 border-b p-4">
        <Button size="icon-sm" variant="ghost" aria-label={t("common.back")} onClick={onBack}>
          <ChevronLeft size={16} />
        </Button>
        <div className="min-w-0 flex-1">
          <p className="truncate font-mono text-sm font-medium">{path}</p>
          <p className="truncate font-mono text-xs text-muted-foreground">{skill.name}</p>
        </div>
        <Button
          size="sm"
          variant="ghost"
          onClick={() => void navigator.clipboard.writeText(content)}
        >
          <Copy size={16} />
          <span className="max-sm:hidden">{t("common.copy")}</span>
        </Button>
        {!readOnly && file.isSuccess && !binaryFile && (
          <Button
            size="sm"
            variant="outline"
            onClick={() => {
              setEditing((value) => !value);
              if (editing) setConvertToManual(false);
            }}
          >
            {editing ? t("common.cancel") : t("common.edit")}
          </Button>
        )}
        <Button size="icon-sm" variant="ghost" aria-label={t("common.close")} onClick={onClose}>
          <X size={16} />
        </Button>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto p-5">
        {file.isLoading ? (
          <Spinner />
        ) : editing ? (
          <Textarea
            value={draft}
            onChange={(e) => setDraft((e.target as HTMLTextAreaElement).value)}
            className="min-h-96 font-mono"
          />
        ) : (
          <SkillFilePreview
            path={path}
            content={content}
            encoding={file.data?.encoding}
            emptyText={t("sessions.skillsList.emptyFile")}
          />
        )}
      </div>
      {editing && (
        <div className="space-y-3 border-t p-4">
          {skill.created_by === "reflect" && (
            <div className="space-y-2 rounded-lg border p-3">
              <div className="flex items-start justify-between gap-4">
                <div className="space-y-0.5">
                  <Label>{t("sessions.skillsList.convertToManual")}</Label>
                  <p className="text-xs text-muted-foreground">
                    {t("sessions.skillsList.convertToManualHint")}
                  </p>
                </div>
                <Switch checked={convertToManual} onCheckedChange={setConvertToManual} />
              </div>
              {convertToManual && (
                <p className="text-xs text-destructive-foreground">
                  {t("sessions.skillsList.convertToManualWarning")}
                </p>
              )}
            </div>
          )}
          <Button className="w-full" onClick={() => void save()}>
            {t("common.save")}
          </Button>
        </div>
      )}
    </div>
  );
}

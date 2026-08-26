import { useCallback, useEffect, useState } from "react";
import { targetValue } from "@/lib/utils";
import {
  createAgentSkill,
  deleteAgentSkill,
  getAgentSkill,
  getAgentSkillFile,
  updateAgentSkill,
} from "@/lib/api-client";
import type { UpdateAgentSkillData } from "@/lib/api-client/types.gen";
import { useI18n } from "@/lib/i18n";
import type { Skill } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Switch } from "@/components/ui/switch";
import { SkillFilePreview } from "@/features/sessions/SkillFilePreview";
import { skillScopeLabelKey } from "@/lib/skill-scope";

interface Props {
  skillId: string | null;
  scope?: string;
  agentId: string;
  onSaved: () => void;
  onDeleted: () => void;
}

interface Form {
  name: string;
  description: string;
  disable_model_invocation: boolean;
  content: string;
}

function emptyForm(): Form {
  return {
    name: "",
    description: "",
    disable_model_invocation: false,
    content: "",
  };
}

function scopeBadgeVariant(scope: string): "outline" | "secondary" | "default" {
  if (scope === "user" || scope === "user_agent") return "default";
  if (scope === "system_agent") return "secondary";
  return "outline";
}

export function SkillPanel({ skillId, scope, agentId, onSaved, onDeleted }: Props) {
  const { t } = useI18n();
  const isNew = skillId === null;
  const [skill, setSkill] = useState<Skill | null>(null);
  const [savedForm, setSavedForm] = useState<Form>(emptyForm());
  const [form, setForm] = useState<Form>(emptyForm());
  const [loading, setLoading] = useState(!isNew);
  const [saving, setSaving] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [editing, setEditing] = useState(isNew);
  const [files, setFiles] = useState<string[]>(["SKILL.md"]);
  const [activeFile, setActiveFile] = useState("SKILL.md");
  const [fileContents, setFileContents] = useState<Record<string, string>>({});
  const [fileEncodings, setFileEncodings] = useState<Record<string, string | undefined>>({});
  const [fileLoading, setFileLoading] = useState(false);

  const isReadOnly = !isNew && skill !== null && skill.scope === "system";

  const patchForm = (patch: Partial<Form>) => setForm((f) => ({ ...f, ...patch }));

  const load = useCallback(async () => {
    if (!skillId || !agentId) return;
    setLoading(true);
    try {
      const skillScope = scope as
        | "project"
        | "user"
        | "user_agent"
        | "system"
        | "system_agent"
        | undefined;
      const { data: skRaw } = await getAgentSkill({
        path: { id: agentId, skillId },
        query: { scope: skillScope },
        throwOnError: true,
      });
      const sk = skRaw as Skill;
      const skillFiles = sk.files?.length ? sk.files : ["SKILL.md"];
      const initialFile = skillFiles.includes("SKILL.md") ? "SKILL.md" : skillFiles[0];
      const res = await getAgentSkillFile({
        path: { id: agentId, skillId },
        query: { path: initialFile, scope: skillScope },
        throwOnError: true,
      }).catch(() => null);
      const fileData = res?.data as { content?: string; encoding?: string } | undefined;
      const content = fileData?.content ?? "";
      const f: Form = {
        name: sk.name,
        description: sk.description ?? "",
        disable_model_invocation: sk.disable_model_invocation ?? false,
        content: initialFile === "SKILL.md" ? content : "",
      };
      setSkill(sk);
      setFiles(skillFiles);
      setActiveFile(initialFile);
      setFileContents({ [initialFile]: content });
      setFileEncodings({ [initialFile]: fileData?.encoding });
      setSavedForm(f);
      setForm(f);
      setEditing(false);
    } catch (e) {
      console.error(e);
    } finally {
      setLoading(false);
    }
  }, [skillId, scope, agentId]);

  useEffect(() => {
    if (isNew) {
      const f = emptyForm();
      setForm(f);
      setSavedForm(f);
      setFiles(["SKILL.md"]);
      setActiveFile("SKILL.md");
      setFileContents({ "SKILL.md": "" });
      setFileEncodings({});
      setEditing(true);
    } else {
      void load();
    }
  }, [isNew, load]);

  const selectFile = useCallback(
    async (path: string) => {
      setActiveFile(path);
      if (!skillId || fileContents[path] !== undefined) return;
      setFileLoading(true);
      try {
        const res = await getAgentSkillFile({
          path: { id: agentId, skillId },
          query: { path, scope: scope as UpdateAgentSkillData["query"]["scope"] },
          throwOnError: true,
        });
        const fileData = res.data as { content?: string; encoding?: string } | undefined;
        const content = fileData?.content ?? "";
        setFileContents((current) => ({ ...current, [path]: content }));
        setFileEncodings((current) => ({ ...current, [path]: fileData?.encoding }));
      } catch (e) {
        console.error(e);
        setFileContents((current) => ({ ...current, [path]: "" }));
      } finally {
        setFileLoading(false);
      }
    },
    [agentId, fileContents, scope, skillId],
  );

  const save = useCallback(async () => {
    setSaving(true);
    try {
      if (isNew) {
        await createAgentSkill({
          path: { id: agentId },
          body: {
            name: form.name,
            scope: "user",
            description: form.description,
            disable_model_invocation: form.disable_model_invocation,
            files: { "SKILL.md": form.content },
          },
          throwOnError: true,
        });
      } else if (skillId) {
        await updateAgentSkill({
          path: { id: agentId, skillId },
          query: { scope: scope as UpdateAgentSkillData["query"]["scope"] },
          body: {
            expected_digest: skill?.content_digest,
            description: form.description,
            disable_model_invocation: form.disable_model_invocation,
            files: { "SKILL.md": form.content },
          },
          throwOnError: true,
        });
      }
      setSavedForm(form);
      setEditing(false);
      onSaved();
    } catch (e) {
      console.error(e);
    } finally {
      setSaving(false);
    }
  }, [isNew, skillId, scope, agentId, skill?.content_digest, form, onSaved]);

  const remove = useCallback(async () => {
    if (!skillId) return;
    setDeleting(true);
    try {
      await deleteAgentSkill({
        path: { id: agentId, skillId },
        query: {
          scope: scope as UpdateAgentSkillData["query"]["scope"],
          ...(skill?.content_digest ? { expected_digest: skill.content_digest } : undefined),
        },
        throwOnError: true,
      });
      onDeleted();
    } catch (e) {
      console.error(e);
    } finally {
      setDeleting(false);
    }
  }, [skillId, scope, agentId, skill?.content_digest, onDeleted]);

  const cancelEdit = () => {
    setForm(savedForm);
    setEditing(false);
  };

  if (loading) {
    return (
      <div className="flex-1 flex items-center justify-center text-sm text-muted-foreground">
        {t("common.loading")}
      </div>
    );
  }

  const dirty = JSON.stringify(form) !== JSON.stringify(savedForm);
  const activeFileContent =
    activeFile === "SKILL.md" ? form.content : (fileContents[activeFile] ?? "");

  return (
    <div className="flex h-full min-w-0 max-w-full flex-col overflow-hidden">
      <div className="min-w-0 max-w-full flex-1 space-y-5 overflow-y-auto overflow-x-hidden p-6">
        {/* Header */}
        <div className="flex min-w-0 items-start justify-between gap-4">
          <div className="min-w-0 max-w-full">
            <h2 className="truncate text-base font-semibold">
              {isNew ? t("sessions.skill.newSkill") : form.name}
            </h2>
            {!isNew && skill && (
              <div className="flex items-center gap-2 mt-1.5">
                <Badge variant={scopeBadgeVariant(skill.scope)} size="sm">
                  {t(skillScopeLabelKey(skill.scope) ?? "skills.scope.project.label")}
                </Badge>
                {isReadOnly && (
                  <Badge variant="outline" size="sm">
                    {t("sessions.skill.readOnly")}
                  </Badge>
                )}
              </div>
            )}
          </div>
          <div className="flex shrink-0 items-center gap-2">
            {!isNew && !isReadOnly && !editing && (
              <Button variant="outline" size="sm" onClick={() => setEditing(true)}>
                {t("common.edit")}
              </Button>
            )}
            {!isNew && !isReadOnly && (
              <Button
                variant="ghost"
                size="sm"
                onClick={() => void remove()}
                disabled={deleting}
                className="text-destructive-foreground hover:text-destructive-foreground"
              >
                {deleting ? t("sessions.skill.deleting") : t("common.delete")}
              </Button>
            )}
          </div>
        </div>

        {/* Read view — shown for all scopes when not editing */}
        {!editing && !isNew && (
          <div className="min-w-0 max-w-full space-y-4 overflow-hidden">
            {form.description && (
              <p className="max-w-full break-words text-sm text-muted-foreground [overflow-wrap:anywhere]">
                {form.description}
              </p>
            )}
            <div className="min-w-0 max-w-full overflow-hidden rounded-xl border border-border bg-background/60">
              <div className="min-w-0 max-w-full border-b border-border p-3">
                <label className="mb-1.5 block text-xs font-mono text-muted-foreground">File</label>
                <select
                  value={activeFile}
                  onChange={(e) => void selectFile(targetValue(e))}
                  className="block h-8 w-full min-w-0 max-w-full truncate rounded-lg border border-input bg-background px-3 text-sm font-mono outline-none focus:ring-2 focus:ring-ring"
                  title={activeFile}
                >
                  {files.map((file) => (
                    <option key={file} value={file}>
                      {file}
                    </option>
                  ))}
                </select>
              </div>
              <div className="min-w-0 max-w-full overflow-hidden p-4">
                <p className="mb-3 max-w-full truncate text-xs font-mono text-muted-foreground">
                  {activeFile}
                </p>
                {fileLoading ? (
                  <p className="text-sm text-muted-foreground">{t("common.loading")}</p>
                ) : (
                  <SkillFilePreview
                    path={activeFile}
                    content={activeFileContent}
                    encoding={fileEncodings[activeFile]}
                    emptyText={t("sessions.skill.noContent")}
                  />
                )}
              </div>
            </div>
            {!isReadOnly && (
              <div className="flex items-center gap-2 text-xs text-muted-foreground">
                <span>{t("sessions.skill.modelInvocationLabel")}</span>
                <span>
                  {form.disable_model_invocation ? t("common.disable") : t("common.enable")}
                </span>
              </div>
            )}
          </div>
        )}

        {/* Editable form (user scope or new) */}
        {!isReadOnly && (editing || isNew) && (
          <>
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <div>
                <label className="block text-sm font-mono mb-1">
                  {t("sessions.skill.fieldName")}
                </label>
                <Input
                  nativeInput
                  value={form.name}
                  onChange={(e) => patchForm({ name: targetValue(e) })}
                  placeholder={t("sessions.skill.namePlaceholder")}
                  className="text-sm font-mono"
                />
              </div>
              <div className="sm:col-span-2">
                <label className="block text-sm font-mono mb-1">
                  {t("sessions.skill.fieldDescription")}
                </label>
                <Input
                  nativeInput
                  value={form.description}
                  onChange={(e) => patchForm({ description: targetValue(e) })}
                  placeholder={t("sessions.skill.descPlaceholder")}
                  className="text-sm"
                />
              </div>
              <div className="sm:col-span-2">
                <label className="block text-sm font-mono mb-1">
                  {t("sessions.skill.fieldContent")}
                </label>
                <Textarea
                  value={form.content}
                  onChange={(e) => patchForm({ content: targetValue(e) })}
                  rows={12}
                  placeholder={"# My Skill\n\nInstructions for the agent…"}
                  className="text-sm font-mono"
                />
              </div>
            </div>
            <div className="pt-1">
              <label className="flex items-center gap-3 cursor-pointer">
                <Switch
                  checked={!form.disable_model_invocation}
                  onCheckedChange={(checked) => patchForm({ disable_model_invocation: !checked })}
                />
                <span className="text-sm">{t("sessions.skill.modelInvocation")}</span>
              </label>
            </div>
          </>
        )}
      </div>

      {/* Footer — only shown when editing or creating */}
      {(editing || isNew) && !isReadOnly && (
        <div className="flex items-center gap-2 px-6 py-4 border-t border-border flex-shrink-0">
          <Button onClick={() => void save()} disabled={saving || (!dirty && !isNew)} size="sm">
            {saving ? t("sessions.skill.saving") : isNew ? t("common.create") : t("common.save")}
          </Button>
          {!isNew && (
            <Button variant="ghost" size="sm" onClick={cancelEdit} disabled={saving}>
              {t("common.cancel")}
            </Button>
          )}
        </div>
      )}
    </div>
  );
}

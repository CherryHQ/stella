import { useCallback, useEffect, useState } from "react";
import {
  createAgentScopedSkill,
  deleteAgentScopedSkill,
  getAgentScopedSkill,
  getAgentScopedSkillFile,
  updateAgentScopedSkill,
} from "@/lib/api-client";
import { useI18n } from "@/lib/i18n";
import type { Skill } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Switch } from "@/components/ui/switch";

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
  status: "active" | "draft" | "deprecated";
  disable_model_invocation: boolean;
  content: string;
}

function emptyForm(): Form {
  return {
    name: "",
    description: "",
    status: "active",
    disable_model_invocation: false,
    content: "",
  };
}

function scopeLabel(scope: string) {
  return { system: "Built-in", agent: "Agent", user: "User" }[scope] ?? scope;
}

function scopeBadgeVariant(scope: string): "outline" | "secondary" | "default" {
  if (scope === "user") return "default";
  if (scope === "agent") return "secondary";
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

  const isReadOnly = !isNew && skill !== null && skill.scope === "system";

  const patchForm = (patch: Partial<Form>) => setForm((f) => ({ ...f, ...patch }));

  const load = useCallback(async () => {
    if (!skillId || !agentId) return;
    setLoading(true);
    try {
      const { data: skRaw } = await getAgentScopedSkill({
        path: {
          id: agentId,
          scope: (scope ?? "user") as "agent" | "project" | "system" | "user",
          skillId,
        },
        throwOnError: true,
      });
      const sk = skRaw as unknown as Skill;
      const res = await getAgentScopedSkillFile({
        path: {
          id: agentId,
          scope: (scope ?? "user") as "agent" | "project" | "system" | "user",
          skillId,
        },
        query: { path: "SKILL.md" },
        throwOnError: true,
      }).catch(() => null);
      const content = res?.data?.content ?? "";
      const f: Form = {
        name: sk.name,
        description: sk.description ?? "",
        status: sk.status ?? "active",
        disable_model_invocation: sk.disable_model_invocation ?? false,
        content,
      };
      setSkill(sk);
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
      setEditing(true);
    } else {
      void load();
    }
  }, [isNew, load]);

  const save = useCallback(async () => {
    setSaving(true);
    try {
      if (isNew) {
        await createAgentScopedSkill({
          path: { id: agentId, scope: "user" },
          body: {
            name: form.name,
            description: form.description,
            status: form.status,
            disable_model_invocation: form.disable_model_invocation,
            files: { "SKILL.md": form.content },
          },
          throwOnError: true,
        });
      } else if (skillId) {
        await updateAgentScopedSkill({
          path: {
            id: agentId,
            scope: (scope ?? "user") as "agent" | "project" | "system" | "user",
            skillId,
          },
          body: {
            description: form.description,
            status: form.status,
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
  }, [isNew, skillId, scope, agentId, form, onSaved]);

  const remove = useCallback(async () => {
    if (!skillId) return;
    setDeleting(true);
    try {
      await deleteAgentScopedSkill({
        path: {
          id: agentId,
          scope: (scope ?? "user") as "agent" | "project" | "system" | "user",
          skillId,
        },
        throwOnError: true,
      });
      onDeleted();
    } catch (e) {
      console.error(e);
    } finally {
      setDeleting(false);
    }
  }, [skillId, scope, agentId, onDeleted]);

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

  return (
    <div className="flex flex-col flex-1 overflow-hidden">
      <div className="flex-1 overflow-y-auto p-6 space-y-5">
        {/* Header */}
        <div className="flex items-start justify-between">
          <div>
            <h2 className="text-base font-semibold">
              {isNew ? t("sessions.skill.newSkill") : form.name}
            </h2>
            {!isNew && skill && (
              <div className="flex items-center gap-2 mt-1.5">
                <Badge variant={scopeBadgeVariant(skill.scope)} size="sm">
                  {scopeLabel(skill.scope)}
                </Badge>
                <Badge variant={skill.status === "active" ? "success" : "outline"} size="sm">
                  {skill.status}
                </Badge>
                {isReadOnly && (
                  <Badge variant="outline" size="sm">
                    {t("sessions.skill.readOnly")}
                  </Badge>
                )}
              </div>
            )}
          </div>
          <div className="flex items-center gap-2">
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
                className="text-destructive hover:text-destructive"
              >
                {deleting ? t("sessions.skill.deleting") : t("common.delete")}
              </Button>
            )}
          </div>
        </div>

        {/* Read view — shown for all scopes when not editing */}
        {!editing && !isNew && (
          <div className="space-y-4">
            {form.description && (
              <p className="text-sm text-muted-foreground">{form.description}</p>
            )}
            <div>
              <p className="text-xs font-mono text-muted-foreground uppercase tracking-wider mb-2">
                SKILL.md
              </p>
              {form.content ? (
                <pre className="text-sm font-mono whitespace-pre-wrap text-foreground/90 leading-relaxed bg-muted/40 rounded-lg p-4">
                  {form.content}
                </pre>
              ) : (
                <p className="text-sm text-muted-foreground italic">
                  {t("sessions.skill.noContent")}
                </p>
              )}
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
            <div className="grid grid-cols-2 gap-4">
              <div>
                <label className="block text-sm font-mono mb-1">
                  {t("sessions.skill.fieldName")}
                </label>
                <Input
                  nativeInput
                  value={form.name}
                  onChange={(e) => patchForm({ name: (e.target as HTMLInputElement).value })}
                  placeholder={t("sessions.skill.namePlaceholder")}
                  className="text-sm font-mono"
                />
              </div>
              <div>
                <label className="block text-sm font-mono mb-1">
                  {t("sessions.skill.fieldStatus")}
                </label>
                <select
                  value={form.status}
                  onChange={(e) => patchForm({ status: e.target.value as Form["status"] })}
                  className="w-full rounded-lg border border-input bg-background px-3 py-2 text-sm outline-none focus:ring-2 focus:ring-ring"
                >
                  <option value="active">active</option>
                  <option value="draft">draft</option>
                  <option value="deprecated">deprecated</option>
                </select>
              </div>
              <div className="col-span-2">
                <label className="block text-sm font-mono mb-1">
                  {t("sessions.skill.fieldDescription")}
                </label>
                <Input
                  nativeInput
                  value={form.description}
                  onChange={(e) => patchForm({ description: (e.target as HTMLInputElement).value })}
                  placeholder={t("sessions.skill.descPlaceholder")}
                  className="text-sm"
                />
              </div>
              <div className="col-span-2">
                <label className="block text-sm font-mono mb-1">
                  {t("sessions.skill.fieldContent")}
                </label>
                <Textarea
                  value={form.content}
                  onChange={(e) => patchForm({ content: (e.target as HTMLTextAreaElement).value })}
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

import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import type { Skill } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Switch } from "@/components/ui/switch";

interface Props {
  skillId: string | null;
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

function skillUrl(agentId: string, skillId: string) {
  return `/api/agents/${encodeURIComponent(agentId)}/skills/${encodeURIComponent(skillId)}`;
}

function skillFileUrl(agentId: string, skillId: string) {
  return `/api/agents/${encodeURIComponent(agentId)}/skills/${encodeURIComponent(skillId)}/file?path=SKILL.md`;
}

function scopeLabel(scope: string) {
  return { system: "Built-in", agent: "Agent", user: "User" }[scope] ?? scope;
}

function scopeBadgeVariant(scope: string): "outline" | "secondary" | "default" {
  if (scope === "user") return "default";
  if (scope === "agent") return "secondary";
  return "outline";
}

export function SkillPanel({ skillId, agentId, onSaved, onDeleted }: Props) {
  const isNew = skillId === null;
  const [skill, setSkill] = useState<Skill | null>(null);
  const [savedForm, setSavedForm] = useState<Form>(emptyForm());
  const [form, setForm] = useState<Form>(emptyForm());
  const [loading, setLoading] = useState(!isNew);
  const [saving, setSaving] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [editing, setEditing] = useState(isNew);

  const isReadOnly = !isNew && skill !== null && skill.scope !== "user";

  const patchForm = (patch: Partial<Form>) => setForm((f) => ({ ...f, ...patch }));

  const load = useCallback(async () => {
    if (!skillId || !agentId) return;
    setLoading(true);
    try {
      const [sk, content] = await Promise.all([
        api<Skill>("GET", skillUrl(agentId, skillId)),
        api<string>("GET", skillFileUrl(agentId, skillId)).catch(() => ""),
      ]);
      const f: Form = {
        name: sk.name,
        description: sk.description,
        status: sk.status,
        disable_model_invocation: sk.disable_model_invocation,
        content: typeof content === "string" ? content : "",
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
  }, [skillId, agentId]);

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
        await api("POST", `/api/agents/${encodeURIComponent(agentId)}/skills`, {
          name: form.name,
          description: form.description,
          status: form.status,
          disable_model_invocation: form.disable_model_invocation,
        });
      } else if (skillId) {
        await api("PUT", skillUrl(agentId, skillId), {
          name: form.name,
          description: form.description,
          status: form.status,
          disable_model_invocation: form.disable_model_invocation,
        });
        await api("PUT", skillFileUrl(agentId, skillId), form.content);
      }
      setSavedForm(form);
      setEditing(false);
      onSaved();
    } catch (e) {
      console.error(e);
    } finally {
      setSaving(false);
    }
  }, [isNew, skillId, agentId, form, onSaved]);

  const remove = useCallback(async () => {
    if (!skillId) return;
    setDeleting(true);
    try {
      await api("DELETE", skillUrl(agentId, skillId));
      onDeleted();
    } catch (e) {
      console.error(e);
    } finally {
      setDeleting(false);
    }
  }, [skillId, agentId, onDeleted]);

  const cancelEdit = () => {
    setForm(savedForm);
    setEditing(false);
  };

  if (loading) {
    return (
      <div className="flex-1 flex items-center justify-center text-sm text-muted-foreground">
        Loading…
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
            <h2 className="text-base font-semibold">{isNew ? "New Skill" : form.name}</h2>
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
                    read only
                  </Badge>
                )}
              </div>
            )}
          </div>
          <div className="flex items-center gap-2">
            {!isNew && !isReadOnly && !editing && (
              <Button variant="outline" size="sm" onClick={() => setEditing(true)}>
                Edit
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
                {deleting ? "Deleting…" : "Delete"}
              </Button>
            )}
          </div>
        </div>

        {/* Read-only view (system / agent scope) */}
        {isReadOnly && (
          <div className="space-y-4">
            <p className="text-xs text-muted-foreground">
              {skill?.scope === "system"
                ? "Built-in skill — read only."
                : "Agent skill — read only."}
            </p>
            {form.description && (
              <div>
                <p className="text-xs font-mono text-muted-foreground uppercase tracking-wider mb-1">
                  Description
                </p>
                <p className="text-sm text-foreground/90">{form.description}</p>
              </div>
            )}
            {form.content && (
              <div>
                <p className="text-xs font-mono text-muted-foreground uppercase tracking-wider mb-2">
                  SKILL.md
                </p>
                <pre className="text-sm font-mono whitespace-pre-wrap text-foreground/90 leading-relaxed bg-muted/40 rounded-lg p-4">
                  {form.content}
                </pre>
              </div>
            )}
          </div>
        )}

        {/* Editable form (user scope or new) */}
        {!isReadOnly && (editing || isNew) && (
          <>
            <div className="grid grid-cols-2 gap-4">
              <div>
                <label className="block text-sm font-mono mb-1">Name</label>
                <Input
                  nativeInput
                  value={form.name}
                  onChange={(e) => patchForm({ name: (e.target as HTMLInputElement).value })}
                  placeholder="my-skill"
                  className="text-sm font-mono"
                />
              </div>
              <div>
                <label className="block text-sm font-mono mb-1">Status</label>
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
                <label className="block text-sm font-mono mb-1">Description</label>
                <Input
                  nativeInput
                  value={form.description}
                  onChange={(e) => patchForm({ description: (e.target as HTMLInputElement).value })}
                  placeholder="What does this skill do?"
                  className="text-sm"
                />
              </div>
              <div className="col-span-2">
                <label className="block text-sm font-mono mb-1">Content (SKILL.md)</label>
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
                <span className="text-sm">Model invocation enabled</span>
              </label>
            </div>
          </>
        )}

        {/* Read view for user skills when not editing */}
        {!isReadOnly && !isNew && !editing && (
          <div className="space-y-4">
            {form.description && (
              <div>
                <p className="text-xs font-mono text-muted-foreground uppercase tracking-wider mb-1">
                  Description
                </p>
                <p className="text-sm text-foreground/90">{form.description}</p>
              </div>
            )}
            {form.content && (
              <div>
                <p className="text-xs font-mono text-muted-foreground uppercase tracking-wider mb-2">
                  SKILL.md
                </p>
                <pre className="text-sm font-mono whitespace-pre-wrap text-foreground/90 leading-relaxed bg-muted/40 rounded-lg p-4">
                  {form.content}
                </pre>
              </div>
            )}
            <div className="flex items-center gap-2 pt-1">
              <span className="text-sm text-muted-foreground">Model invocation:</span>
              <span className="text-sm">
                {form.disable_model_invocation ? "disabled" : "enabled"}
              </span>
            </div>
          </div>
        )}
      </div>

      {/* Footer — only shown when editing or creating */}
      {(editing || isNew) && !isReadOnly && (
        <div className="flex items-center gap-2 px-6 py-4 border-t border-border flex-shrink-0">
          <Button onClick={() => void save()} disabled={saving || (!dirty && !isNew)} size="sm">
            {saving ? "Saving…" : isNew ? "Create" : "Save"}
          </Button>
          {!isNew && (
            <Button variant="ghost" size="sm" onClick={cancelEdit} disabled={saving}>
              Cancel
            </Button>
          )}
        </div>
      )}
    </div>
  );
}

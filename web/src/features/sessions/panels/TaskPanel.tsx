import { useCallback, useState } from "react";
import { createAgentTask } from "@/lib/api-client";
import type { ComponentsAgentTask } from "@/lib/api-client/types.gen";
import { useI18n } from "@/lib/i18n";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";

interface Props {
  agentId: string;
  onCreated: (task: ComponentsAgentTask) => void;
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="block space-y-1.5">
      <span className="block text-[11px] font-mono font-medium uppercase tracking-wider text-muted-foreground">
        {label}
      </span>
      {children}
    </label>
  );
}

export function TaskPanel({ agentId, onCreated }: Props) {
  const { t } = useI18n();
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [priority, setPriority] = useState<"routine" | "urgent">("routine");
  const [saving, setSaving] = useState(false);

  const create = useCallback(async () => {
    if (!title.trim()) return;
    setSaving(true);
    try {
      const { data: task } = await createAgentTask({
        path: { agentID: agentId },
        body: {
          title: title.trim(),
          description: description.trim() || undefined,
          priority,
          agent_id: agentId,
        },
        throwOnError: true,
      });
      onCreated(task);
    } catch (e) {
      console.error(e);
    } finally {
      setSaving(false);
    }
  }, [title, description, priority, agentId, onCreated]);

  return (
    <div className="flex flex-col flex-1 overflow-hidden">
      <div className="flex-1 overflow-y-auto p-6 space-y-5">
        <div>
          <div className="text-[11px] font-mono font-medium uppercase tracking-wider text-muted-foreground">
            {t("sessions.task.eyebrow")}
          </div>
          <h2 className="mt-1.5 font-serif text-2xl italic tracking-tight">
            {t("sessions.task.title")}
          </h2>
          <p className="mt-1 text-sm text-muted-foreground">{t("sessions.task.subtitle")}</p>
        </div>

        <div className="space-y-4">
          <Field label={t("tasks.fieldTitle")}>
            <Input
              nativeInput
              value={title}
              onChange={(e) => setTitle((e.target as HTMLInputElement).value)}
              placeholder={t("tasks.fieldTitlePlaceholder")}
              className="text-sm"
              autoFocus
            />
          </Field>

          <Field label={t("tasks.fieldDescription")}>
            <Textarea
              value={description}
              onChange={(e) => setDescription((e.target as HTMLTextAreaElement).value)}
              rows={6}
              placeholder={t("sessions.task.descPlaceholder")}
              className="text-sm"
            />
          </Field>

          <Field label={t("tasks.fieldPriority")}>
            <select
              value={priority}
              onChange={(e) => setPriority(e.target.value as "routine" | "urgent")}
              className="h-8.5 w-full rounded-lg border border-input bg-background px-3 text-sm text-foreground outline-none focus:ring-2 focus:ring-ring"
            >
              <option value="routine">{t("sessions.task.priorityRoutineDesc")}</option>
              <option value="urgent">{t("sessions.task.priorityUrgentDesc")}</option>
            </select>
          </Field>
        </div>
      </div>

      <div className="flex items-center gap-2 px-6 py-4 border-t border-border flex-shrink-0">
        <Button size="sm" disabled={saving || !title.trim()} onClick={() => void create()}>
          {saving ? t("sessions.task.creating") : t("sessions.task.createBtn")}
        </Button>
      </div>
    </div>
  );
}

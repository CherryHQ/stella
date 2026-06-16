import { useCallback, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { createTask, createSchedulerJob } from "@/lib/api-client";
import type { ComponentsTask, ComponentsJobInput } from "@/lib/api-client/types.gen";
import type { SchedulerJob } from "@/lib/types";
import { jobTemplatesQueryOptions } from "@/lib/queries/scheduler";
import {
  SchedulePicker,
  isScheduleValid,
  emptySchedule,
  scheduleFromString,
  type ScheduleValue,
} from "@/features/automations/SchedulePicker";
import { useI18n } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Switch } from "@/components/ui/switch";
import { Badge } from "@/components/ui/badge";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";

interface Props {
  agentId: string;
  projectId?: string;
  onCreatedTask: (task: ComponentsTask) => void;
  onCreatedJob: (jobId: string) => void;
}

type When = "now" | "scheduled" | "template";

const SELECT_CLS =
  "h-8.5 w-full rounded-lg border border-input bg-background px-3 text-sm text-foreground outline-none focus:ring-2 focus:ring-ring";

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="block space-y-1.5">
      <span className="block text-xs font-medium text-muted-foreground">{label}</span>
      {children}
    </label>
  );
}

export function TaskPanel({ agentId, projectId, onCreatedTask, onCreatedJob }: Props) {
  const { t } = useI18n();
  const [name, setName] = useState("");
  const [instruction, setInstruction] = useState("");
  const [when, setWhen] = useState<When>("now");
  const [priority, setPriority] = useState<"routine" | "urgent">("routine");
  const [schedule, setSchedule] = useState<ScheduleValue>(emptySchedule);
  const [sessionMode, setSessionMode] = useState("reuse");
  const [templateKey, setTemplateKey] = useState<string | null>(null);
  const [enabled, setEnabled] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const { data: templates = [] } = useQuery({
    ...jobTemplatesQueryOptions,
    enabled: when === "template",
  });

  const pickTemplate = (key: string, defaultSchedule: string, mode: string) => {
    setTemplateKey(key);
    setSchedule(scheduleFromString(defaultSchedule));
    setSessionMode(mode);
  };

  const valid = (() => {
    if (when === "now") return !!name.trim() && !!instruction.trim();
    if (when === "template") return !!templateKey && isScheduleValid(schedule);
    return !!name.trim() && !!instruction.trim() && isScheduleValid(schedule);
  })();

  const create = useCallback(async () => {
    if (!valid) return;
    setSaving(true);
    setError(null);
    try {
      if (when === "now") {
        const { data: task } = await createTask({
          body: {
            title: name.trim(),
            description: instruction.trim() || undefined,
            priority,
            agent_id: agentId,
            project_id: projectId,
          },
          throwOnError: true,
        });
        onCreatedTask(task);
        return;
      }
      const payload: ComponentsJobInput =
        when === "template"
          ? {
              template_key: templateKey ?? undefined,
              cron: schedule.cron,
              every: schedule.every,
              at: schedule.at,
              session_mode: sessionMode,
              enabled,
              agent_id: agentId,
            }
          : {
              name: name.trim(),
              message: instruction.trim(),
              cron: schedule.cron,
              every: schedule.every,
              at: schedule.at,
              session_mode: sessionMode,
              enabled,
              agent_id: agentId,
            };
      const { data } = await createSchedulerJob({
        path: { agentId },
        body: payload,
        throwOnError: true,
      });
      if (data) onCreatedJob((data as SchedulerJob).id);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Request failed");
    } finally {
      setSaving(false);
    }
  }, [
    valid,
    when,
    name,
    instruction,
    priority,
    schedule,
    sessionMode,
    templateKey,
    enabled,
    agentId,
    projectId,
    onCreatedTask,
    onCreatedJob,
  ]);

  return (
    <div className="flex flex-1 flex-col overflow-hidden">
      <div className="flex-1 space-y-5 overflow-y-auto p-6">
        <div>
          <div className="text-xs font-medium text-muted-foreground">
            {t("sessions.task.eyebrow")}
          </div>
          <h2 className="mt-1.5 font-serif text-2xl italic tracking-tight">
            {t("sessions.task.title")}
          </h2>
          <p className="mt-1 text-sm text-muted-foreground">{t("sessions.task.subtitle")}</p>
        </div>

        <div className="space-y-4">
          <div>
            <span className="mb-1.5 block text-xs font-medium text-muted-foreground">
              {t("schedule.whenLabel")}
            </span>
            <ToggleGroup
              variant="outline"
              value={[when]}
              onValueChange={(v: string[]) => v[0] && setWhen(v[0] as When)}
            >
              <ToggleGroupItem value="now">{t("schedule.whenNow")}</ToggleGroupItem>
              <ToggleGroupItem value="scheduled">{t("schedule.whenScheduled")}</ToggleGroupItem>
              <ToggleGroupItem value="template">{t("schedule.whenTemplate")}</ToggleGroupItem>
            </ToggleGroup>
            {when === "now" && (
              <p className="mt-1.5 text-xs text-muted-foreground">{t("schedule.whenNowHint")}</p>
            )}
          </div>

          {when === "template" ? (
            <Field label={t("schedule.pickTemplate")}>
              <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                {templates.map((tpl) => {
                  const subscribed = !!tpl.subscribed_job_id;
                  const active = templateKey === tpl.key;
                  return (
                    <button
                      key={tpl.key}
                      type="button"
                      disabled={subscribed}
                      onClick={() => pickTemplate(tpl.key, tpl.default_schedule, tpl.session_mode)}
                      className={cn(
                        "rounded-xl border px-4 py-3 text-left transition-colors hover:border-foreground/20 disabled:cursor-not-allowed disabled:opacity-60",
                        active ? "border-primary bg-primary/[0.06]" : "border-border",
                      )}
                    >
                      <div className="flex items-center gap-2">
                        <p className="min-w-0 flex-1 truncate text-sm font-semibold">{tpl.name}</p>
                        {subscribed && (
                          <Badge size="sm" variant="secondary">
                            {t("automations.alreadySubscribed")}
                          </Badge>
                        )}
                      </div>
                      <p className="mt-2 line-clamp-2 text-xs leading-relaxed text-muted-foreground">
                        {tpl.description}
                      </p>
                    </button>
                  );
                })}
              </div>
            </Field>
          ) : (
            <>
              <Field label={t("task.create.nameLabel")}>
                <Input
                  nativeInput
                  value={name}
                  onChange={(e) => setName((e.target as HTMLInputElement).value)}
                  placeholder={t("task.create.namePlaceholder")}
                  className="text-sm"
                  autoFocus
                />
              </Field>
              <Field label={t("task.create.instructionLabel")}>
                <Textarea
                  value={instruction}
                  onChange={(e) => setInstruction((e.target as HTMLTextAreaElement).value)}
                  rows={6}
                  placeholder={t("task.create.instructionPlaceholder")}
                  className="text-sm"
                />
              </Field>
            </>
          )}

          {when === "now" && (
            <Field label={t("tasks.fieldPriority")}>
              <select
                value={priority}
                onChange={(e) => setPriority(e.target.value as "routine" | "urgent")}
                className={SELECT_CLS}
              >
                <option value="routine">{t("sessions.task.priorityRoutineDesc")}</option>
                <option value="urgent">{t("sessions.task.priorityUrgentDesc")}</option>
              </select>
            </Field>
          )}

          {(when === "scheduled" || (when === "template" && templateKey)) && (
            <>
              <SchedulePicker value={schedule} onChange={setSchedule} />
              {when === "scheduled" && (
                <Field label={t("hub.sessionMode")}>
                  <select
                    value={sessionMode}
                    onChange={(e) => setSessionMode(e.target.value)}
                    className={SELECT_CLS}
                  >
                    <option value="reuse">{t("scheduler.reuseSession")}</option>
                    <option value="new">{t("scheduler.newSessionEachRun")}</option>
                  </select>
                </Field>
              )}
              <div className="flex items-center gap-2.5">
                <Switch checked={enabled} onCheckedChange={setEnabled} />
                <span className="text-sm">{t("scheduler.enabled")}</span>
              </div>
            </>
          )}

          {error && <p className="text-xs text-destructive">{error}</p>}
        </div>
      </div>

      <div className="flex flex-shrink-0 items-center gap-2 border-t border-border px-6 py-4">
        <Button size="sm" disabled={saving || !valid} onClick={() => void create()}>
          {saving ? t("sessions.task.creating") : t("sessions.task.createBtn")}
        </Button>
      </div>
    </div>
  );
}

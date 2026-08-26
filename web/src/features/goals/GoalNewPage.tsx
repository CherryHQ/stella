import { useCallback, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { createSchedulerJob } from "@/lib/api-client";
import type { ComponentsJobInput } from "@/lib/api-client/types.gen";
import type { SchedulerJob } from "@/lib/types";
import { jobTemplatesQueryOptions } from "@/lib/queries/scheduler";
import { GoalForm } from "@/features/goals/GoalForm";
import {
  SchedulePicker,
  isScheduleValid,
  emptySchedule,
  scheduleFromString,
  type ScheduleValue,
} from "@/features/goals/SchedulePicker";
import { useI18n } from "@/lib/i18n";
import { targetValue, cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Switch } from "@/components/ui/switch";
import { Badge } from "@/components/ui/badge";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";

type Mode = "goal" | "schedule";
type When = "scheduled" | "template";

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

export function GoalNewPage() {
  const { t } = useI18n();
  const { agentId } = useParams({ from: "/_app/agents/$agentId/goals/new" });
  const { project_id: projectId } = useSearch({ from: "/_app/agents/$agentId/goals/new" });
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [mode, setMode] = useState<Mode>("goal");
  // SAFETY: the ToggleGroup items are the Mode values, so its callback emits a Mode.
  const onModeChange = (v: string[]) => v[0] && setMode(v[0] as Mode);

  return (
    <div className="mx-auto flex h-full w-full max-w-[640px] flex-col overflow-y-auto px-6 py-8">
      <div className="mb-5">
        <ToggleGroup variant="outline" value={[mode]} onValueChange={onModeChange}>
          <ToggleGroupItem value="goal">{t("goals.new")}</ToggleGroupItem>
          <ToggleGroupItem value="schedule">{t("hub.newSchedule")}</ToggleGroupItem>
        </ToggleGroup>
      </div>

      {mode === "goal" ? (
        <GoalForm
          agentId={agentId}
          projectId={projectId}
          onCreated={(d) => {
            void queryClient.invalidateQueries({ queryKey: ["goals-page"] });
            void queryClient.invalidateQueries({ queryKey: ["goals-counts"] });
            void navigate({
              to: "/agents/$agentId/goals/$goalId",
              params: { agentId, goalId: d.id },
            });
          }}
        />
      ) : (
        <ScheduleForm
          agentId={agentId}
          onCreated={(jobId) => {
            void queryClient.invalidateQueries({ queryKey: ["agent-scheduler-jobs"] });
            void navigate({
              to: "/agents/$agentId/goals/schedules/$scheduleId",
              params: { agentId, scheduleId: jobId },
            });
          }}
        />
      )}
    </div>
  );
}

/** Recurring scheduler-job creation — a custom job or a template subscription. */
function ScheduleForm({
  agentId,
  onCreated,
}: {
  agentId: string;
  onCreated: (jobId: string) => void;
}) {
  const { t } = useI18n();
  const [when, setWhen] = useState<When>("scheduled");
  // SAFETY: the ToggleGroup items are the When values, so its callback emits a When.
  const onWhenChange = (v: string[]) => v[0] && setWhen(v[0] as When);
  const [name, setName] = useState("");
  const [instruction, setInstruction] = useState("");
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

  const pickTemplate = (key: string, defaultSchedule: string, smode: string) => {
    setTemplateKey(key);
    setSchedule(scheduleFromString(defaultSchedule));
    setSessionMode(smode);
  };

  const valid =
    when === "template"
      ? !!templateKey && isScheduleValid(schedule)
      : !!name.trim() && !!instruction.trim() && isScheduleValid(schedule);

  const create = useCallback(async () => {
    if (!valid) return;
    setSaving(true);
    setError(null);
    try {
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
      // SAFETY: createSchedulerJob returns the created job under data, whose id the form needs.
      const jobId = (data as SchedulerJob | undefined)?.id;
      if (jobId) onCreated(jobId);
    } catch (e) {
      setError(e instanceof Error ? e.message : t("common.error"));
    } finally {
      setSaving(false);
    }
  }, [
    valid,
    when,
    name,
    instruction,
    schedule,
    sessionMode,
    templateKey,
    enabled,
    agentId,
    onCreated,
    t,
  ]);

  return (
    <div className="space-y-4">
      <ToggleGroup variant="outline" value={[when]} onValueChange={onWhenChange}>
        <ToggleGroupItem value="scheduled">{t("schedule.whenScheduled")}</ToggleGroupItem>
        <ToggleGroupItem value="template">{t("schedule.whenTemplate")}</ToggleGroupItem>
      </ToggleGroup>

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
          <Field label={t("schedule.create.nameLabel")}>
            <Input
              nativeInput
              value={name}
              onChange={(e) => setName(targetValue(e))}
              placeholder={t("schedule.create.namePlaceholder")}
              className="text-sm"
              autoFocus
            />
          </Field>
          <Field label={t("schedule.create.instructionLabel")}>
            <Textarea
              value={instruction}
              onChange={(e) => setInstruction(targetValue(e))}
              rows={6}
              placeholder={t("schedule.create.instructionPlaceholder")}
              className="text-sm"
            />
          </Field>
        </>
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

      {error && <p className="text-xs text-destructive-foreground">{error}</p>}

      <div className="flex items-center gap-2 pt-1">
        <Button size="sm" disabled={saving || !valid} onClick={() => void create()}>
          {saving ? t("schedule.create.creating") : t("schedule.create.createBtn")}
        </Button>
      </div>
    </div>
  );
}

import { useCallback, useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { createSchedulerJob, deleteSchedulerJob, updateSchedulerJob } from "@/lib/api-client";
import type { ComponentsJobInput, ComponentsJobTemplate } from "@/lib/api-client/types.gen";
import type { SchedulerJob } from "@/lib/types";
import { jobTemplatesQueryOptions } from "@/lib/queries/scheduler";
import { useI18n } from "@/lib/i18n";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Switch } from "@/components/ui/switch";
import { Badge } from "@/components/ui/badge";
import { Radio, RadioGroup } from "@/components/ui/radio-group";
import { Dialog, DialogPopup, DialogTitle } from "@/components/ui/dialog";
import { Sheet, SheetPopup, SheetHeader, SheetFooter, SheetTitle } from "@/components/ui/sheet";
import {
  SchedulePicker,
  scheduleFromString,
  isScheduleValid,
  emptySchedule,
  type ScheduleValue,
} from "./SchedulePicker";

interface JobForm {
  name: string;
  message: string;
  session_mode: string;
  enabled: boolean;
  schedule: ScheduleValue;
}

const emptyForm = (): JobForm => ({
  name: "",
  message: "",
  session_mode: "reuse",
  enabled: true,
  schedule: emptySchedule(),
});

function formFromJob(j: SchedulerJob): JobForm {
  return {
    name: j.name,
    message: j.message,
    schedule: { cron: j.cron || "", every: j.every || "", at: j.at || "" },
    session_mode: j.session_mode || "reuse",
    enabled: j.enabled,
  };
}

interface ScheduleSheetProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** null = create mode */
  job: SchedulerJob | null;
  agentId: string;
  onCreated?: (jobId: string) => void;
  onDeleted?: () => void;
}

/** Create/edit form for a scheduled job, demoted to a side sheet. Delete lives inside. */
export function ScheduleSheet({
  open,
  onOpenChange,
  job,
  agentId,
  onCreated,
  onDeleted,
}: ScheduleSheetProps) {
  const { t } = useI18n();
  const qc = useQueryClient();

  // "custom" or a template key — only relevant in create mode
  const [source, setSource] = useState<string>("custom");
  const [form, setForm] = useState<JobForm>(job ? formFromJob(job) : emptyForm());
  const [saving, setSaving] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const effectiveAgentId = job?.agent_id || agentId;
  const isSubscription = !!job?.template_key;

  // Fetch templates only in create mode (job === null) and when sheet is open
  const { data: templates = [] } = useQuery({
    ...jobTemplatesQueryOptions,
    enabled: open && job === null,
  });

  useEffect(() => {
    if (open) {
      setSource("custom");
      setForm(job ? formFromJob(job) : emptyForm());
      setError(null);
    }
  }, [open, job]);

  // When a template is selected, pre-fill schedule from its default_schedule
  const handleSelectTemplate = useCallback((tpl: ComponentsJobTemplate) => {
    if (tpl.subscribed_job_id) return; // already subscribed — disabled
    setSource(tpl.key);
    setForm((f) => ({
      ...f,
      schedule: scheduleFromString(tpl.default_schedule),
      session_mode: tpl.session_mode,
    }));
  }, []);

  const invalidate = useCallback(() => {
    void qc.invalidateQueries({ queryKey: ["agent-scheduler-jobs"] });
    void qc.invalidateQueries({ queryKey: ["job-templates"] });
    if (job)
      void qc.invalidateQueries({ queryKey: ["scheduler-job-runs", effectiveAgentId, job.id] });
  }, [qc, effectiveAgentId, job]);

  // For custom mode: name + message + schedule required. For template mode: only schedule required.
  const isFormValid = (() => {
    if (!isScheduleValid(form.schedule)) return false;
    if (job) {
      // edit mode
      return !!form.name;
    }
    if (source === "custom") {
      return !!form.name && !!form.message;
    }
    // template mode — name/message come from template
    return true;
  })();

  const handleSave = useCallback(async () => {
    let payload: ComponentsJobInput;

    if (job) {
      // Edit mode — never send template_key on update
      payload = {
        name: form.name,
        message: isSubscription ? undefined : form.message,
        cron: form.schedule.cron,
        every: form.schedule.every,
        at: form.schedule.at,
        session_mode: form.session_mode,
        enabled: form.enabled,
        agent_id: effectiveAgentId,
      };
    } else if (source === "custom") {
      payload = {
        name: form.name,
        message: form.message,
        cron: form.schedule.cron,
        every: form.schedule.every,
        at: form.schedule.at,
        session_mode: form.session_mode,
        enabled: form.enabled,
        agent_id: effectiveAgentId,
      };
    } else {
      // Template subscription
      payload = {
        template_key: source,
        cron: form.schedule.cron,
        every: form.schedule.every,
        at: form.schedule.at,
        session_mode: form.session_mode,
        enabled: form.enabled,
        agent_id: effectiveAgentId,
      };
    }

    setSaving(true);
    setError(null);
    try {
      if (job) {
        await updateSchedulerJob({
          path: { agentId: effectiveAgentId, jobId: job.id },
          body: payload,
          throwOnError: true,
        });
      } else {
        const { data } = await createSchedulerJob({
          path: { agentId: effectiveAgentId },
          body: payload,
          throwOnError: true,
        });
        if (data && onCreated) onCreated((data as SchedulerJob).id);
      }
      invalidate();
      onOpenChange(false);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Request failed");
    } finally {
      setSaving(false);
    }
  }, [form, job, source, isSubscription, effectiveAgentId, invalidate, onCreated, onOpenChange]);

  const handleDelete = useCallback(async () => {
    if (!job) return;
    setSaving(true);
    setError(null);
    try {
      await deleteSchedulerJob({
        path: { agentId: effectiveAgentId, jobId: job.id },
        throwOnError: true,
      });
      invalidate();
      onOpenChange(false);
      onDeleted?.();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Request failed");
    } finally {
      setSaving(false);
    }
  }, [job, effectiveAgentId, invalidate, onOpenChange, onDeleted]);

  const up = (patch: Partial<JobForm>) => setForm((f) => ({ ...f, ...patch }));

  const isTemplateMode = !job && source !== "custom";

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetPopup side="right" className="max-w-sm">
        <SheetHeader>
          <SheetTitle>{job ? t("hub.editSchedule") : t("hub.newSchedule")}</SheetTitle>
        </SheetHeader>
        <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-4 py-4">
          {/* Source selector — create mode only, when templates are available */}
          {!job && templates.length > 0 && (
            <div>
              <RadioGroup
                className="mb-2 flex-row items-center gap-4"
                value={source === "custom" ? "custom" : "template"}
                onValueChange={(v) => {
                  if (v === "custom") {
                    setSource("custom");
                    setForm(emptyForm());
                    return;
                  }
                  // Select first non-subscribed template if any
                  const first = templates.find((tpl) => !tpl.subscribed_job_id);
                  if (first) handleSelectTemplate(first);
                }}
              >
                <label className="flex cursor-pointer items-center gap-2 text-sm">
                  <Radio value="custom" />
                  {t("automations.sourceCustom")}
                </label>
                <label className="flex cursor-pointer items-center gap-2 text-sm">
                  <Radio value="template" />
                  {t("automations.sourceTemplate")}
                </label>
              </RadioGroup>

              {/* Template cards — visible when template source is selected */}
              {source !== "custom" && (
                <div className="flex flex-col gap-2">
                  {templates.map((tpl) => {
                    const selected = source === tpl.key;
                    const subscribed = !!tpl.subscribed_job_id;
                    return (
                      <button
                        key={tpl.key}
                        type="button"
                        disabled={subscribed}
                        onClick={() => handleSelectTemplate(tpl)}
                        className={`rounded-xl border px-3 py-2.5 text-left text-[13px] transition-colors ${
                          selected
                            ? "border-primary bg-primary/5"
                            : subscribed
                              ? "cursor-not-allowed border-border bg-muted/30 opacity-60"
                              : "border-border hover:bg-muted/40"
                        }`}
                      >
                        <div className="flex items-center gap-2">
                          <span className="font-medium">{tpl.name}</span>
                          {subscribed && (
                            <Badge size="sm" variant="secondary">
                              {t("automations.alreadySubscribed")}
                            </Badge>
                          )}
                        </div>
                        <div className="mt-0.5 text-muted-foreground">{tpl.description}</div>
                        <div className="mt-1 font-mono text-xs text-muted-foreground">
                          {t("automations.templateDefaultSchedule", {
                            schedule: tpl.default_schedule,
                          })}
                        </div>
                      </button>
                    );
                  })}
                </div>
              )}
            </div>
          )}

          {/* Name field — hidden in template mode (server fills it from template) */}
          {!isTemplateMode && (
            <Field label={t("hub.name")}>
              <Input
                type="text"
                value={form.name}
                onChange={(e) => up({ name: e.target.value })}
                placeholder={t("scheduler.dailySummary")}
                nativeInput
              />
            </Field>
          )}

          <Field label={t("automations.scheduleField")}>
            <SchedulePicker value={form.schedule} onChange={(s) => up({ schedule: s })} />
          </Field>
          <Field label={t("hub.sessionMode")}>
            <select
              value={form.session_mode}
              onChange={(e) => up({ session_mode: e.target.value })}
              className="h-8.5 w-full rounded-lg border border-input bg-background px-3 text-sm text-foreground outline-none focus:ring-2 focus:ring-ring"
            >
              <option value="reuse">{t("scheduler.reuseSession")}</option>
              <option value="new">{t("scheduler.newSessionEachRun")}</option>
            </select>
          </Field>

          {/* Message field — hidden in template mode; disabled for subscription edits */}
          {!isTemplateMode && (
            <Field label={t("automations.messageField")}>
              {isSubscription && (
                <p className="mb-1.5 text-xs text-muted-foreground">
                  {t("automations.templateMessageReadOnly")}
                </p>
              )}
              <Textarea
                value={form.message}
                onChange={(e) => up({ message: e.target.value })}
                placeholder={t("automations.messagePlaceholder")}
                className="min-h-32"
                disabled={isSubscription}
              />
            </Field>
          )}

          <div className="flex items-center gap-2.5">
            <Switch checked={form.enabled} onCheckedChange={(v) => up({ enabled: v })} />
            <span className="text-sm">{t("scheduler.enabled")}</span>
          </div>
          {error && <p className="text-xs text-destructive-foreground">{error}</p>}
          {job && (
            <div className="border-t border-border pt-4">
              <Button
                variant="ghost"
                size="sm"
                className="text-destructive-foreground hover:text-destructive-foreground"
                onClick={() => setConfirmDelete(true)}
              >
                {t("hub.deleteSchedule")}
              </Button>
            </div>
          )}
        </div>
        <SheetFooter>
          <Button variant="outline" size="sm" onClick={() => onOpenChange(false)}>
            {t("common.cancel")}
          </Button>
          <Button size="sm" disabled={!isFormValid} loading={saving} onClick={handleSave}>
            {job ? t("common.save") : t("common.create")}
          </Button>
        </SheetFooter>

        <Dialog open={confirmDelete} onOpenChange={setConfirmDelete}>
          <DialogPopup className="max-w-sm">
            <DialogTitle>{t("hub.deleteConfirm")}</DialogTitle>
            <div className="mt-4 flex justify-end gap-2">
              <Button variant="outline" size="sm" onClick={() => setConfirmDelete(false)}>
                {t("common.cancel")}
              </Button>
              <Button
                variant="destructive"
                size="sm"
                onClick={() => {
                  setConfirmDelete(false);
                  void handleDelete();
                }}
              >
                {t("common.delete")}
              </Button>
            </div>
          </DialogPopup>
        </Dialog>
      </SheetPopup>
    </Sheet>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <label className="mb-1.5 block text-xs font-medium text-muted-foreground">{label}</label>
      {children}
    </div>
  );
}

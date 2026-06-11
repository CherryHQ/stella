import { useCallback, useEffect, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { createSchedulerJob, deleteSchedulerJob, updateSchedulerJob } from "@/lib/api-client";
import type { ComponentsJobInput } from "@/lib/api-client/types.gen";
import type { SchedulerJob } from "@/lib/types";
import { useI18n } from "@/lib/i18n";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Switch } from "@/components/ui/switch";
import { Dialog, DialogPopup, DialogTitle } from "@/components/ui/dialog";
import { Sheet, SheetPopup, SheetHeader, SheetFooter, SheetTitle } from "@/components/ui/sheet";

interface JobForm {
  name: string;
  cron: string;
  every: string;
  message: string;
  session_mode: string;
  enabled: boolean;
  schedule_type: "cron" | "every";
}

const emptyForm = (): JobForm => ({
  name: "",
  cron: "",
  every: "",
  message: "",
  session_mode: "reuse",
  enabled: true,
  schedule_type: "cron",
});

function formFromJob(j: SchedulerJob): JobForm {
  return {
    name: j.name,
    message: j.message,
    schedule_type: j.cron ? "cron" : "every",
    cron: j.cron || "",
    every: j.every || "",
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
  const [form, setForm] = useState<JobForm>(job ? formFromJob(job) : emptyForm());
  const [saving, setSaving] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const effectiveAgentId = job?.agent_id || agentId;

  useEffect(() => {
    if (open) {
      setForm(job ? formFromJob(job) : emptyForm());
      setError(null);
    }
  }, [open, job]);

  const invalidate = useCallback(() => {
    void qc.invalidateQueries({ queryKey: ["agent-scheduler-jobs"] });
    if (job)
      void qc.invalidateQueries({ queryKey: ["scheduler-job-runs", effectiveAgentId, job.id] });
  }, [qc, effectiveAgentId, job]);

  const isFormValid =
    form.name && form.message && (form.schedule_type === "cron" ? !!form.cron : !!form.every);

  const handleSave = useCallback(async () => {
    const payload: ComponentsJobInput = {
      name: form.name,
      message: form.message,
      cron: form.schedule_type === "cron" ? form.cron : "",
      every: form.schedule_type === "every" ? form.every : "",
      session_mode: form.session_mode,
      enabled: form.enabled,
      agent_id: effectiveAgentId,
    };
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
  }, [form, job, effectiveAgentId, invalidate, onCreated, onOpenChange]);

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

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetPopup side="right" className="max-w-sm">
        <SheetHeader>
          <SheetTitle>{job ? t("hub.editSchedule") : t("hub.newSchedule")}</SheetTitle>
        </SheetHeader>
        <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-4 py-4">
          <Field label={t("hub.name")}>
            <Input
              type="text"
              value={form.name}
              onChange={(e) => up({ name: e.target.value })}
              placeholder={t("scheduler.dailySummary")}
              nativeInput
            />
          </Field>
          <Field label={t("automations.scheduleField")}>
            <div className="mb-2 flex items-center gap-4">
              <label className="flex cursor-pointer items-center gap-2 text-sm">
                <input
                  type="radio"
                  name="schedule_type"
                  value="cron"
                  checked={form.schedule_type === "cron"}
                  onChange={() => up({ schedule_type: "cron" })}
                  className="accent-primary"
                />
                {t("automations.cronLabel")}
              </label>
              <label className="flex cursor-pointer items-center gap-2 text-sm">
                <input
                  type="radio"
                  name="schedule_type"
                  value="every"
                  checked={form.schedule_type === "every"}
                  onChange={() => up({ schedule_type: "every" })}
                  className="accent-primary"
                />
                {t("automations.intervalLabel")}
              </label>
            </div>
            {form.schedule_type === "cron" ? (
              <Input
                type="text"
                value={form.cron}
                onChange={(e) => up({ cron: e.target.value })}
                placeholder="0 9 * * 1-5"
                className="font-mono"
                nativeInput
              />
            ) : (
              <Input
                type="text"
                value={form.every}
                onChange={(e) => up({ every: e.target.value })}
                placeholder="30m, 2h"
                className="font-mono"
                nativeInput
              />
            )}
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
          <Field label={t("automations.messageField")}>
            <Textarea
              value={form.message}
              onChange={(e) => up({ message: e.target.value })}
              placeholder={t("automations.messagePlaceholder")}
              className="min-h-32"
            />
          </Field>
          <div className="flex items-center gap-2.5">
            <Switch checked={form.enabled} onCheckedChange={(v) => up({ enabled: v })} />
            <span className="text-sm">{t("scheduler.enabled")}</span>
          </div>
          {error && <p className="text-xs text-destructive">{error}</p>}
          {job && (
            <div className="border-t border-border pt-4">
              <Button
                variant="ghost"
                size="sm"
                className="text-destructive hover:text-destructive"
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

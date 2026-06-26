import { useCallback, useEffect, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { scheduleWorkflow } from "@/lib/api-client";
import { useI18n } from "@/lib/i18n";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Dialog, DialogPopup, DialogTitle } from "@/components/ui/dialog";
import {
  SchedulePicker,
  emptySchedule,
  isScheduleValid,
  type ScheduleValue,
} from "@/features/goals/SchedulePicker";

interface ScheduleWorkflowDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  workflowId: string;
  /** Seeds the job name; usually the workflow name. */
  defaultName: string;
  onScheduled?: () => void;
}

/** Blocking form to schedule a workflow: the dispatcher instantiates a fresh
 * goal tree on each fire. Name + exactly one of cron/every/at. */
export function ScheduleWorkflowDialog({
  open,
  onOpenChange,
  workflowId,
  defaultName,
  onScheduled,
}: ScheduleWorkflowDialogProps) {
  const { t } = useI18n();
  const qc = useQueryClient();
  const [name, setName] = useState(defaultName);
  const [schedule, setSchedule] = useState<ScheduleValue>(emptySchedule());
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (open) {
      setName(defaultName);
      setSchedule(emptySchedule());
      setError(null);
    }
  }, [open, defaultName]);

  const valid = !!name && isScheduleValid(schedule);

  const handleSave = useCallback(async () => {
    setSaving(true);
    setError(null);
    try {
      await scheduleWorkflow({
        path: { id: workflowId },
        body: {
          name,
          cron: schedule.cron || undefined,
          every: schedule.every || undefined,
          at: schedule.at || undefined,
        },
        throwOnError: true,
      });
      void qc.invalidateQueries({ queryKey: ["agent-scheduler-jobs"] });
      onOpenChange(false);
      onScheduled?.();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Request failed");
    } finally {
      setSaving(false);
    }
  }, [workflowId, name, schedule, qc, onOpenChange, onScheduled]);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogPopup className="max-w-md">
        <DialogTitle>{t("workflows.scheduleTitle")}</DialogTitle>
        <div className="mt-4 flex flex-col gap-4">
          <div>
            <label className="mb-1.5 block text-xs font-medium text-muted-foreground">
              {t("hub.name")}
            </label>
            <Input
              type="text"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={defaultName}
              nativeInput
            />
          </div>
          <div>
            <label className="mb-1.5 block text-xs font-medium text-muted-foreground">
              {t("automations.scheduleField")}
            </label>
            <SchedulePicker value={schedule} onChange={setSchedule} />
          </div>
          {error && <p className="text-xs text-destructive">{error}</p>}
        </div>
        <div className="mt-5 flex justify-end gap-2">
          <Button variant="outline" size="sm" onClick={() => onOpenChange(false)}>
            {t("common.cancel")}
          </Button>
          <Button size="sm" disabled={!valid} loading={saving} onClick={handleSave}>
            {t("workflows.schedule")}
          </Button>
        </div>
      </DialogPopup>
    </Dialog>
  );
}

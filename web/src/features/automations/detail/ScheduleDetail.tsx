import { useCallback, useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  createSchedulerJob,
  deleteSchedulerJob,
  triggerSchedulerJob,
  updateSchedulerJob,
} from "@/lib/api-client/sdk.gen";
import type { ComponentsJobInput } from "@/lib/api-client/types.gen";
import type { SchedulerJob } from "@/lib/types";
import { schedulerJobRunsOptions } from "../queries";
import { useI18n } from "@/lib/i18n";
import { formatTime } from "@/lib/time";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Switch } from "@/components/ui/switch";
import { Badge } from "@/components/ui/badge";
import { Dialog, DialogPopup, DialogTitle } from "@/components/ui/dialog";
import { MarkdownPreview } from "@/components/MarkdownPreview";
import { StatusPill } from "../lib";

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

interface ScheduleDetailProps {
  job: SchedulerJob | null;
  agentId: string;
  mode: "create" | "edit" | "readonly";
  onCreated?: (jobId: string) => void;
  onDeleted?: () => void;
}

export function ScheduleDetail({ job, agentId, mode, onCreated, onDeleted }: ScheduleDetailProps) {
  const { t } = useI18n();
  const qc = useQueryClient();
  const [form, setForm] = useState<JobForm>(job ? formFromJob(job) : emptyForm());
  const [triggering, setTriggering] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [toast, setToast] = useState<{ msg: string; kind: "success" | "error" } | null>(null);

  // Use the job's own agent_id for API calls — it may differ from the route agentId
  const effectiveAgentId = job?.agent_id || agentId;

  useEffect(() => {
    setForm(job ? formFromJob(job) : emptyForm());
  }, [job?.id]);

  const { data: runs = [] } = useQuery(schedulerJobRunsOptions(effectiveAgentId, job?.id ?? ""));

  const showToast = useCallback((msg: string, kind: "success" | "error" = "success") => {
    setToast({ msg, kind });
    setTimeout(() => setToast(null), 3000);
  }, []);

  const invalidate = useCallback(() => {
    void qc.invalidateQueries({ queryKey: ["agent-scheduler-jobs"] });
    if (job)
      void qc.invalidateQueries({
        queryKey: ["scheduler-job-runs", effectiveAgentId, job.id],
      });
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
    try {
      if (mode === "edit" && job) {
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
      showToast(t("hub.saved"));
    } catch (e) {
      showToast(e instanceof Error ? e.message : "Request failed", "error");
    }
  }, [form, mode, job, effectiveAgentId, invalidate, showToast, onCreated, t]);

  const handleDelete = useCallback(async () => {
    if (!job) return;
    try {
      await deleteSchedulerJob({
        path: { agentId: effectiveAgentId, jobId: job.id },
        throwOnError: true,
      });
      invalidate();
      showToast(t("hub.deleted"));
      onDeleted?.();
    } catch (e) {
      showToast(e instanceof Error ? e.message : "Request failed", "error");
    }
  }, [job, effectiveAgentId, invalidate, showToast, onDeleted, t]);

  const handleTrigger = useCallback(async () => {
    if (!job) return;
    setTriggering(true);
    try {
      await triggerSchedulerJob({
        path: { agentId: effectiveAgentId, jobId: job.id },
        throwOnError: true,
      });
      invalidate();
      showToast(t("hub.triggered"));
    } catch (e) {
      showToast(e instanceof Error ? e.message : "Request failed", "error");
    } finally {
      setTriggering(false);
    }
  }, [job, effectiveAgentId, invalidate, showToast, t]);

  const up = (patch: Partial<JobForm>) => setForm((f) => ({ ...f, ...patch }));

  // Read-only mode for plugin/system jobs
  if (mode === "readonly" && job) {
    return (
      <div className="max-w-[680px] px-9 py-7">
        <div className="mb-2 flex items-center gap-1.5">
          <span className="font-mono text-[10px] font-semibold uppercase tracking-[0.1em] text-blue-500">
            {t("hub.chipSchedule")}
          </span>
          <span className="text-[10px] text-border">/</span>
          <StatusPill
            status={job.enabled ? "running" : "draft"}
            label={job.enabled ? t("scheduler.enabled") : t("scheduler.disabled")}
          />
          {job.owner_kind === "plugin" && (
            <Badge size="sm" variant="info">
              plugin:{job.plugin_id}
            </Badge>
          )}
          {job.owner_kind === "system" && (
            <Badge size="sm" variant="secondary">
              system
            </Badge>
          )}
        </div>

        <h2 className="font-serif text-[22px] font-semibold tracking-tight leading-snug">
          {job.name}
        </h2>
        {(job.description || job.message) && (
          <div className="mt-3">
            <MarkdownPreview
              content={job.description || job.message}
              className="[&_ol]:pl-5 [&_ul]:pl-5"
            />
          </div>
        )}

        <div className="mt-5 flex gap-2">
          <Button variant="outline" size="sm" loading={triggering} onClick={handleTrigger}>
            {t("hub.runNow")}
          </Button>
        </div>

        <hr className="my-6 border-border" />

        <div className="grid grid-cols-2 gap-x-6 gap-y-4">
          <PropField label={t("hub.schedule")}>
            <span className="font-mono text-xs">{job.cron || job.every || job.at || "—"}</span>
          </PropField>
          <PropField label={t("hub.owner")}>
            <span className="text-[13px] font-medium">
              {job.owner_kind}:{job.plugin_id || "system"}
            </span>
          </PropField>
          <PropField label={t("hub.sessionMode")}>
            <span className="text-[13px] font-medium">{job.session_mode}</span>
          </PropField>
          <PropField label={t("hub.lastRun")}>
            <span className="font-mono text-xs">
              {job.last_run_at ? formatTime(job.last_run_at) : "—"}
            </span>
          </PropField>
        </div>

        <RunHistory runs={runs} agentId={effectiveAgentId} />
      </div>
    );
  }

  // Create / Edit mode
  return (
    <div className="max-w-[680px] px-9 py-7">
      <div className="mb-2 flex items-center gap-1.5">
        <span className="font-mono text-[10px] font-semibold uppercase tracking-[0.1em] text-blue-500">
          {t("hub.chipSchedule")}
        </span>
        {job && (
          <>
            <span className="text-[10px] text-border">/</span>
            <StatusPill
              status={job.enabled ? "running" : "draft"}
              label={job.enabled ? t("scheduler.enabled") : t("scheduler.disabled")}
            />
          </>
        )}
      </div>
      <h2 className="mb-5 font-serif text-[22px] font-semibold tracking-tight leading-snug">
        {mode === "create" ? t("hub.newAutomation") : job?.name}
      </h2>

      {job && (
        <div className="mb-5 flex gap-2">
          <Button variant="outline" size="sm" loading={triggering} onClick={handleTrigger}>
            {t("hub.runNow")}
          </Button>
          <Button
            variant="ghost"
            size="sm"
            className="text-destructive hover:text-destructive"
            onClick={() => setConfirmDelete(true)}
          >
            {t("common.delete")}
          </Button>
        </div>
      )}

      <hr className="my-5 border-border" />

      {/* Form */}
      <div className="grid grid-cols-1 gap-x-5 gap-y-4 sm:grid-cols-2">
        <FormField label={t("hub.name")}>
          <Input
            type="text"
            value={form.name}
            onChange={(e) => up({ name: e.target.value })}
            placeholder={t("scheduler.dailySummary")}
            nativeInput
          />
        </FormField>
        <FormField label={t("hub.sessionMode")}>
          <select
            value={form.session_mode}
            onChange={(e) => up({ session_mode: e.target.value })}
            className="h-8.5 w-full rounded-lg border border-input bg-background px-3 text-sm text-foreground outline-none focus:ring-2 focus:ring-ring"
          >
            <option value="reuse">{t("scheduler.reuseSession")}</option>
            <option value="new">{t("scheduler.newSessionEachRun")}</option>
          </select>
        </FormField>
      </div>

      <div className="mt-4">
        <FormField label={t("automations.scheduleField")}>
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
        </FormField>
      </div>

      <div className="mt-4">
        <FormField label={t("automations.messageField")}>
          <Textarea
            value={form.message}
            onChange={(e) => up({ message: e.target.value })}
            placeholder={t("automations.messagePlaceholder")}
          />
        </FormField>
      </div>

      <div className="mt-4 flex items-center gap-2.5">
        <Switch checked={form.enabled} onCheckedChange={(v) => up({ enabled: v })} />
        <span className="text-sm">{t("scheduler.enabled")}</span>
      </div>

      <div className="mt-5 flex items-center gap-2 border-t border-border pt-5">
        <Button size="sm" disabled={!isFormValid} onClick={handleSave}>
          {mode === "create" ? t("common.create") : t("common.save")}
        </Button>
      </div>

      {/* Run history (edit mode only) */}
      {mode === "edit" && <RunHistory runs={runs} agentId={effectiveAgentId} />}

      {/* Delete confirm */}
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

      {/* Toast */}
      {toast && (
        <div
          className={cn(
            "fixed bottom-4 right-4 z-50 rounded-lg border px-4 py-3 text-sm shadow-md max-w-sm",
            toast.kind === "error"
              ? "border-destructive/36 bg-destructive/8 text-destructive-foreground"
              : "border-success/36 bg-success/8 text-success-foreground",
          )}
        >
          {toast.msg}
        </div>
      )}
    </div>
  );
}

function RunHistory({
  runs,
  agentId,
}: {
  runs: {
    id: string;
    status: string;
    started_at?: string;
    duration?: string;
    session_id?: string;
    error?: string;
  }[];
  agentId: string;
}) {
  const { t } = useI18n();
  if (!runs.length) return null;

  return (
    <div className="mt-8 border-t border-border pt-6">
      <span className="mb-3 block font-mono text-[10px] font-semibold uppercase tracking-[0.1em] text-muted-foreground">
        {t("hub.recentRuns")}
      </span>
      {runs.slice(0, 10).map((run) => (
        <div
          key={run.id}
          className="flex items-center gap-2.5 border-b border-border/50 py-2 text-xs last:border-0"
        >
          <Badge size="sm" variant={runBadgeVariant(run.status)}>
            {run.status}
          </Badge>
          <span className="font-mono text-[11px] text-muted-foreground">
            {run.started_at ? formatTime(run.started_at) : "—"}
          </span>
          {run.duration && (
            <span className="font-mono text-[11px] text-muted-foreground/60">{run.duration}</span>
          )}
          {run.session_id && (
            <a
              href={`/agents/${encodeURIComponent(agentId)}/sessions/${encodeURIComponent(run.session_id)}`}
              className="text-[11px] text-primary hover:underline"
            >
              session
            </a>
          )}
          {run.error && (
            <span className="max-w-xs truncate text-destructive" title={run.error}>
              {run.error}
            </span>
          )}
        </div>
      ))}
    </div>
  );
}

function PropField({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <div className="mb-1 text-[11px] text-muted-foreground">{label}</div>
      <div>{children}</div>
    </div>
  );
}

function FormField({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <label className="mb-1.5 block font-mono text-[10px] font-semibold uppercase tracking-[0.08em] text-muted-foreground">
        {label}
      </label>
      {children}
    </div>
  );
}

function runBadgeVariant(status: string): "success" | "error" | "warning" | "outline" {
  if (status === "success" || status === "completed") return "success";
  if (status === "error" || status === "failed") return "error";
  if (status === "running") return "warning";
  return "outline";
}

import { useCallback, useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate, useParams } from "@tanstack/react-router";
import { triggerSchedulerJob, updateSchedulerJob } from "@/lib/api-client";
import type { SchedulerJob } from "@/lib/types";
import { agentSchedulerJobsOptions } from "@/lib/queries/agents";
import { useI18n } from "@/lib/i18n";
import { useToast, ToastContainer } from "@/hooks/use-toast";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { schedulerJobRunsOptions } from "@/lib/queries/scheduler";
import { jobNextRunAt } from "./types";
import { StatusPill, formatUntil, humanScheduleText } from "./lib";
import { AgentChip, DetailSection, DetailShell, MetaSep } from "./DetailShell";
import { RunsTimeline } from "./RunsTimeline";
import { ScheduleSheet } from "./ScheduleSheet";

export function SchedulePage() {
  const { t } = useI18n();
  const { agentId, scheduleId } = useParams({ strict: false }) as {
    agentId: string;
    scheduleId: string;
  };
  const navigate = useNavigate();
  const qc = useQueryClient();
  const [editOpen, setEditOpen] = useState(false);
  const [acting, setActing] = useState(false);

  const { toasts, showToast } = useToast();
  const { data: jobs = [], isSuccess } = useQuery(agentSchedulerJobsOptions(agentId));
  const job = useMemo(
    () => (jobs as SchedulerJob[]).find((j) => j.id === scheduleId) ?? null,
    [jobs, scheduleId],
  );
  const effectiveAgentId = job?.agent_id || agentId;
  const { data: runs = [] } = useQuery(schedulerJobRunsOptions(effectiveAgentId, scheduleId));

  const invalidate = useCallback(() => {
    void qc.invalidateQueries({ queryKey: ["agent-scheduler-jobs"] });
    void qc.invalidateQueries({ queryKey: ["scheduler-job-runs", effectiveAgentId, scheduleId] });
  }, [qc, effectiveAgentId, scheduleId]);

  const handleTrigger = useCallback(async () => {
    if (!job) return;
    setActing(true);
    try {
      await triggerSchedulerJob({
        path: { agentId: effectiveAgentId, jobId: job.id },
        throwOnError: true,
      });
      showToast(t("hub.runNowStarted"));
      invalidate();
    } catch {
      showToast(t("hub.runNowFailed"), "error");
    } finally {
      setActing(false);
    }
  }, [job, effectiveAgentId, invalidate, showToast, t]);

  const handleToggleEnabled = useCallback(async () => {
    if (!job) return;
    setActing(true);
    try {
      await updateSchedulerJob({
        path: { agentId: effectiveAgentId, jobId: job.id },
        body: {
          name: job.name,
          message: job.message,
          cron: job.cron || "",
          every: job.every || "",
          at: job.at || "",
          session_mode: job.session_mode,
          enabled: !job.enabled,
          agent_id: effectiveAgentId,
        },
        throwOnError: true,
      });
      invalidate();
    } finally {
      setActing(false);
    }
  }, [job, effectiveAgentId, invalidate]);

  if (isSuccess && !job) {
    return (
      <DetailShell agentId={agentId} kindLabel={t("hub.kindSchedule")} title={t("hub.notFound")}>
        <div />
      </DetailShell>
    );
  }
  if (!job) return null;

  const isSubscription = !!job.template_key;
  const next = jobNextRunAt(job);

  return (
    <DetailShell
      agentId={agentId}
      kindLabel={t("hub.kindSchedule")}
      title={job.name}
      pill={
        <span className="inline-flex items-center gap-1.5">
          <StatusPill
            status={job.enabled ? "active" : "draft"}
            label={job.enabled ? t("scheduler.enabled") : t("scheduler.disabled")}
          />
          {isSubscription && (
            <Badge size="sm" variant="secondary">
              {t("automations.subscriptionBadge", { key: job.template_key })}
            </Badge>
          )}
        </span>
      }
      actions={
        <>
          <Button size="sm" loading={acting} onClick={handleTrigger}>
            {t("hub.runNow")}
          </Button>
          <Button variant="outline" size="sm" loading={acting} onClick={handleToggleEnabled}>
            {job.enabled ? t("hub.pause") : t("hub.resume")}
          </Button>
          <Button variant="outline" size="sm" onClick={() => setEditOpen(true)}>
            {t("common.edit")}
          </Button>
        </>
      }
    >
      <div className="mt-2.5 flex flex-wrap items-center gap-x-3.5 gap-y-1.5 text-[12.5px] text-muted-foreground">
        <span className="font-mono">{humanScheduleText(t, job)}</span>
        {next && (
          <>
            <MetaSep />
            <span>
              {t("hub.nextRunLabel")}:{" "}
              <span className="font-medium text-foreground">{formatUntil(t, next)}</span>
            </span>
          </>
        )}
        <MetaSep />
        <span>
          {job.session_mode === "new"
            ? t("scheduler.newSessionEachRun")
            : t("scheduler.reuseSession")}
        </span>
        <MetaSep />
        <AgentChip agentId={effectiveAgentId} />
      </div>

      <DetailSection title={t("hub.runHistory")}>
        <RunsTimeline
          runs={runs.slice(0, 20).map((r) => ({
            id: r.id,
            status: r.status,
            startedAt: r.started_at,
            duration: r.duration,
            error: r.error,
            output: r.output,
          }))}
        />
      </DetailSection>

      <DetailSection title={t("hub.promptSection")}>
        {isSubscription && (
          <p className="mb-2 text-xs text-muted-foreground">
            {t("automations.templateMessageReadOnly")}
          </p>
        )}
        <div className="whitespace-pre-wrap rounded-xl border border-border bg-muted/40 px-4 py-3.5 text-[13px] leading-relaxed text-muted-foreground">
          {job.message}
        </div>
      </DetailSection>

      <ScheduleSheet
        open={editOpen}
        onOpenChange={setEditOpen}
        job={job}
        agentId={agentId}
        onDeleted={() => navigate({ to: "/agents/$agentId/goals", params: { agentId } })}
      />
      <ToastContainer messages={toasts} />
    </DetailShell>
  );
}

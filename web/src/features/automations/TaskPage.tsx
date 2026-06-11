import { useCallback, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { cancelTask, reopenTask } from "@/lib/api-client";
import { taskRunsOptions } from "@/lib/queries/goals";
import { useI18n } from "@/lib/i18n";
import { formatTime } from "@/lib/time";
import { Button } from "@/components/ui/button";
import { taskOptions } from "./queries";
import { StatusPill, priorityLabel, statusLabel } from "./lib";
import { AgentChip, DetailSection, DetailShell, MetaSep } from "./DetailShell";
import { RunsTimeline } from "./RunsTimeline";

export function TaskPage() {
  const { t } = useI18n();
  const { agentId, taskId } = useParams({ strict: false }) as {
    agentId: string;
    taskId: string;
  };
  const navigate = useNavigate();
  const qc = useQueryClient();
  const [acting, setActing] = useState(false);

  const { data: task, isError } = useQuery(taskOptions(taskId));
  const { data: runs = [] } = useQuery(taskRunsOptions(taskId));

  const invalidate = useCallback(() => {
    void qc.invalidateQueries({ queryKey: ["task", taskId] });
    void qc.invalidateQueries({ queryKey: ["standalone-tasks"] });
    void qc.invalidateQueries({ queryKey: ["task-runs", taskId] });
  }, [qc, taskId]);

  const act = useCallback(
    async (fn: () => Promise<unknown>) => {
      setActing(true);
      try {
        await fn();
        invalidate();
      } finally {
        setActing(false);
      }
    },
    [invalidate],
  );

  if (isError) {
    return (
      <DetailShell agentId={agentId} kindLabel={t("hub.kindTask")} title={t("hub.notFound")}>
        <div />
      </DetailShell>
    );
  }
  if (!task) return null;

  const taskAgentId = task.agent_id || agentId;
  const lastFailedRun = runs.find((r) => r.status === "failed" || r.status === "timed_out");
  const isClosed = ["done", "failed", "cancelled"].includes(task.status);

  return (
    <DetailShell
      agentId={agentId}
      kindLabel={t("hub.kindTask")}
      title={task.title}
      pill={<StatusPill status={task.status} label={statusLabel(t, task.status)} />}
      actions={
        <>
          {task.status === "failed" && (
            <Button
              size="sm"
              loading={acting}
              onClick={() =>
                act(() => reopenTask({ path: { taskId: task.id }, throwOnError: true }))
              }
            >
              {t("hub.retry")}
            </Button>
          )}
          {task.session_id && (
            <Button
              variant="outline"
              size="sm"
              onClick={() =>
                navigate({
                  to: "/agents/$agentId/sessions/$sessionId",
                  params: { agentId: taskAgentId, sessionId: task.session_id },
                })
              }
            >
              {t("hub.openSession")}
            </Button>
          )}
          {isClosed && task.status !== "failed" && (
            <Button
              variant="outline"
              size="sm"
              loading={acting}
              onClick={() =>
                act(() => reopenTask({ path: { taskId: task.id }, throwOnError: true }))
              }
            >
              {t("hub.reopen")}
            </Button>
          )}
          {!isClosed && (
            <Button
              variant="ghost"
              size="sm"
              loading={acting}
              onClick={() =>
                act(() => cancelTask({ path: { taskId: task.id }, throwOnError: true }))
              }
            >
              {t("common.cancel")}
            </Button>
          )}
        </>
      }
    >
      <div className="mt-2.5 flex flex-wrap items-center gap-x-3.5 gap-y-1.5 text-[12.5px] text-muted-foreground">
        <AgentChip agentId={taskAgentId} />
        <MetaSep />
        <span>{priorityLabel(t, task.priority)}</span>
        <MetaSep />
        <span>{t("hub.createdAt", { time: formatTime(task.created_at) })}</span>
        <MetaSep />
        <span>
          {task.status === "failed"
            ? t("hub.failedAt", { time: formatTime(task.updated_at) })
            : t("hub.updatedAt", { time: formatTime(task.updated_at) })}
        </span>
        {task.retry_count > 0 && (
          <>
            <MetaSep />
            <span>
              {t("hub.retries")}: {task.retry_count}/{task.max_retries}
            </span>
          </>
        )}
        {task.goal_id && (
          <>
            <MetaSep />
            <Link
              to="/agents/$agentId/tasks/goals/$goalId"
              params={{ agentId, goalId: task.goal_id }}
              className="font-medium text-primary hover:underline"
            >
              {t("hub.parentGoal")} →
            </Link>
          </>
        )}
      </div>

      {task.status === "failed" && lastFailedRun?.error && (
        <DetailSection title={t("hub.failureReason")}>
          <div className="whitespace-pre-wrap rounded-xl border border-destructive/25 bg-destructive/[0.06] px-4 py-3 font-mono text-[11.5px] leading-relaxed text-destructive">
            {lastFailedRun.error}
          </div>
        </DetailSection>
      )}

      {runs.length > 0 && (
        <DetailSection title={t("hub.runHistory")}>
          <RunsTimeline
            agentId={taskAgentId}
            runs={runs.slice(0, 20).map((r) => ({
              id: r.id,
              status: r.status,
              startedAt: r.started_at,
              error: r.error,
              sessionId: r.session_id,
            }))}
          />
        </DetailSection>
      )}

      {task.description && (
        <DetailSection title={t("hub.description")}>
          <div className="whitespace-pre-wrap rounded-xl border border-border bg-muted/40 px-4 py-3.5 text-[13px] leading-relaxed text-muted-foreground">
            {task.description}
          </div>
        </DetailSection>
      )}
    </DetailShell>
  );
}

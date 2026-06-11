import { useCallback, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import { cancelTask, reopenTask, resolveTaskBlocker, waiveTaskDep } from "@/lib/api-client";
import { taskRunsOptions } from "@/lib/queries/goals";
import type { TFunction } from "i18next";
import { useI18n } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/messages";
import { formatTime } from "@/lib/time";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { MarkdownPreview } from "@/components/MarkdownPreview";
import { taskBlockerOptions, taskDepsOptions, taskOptions } from "./queries";
import { StatusPill, priorityLabel, statusLabel } from "./lib";
import { AgentChip, DetailSection, DetailShell, MetaSep } from "./DetailShell";
import { RunsTimeline } from "./RunsTimeline";

const BLOCKER_KIND_KEYS: Record<string, MessageKey> = {
  user_input: "hub.blockerKindUserInput",
  external_dependency: "hub.blockerKindExternalDependency",
  tool_error: "hub.blockerKindToolError",
  policy_hold: "hub.blockerKindPolicyHold",
  dep_failure: "hub.blockerKindDepFailure",
};

function blockerKindLabel(t: TFunction, kind: string): string {
  const key = BLOCKER_KIND_KEYS[kind];
  return key ? t(key) : kind;
}

export function TaskPage() {
  const { t } = useI18n();
  const { agentId, taskId } = useParams({ strict: false }) as {
    agentId: string;
    taskId: string;
  };
  const qc = useQueryClient();
  const [acting, setActing] = useState(false);
  const [answer, setAnswer] = useState("");
  const [waiveReason, setWaiveReason] = useState("");

  const { data: task, isError } = useQuery(taskOptions(taskId));
  const { data: runs = [] } = useQuery(taskRunsOptions(taskId));
  const isBlocked = task?.status === "blocked";
  const { data: blocker } = useQuery(
    taskBlockerOptions(taskId, isBlocked ? task?.active_blocker_id : undefined),
  );
  const isDepFailure = blocker?.kind === "dep_failure";
  const { data: deps = [] } = useQuery(taskDepsOptions(taskId, isBlocked && isDepFailure));

  const invalidate = useCallback(() => {
    void qc.invalidateQueries({ queryKey: ["task", taskId] });
    void qc.invalidateQueries({ queryKey: ["standalone-tasks"] });
    void qc.invalidateQueries({ queryKey: ["task-runs", taskId] });
    void qc.invalidateQueries({ queryKey: ["task-blocker", taskId] });
    void qc.invalidateQueries({ queryKey: ["task-deps", taskId] });
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
              render={
                <Link
                  to="/agents/$agentId/sessions/$sessionId"
                  params={{ agentId: taskAgentId, sessionId: task.session_id }}
                />
              }
              variant="outline"
              size="sm"
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

      {isBlocked && blocker && (
        <DetailSection title={t("hub.blockedReason")}>
          <div className="rounded-xl border border-chart-4/30 bg-chart-4/[0.06] px-4 py-3.5">
            <p className="text-xs font-medium uppercase tracking-wide text-chart-4">
              {blockerKindLabel(t, blocker.kind)}
            </p>
            {blocker.question && (
              <MarkdownPreview
                content={blocker.question}
                className="mt-2 text-[13px] [&_ol]:pl-5 [&_ul]:pl-5"
              />
            )}
            {!isDepFailure && (
              <div className="mt-3 space-y-2">
                <Textarea
                  value={answer}
                  onChange={(e) => setAnswer(e.target.value)}
                  placeholder={t("hub.blockerAnswerPlaceholder")}
                  rows={3}
                />
                <Button
                  size="sm"
                  loading={acting}
                  disabled={!answer.trim()}
                  onClick={() =>
                    act(async () => {
                      await resolveTaskBlocker({
                        path: { taskId: task.id, blockerId: blocker.id },
                        body: { resolution: answer.trim() },
                        throwOnError: true,
                      });
                      setAnswer("");
                    })
                  }
                >
                  {t("hub.resolveBlocker")}
                </Button>
              </div>
            )}
            {isDepFailure && (
              <div className="mt-3 space-y-2">
                <p className="text-xs font-medium text-muted-foreground">{t("hub.failedDeps")}</p>
                <Input
                  value={waiveReason}
                  onChange={(e) => setWaiveReason(e.target.value)}
                  placeholder={t("hub.waiveReasonPlaceholder")}
                />
                {deps
                  .filter(
                    (d) =>
                      !d.waived_at &&
                      (d.upstream_status === "failed" || d.upstream_status === "cancelled"),
                  )
                  .map((d) => (
                    <div key={d.dep_task_id} className="flex items-center gap-3 text-xs">
                      <Link
                        to="/agents/$agentId/tasks/$taskId"
                        params={{ agentId, taskId: d.dep_task_id }}
                        className="font-mono font-medium text-primary hover:underline"
                      >
                        {d.dep_task_id.slice(0, 8)}
                      </Link>
                      <span className="text-destructive">
                        {statusLabel(t, d.upstream_status ?? "")}
                      </span>
                      <Button
                        size="sm"
                        variant="outline"
                        loading={acting}
                        disabled={!waiveReason.trim()}
                        onClick={() =>
                          act(() =>
                            waiveTaskDep({
                              path: { taskId: task.id, depTaskId: d.dep_task_id },
                              body: { reason: waiveReason.trim() },
                              throwOnError: true,
                            }),
                          )
                        }
                      >
                        {t("hub.waiveDep")}
                      </Button>
                    </div>
                  ))}
              </div>
            )}
          </div>
        </DetailSection>
      )}

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

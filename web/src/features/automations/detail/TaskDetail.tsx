import { useCallback, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { cancelTask, reopenTask } from "@/lib/api-client";
import type { ComponentsTask } from "@/lib/api-client/types.gen";
import { taskRunsOptions } from "@/lib/queries/goals";
import { useI18n } from "@/lib/i18n";
import { formatTime } from "@/lib/time";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { StatusPill, statusLabel } from "../lib";

export function TaskDetail({ task }: { task: ComponentsTask }) {
  const { t } = useI18n();
  const qc = useQueryClient();
  const [acting, setActing] = useState(false);

  const { data: runs = [] } = useQuery(taskRunsOptions(task.id));

  const invalidate = useCallback(() => {
    void qc.invalidateQueries({ queryKey: ["standalone-tasks"] });
    void qc.invalidateQueries({ queryKey: ["task-runs", task.id] });
  }, [qc, task.id]);

  const handleCancel = useCallback(async () => {
    setActing(true);
    try {
      await cancelTask({ path: { taskId: task.id }, throwOnError: true });
      invalidate();
    } finally {
      setActing(false);
    }
  }, [task.id, invalidate]);

  const handleReopen = useCallback(async () => {
    setActing(true);
    try {
      await reopenTask({ path: { taskId: task.id }, throwOnError: true });
      invalidate();
    } finally {
      setActing(false);
    }
  }, [task.id, invalidate]);

  const lastFailedRun = runs.find((r) => r.status === "failed" || r.status === "timed_out");

  return (
    <div className="max-w-[680px] px-9 py-7">
      {/* Eyebrow */}
      <div className="mb-2 flex items-center gap-1.5">
        <span className="font-mono text-[10px] font-semibold uppercase tracking-[0.1em] text-emerald-500">
          {t("hub.chipTask")}
        </span>
        <span className="text-[10px] text-border">/</span>
        <StatusPill status={task.status} label={statusLabel(t, task.status)} />
        {task.priority === "urgent" && (
          <span className="rounded-md border border-amber-500/25 bg-amber-500/10 px-2 py-0.5 font-mono text-[10px] font-medium text-amber-600 dark:text-amber-400">
            urgent
          </span>
        )}
      </div>

      <h2 className="font-serif text-[22px] font-semibold tracking-tight leading-snug">
        {task.title}
      </h2>
      {task.description && (
        <p className="mt-1.5 text-sm leading-relaxed text-muted-foreground">{task.description}</p>
      )}

      {/* Actions */}
      <div className="mt-5 flex flex-wrap gap-2">
        {task.status === "failed" && (
          <Button size="sm" loading={acting} onClick={handleReopen}>
            {t("hub.retry")}
          </Button>
        )}
        {["done", "failed", "cancelled"].includes(task.status) && (
          <Button variant="outline" size="sm" loading={acting} onClick={handleReopen}>
            {t("hub.reopen")}
          </Button>
        )}
        {!["done", "failed", "cancelled"].includes(task.status) && (
          <Button variant="ghost" size="sm" loading={acting} onClick={handleCancel}>
            {t("common.cancel")}
          </Button>
        )}
      </div>

      <hr className="my-6 border-border" />

      {/* Properties */}
      <div className="grid grid-cols-2 gap-x-6 gap-y-4">
        <PropField label={t("goals.colStatus")}>
          <StatusPill status={task.status} label={statusLabel(t, task.status)} />
        </PropField>
        <PropField label="Priority">
          <span className="text-[13px] font-medium capitalize">{task.priority}</span>
        </PropField>
        <PropField label="Agent">
          <span className="text-[13px] font-medium">{task.agent_id || "—"}</span>
        </PropField>
        <PropField label="Retries">
          <span
            className={cn(
              "text-[13px] font-medium",
              task.retry_count >= task.max_retries && task.max_retries > 0
                ? "text-destructive"
                : "",
            )}
          >
            {task.retry_count} / {task.max_retries}
            {task.retry_count >= task.max_retries && task.max_retries > 0
              ? ` (${t("hub.exhausted")})`
              : ""}
          </span>
        </PropField>
        <PropField label="Parent Goal">
          <span className="text-[13px] font-medium text-muted-foreground">
            {task.goal_id || t("hub.standalone")}
          </span>
        </PropField>
        <PropField label="Updated">
          <span className="font-mono text-xs">{formatTime(task.updated_at)}</span>
        </PropField>
      </div>

      {/* Error section */}
      {task.status === "failed" && lastFailedRun?.error && (
        <div className="mt-6">
          <SectionHead title={t("hub.error")} />
          <div className="rounded-lg border border-destructive/25 bg-destructive/[0.06] px-3.5 py-3 font-mono text-xs leading-relaxed text-destructive">
            {lastFailedRun.error}
          </div>
        </div>
      )}

      {/* Current run */}
      {task.status === "running" && runs.length > 0 && runs[0].status === "running" && (
        <div className="mt-6">
          <SectionHead title={t("hub.currentRun")} />
          <div className="rounded-lg border border-emerald-500/25 bg-emerald-500/[0.06] px-3.5 py-3">
            <div className="flex items-center gap-2">
              <span className="size-1.5 animate-pulse rounded-full bg-emerald-500" />
              <span className="text-xs font-semibold">Run #{runs[0].attempt_no}</span>
              <span className="font-mono text-[10px] text-muted-foreground">
                started {formatTime(runs[0].started_at)}
              </span>
            </div>
          </div>
        </div>
      )}

      {/* Run history */}
      {runs.length > 0 && (
        <div className="mt-6">
          <SectionHead title={t("hub.recentRuns")} />
          <div>
            {runs.slice(0, 10).map((run) => (
              <div
                key={run.id}
                className="flex items-center gap-2.5 border-b border-border/50 py-2 text-xs last:border-0"
              >
                <span className={cn("size-1.5 shrink-0 rounded-full", runDotClass(run.status))} />
                <span className="font-mono text-[11px] text-muted-foreground">
                  {formatTime(run.started_at)}
                </span>
                <Badge size="sm" variant={runBadgeVariant(run.status)}>
                  {run.status}
                </Badge>
                {run.error && (
                  <span className="truncate text-[11px] text-destructive" title={run.error}>
                    {run.error}
                  </span>
                )}
              </div>
            ))}
          </div>
        </div>
      )}
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

function SectionHead({ title }: { title: string }) {
  return (
    <div className="mb-2.5 border-b border-border pb-2">
      <span className="font-mono text-[10px] font-semibold uppercase tracking-[0.1em] text-muted-foreground">
        {title}
      </span>
    </div>
  );
}

function runDotClass(status: string): string {
  if (status === "completed") return "bg-emerald-500";
  if (status === "failed" || status === "timed_out") return "bg-destructive";
  if (status === "running") return "bg-amber-500";
  return "bg-muted-foreground/40";
}

function runBadgeVariant(status: string): "success" | "error" | "warning" | "outline" {
  if (status === "completed") return "success";
  if (status === "failed" || status === "timed_out") return "error";
  if (status === "running") return "warning";
  return "outline";
}

import { useCallback, useEffect, useMemo, useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import type { ComponentsTask } from "@/lib/api-client/types.gen";
import type { SchedulerJob, SchedulerJobRun } from "@/lib/types";
import { fetchAllSchedulerJobRuns, fetchAllTasks } from "@/lib/paginated";
import { cn } from "@/lib/utils";
import { formatTime } from "@/lib/time";
import { useI18n } from "@/lib/i18n";
import { useMediaQuery } from "@/hooks/use-media-query";
import { Button } from "@/components/ui/button";
import { Sheet, SheetPopup, SheetTitle, SheetDescription } from "@/components/ui/sheet";
import { SessionConversation } from "@/features/sessions/SessionConversation";
import { ResizableSidePanel } from "./ResizableSidePanel";

interface Props {
  agentId: string;
  schedulerJobs: SchedulerJob[];
  selectedJobId?: string;
  selectedRunId?: string;
  onSelectJob: (jobId: string | null) => void;
  onSelectRun: (jobId: string, runId: string | null) => void;
  onEditJob: (jobId: string) => void;
  onCreateTask: () => void;
  onCreateJob: () => void;
}

type RunWithMeta = SchedulerJobRun & {
  job_name?: string;
  job_id?: string;
  job_agent_id?: string;
  job_session_mode?: string;
};

type BoardItem =
  | { kind: "task"; task: ComponentsTask }
  | { kind: "run"; run: RunWithMeta }
  | { kind: "schedule"; job: SchedulerJob };

function schedulerRunSessionId(
  job: { id: string; agent_id?: string; session_mode?: string },
  run: SchedulerJobRun,
): string {
  if (run.session_id) return run.session_id;
  if (job.session_mode !== "new") {
    return `${job.agent_id ? `${job.agent_id}:` : ""}scheduler:${job.id}`;
  }
  return "";
}

export function AutomationDashPanel({
  agentId,
  schedulerJobs,
  selectedJobId,
  selectedRunId,
  onSelectJob,
  onSelectRun,
  onEditJob,
  onCreateTask,
  onCreateJob,
}: Props) {
  const { t } = useI18n();
  const navigate = useNavigate();
  const isDesktop = useMediaQuery("(min-width: 768px)");
  const [tasks, setTasks] = useState<ComponentsTask[]>([]);
  const [tasksLoading, setTasksLoading] = useState(false);
  const [runs, setRuns] = useState<RunWithMeta[]>([]);
  const [runsLoading, setRunsLoading] = useState(false);
  const [jobRuns, setJobRuns] = useState<SchedulerJobRun[]>([]);
  const [jobRunsLoading, setJobRunsLoading] = useState(false);

  const selectedJob = selectedJobId
    ? (schedulerJobs.find((j) => j.id === selectedJobId) ?? null)
    : null;

  const selectedRunFromRuns = selectedRunId
    ? (runs.find((r) => r.id === selectedRunId) ?? null)
    : null;
  const selectedRunFromJobRuns =
    selectedRunId && selectedJobId && !selectedRunFromRuns
      ? (jobRuns.find((r) => r.id === selectedRunId) ?? null)
      : null;
  const selectedRun: RunWithMeta | null =
    selectedRunFromRuns ??
    (selectedRunFromJobRuns
      ? ({
          ...selectedRunFromJobRuns,
          job_name: selectedJob?.name,
          job_id: selectedJobId,
          job_agent_id: selectedJob?.agent_id,
          job_session_mode: selectedJob?.session_mode,
        } as RunWithMeta)
      : null);

  const loadRuns = useCallback(async () => {
    setRunsLoading(true);
    try {
      const allRuns: RunWithMeta[] = [];
      await Promise.all(
        schedulerJobs.slice(0, 10).map(async (job) => {
          try {
            const runs = await fetchAllSchedulerJobRuns(job.agent_id || agentId, job.id);
            for (const r of runs as SchedulerJobRun[]) {
              allRuns.push({
                ...r,
                job_name: job.name,
                job_id: job.id,
                job_agent_id: job.agent_id,
                job_session_mode: job.session_mode,
              });
            }
          } catch {
            /* skip */
          }
        }),
      );
      allRuns.sort((a, b) => new Date(b.started_at).getTime() - new Date(a.started_at).getTime());
      setRuns(allRuns);
    } finally {
      setRunsLoading(false);
    }
  }, [agentId, schedulerJobs]);

  useEffect(() => {
    void loadRuns();
  }, [loadRuns]);

  const loadTasks = useCallback(async () => {
    setTasksLoading(true);
    try {
      setTasks(await fetchAllTasks(agentId));
    } catch (e) {
      console.error(e);
      setTasks([]);
    } finally {
      setTasksLoading(false);
    }
  }, [agentId]);

  useEffect(() => {
    void loadTasks();
  }, [loadTasks]);

  const loadJobRuns = useCallback(
    async (jobId: string) => {
      setJobRunsLoading(true);
      try {
        const runs = await fetchAllSchedulerJobRuns(selectedJob?.agent_id || agentId, jobId);
        setJobRuns(runs as SchedulerJobRun[]);
      } catch {
        setJobRuns([]);
      } finally {
        setJobRunsLoading(false);
      }
    },
    [agentId, selectedJob?.agent_id],
  );

  useEffect(() => {
    if (selectedJobId) void loadJobRuns(selectedJobId);
  }, [selectedJobId, loadJobRuns]);

  const handleSelectJob = useCallback(
    (job: SchedulerJob) => {
      onSelectJob(job.id);
      void loadJobRuns(job.id);
    },
    [onSelectJob, loadJobRuns],
  );

  const handleSelectRunFromJob = useCallback(
    (run: SchedulerJobRun) => {
      if (!selectedJob) return;
      onSelectRun(selectedJob.id, run.id);
    },
    [selectedJob, onSelectRun],
  );

  const boardColumns = useMemo(
    () => [
      {
        id: "needs",
        title: "Needs you",
        detail: "Blocked, review, or failed",
        items: [
          ...tasks
            .filter(
              (task) =>
                task.status === "blocked" ||
                task.status === "reviewing" ||
                task.status === "failed",
            )
            .map((task): BoardItem => ({ kind: "task", task })),
          ...runs
            .filter((run) => run.status === "failed")
            .map((run): BoardItem => ({ kind: "run", run })),
        ],
      },
      {
        id: "running",
        title: "Running",
        detail: "Active tasks and runs",
        items: [
          ...tasks
            .filter((task) => task.status === "running")
            .map((task): BoardItem => ({ kind: "task", task })),
          ...runs
            .filter((run) => run.status === "running")
            .map((run): BoardItem => ({ kind: "run", run })),
        ],
      },
      {
        id: "queued",
        title: "Queued",
        detail: "Ready but not started",
        items: tasks
          .filter((task) => task.status === "draft" || task.status === "ready")
          .map((task): BoardItem => ({ kind: "task", task })),
      },
      {
        id: "scheduled",
        title: "Scheduled",
        detail: "Enabled recurring work",
        items: schedulerJobs
          .filter((job) => job.enabled)
          .map((job): BoardItem => ({ kind: "schedule", job })),
      },
    ],
    [runs, schedulerJobs, tasks],
  );

  const openTask = useCallback(
    (task: ComponentsTask) => {
      if (!task.session_id) return;
      void navigate({
        to: "/agents/$agentId/sessions/$sessionId",
        params: { agentId, sessionId: task.session_id },
      });
    },
    [agentId, navigate],
  );

  const hasSidePanel = !!(selectedRun || (selectedJob && !selectedRun));
  const sidePanelContent = selectedRun ? (
    <RunDetailPanel
      agentId={agentId}
      run={selectedRun}
      onClose={() => onSelectRun(selectedRun.job_id ?? "", null)}
    />
  ) : selectedJob && !selectedRun ? (
    <JobDetailPanel
      job={selectedJob}
      runs={jobRuns}
      runsLoading={jobRunsLoading}
      onClose={() => onSelectJob(null)}
      onSelectRun={handleSelectRunFromJob}
      onEditJob={() => {
        onEditJob(selectedJob.id);
      }}
    />
  ) : null;

  return (
    <div className="flex-1 min-w-0 flex overflow-hidden">
      {/* Main content */}
      <div className="flex-1 min-w-0 flex flex-col overflow-hidden">
        {/* Header */}
        <div className="flex-shrink-0 h-12 px-3 border-b border-border/60 bg-background flex items-center gap-2 sm:px-5 sm:gap-3">
          <h2 className="shrink-0 text-[15px] font-medium tracking-tight">
            {t("sessions.sidebar.work")}
          </h2>
          <span className="rounded-full bg-muted px-2.5 py-1 text-xs font-medium text-muted-foreground">
            Board
          </span>
          <div className="ml-auto flex items-center gap-1.5">
            <Button
              size="icon"
              variant="outline"
              onClick={onCreateTask}
              className="size-7 rounded-lg bg-background sm:hidden"
            >
              <svg
                className="size-3.5"
                viewBox="0 0 24 24"
                fill="none"
                stroke="currentColor"
                strokeWidth="2.5"
              >
                <path d="M12 5v14M5 12h14" />
              </svg>
            </Button>
            <Button
              size="sm"
              variant="ghost"
              onClick={onCreateJob}
              className="hidden h-8 rounded-lg px-2.5 text-xs text-muted-foreground sm:inline-flex"
            >
              Schedule
            </Button>
            <Button
              size="sm"
              variant="outline"
              onClick={onCreateTask}
              className="hidden h-8 rounded-lg border-border/80 bg-background px-2.5 text-xs font-medium shadow-none sm:inline-flex"
            >
              <svg
                className="size-3"
                viewBox="0 0 24 24"
                fill="none"
                stroke="currentColor"
                strokeWidth="2.5"
              >
                <path d="M12 5v14M5 12h14" />
              </svg>
              New Task
            </Button>
          </div>
        </div>

        <div className="min-h-0 flex-1 overflow-x-auto overflow-y-hidden p-3">
          {tasksLoading || runsLoading ? (
            <div className="flex h-full items-center justify-center">
              <div className="size-4 animate-spin rounded-full border-2 border-muted-foreground/30 border-t-muted-foreground" />
            </div>
          ) : (
            <div className="grid h-full min-w-[980px] grid-cols-4 gap-3">
              {boardColumns.map((column) => (
                <KanbanColumn
                  key={column.id}
                  title={column.title}
                  detail={column.detail}
                  items={column.items}
                  selectedJobId={selectedJob?.id}
                  selectedRunId={selectedRun?.id}
                  onOpenTask={openTask}
                  onOpenJob={handleSelectJob}
                  onOpenRun={(run) => onSelectRun(run.job_id ?? "", run.id)}
                />
              ))}
            </div>
          )}
        </div>
      </div>

      {/* Side panel: desktop inline */}
      {/* Side panel: desktop inline */}
      {hasSidePanel && isDesktop && sidePanelContent}

      {/* Side panel: mobile sheet */}
      {!isDesktop && (
        <Sheet
          open={hasSidePanel}
          onOpenChange={(open) => {
            if (!open) {
              if (selectedRun) onSelectRun(selectedRun.job_id ?? "", null);
              else if (selectedJob) onSelectJob(null);
            }
          }}
        >
          <SheetPopup side="bottom" showCloseButton={false} className="h-[85vh]">
            <SheetTitle className="sr-only">Details</SheetTitle>
            <SheetDescription className="sr-only">Job or run details</SheetDescription>
            <div className="flex h-full flex-col overflow-hidden">{sidePanelContent}</div>
          </SheetPopup>
        </Sheet>
      )}
    </div>
  );
}

function KanbanColumn({
  title,
  detail,
  items,
  selectedJobId,
  selectedRunId,
  onOpenTask,
  onOpenJob,
  onOpenRun,
}: {
  title: string;
  detail: string;
  items: BoardItem[];
  selectedJobId?: string;
  selectedRunId?: string;
  onOpenTask: (task: ComponentsTask) => void;
  onOpenJob: (job: SchedulerJob) => void;
  onOpenRun: (run: RunWithMeta) => void;
}) {
  return (
    <section className="flex min-h-0 flex-col overflow-hidden rounded-2xl border border-border/70 bg-card/55">
      <div className="shrink-0 border-b border-border/60 px-3 py-2.5">
        <div className="flex items-center justify-between gap-2">
          <h3 className="truncate text-sm font-semibold tracking-[-0.01em]">{title}</h3>
          <span className="rounded-full bg-muted px-2 py-0.5 font-mono text-[10px] text-muted-foreground">
            {items.length}
          </span>
        </div>
        <p className="mt-0.5 truncate text-[11px] text-muted-foreground">{detail}</p>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto p-2">
        {items.length === 0 ? (
          <div className="grid h-24 place-items-center rounded-xl border border-dashed border-border/70 text-[11px] text-muted-foreground/55">
            Empty
          </div>
        ) : (
          <div className="grid gap-2">
            {items.map((item) => {
              if (item.kind === "task") {
                return (
                  <TaskKanbanCard
                    key={`task:${item.task.id}`}
                    task={item.task}
                    onClick={() => onOpenTask(item.task)}
                  />
                );
              }
              if (item.kind === "run") {
                return (
                  <RunKanbanCard
                    key={`run:${item.run.id}`}
                    run={item.run}
                    selected={selectedRunId === item.run.id}
                    onClick={() => onOpenRun(item.run)}
                  />
                );
              }
              return (
                <ScheduleKanbanCard
                  key={`schedule:${item.job.id}`}
                  job={item.job}
                  selected={selectedJobId === item.job.id}
                  onClick={() => onOpenJob(item.job)}
                />
              );
            })}
          </div>
        )}
      </div>
    </section>
  );
}

function TaskKanbanCard({ task, onClick }: { task: ComponentsTask; onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={!task.session_id}
      className={cn(
        "w-full rounded-xl border border-border/70 bg-background/70 p-3 text-left transition-all hover:border-primary/30 hover:shadow-sm",
        !task.session_id && "cursor-default opacity-75 hover:border-border/70 hover:shadow-none",
      )}
    >
      <div className="mb-2 flex items-center justify-between gap-2">
        <span className="inline-flex items-center gap-1.5 rounded-full bg-muted px-2 py-0.5 text-[10px] font-medium text-muted-foreground">
          <StatusDot status={task.status} />
          {formatStatus(task.status)}
        </span>
        {task.priority === "urgent" && (
          <span className="rounded-full bg-destructive/10 px-1.5 py-0.5 text-[9px] font-medium text-destructive">
            urgent
          </span>
        )}
      </div>
      <p className="line-clamp-2 text-sm font-medium leading-snug">{task.title}</p>
      {task.description && (
        <p className="mt-1 line-clamp-2 text-[11px] leading-relaxed text-muted-foreground">
          {task.description}
        </p>
      )}
      <p className="mt-2 font-mono text-[10px] text-muted-foreground/55">
        updated {formatTime(task.updated_at)}
      </p>
    </button>
  );
}

function RunKanbanCard({
  run,
  selected,
  onClick,
}: {
  run: RunWithMeta;
  selected: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "w-full rounded-xl border bg-background/70 p-3 text-left transition-all hover:border-primary/30 hover:shadow-sm",
        selected ? "border-primary/40 shadow-sm" : "border-border/70",
      )}
    >
      <div className="mb-2 flex items-center justify-between gap-2">
        <span className="inline-flex items-center gap-1.5 rounded-full bg-muted px-2 py-0.5 text-[10px] font-medium text-muted-foreground">
          <RunStatusDot status={run.status} />
          run
        </span>
        <span className="font-mono text-[10px] text-muted-foreground/55">
          {formatTime(run.started_at)}
        </span>
      </div>
      <p className="line-clamp-2 text-sm font-medium leading-snug">{run.job_name || "Run"}</p>
      {run.error && (
        <p className="mt-1 line-clamp-2 text-[11px] leading-relaxed text-destructive/75">
          {run.error}
        </p>
      )}
      {run.duration && (
        <p className="mt-2 font-mono text-[10px] text-muted-foreground/55">{run.duration}</p>
      )}
    </button>
  );
}

function ScheduleKanbanCard({
  job,
  selected,
  onClick,
}: {
  job: SchedulerJob;
  selected: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "w-full rounded-xl border bg-background/70 p-3 text-left transition-all hover:border-primary/30 hover:shadow-sm",
        selected ? "border-primary/40 shadow-sm" : "border-border/70",
      )}
    >
      <div className="mb-2 flex items-center justify-between gap-2">
        <span className="rounded-full bg-emerald-500/10 px-2 py-0.5 text-[10px] font-medium text-emerald-600">
          schedule
        </span>
        <span className="font-mono text-[10px] text-muted-foreground/55">
          {job.owner_kind === "system" ? "system" : "user"}
        </span>
      </div>
      <p className="line-clamp-2 text-sm font-medium leading-snug">{job.name}</p>
      <p className="mt-1 truncate font-mono text-[11px] text-muted-foreground">
        {scheduleLabel(job)}
      </p>
      {job.description && (
        <p className="mt-1 line-clamp-2 text-[11px] leading-relaxed text-muted-foreground">
          {job.description}
        </p>
      )}
      {job.last_run_at && (
        <p className="mt-2 font-mono text-[10px] text-muted-foreground/55">
          last {formatTime(job.last_run_at)}
        </p>
      )}
    </button>
  );
}

function JobDetailPanel({
  job,
  runs,
  runsLoading,
  onClose,
  onSelectRun,
  onEditJob,
}: {
  job: SchedulerJob;
  runs: SchedulerJobRun[];
  runsLoading: boolean;
  onClose: () => void;
  onSelectRun: (run: SchedulerJobRun) => void;
  onEditJob: () => void;
}) {
  const schedule = scheduleLabel(job);

  return (
    <ResizableSidePanel>
      {/* Header */}
      <div className="flex-shrink-0 h-12 px-4 border-b border-border/60 flex items-center gap-2">
        <span
          className={cn(
            "text-[9px] font-mono px-2 py-0.5 rounded-full",
            job.enabled
              ? "bg-emerald-500/10 text-emerald-600"
              : "bg-muted text-muted-foreground/60",
          )}
        >
          {job.enabled ? "on" : "off"}
        </span>
        <h3 className="flex-1 text-[13px] font-medium truncate">{job.name}</h3>
        <Button
          size="xs"
          variant="ghost"
          onClick={onEditJob}
          className="text-[10px] text-muted-foreground"
        >
          Edit
        </Button>
        <button
          onClick={onClose}
          className="text-muted-foreground/50 hover:text-foreground text-sm cursor-pointer"
        >
          ×
        </button>
      </div>

      {/* Job meta */}
      <div className="flex-shrink-0 px-4 py-3 border-b border-border/60 space-y-2">
        {job.description && (
          <p className="text-[12px] text-muted-foreground/80 leading-relaxed">{job.description}</p>
        )}
        <dl className="grid grid-cols-[72px_1fr] gap-x-2 gap-y-1.5 text-[11px]">
          <dt className="font-mono text-muted-foreground/50">Schedule</dt>
          <dd className="text-foreground/70 font-mono">{schedule}</dd>
          <dt className="font-mono text-muted-foreground/50">Session</dt>
          <dd className="text-foreground/70 font-mono">{job.session_mode || "new"}</dd>
          {job.owner_kind === "system" && (
            <>
              <dt className="font-mono text-muted-foreground/50">Owner</dt>
              <dd className="text-foreground/70">system</dd>
            </>
          )}
          {job.last_run_at && (
            <>
              <dt className="font-mono text-muted-foreground/50">Last run</dt>
              <dd className="text-foreground/70">{formatTime(job.last_run_at)}</dd>
            </>
          )}
        </dl>
      </div>

      {/* Runs list */}
      <div className="flex-1 min-h-0 flex flex-col overflow-hidden">
        <div className="flex-shrink-0 px-4 py-2 border-b border-border/60">
          <p className="text-[10px] font-mono font-medium uppercase tracking-wider text-muted-foreground/50">
            Runs
            {!runsLoading && <span className="ml-1.5 text-muted-foreground/40">{runs.length}</span>}
          </p>
        </div>
        <div className="flex-1 overflow-y-auto">
          {runsLoading ? (
            <div className="flex items-center justify-center py-8">
              <div className="w-3.5 h-3.5 border-2 border-muted-foreground/30 border-t-muted-foreground rounded-full animate-spin" />
            </div>
          ) : runs.length === 0 ? (
            <p className="text-center text-[11px] text-muted-foreground/50 py-8 font-mono">
              No runs yet
            </p>
          ) : (
            <div className="p-2 space-y-1">
              {runs.map((run) => (
                <div
                  key={run.id}
                  onClick={() => onSelectRun(run)}
                  className="flex items-center gap-2 px-3 py-2 rounded-lg hover:bg-muted/50 transition-colors duration-150 cursor-pointer"
                >
                  <RunStatusDot status={run.status} />
                  <span className="text-[11px] font-mono text-muted-foreground/50 flex-1">
                    {formatTime(run.started_at)}
                  </span>
                  {run.duration && (
                    <span className="text-[10px] font-mono text-muted-foreground/40">
                      {run.duration}
                    </span>
                  )}
                  <span
                    className={cn(
                      "text-[9px] font-mono px-1.5 py-0.5 rounded-full",
                      run.status === "success" && "bg-emerald-500/10 text-emerald-600",
                      run.status === "running" && "bg-blue-500/10 text-blue-600",
                      run.status === "failed" && "bg-destructive/10 text-destructive",
                      run.status === "skipped" && "bg-muted text-muted-foreground",
                    )}
                  >
                    {run.status}
                  </span>
                </div>
              ))}
            </div>
          )}
        </div>
      </div>
    </ResizableSidePanel>
  );
}

function RunDetailPanel({
  agentId,
  run,
  onClose,
}: {
  agentId: string;
  run: RunWithMeta;
  onClose: () => void;
}) {
  const sessionId = schedulerRunSessionId(
    { id: run.job_id ?? "", agent_id: run.job_agent_id, session_mode: run.job_session_mode },
    run,
  );

  return (
    <ResizableSidePanel>
      {/* Detail header */}
      <div className="flex-shrink-0 h-12 px-4 border-b border-border/60 flex items-center gap-2">
        <span
          className={cn(
            "text-[9px] font-mono px-2 py-0.5 rounded-full",
            run.status === "success" && "bg-emerald-500/10 text-emerald-600",
            run.status === "running" && "bg-blue-500/10 text-blue-600",
            run.status === "failed" && "bg-destructive/10 text-destructive",
            run.status === "skipped" && "bg-muted text-muted-foreground",
          )}
        >
          {run.status}
        </span>
        <h3 className="flex-1 text-[13px] font-medium truncate">{run.job_name || "Run"}</h3>
        <button
          onClick={onClose}
          className="text-muted-foreground/50 hover:text-foreground text-sm cursor-pointer"
        >
          ×
        </button>
      </div>

      {/* Run meta */}
      <div className="flex-shrink-0 px-4 py-3 border-b border-border/60 space-y-2">
        {run.error && (
          <p className="text-[12px] text-destructive/80 leading-relaxed">{run.error}</p>
        )}
        <dl className="grid grid-cols-[72px_1fr] gap-x-2 gap-y-1.5 text-[11px]">
          <dt className="font-mono text-muted-foreground/50">Status</dt>
          <dd
            className={cn(
              run.status === "failed" ? "text-destructive font-medium" : "text-foreground/70",
            )}
          >
            {run.status}
          </dd>
          <dt className="font-mono text-muted-foreground/50">Started</dt>
          <dd className="text-foreground/70">{formatTime(run.started_at)}</dd>
          {run.duration && (
            <>
              <dt className="font-mono text-muted-foreground/50">Duration</dt>
              <dd className="text-foreground/70 font-mono">{run.duration}</dd>
            </>
          )}
          <dt className="font-mono text-muted-foreground/50">Job</dt>
          <dd className="text-foreground/70 truncate">{run.job_name}</dd>
        </dl>
      </div>

      {/* Conversation */}
      <div className="flex-1 min-h-0 flex flex-col overflow-hidden">
        {sessionId ? (
          <SessionConversation
            agentId={run.job_agent_id || agentId}
            sessionId={sessionId}
            placeholder="Ask about this run..."
            className="h-full"
            bodyClassName="min-h-0 flex-1"
            after={run.started_at}
            before={run.finished_at}
            inline
          />
        ) : (
          <div className="flex-1 flex items-center justify-center px-4">
            <p className="text-[11px] text-muted-foreground/50 font-mono text-center">
              No conversation session for this run
            </p>
          </div>
        )}
      </div>
    </ResizableSidePanel>
  );
}

function RunStatusDot({ status }: { status: string }) {
  return (
    <span
      className={cn(
        "w-2 h-2 rounded-full shrink-0",
        status === "success" && "bg-emerald-500",
        status === "running" && "bg-blue-500",
        status === "failed" && "bg-destructive",
        status === "skipped" && "bg-muted-foreground/30",
      )}
    />
  );
}

function StatusDot({ status }: { status: string }) {
  return (
    <span
      className={cn(
        "size-2 rounded-full",
        status === "done" && "bg-emerald-500",
        status === "running" && "bg-blue-500",
        (status === "draft" || status === "ready") && "bg-muted-foreground/30",
        status === "failed" && "bg-destructive",
        (status === "blocked" || status === "reviewing") && "bg-amber-500",
        status === "cancelled" && "bg-muted-foreground/20",
      )}
    />
  );
}

function formatStatus(status: string): string {
  if (status === "reviewing") return "Reviewing";
  return status;
}

function scheduleLabel(job: SchedulerJob): string {
  return job.cron || (job.every ? `every ${job.every}` : job.at || "manual");
}

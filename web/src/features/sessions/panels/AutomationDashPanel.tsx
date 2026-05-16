import { useCallback, useEffect, useState } from "react";
import type { SchedulerJob, SchedulerJobRun } from "@/lib/types";
import { api } from "@/lib/api";
import { cn } from "@/lib/utils";
import { formatTime } from "@/lib/time";
import { useI18n } from "@/lib/i18n";
import { Button } from "@/components/ui/button";
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
  onCreateJob: () => void;
}

type RunWithMeta = SchedulerJobRun & {
  job_name?: string;
  job_id?: string;
  job_agent_id?: string;
  job_session_mode?: string;
};

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
  schedulerJobs,
  selectedJobId,
  selectedRunId,
  onSelectJob,
  onSelectRun,
  onEditJob,
  onCreateJob,
}: Props) {
  const { t } = useI18n();
  const [tab, setTab] = useState<"schedules" | "runs">("schedules");
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
    selectedRunId && selectedJob && !selectedRunFromRuns
      ? (jobRuns.find((r) => r.id === selectedRunId) ?? null)
      : null;
  const selectedRun: RunWithMeta | null =
    selectedRunFromRuns ??
    (selectedRunFromJobRuns
      ? {
          ...selectedRunFromJobRuns,
          job_name: selectedJob?.name,
          job_id: selectedJob?.id,
          job_agent_id: selectedJob?.agent_id,
          job_session_mode: selectedJob?.session_mode,
        }
      : null);

  const loadRuns = useCallback(async () => {
    setRunsLoading(true);
    try {
      const allRuns: RunWithMeta[] = [];
      await Promise.all(
        schedulerJobs.slice(0, 10).map(async (job) => {
          try {
            const res = await api<SchedulerJobRun[]>(
              "GET",
              `/api/scheduler/jobs/${encodeURIComponent(job.id)}/runs`,
            );
            for (const r of res ?? []) {
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
  }, [schedulerJobs]);

  useEffect(() => {
    if (tab === "runs") void loadRuns();
  }, [tab, loadRuns]);

  const loadJobRuns = useCallback(async (jobId: string) => {
    setJobRunsLoading(true);
    try {
      const res = await api<SchedulerJobRun[]>(
        "GET",
        `/api/scheduler/jobs/${encodeURIComponent(jobId)}/runs`,
      );
      setJobRuns(res ?? []);
    } catch {
      setJobRuns([]);
    } finally {
      setJobRunsLoading(false);
    }
  }, []);

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

  const userJobs = schedulerJobs.filter((j) => j.owner_kind !== "system");
  const systemJobs = schedulerJobs.filter((j) => j.owner_kind === "system");

  return (
    <div className="flex-1 min-w-0 flex overflow-hidden">
      {/* Main content */}
      <div className="flex-1 min-w-0 flex flex-col overflow-hidden">
        {/* Header */}
        <div className="flex-shrink-0 h-12 px-5 border-b border-border/60 bg-background flex items-center gap-3">
          <h2 className="text-[15px] font-medium tracking-tight">
            {t("sessions.sidebar.automations")}
          </h2>
          <div className="flex items-center gap-1 ml-4">
            <button
              onClick={() => setTab("schedules")}
              className={cn(
                "px-3 py-1 rounded-lg text-xs font-medium transition-colors duration-150",
                tab === "schedules"
                  ? "bg-muted text-foreground"
                  : "text-muted-foreground hover:text-foreground",
              )}
            >
              Schedules
            </button>
            <button
              onClick={() => setTab("runs")}
              className={cn(
                "px-3 py-1 rounded-lg text-xs font-medium transition-colors duration-150",
                tab === "runs"
                  ? "bg-muted text-foreground"
                  : "text-muted-foreground hover:text-foreground",
              )}
            >
              Runs
            </button>
          </div>
          <div className="ml-auto">
            <Button size="sm" onClick={onCreateJob} className="rounded-xl text-xs gap-1.5">
              <svg
                className="w-3 h-3"
                viewBox="0 0 24 24"
                fill="none"
                stroke="currentColor"
                strokeWidth="2.5"
              >
                <path d="M12 5v14M5 12h14" />
              </svg>
              New Schedule
            </Button>
          </div>
        </div>

        {/* Body */}
        {tab === "schedules" ? (
          <div className="flex-1 overflow-y-auto p-4">
            {userJobs.length === 0 && systemJobs.length === 0 ? (
              <div className="text-center py-16">
                <p className="text-sm text-muted-foreground/60">No automations yet</p>
                <p className="text-[11px] text-muted-foreground/40 font-mono mt-1">
                  Create a schedule to automate recurring tasks
                </p>
              </div>
            ) : (
              <div className="space-y-4">
                {userJobs.length > 0 && (
                  <div>
                    <div className="grid grid-cols-1 lg:grid-cols-2 gap-3">
                      {userJobs.map((job) => (
                        <JobCard
                          key={job.id}
                          job={job}
                          selected={selectedJob?.id === job.id}
                          onClick={() => handleSelectJob(job)}
                        />
                      ))}
                    </div>
                  </div>
                )}
                {systemJobs.length > 0 && (
                  <div>
                    <p className="text-[10px] font-mono font-medium uppercase tracking-wider text-muted-foreground/50 mb-2 mt-4">
                      System
                    </p>
                    <div className="grid grid-cols-1 lg:grid-cols-2 gap-3">
                      {systemJobs.map((job) => (
                        <JobCard
                          key={job.id}
                          job={job}
                          selected={selectedJob?.id === job.id}
                          onClick={() => handleSelectJob(job)}
                        />
                      ))}
                    </div>
                  </div>
                )}
              </div>
            )}
          </div>
        ) : (
          <div className="flex-1 overflow-y-auto p-4">
            {runsLoading ? (
              <div className="flex items-center justify-center py-12">
                <div className="w-4 h-4 border-2 border-muted-foreground/30 border-t-muted-foreground rounded-full animate-spin" />
              </div>
            ) : runs.length === 0 ? (
              <p className="text-center text-sm text-muted-foreground/50 py-12 font-mono">
                No runs recorded yet
              </p>
            ) : (
              <div className="max-w-2xl space-y-2">
                {runs.map((run) => (
                  <div
                    key={run.id}
                    onClick={() => {
                      onSelectRun(run.job_id ?? "", run.id);
                    }}
                    className={cn(
                      "flex items-center gap-3 px-3 py-2.5 rounded-lg border bg-card hover:shadow-sm transition-all duration-150 cursor-pointer",
                      selectedRun?.id === run.id
                        ? "border-primary/40 shadow-sm"
                        : "border-border/60",
                    )}
                  >
                    <RunStatusDot status={run.status} />
                    <div className="flex-1 min-w-0">
                      <p className="text-[13px] font-medium truncate">
                        {run.job_name || "Unknown job"}
                      </p>
                      {run.error && (
                        <p className="text-[11px] text-destructive/70 truncate mt-0.5">
                          {run.error}
                        </p>
                      )}
                    </div>
                    <span className="text-[10px] font-mono text-muted-foreground/50 shrink-0">
                      {formatTime(run.started_at)}
                    </span>
                    {run.duration && (
                      <span className="text-[10px] font-mono text-muted-foreground/40 shrink-0">
                        {run.duration}
                      </span>
                    )}
                    <span
                      className={cn(
                        "text-[9px] font-mono px-1.5 py-0.5 rounded-full shrink-0",
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
        )}
      </div>

      {/* Side panel: job detail or run detail */}
      {selectedJob && (
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
      )}
      {selectedRun && !selectedJob && (
        <RunDetailPanel
          run={selectedRun}
          onClose={() => onSelectRun(selectedRun.job_id ?? "", null)}
        />
      )}
    </div>
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
  const schedule = job.cron || (job.every ? `every ${job.every}` : job.at || "manual");

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

function RunDetailPanel({ run, onClose }: { run: RunWithMeta; onClose: () => void }) {
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
            sessionId={sessionId}
            placeholder="Ask about this run..."
            className="h-full"
            bodyClassName="min-h-0 flex-1"
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

function JobCard({
  job,
  selected,
  onClick,
}: {
  job: SchedulerJob;
  selected: boolean;
  onClick: () => void;
}) {
  const schedule = job.cron || (job.every ? `every ${job.every}` : job.at || "manual");

  return (
    <div
      onClick={onClick}
      className={cn(
        "rounded-xl border bg-card p-4 cursor-pointer hover:shadow-sm transition-all duration-150",
        selected ? "border-primary/40 shadow-sm" : "border-border/60",
      )}
    >
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <p className="text-[13px] font-medium truncate">{job.name}</p>
          <p className="text-[11px] font-mono text-muted-foreground/50 mt-0.5">{schedule}</p>
        </div>
        <span
          className={cn(
            "text-[9px] font-mono px-2 py-0.5 rounded-full shrink-0",
            job.enabled
              ? "bg-emerald-500/10 text-emerald-600"
              : "bg-muted text-muted-foreground/60",
          )}
        >
          {job.enabled ? "on" : "off"}
        </span>
      </div>
      {job.description && (
        <p className="text-[11px] text-muted-foreground/60 mt-2 line-clamp-2">{job.description}</p>
      )}
      {job.last_run_at && (
        <p className="text-[10px] font-mono text-muted-foreground/40 mt-2">
          Last run: {formatTime(job.last_run_at)}
          {job.last_error && <span className="text-destructive/60 ml-2">failed</span>}
        </p>
      )}
    </div>
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

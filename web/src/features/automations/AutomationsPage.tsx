import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import { Streamdown } from "streamdown";
import { useQuery } from "@tanstack/react-query";
import { useNavigate, useParams } from "@tanstack/react-router";
import { meQueryOptions } from "@/lib/queries/me";
import { api } from "@/lib/api";
import { useI18n } from "@/lib/i18n";
import { formatTime } from "@/lib/time";
import type { ComponentsAgentTask } from "@/lib/api-client/types.gen";
import type { Agent, SchedulerJob, SchedulerJobList, SchedulerJobRun } from "@/lib/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogFooter,
  DialogHeader,
  DialogPanel,
  DialogPopup,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { SessionConversation } from "@/features/sessions/SessionConversation";
import { TaskDetail } from "@/features/tasks/TaskDetail";

type ViewTab = "work" | "schedules" | "history";
type AutomationsRoute = { tab: ViewTab; selection: Selection } | { redirect: string };
type WorkLaneKey = "attention" | "running" | "pending" | "failed";
type Selection =
  | { type: "task"; id: string }
  | { type: "job"; id: string }
  | { type: "run"; jobId: string; runId: string }
  | { type: "new-job" }
  | null;
type TimelineKind = "task" | "schedule" | "run";

interface JobForm {
  name: string;
  cron: string;
  every: string;
  message: string;
  session_mode: string;
  enabled: boolean;
  agent_id: string;
  schedule_type: "cron" | "every";
  system_job: boolean;
}

interface TaskForm {
  title: string;
  description: string;
  priority: "routine" | "urgent";
}

interface TimelineItem {
  id: string;
  kind: TimelineKind;
  title: string;
  description: string;
  time: string;
  status: string;
  selection: Selection;
}

interface Toast {
  msg: string;
  kind: "success" | "error";
}

interface ConfirmState {
  msg: string;
  action: () => void;
}

const emptyTaskForm = (): TaskForm => ({ title: "", description: "", priority: "routine" });
const emptyJobForm = (): JobForm => ({
  name: "",
  cron: "",
  every: "",
  message: "",
  session_mode: "reuse",
  enabled: true,
  agent_id: "",
  schedule_type: "cron",
  system_job: false,
});

function taskBadgeVariant(status: string): "success" | "error" | "warning" | "outline" {
  if (status === "done") return "success";
  if (status === "failed" || status === "cancelled" || status === "review_requested")
    return "error";
  if (status === "running" || status === "blocked") return "warning";
  return "outline";
}

function runBadgeVariant(status: string): "success" | "error" | "warning" | "outline" {
  if (status === "success" || status === "done") return "success";
  if (status === "error" || status === "failed") return "error";
  if (status === "running") return "warning";
  return "outline";
}

function jobScheduleText(job: SchedulerJob): string {
  if (job.cron) return job.cron;
  if (job.every) return `every ${job.every}`;
  if (job.at) return `at ${job.at}`;
  return "unscheduled";
}

function taskNeedsAttention(task: ComponentsAgentTask): boolean {
  return task.status === "blocked" || task.status === "review_requested";
}

function isFailedTask(task: ComponentsAgentTask): boolean {
  return task.status === "failed" || task.status === "cancelled";
}

function isFailedJob(job: SchedulerJob): boolean {
  return Boolean(job.last_error);
}

function schedulerRunSessionId(job: SchedulerJob, run: SchedulerJobRun): string {
  if (run.session_id) return run.session_id;
  if (job.session_mode !== "new") {
    return `${job.agent_id ? `${job.agent_id}:` : ""}scheduler:${job.id}`;
  }
  return "";
}

function parseAutomationsRoute(splat?: string): AutomationsRoute {
  const segments = splat?.split("/").filter(Boolean) ?? [];
  const [section, kind, id, runId] = segments;

  if (!section) return { redirect: "/automations/tasks" };
  if (section === "tasks") {
    return kind
      ? { tab: "work", selection: { type: "task", id: kind } }
      : { tab: "work", selection: null };
  }
  if (section === "scheduler") {
    if (kind === "new") return { tab: "schedules", selection: { type: "new-job" } };
    if (kind === "jobs" && id) return { tab: "schedules", selection: { type: "job", id } };
    return { tab: "schedules", selection: null };
  }
  if (section === "logs") {
    if (kind === "tasks" && id) return { tab: "history", selection: { type: "task", id } };
    if (kind === "runs" && id && runId) {
      return {
        tab: "history",
        selection: { type: "run", jobId: id, runId },
      };
    }
    return { tab: "history", selection: null };
  }
  return { redirect: "/automations/tasks" };
}

function selectionPath(tab: ViewTab, selection: Selection): string {
  if (selection?.type === "task")
    return tab === "history"
      ? `/automations/logs/tasks/${selection.id}`
      : `/automations/tasks/${selection.id}`;
  if (selection?.type === "job") return `/automations/scheduler/jobs/${selection.id}`;
  if (selection?.type === "run")
    return `/automations/logs/runs/${selection.jobId}/${selection.runId}`;
  if (selection?.type === "new-job") return "/automations/scheduler/new";
  if (tab === "schedules") return "/automations/scheduler";
  if (tab === "history") return "/automations/logs";
  return "/automations/tasks";
}

export function AutomationsPage() {
  const { t } = useI18n();
  const navigate = useNavigate();
  const params = useParams({ strict: false }) as { _splat?: string };
  const { data: me } = useQuery(meQueryOptions);
  const isAdmin = me?.is_admin ?? false;
  const [tasks, setTasks] = useState<ComponentsAgentTask[]>([]);
  const [detailTask, setDetailTask] = useState<ComponentsAgentTask | null>(null);
  const [jobs, setJobs] = useState<SchedulerJob[]>([]);
  const [agents, setAgents] = useState<Agent[]>([]);
  const [runsByJob, setRunsByJob] = useState<Record<string, SchedulerJobRun[]>>({});
  const [tab, setTab] = useState<ViewTab>("work");
  const [workLane, setWorkLane] = useState<WorkLaneKey>("attention");
  const [selection, setSelection] = useState<Selection>(null);
  const [detailModalMode, setDetailModalMode] = useState(false);
  const [loading, setLoading] = useState(true);
  const [creatingTask, setCreatingTask] = useState(false);
  const [taskForm, setTaskForm] = useState<TaskForm>(emptyTaskForm());
  const [jobForm, setJobForm] = useState<JobForm>(emptyJobForm());
  const [triggeringJobId, setTriggeringJobId] = useState<string | null>(null);
  const [toast, setToast] = useState<Toast | null>(null);
  const [confirm, setConfirm] = useState<ConfirmState | null>(null);

  const routeTo = useCallback(
    (nextTab: ViewTab, nextSelection: Selection = null) => {
      void navigate({ to: selectionPath(nextTab, nextSelection) });
    },
    [navigate],
  );

  useEffect(() => {
    const route = parseAutomationsRoute(params._splat);
    if ("redirect" in route) {
      void navigate({ to: route.redirect, replace: true });
      return;
    }
    setTab(route.tab);
    setSelection(route.selection);
  }, [navigate, params._splat]);

  const showToast = useCallback((msg: string, kind: "success" | "error" = "success") => {
    setToast({ msg, kind });
    setTimeout(() => setToast(null), 3000);
  }, []);

  const loadRuns = useCallback(async (jobId: string) => {
    try {
      const runs = await api<SchedulerJobRun[]>("GET", `/api/scheduler/jobs/${jobId}/runs`);
      setRunsByJob((prev) => ({ ...prev, [jobId]: runs || [] }));
    } catch (e) {
      console.error(e);
    }
  }, []);

  const load = useCallback(async () => {
    try {
      setLoading(true);
      const [taskList, jobList, agentList] = await Promise.all([
        api<{ items: ComponentsAgentTask[] }>("GET", "/api/tasks"),
        api<SchedulerJobList>("GET", "/api/scheduler/jobs"),
        api<Agent[]>("GET", "/api/agents"),
      ]);
      const nextJobs = jobList.items || [];
      setTasks(taskList.items || []);
      setJobs(nextJobs);
      setAgents(agentList || []);
      await Promise.all(nextJobs.map((job) => loadRuns(job.id)));
    } finally {
      setLoading(false);
    }
  }, [loadRuns]);

  useEffect(() => {
    void load();
    const id = setInterval(() => void load(), 15_000);
    return () => clearInterval(id);
  }, [load]);

  useEffect(() => {
    const query = window.matchMedia("(max-width: 1279px)");
    const update = () => setDetailModalMode(query.matches);
    update();
    query.addEventListener("change", update);
    return () => query.removeEventListener("change", update);
  }, []);

  const selectJob = useCallback(
    (job: SchedulerJob) => {
      routeTo("schedules", { type: "job", id: job.id });
      setJobForm({
        name: job.name,
        message: job.message,
        schedule_type: job.cron ? "cron" : "every",
        cron: job.cron || "",
        every: job.every || "",
        session_mode: job.session_mode || "reuse",
        enabled: job.enabled,
        agent_id: job.agent_id || "",
        system_job: !job.user_id,
      });
      void loadRuns(job.id);
    },
    [loadRuns, routeTo],
  );

  const startNewJob = useCallback(() => {
    routeTo("schedules", { type: "new-job" });
    setJobForm(emptyJobForm());
  }, [routeTo]);

  const createTask = useCallback(async () => {
    try {
      const created = await api<ComponentsAgentTask>("POST", "/api/tasks", {
        title: taskForm.title,
        description: taskForm.description || undefined,
        priority: taskForm.priority,
      });
      setCreatingTask(false);
      setTaskForm(emptyTaskForm());
      routeTo("work", { type: "task", id: created.id });
      await load();
      showToast("Task created");
    } catch (e) {
      showToast(e instanceof Error ? e.message : "Request failed", "error");
    }
  }, [load, routeTo, showToast, taskForm]);

  const saveJob = useCallback(async () => {
    const payload: Record<string, unknown> = {
      name: jobForm.name,
      message: jobForm.message,
      cron: jobForm.schedule_type === "cron" ? jobForm.cron : "",
      every: jobForm.schedule_type === "every" ? jobForm.every : "",
      session_mode: jobForm.session_mode,
      enabled: jobForm.enabled,
      agent_id: jobForm.agent_id,
    };
    if (isAdmin && jobForm.system_job) payload.user_id = 0;

    try {
      if (selection?.type === "job") {
        await api("PUT", `/api/scheduler/jobs/${selection.id}`, payload);
        await load();
        showToast("Schedule saved");
      } else {
        const created = await api<SchedulerJob>("POST", "/api/scheduler/jobs", payload);
        await load();
        selectJob(created);
        showToast("Schedule created");
      }
    } catch (e) {
      showToast(e instanceof Error ? e.message : "Request failed", "error");
    }
  }, [isAdmin, jobForm, load, selectJob, selection, showToast]);

  const deleteTask = useCallback(
    async (id: string) => {
      try {
        await api("DELETE", `/api/tasks/${id}`);
        routeTo(tab);
        await load();
        showToast("Task deleted");
      } catch (e) {
        showToast(e instanceof Error ? e.message : "Request failed", "error");
      }
    },
    [load, routeTo, showToast, tab],
  );

  const deleteJob = useCallback(
    async (id: string) => {
      try {
        await api("DELETE", `/api/scheduler/jobs/${id}`);
        routeTo(tab);
        await load();
        showToast("Schedule deleted");
      } catch (e) {
        showToast(e instanceof Error ? e.message : "Request failed", "error");
      }
    },
    [load, routeTo, showToast, tab],
  );

  const triggerJob = useCallback(
    async (job: SchedulerJob) => {
      setTriggeringJobId(job.id);
      try {
        await api("POST", `/api/scheduler/jobs/${job.id}/run`);
        await Promise.all([load(), loadRuns(job.id)]);
        showToast("Schedule triggered");
      } catch (e) {
        showToast(e instanceof Error ? e.message : "Request failed", "error");
      } finally {
        setTriggeringJobId(null);
      }
    },
    [load, loadRuns, showToast],
  );

  const handleTaskAction = useCallback((updated: ComponentsAgentTask) => {
    setTasks((prev) => prev.map((task) => (task.id === updated.id ? updated : task)));
  }, []);

  useEffect(() => {
    if (selection?.type !== "task") {
      setDetailTask(null);
      return;
    }
    if (detailTask?.id === selection.id || tasks.some((task) => task.id === selection.id)) return;
    api<ComponentsAgentTask>("GET", `/api/tasks/${encodeURIComponent(selection.id)}`)
      .then(setDetailTask)
      .catch(console.error);
  }, [selection, detailTask?.id, tasks]);

  const attentionTasks = tasks.filter(taskNeedsAttention);
  const runningTasks = tasks.filter((task) => task.status === "running");
  const pendingTasks = tasks.filter((task) => task.status === "pending");
  const failedTasks = tasks.filter(isFailedTask);
  const selectedTask =
    selection?.type === "task"
      ? (tasks.find((task) => task.id === selection.id) ??
        (detailTask?.id === selection.id ? detailTask : null))
      : null;
  const selectedJob =
    selection?.type === "job" ? jobs.find((job) => job.id === selection.id) : null;
  const selectedRunJob =
    selection?.type === "run" ? jobs.find((job) => job.id === selection.jobId) : null;
  const selectedRun =
    selectedRunJob && selection?.type === "run"
      ? (runsByJob[selectedRunJob.id] || []).find((run) => run.id === selection.runId)
      : null;
  const isJobFormValid =
    jobForm.name &&
    jobForm.message &&
    (jobForm.schedule_type === "cron" ? jobForm.cron : jobForm.every);

  const workLanes: Array<{ key: WorkLaneKey; title: string; count: number; hint: string }> = [
    {
      key: "attention",
      title: t("automations.lane.needsYou"),
      count: attentionTasks.length,
      hint: t("automations.lane.needsYouHint"),
    },
    {
      key: "running",
      title: t("automations.lane.running"),
      count: runningTasks.length,
      hint: t("automations.lane.runningHint"),
    },
    {
      key: "pending",
      title: t("automations.lane.pending"),
      count: pendingTasks.length,
      hint: t("automations.lane.pendingHint"),
    },
    {
      key: "failed",
      title: t("automations.lane.failures"),
      count: failedTasks.length,
      hint: t("automations.lane.failuresHint"),
    },
  ];

  const timeline = useMemo<TimelineItem[]>(() => {
    const taskItems = tasks.slice(0, 16).map((task) => ({
      id: `task:${task.id}`,
      kind: "task" as const,
      title: task.title,
      description: task.description || "Async task run",
      time: task.updated_at,
      status: task.status,
      selection: { type: "task", id: task.id } as Selection,
    }));

    const runItems = jobs.flatMap((job) =>
      (runsByJob[job.id] || []).slice(0, 4).map((run) => ({
        id: `run:${job.id}:${run.id}`,
        kind: "run" as const,
        title: `${job.name} run`,
        description: run.error || run.session_id || jobScheduleText(job),
        time: run.started_at,
        status: run.status,
        selection: { type: "run", jobId: job.id, runId: run.id } as Selection,
      })),
    );

    return [...taskItems, ...runItems]
      .filter((item) => item.time)
      .sort((a, b) => Date.parse(b.time) - Date.parse(a.time))
      .slice(0, 18);
  }, [jobs, runsByJob, tasks]);

  return (
    <div className="min-h-[calc(100vh-3.5rem)] bg-background">
      <div className="flex flex-col gap-4 px-5 py-4">
        <header className="flex flex-col gap-3 border-b border-border pb-4 lg:flex-row lg:items-center lg:justify-between">
          <div className="min-w-0">
            <div className="flex items-baseline gap-3">
              <h1 className="font-serif text-3xl italic leading-none tracking-tight text-foreground">
                Automations
              </h1>
              <div className="hidden text-sm text-muted-foreground md:block">
                {t("automations.subtitle")}
              </div>
            </div>
          </div>
          <div className="grid h-10 grid-cols-3 rounded-xl border border-border bg-muted p-1 sm:flex sm:w-auto">
            <TabButton
              active={tab === "work"}
              onClick={() => routeTo("work")}
              label={t("automations.tab.tasks")}
            />
            <TabButton
              active={tab === "schedules"}
              onClick={() => routeTo("schedules")}
              label={t("automations.tab.scheduler")}
            />
            <TabButton
              active={tab === "history"}
              onClick={() => routeTo("history")}
              label={t("automations.tab.log")}
            />
          </div>
        </header>
        <div className="grid gap-5 xl:grid-cols-[minmax(0,1fr)_32rem]">
          <section className="p-4">
            {tab === "work" && (
              <>
                <div className="mb-4 flex items-center justify-between gap-3">
                  <div>
                    <h2 className="text-lg font-semibold tracking-tight">
                      {t("automations.tasks.title")}
                    </h2>
                    <p className="text-sm text-muted-foreground">
                      {t("automations.tasks.description")}
                    </p>
                  </div>
                  <div className="flex items-center gap-2">
                    {loading && <Badge variant="outline">{t("automations.refreshing")}</Badge>}
                    <Button size="sm" onClick={() => setCreatingTask(true)}>
                      {t("automations.newTask")}
                    </Button>
                  </div>
                </div>
                <div className="mb-4 grid gap-2 md:grid-cols-4">
                  {workLanes.map((item) => (
                    <button
                      key={item.key}
                      type="button"
                      onClick={() => {
                        setWorkLane(item.key);
                        routeTo("work");
                      }}
                      className={`rounded-xl border p-3 text-left transition-all ${workLane === item.key ? "border-primary bg-primary/[0.04]" : item.key === "attention" && item.count > 0 ? "border-destructive/30 bg-destructive/[0.03]" : "border-border bg-background/60 hover:bg-muted/40"}`}
                    >
                      <div className="text-2xl font-semibold tracking-tight">{item.count}</div>
                      <div className="mt-1 text-sm font-medium">{item.title}</div>
                      <div className="mt-1 text-[11px] font-mono text-muted-foreground">
                        {item.hint}
                      </div>
                    </button>
                  ))}
                </div>
                <div className="grid gap-3 2xl:grid-cols-4">
                  <LaneColumn
                    title={t("automations.lane.needsYou")}
                    active={workLane === "attention"}
                    hidden={workLane !== "attention"}
                  >
                    {attentionTasks.map((task) => (
                      <TaskCard
                        key={task.id}
                        task={task}
                        tone="attention"
                        onSelect={() => routeTo("work", { type: "task", id: task.id })}
                      />
                    ))}
                    {attentionTasks.length === 0 && (
                      <EmptyCard text={t("automations.empty.needsYou")} />
                    )}
                  </LaneColumn>
                  <LaneColumn
                    title={t("automations.lane.running")}
                    active={workLane === "running"}
                    hidden={workLane !== "running"}
                  >
                    {runningTasks.map((task) => (
                      <TaskCard
                        key={task.id}
                        task={task}
                        tone="running"
                        onSelect={() => routeTo("work", { type: "task", id: task.id })}
                      />
                    ))}
                    {runningTasks.length === 0 && (
                      <EmptyCard text={t("automations.empty.running")} />
                    )}
                  </LaneColumn>
                  <LaneColumn
                    title={t("automations.lane.pending")}
                    active={workLane === "pending"}
                    hidden={workLane !== "pending"}
                  >
                    {pendingTasks.map((task) => (
                      <TaskCard
                        key={task.id}
                        task={task}
                        tone="running"
                        onSelect={() => routeTo("work", { type: "task", id: task.id })}
                      />
                    ))}
                    {pendingTasks.length === 0 && (
                      <EmptyCard text={t("automations.empty.pending")} />
                    )}
                  </LaneColumn>
                  <LaneColumn
                    title={t("automations.lane.failures")}
                    active={workLane === "failed"}
                    hidden={workLane !== "failed"}
                  >
                    {failedTasks.map((task) => (
                      <TaskCard
                        key={task.id}
                        task={task}
                        tone="failed"
                        onSelect={() => routeTo("work", { type: "task", id: task.id })}
                      />
                    ))}
                    {failedTasks.length === 0 && (
                      <EmptyCard text={t("automations.empty.failures")} />
                    )}
                  </LaneColumn>
                </div>
              </>
            )}

            {tab === "schedules" && (
              <>
                <div className="mb-4 flex items-center justify-between gap-3">
                  <div>
                    <h2 className="text-lg font-semibold tracking-tight">
                      {t("automations.scheduler.title")}
                    </h2>
                    <p className="text-sm text-muted-foreground">
                      {t("automations.scheduler.description")}
                    </p>
                  </div>
                  <Button size="sm" onClick={startNewJob}>
                    {t("automations.newSchedule")}
                  </Button>
                </div>
                <div className="grid gap-3 lg:grid-cols-2">
                  {jobs.map((job) => (
                    <ScheduleCard
                      key={job.id}
                      job={job}
                      failed={isFailedJob(job)}
                      onSelect={() => selectJob(job)}
                    />
                  ))}
                  {jobs.length === 0 && <EmptyCard text={t("automations.scheduler.empty")} />}
                </div>
              </>
            )}

            {tab === "history" && (
              <>
                <div className="mb-4">
                  <h2 className="text-lg font-semibold tracking-tight">
                    {t("automations.log.title")}
                  </h2>
                  <p className="text-sm text-muted-foreground">
                    {t("automations.log.description")}
                  </p>
                </div>
                <div className="relative grid gap-3 before:absolute before:bottom-2 before:left-3 before:top-2 before:w-px before:bg-border lg:grid-cols-2 lg:before:hidden">
                  {timeline.map((item) => (
                    <TimelineRow
                      key={item.id}
                      item={item}
                      onSelect={() => routeTo("history", item.selection)}
                    />
                  ))}
                  {timeline.length === 0 && <EmptyCard text={t("automations.log.empty")} />}
                </div>
              </>
            )}
          </section>

          <aside className="hidden border-l border-border p-5 xl:sticky xl:top-5 xl:block xl:max-h-[calc(100vh-6rem)] xl:overflow-y-auto">
            {selectedTask ? (
              <DetailShell
                title={t("automations.detail.task")}
                onDelete={() =>
                  setConfirm({
                    msg: t("automations.confirm.deleteTask"),
                    action: () => deleteTask(selectedTask.id),
                  })
                }
              >
                <TaskDetail task={selectedTask} onAction={handleTaskAction} onToast={showToast} />
              </DetailShell>
            ) : selectedRunJob && selectedRun ? (
              <SchedulerRunDetail job={selectedRunJob} run={selectedRun} />
            ) : selectedJob || selection?.type === "new-job" ? (
              <ScheduleDetail
                job={selectedJob}
                agents={agents}
                isAdmin={isAdmin}
                form={jobForm}
                setForm={setJobForm}
                isValid={Boolean(isJobFormValid)}
                savingLabel={
                  selectedJob ? t("automations.saveSchedule") : t("automations.createSchedule")
                }
                triggering={selectedJob ? triggeringJobId === selectedJob.id : false}
                onSave={saveJob}
                onTrigger={selectedJob ? () => triggerJob(selectedJob) : undefined}
                onDelete={
                  selectedJob && selectedJob.owner_kind === "user"
                    ? () =>
                        setConfirm({
                          msg: t("automations.confirm.deleteSchedule"),
                          action: () => deleteJob(selectedJob.id),
                        })
                    : undefined
                }
              />
            ) : (
              <div className="flex min-h-[30rem] flex-col items-center justify-center rounded-xl border border-dashed border-border bg-muted/30 p-6 text-center">
                <h2 className="font-serif text-2xl italic tracking-tight">
                  {t("automations.select.title")}
                </h2>
                <p className="mt-2 max-w-sm text-sm leading-6 text-muted-foreground">
                  {t("automations.select.description")}
                </p>
              </div>
            )}
          </aside>
        </div>{" "}
      </div>

      <Dialog
        open={detailModalMode && Boolean(selection)}
        onOpenChange={(open) => !open && routeTo(tab)}
      >
        <DialogPopup className="h-[85vh] max-w-3xl xl:hidden" showCloseButton>
          <DialogPanel className="p-4" scrollFade={false}>
            {selectedTask ? (
              <DetailShell
                title={t("automations.detail.task")}
                onDelete={() =>
                  setConfirm({
                    msg: t("automations.confirm.deleteTask"),
                    action: () => deleteTask(selectedTask.id),
                  })
                }
              >
                <TaskDetail task={selectedTask} onAction={handleTaskAction} onToast={showToast} />
              </DetailShell>
            ) : selectedRunJob && selectedRun ? (
              <SchedulerRunDetail job={selectedRunJob} run={selectedRun} />
            ) : selectedJob || selection?.type === "new-job" ? (
              <ScheduleDetail
                job={selectedJob}
                agents={agents}
                isAdmin={isAdmin}
                form={jobForm}
                setForm={setJobForm}
                isValid={Boolean(isJobFormValid)}
                savingLabel={
                  selectedJob ? t("automations.saveSchedule") : t("automations.createSchedule")
                }
                triggering={selectedJob ? triggeringJobId === selectedJob.id : false}
                onSave={saveJob}
                onTrigger={selectedJob ? () => triggerJob(selectedJob) : undefined}
                onDelete={
                  selectedJob && selectedJob.owner_kind === "user"
                    ? () =>
                        setConfirm({
                          msg: t("automations.confirm.deleteSchedule"),
                          action: () => deleteJob(selectedJob.id),
                        })
                    : undefined
                }
              />
            ) : null}
          </DialogPanel>
        </DialogPopup>
      </Dialog>

      <Dialog open={creatingTask} onOpenChange={(open) => !open && setCreatingTask(false)}>
        <DialogPopup className="max-w-md">
          <DialogHeader>
            <DialogTitle>{t("automations.newTask")}</DialogTitle>
          </DialogHeader>
          <DialogPanel>
            <div className="space-y-4">
              <Field label={t("tasks.fieldTitle")}>
                <Input
                  type="text"
                  value={taskForm.title}
                  onChange={(e) => setTaskForm((form) => ({ ...form, title: e.target.value }))}
                  placeholder={t("tasks.fieldTitlePlaceholder")}
                  nativeInput
                />
              </Field>
              <Field label={t("tasks.fieldDescription")}>
                <Textarea
                  value={taskForm.description}
                  onChange={(e) =>
                    setTaskForm((form) => ({ ...form, description: e.target.value }))
                  }
                  placeholder={t("tasks.fieldDescriptionPlaceholder")}
                />
              </Field>
              <Field label={t("tasks.fieldPriority")}>
                <select
                  value={taskForm.priority}
                  onChange={(e) =>
                    setTaskForm((form) => ({
                      ...form,
                      priority: e.target.value as "routine" | "urgent",
                    }))
                  }
                  className="h-8.5 w-full rounded-lg border border-input bg-background px-3 text-sm text-foreground outline-none focus:ring-2 focus:ring-ring"
                >
                  <option value="routine">{t("tasks.priorityRoutine")}</option>
                  <option value="urgent">{t("tasks.priorityUrgent")}</option>
                </select>
              </Field>
            </div>
          </DialogPanel>
          <DialogFooter variant="bare">
            <Button size="sm" disabled={!taskForm.title.trim()} onClick={createTask}>
              {t("common.create")}
            </Button>
            <Button variant="outline" size="sm" onClick={() => setCreatingTask(false)}>
              {t("common.cancel")}
            </Button>
          </DialogFooter>
        </DialogPopup>
      </Dialog>

      <Dialog open={!!confirm} onOpenChange={(open) => !open && setConfirm(null)}>
        <DialogPopup className="max-w-sm">
          <DialogHeader>
            <DialogTitle>{confirm?.msg}</DialogTitle>
          </DialogHeader>
          <DialogFooter variant="bare">
            <Button variant="outline" size="sm" onClick={() => setConfirm(null)}>
              {t("common.cancel")}
            </Button>
            <Button
              variant="destructive"
              size="sm"
              onClick={() => {
                confirm?.action();
                setConfirm(null);
              }}
            >
              {t("common.delete")}
            </Button>
          </DialogFooter>
        </DialogPopup>
      </Dialog>

      {toast && (
        <div
          className={`fixed bottom-4 right-4 z-50 max-w-sm rounded-lg border px-4 py-3 text-sm shadow-md ${toast.kind === "error" ? "border-destructive/36 bg-destructive/8 text-destructive-foreground" : "border-success/36 bg-success/8 text-success-foreground"}`}
        >
          {toast.msg}
        </div>
      )}
    </div>
  );
}

function TabButton({
  active,
  onClick,
  label,
}: {
  active: boolean;
  onClick: () => void;
  label: string;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={`h-8 rounded-lg px-3 text-sm font-medium transition-colors sm:min-w-24 ${active ? "bg-primary text-primary-foreground" : "text-muted-foreground hover:bg-muted/60 hover:text-foreground"}`}
    >
      {label}
    </button>
  );
}

function DetailShell({
  title,
  onDelete,
  children,
}: {
  title: string;
  onDelete: () => void;
  children: ReactNode;
}) {
  const { t } = useI18n();

  return (
    <div>
      <div className="mb-5 flex items-center justify-between gap-3 pr-10 xl:pr-0">
        <div className="text-[11px] font-mono font-medium uppercase tracking-wider text-muted-foreground">
          {title}
        </div>
        <Button
          variant="ghost"
          size="sm"
          className="text-destructive hover:text-destructive"
          onClick={onDelete}
        >
          {t("common.delete")}
        </Button>
      </div>
      {children}
    </div>
  );
}

function SchedulerRunDetail({ job, run }: { job: SchedulerJob; run: SchedulerJobRun }) {
  return (
    <div>
      <div className="mb-5">
        <div className="text-[11px] font-mono font-medium uppercase tracking-wider text-muted-foreground">
          Scheduler run
        </div>
        <h2 className="mt-2 font-serif text-2xl italic tracking-tight">{job.name}</h2>
      </div>
      <div className="rounded-xl border border-border bg-muted/30 p-4">
        <div className="mb-4 flex flex-wrap gap-2">
          <Badge variant={runBadgeVariant(run.status)}>{run.status}</Badge>
          <Badge variant="outline">{jobScheduleText(job)}</Badge>
          <Badge variant="outline">{job.owner_kind || "user"}</Badge>
        </div>
        <div className="space-y-3 text-sm">
          <DetailRow label="Started" value={formatTime(run.started_at)} />
          {run.duration && <DetailRow label="Duration" value={run.duration} />}
          {run.error && <DetailRow label="Error" value={run.error} danger />}
        </div>
      </div>
      {schedulerRunSessionId(job, run) && (
        <div className="mt-4">
          <SessionConversation
            sessionId={schedulerRunSessionId(job, run)}
            placeholder="Ask Stella about this run…"
          />
        </div>
      )}
      <ScheduleSummary job={job} />
    </div>
  );
}

function DetailRow({
  label,
  value,
  danger = false,
}: {
  label: string;
  value: string;
  danger?: boolean;
}) {
  return (
    <div className="grid grid-cols-[5rem_1fr] gap-3">
      <span className="text-muted-foreground">{label}</span>
      <span className={danger ? "text-destructive" : "text-foreground"}>{value}</span>
    </div>
  );
}

function ScheduleDetail({
  job,
  agents,
  isAdmin,
  form,
  setForm,
  isValid,
  savingLabel,
  triggering,
  onSave,
  onTrigger,
  onDelete,
}: {
  job: SchedulerJob | null | undefined;
  agents: Agent[];
  isAdmin: boolean;
  form: JobForm;
  setForm: (updater: (form: JobForm) => JobForm) => void;
  isValid: boolean;
  savingLabel: string;
  triggering: boolean;
  onSave: () => void;
  onTrigger?: () => void;
  onDelete?: () => void;
}) {
  const { t } = useI18n();
  const isNew = !job;
  const readOnly = Boolean(job && job.owner_kind !== "user");

  return (
    <div>
      <div className="mb-5 flex items-start justify-between gap-3 pr-10 xl:pr-0">
        <div>
          <div className="text-[11px] font-mono font-medium uppercase tracking-wider text-muted-foreground">
            {isNew ? "New schedule" : "Schedule runs"}
          </div>
          <h2 className="mt-2 font-serif text-2xl italic tracking-tight">
            {job?.name || "Create a schedule"}
          </h2>
        </div>
        <div className="flex gap-2">
          {onTrigger && (
            <Button variant="outline" size="sm" loading={triggering} onClick={onTrigger}>
              Run now
            </Button>
          )}
          {onDelete && (
            <Button
              variant="ghost"
              size="sm"
              className="text-destructive hover:text-destructive"
              onClick={onDelete}
            >
              {t("common.delete")}
            </Button>
          )}
        </div>
      </div>

      {job && <ScheduleSummary job={job} />}

      {job && readOnly && <PluginSchedule job={job} />}

      {!readOnly && (
        <details className="mt-6 rounded-xl border border-border bg-muted/30 p-4" open={isNew}>
          <summary className="cursor-pointer text-sm font-semibold">
            {isNew ? "Schedule settings" : "Edit schedule settings"}
          </summary>
          <div className="mt-4 space-y-4">
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="Name">
                <Input
                  type="text"
                  value={form.name}
                  onChange={(e) => setForm((prev) => ({ ...prev, name: e.target.value }))}
                  placeholder="Daily summary"
                  nativeInput
                />
              </Field>
              <Field label="Session mode">
                <select
                  value={form.session_mode}
                  onChange={(e) => setForm((prev) => ({ ...prev, session_mode: e.target.value }))}
                  className="h-8.5 w-full rounded-lg border border-input bg-background px-3 text-sm text-foreground outline-none focus:ring-2 focus:ring-ring"
                >
                  <option value="reuse">Reuse session</option>
                  <option value="new">New session each run</option>
                </select>
              </Field>
            </div>
            <Field label="Schedule">
              <div className="mb-2 flex items-center gap-4">
                <label className="flex cursor-pointer items-center gap-2 text-sm">
                  <input
                    type="radio"
                    name="schedule_type"
                    checked={form.schedule_type === "cron"}
                    onChange={() => setForm((prev) => ({ ...prev, schedule_type: "cron" }))}
                    className="accent-primary"
                  />
                  Cron
                </label>
                <label className="flex cursor-pointer items-center gap-2 text-sm">
                  <input
                    type="radio"
                    name="schedule_type"
                    checked={form.schedule_type === "every"}
                    onChange={() => setForm((prev) => ({ ...prev, schedule_type: "every" }))}
                    className="accent-primary"
                  />
                  Interval
                </label>
              </div>
              {form.schedule_type === "cron" ? (
                <Input
                  type="text"
                  value={form.cron}
                  onChange={(e) => setForm((prev) => ({ ...prev, cron: e.target.value }))}
                  placeholder="0 9 * * 1-5"
                  className="font-mono"
                  nativeInput
                />
              ) : (
                <Input
                  type="text"
                  value={form.every}
                  onChange={(e) => setForm((prev) => ({ ...prev, every: e.target.value }))}
                  placeholder="30m, 2h"
                  className="font-mono"
                  nativeInput
                />
              )}
            </Field>
            <Field label="Agent">
              <select
                value={form.agent_id}
                onChange={(e) => setForm((prev) => ({ ...prev, agent_id: e.target.value }))}
                className="h-8.5 w-full rounded-lg border border-input bg-background px-3 text-sm text-foreground outline-none focus:ring-2 focus:ring-ring"
              >
                <option value="">Default agent</option>
                {agents.map((agent) => (
                  <option key={agent.id} value={agent.id}>
                    {agent.name}
                  </option>
                ))}
              </select>
            </Field>
            {isAdmin && (
              <div className="flex items-center gap-2">
                <Switch
                  checked={form.system_job}
                  onCheckedChange={(value) => setForm((prev) => ({ ...prev, system_job: value }))}
                />
                <span className="text-sm">System job</span>
                <span className="text-xs text-muted-foreground">broadcasts to all users</span>
              </div>
            )}
            <Field label="Message">
              <Textarea
                value={form.message}
                onChange={(e) => setForm((prev) => ({ ...prev, message: e.target.value }))}
                placeholder="What should the agent do?"
              />
            </Field>
            <div className="flex items-center justify-between border-t border-border pt-4">
              <label className="flex items-center gap-2 text-sm">
                <Switch
                  checked={form.enabled}
                  onCheckedChange={(value) => setForm((prev) => ({ ...prev, enabled: value }))}
                />
                Enabled
              </label>
              <Button size="sm" disabled={!isValid} onClick={onSave}>
                {savingLabel}
              </Button>
            </div>
          </div>
        </details>
      )}
    </div>
  );
}

function ScheduleSummary({ job }: { job: SchedulerJob }) {
  return (
    <div className="rounded-xl border border-border bg-muted/30 p-4">
      <div className="mb-3 flex flex-wrap gap-2">
        <Badge variant={job.enabled ? "success" : "outline"}>
          {job.enabled ? "enabled" : "disabled"}
        </Badge>
        <Badge variant="outline">{jobScheduleText(job)}</Badge>
        <Badge variant="outline">{job.owner_kind || "user"}</Badge>
        {job.session_mode && <Badge variant="outline">session:{job.session_mode}</Badge>}
      </div>
    </div>
  );
}

function PluginSchedule({ job }: { job: SchedulerJob }) {
  return (
    <details className="mt-6 rounded-xl border border-border bg-muted/30 p-4">
      <summary className="cursor-pointer text-sm font-semibold">Schedule definition</summary>
      <div className="mt-4 space-y-3">
        <div className="prose prose-sm max-w-none text-foreground [&_ol]:pl-5 [&_ul]:pl-5">
          <Streamdown>{job.description || job.message || ""}</Streamdown>
        </div>
        <div className="flex flex-wrap gap-2">
          <Badge variant="info">plugin:{job.plugin_id}</Badge>
          <Badge variant="outline">key:{job.job_key}</Badge>
          <Badge variant="outline">runtime:{job.runtime_name}</Badge>
          <Badge variant={job.enabled ? "success" : "outline"}>{job.enabled ? "on" : "off"}</Badge>
        </div>
        {job.payload && Object.keys(job.payload).length > 0 && (
          <pre className="overflow-x-auto rounded-lg bg-muted p-3 text-xs">
            {JSON.stringify(job.payload, null, 2)}
          </pre>
        )}
      </div>
    </details>
  );
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <label className="block space-y-1.5">
      <span className="block text-[11px] font-mono font-medium uppercase tracking-wider text-muted-foreground">
        {label}
      </span>
      {children}
    </label>
  );
}

function LaneColumn({
  title,
  active,
  hidden,
  children,
}: {
  title: string;
  active: boolean;
  hidden: boolean;
  children: ReactNode;
}) {
  return (
    <div
      className={`min-h-[18rem] rounded-xl border p-2 2xl:min-h-[30rem] ${hidden ? "hidden 2xl:block" : ""} ${active ? "border-primary/40 bg-primary/[0.03]" : "border-border bg-muted/30"}`}
    >
      <div className="px-2 py-2 text-[11px] font-mono font-medium uppercase tracking-wider text-muted-foreground">
        {title}
      </div>
      <div className="space-y-2">{children}</div>
    </div>
  );
}

function TaskCard({
  task,
  tone,
  onSelect,
}: {
  task: ComponentsAgentTask;
  tone: "attention" | "running" | "failed";
  onSelect: () => void;
}) {
  const border =
    tone === "attention"
      ? "border-destructive/30"
      : tone === "running"
        ? "border-primary/30"
        : "border-destructive/30";
  return (
    <button
      type="button"
      onClick={onSelect}
      className={`block w-full rounded-xl border ${border} bg-card p-3 text-left transition-colors hover:bg-muted/40`}
    >
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0 text-sm font-medium leading-5">{task.title}</div>
        <Badge variant={taskBadgeVariant(task.status)}>{task.status}</Badge>
      </div>
      {task.description && (
        <p className="mt-2 line-clamp-2 text-xs leading-5 text-muted-foreground">
          {task.description}
        </p>
      )}
      <div className="mt-3 flex flex-wrap gap-1.5">
        <Badge variant="outline">task</Badge>
        <Badge variant="outline">{task.priority}</Badge>
        {task.notify_at && <Badge variant="outline">notify {formatTime(task.notify_at)}</Badge>}
      </div>
    </button>
  );
}

function ScheduleCard({
  job,
  failed = false,
  onSelect,
}: {
  job: SchedulerJob;
  failed?: boolean;
  onSelect: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onSelect}
      className={`block w-full rounded-xl border bg-card p-3 text-left transition-colors hover:bg-muted/40 ${failed ? "border-destructive/30" : "border-border"}`}
    >
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0 text-sm font-medium leading-5">{job.name}</div>
        <Badge variant={failed ? "error" : job.enabled ? "success" : "outline"}>
          {failed ? "failed" : job.enabled ? "enabled" : "disabled"}
        </Badge>
      </div>
      <p className="mt-2 line-clamp-2 text-xs leading-5 text-muted-foreground">
        {failed ? job.last_error : job.message || job.description || jobScheduleText(job)}
      </p>
      <div className="mt-3 flex flex-wrap gap-1.5">
        <Badge variant="outline">schedule</Badge>
        <Badge variant="outline">{job.owner_kind || "user"}</Badge>
        <Badge variant="outline">{jobScheduleText(job)}</Badge>
      </div>
    </button>
  );
}

function TimelineRow({ item, onSelect }: { item: TimelineItem; onSelect: () => void }) {
  const badgeVariant =
    item.kind === "run" ? runBadgeVariant(item.status) : taskBadgeVariant(item.status);
  return (
    <button
      type="button"
      onClick={onSelect}
      className="relative ml-7 block rounded-xl border border-border bg-card p-3 text-left transition-colors hover:bg-muted/40"
    >
      <span className="absolute -left-[1.72rem] top-4 size-2.5 rounded-full bg-primary ring-4 ring-background" />
      <div className="flex items-start justify-between gap-2">
        <div>
          <div className="text-xs font-mono text-muted-foreground">{formatTime(item.time)}</div>
          <div className="mt-1 text-sm font-medium leading-5">{item.title}</div>
        </div>
        <Badge variant={badgeVariant}>{item.status}</Badge>
      </div>
      <p className="mt-2 line-clamp-2 text-xs leading-5 text-muted-foreground">
        {item.description}
      </p>
      <div className="mt-3">
        <Badge variant="outline">{item.kind}</Badge>
      </div>
    </button>
  );
}

function EmptyCard({ text }: { text: string }) {
  return (
    <div className="rounded-xl border border-dashed border-border bg-background/70 p-3 text-xs leading-5 text-muted-foreground">
      {text}
    </div>
  );
}

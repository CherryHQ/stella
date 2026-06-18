import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { reopenTask, updateSchedulerJob } from "@/lib/api-client";
import type { ComponentsGoal, ComponentsTask, JobRun } from "@/lib/api-client/types.gen";
import type { SchedulerJob } from "@/lib/types";
import { goalsOptions, goalGraphOptions } from "@/lib/queries/goals";
import { agentSchedulerJobsOptions } from "@/lib/queries/agents";
import { useI18n } from "@/lib/i18n";
import { useAppShell } from "@/layouts/AppShell";
import { formatTime } from "@/lib/time";
import { cn } from "@/lib/utils";
import { Plus } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { standaloneTasksOptions, schedulerJobRecentRunsOptions } from "./queries";
import { jobNextRunAt } from "./types";
import {
  ProgressBar,
  formatUntil,
  humanScheduleText,
  rollup,
  statusLabel,
  statusMeta,
} from "./lib";

export function OverviewPage() {
  const { t } = useI18n();
  const { agentId, projectId } = useParams({ strict: false }) as {
    agentId: string;
    projectId?: string;
  };
  const navigate = useNavigate();
  const { setHeaderActions } = useAppShell();

  const { data: goals = [] } = useQuery(goalsOptions(agentId));
  const { data: jobs = [] } = useQuery(agentSchedulerJobsOptions(agentId));
  const { data: tasks = [] } = useQuery(standaloneTasksOptions(agentId, projectId));

  useEffect(() => {
    setHeaderActions(
      <div className="flex items-center gap-1">
        <Button
          render={
            <Link
              to="/agents/$agentId/tasks/new"
              params={{ agentId }}
              search={projectId ? { project_id: projectId } : {}}
            />
          }
          variant="outline"
          size="sm"
        >
          <Plus />
          <span className="max-sm:hidden">{t("hub.newTask")}</span>
        </Button>
      </div>,
    );
    return () => {
      setHeaderActions(null);
    };
  }, [setHeaderActions, t, agentId, projectId]);

  const needsYou = useMemo(() => {
    const items: NeedsYouItem[] = [];
    for (const g of goals) {
      if (g.status === "blocked" || g.status === "reviewing") items.push({ kind: "goal", goal: g });
    }
    for (const task of tasks) {
      if (task.status === "blocked" || task.status === "reviewing" || task.status === "failed")
        items.push({ kind: "task", task });
    }
    items.sort((a, b) => new Date(itemUpdated(b)).getTime() - new Date(itemUpdated(a)).getTime());
    return items;
  }, [goals, tasks]);

  const runningCount = useMemo(
    () =>
      goals.filter((g) => g.status === "running" || g.status === "planning").length +
      tasks.filter((task) => task.status === "running").length,
    [goals, tasks],
  );

  const doneThisWeek = useMemo(() => {
    const cutoff = Date.now() - 7 * 86_400_000;
    const fresh = (s: string) => new Date(s).getTime() >= cutoff;
    return (
      goals.filter((g) => g.status === "done" && fresh(g.updated_at)).length +
      tasks.filter((task) => task.status === "done" && fresh(task.updated_at)).length
    );
  }, [goals, tasks]);

  const sortedJobs = useMemo(() => {
    const byEnabledThenName = (a: SchedulerJob, b: SchedulerJob) => {
      if (a.enabled !== b.enabled) return a.enabled ? -1 : 1;
      return a.name.localeCompare(b.name);
    };
    return [...(jobs as SchedulerJob[])].sort(byEnabledThenName);
  }, [jobs]);

  const soonestNextRun = useMemo(() => {
    let soonest: Date | null = null;
    for (const j of sortedJobs) {
      const next = jobNextRunAt(j);
      if (next && (!soonest || next < soonest)) soonest = next;
    }
    return soonest;
  }, [sortedJobs]);

  const sortedGoals = useMemo(
    () =>
      [...goals].sort(
        (a, b) => new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime(),
      ),
    [goals],
  );

  const activeGoals = useMemo(
    () => sortedGoals.filter((g) => !["done", "failed", "cancelled"].includes(g.status)),
    [sortedGoals],
  );
  const historyGoalsCount = sortedGoals.length - activeGoals.length;
  const previewGoals = useMemo(() => activeGoals.slice(0, 6), [activeGoals]);

  const recentDone = useMemo(() => {
    const items: NeedsYouItem[] = [];
    for (const g of goals) if (g.status === "done") items.push({ kind: "goal", goal: g });
    for (const task of tasks) if (task.status === "done") items.push({ kind: "task", task });
    items.sort((a, b) => new Date(itemUpdated(b)).getTime() - new Date(itemUpdated(a)).getTime());
    return items.slice(0, 5);
  }, [goals, tasks]);

  const openItem = useCallback(
    (item: NeedsYouItem) => {
      if (item.kind === "goal")
        void navigate({
          to: "/agents/$agentId/tasks/goals/$goalId",
          params: { agentId, goalId: item.goal.id },
        });
      else
        void navigate({
          to: "/agents/$agentId/tasks/$taskId",
          params: { agentId, taskId: item.task.id },
        });
    },
    [navigate, agentId],
  );

  return (
    <div className="h-full min-h-0 overflow-y-auto bg-background">
      <div className="mx-auto max-w-[980px] px-6 py-6 pb-20 sm:px-8">
        {/* Stats */}
        <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
          <StatCard
            num={needsYou.length}
            label={t("hub.secNeedsYou")}
            tone={needsYou.length > 0 ? "attn" : undefined}
          />
          <StatCard num={runningCount} label={t("hub.statRunning")} tone="live" />
          <StatCard
            num={sortedJobs.filter((j) => j.enabled).length}
            label={
              soonestNextRun
                ? `${t("hub.secSchedules")} · ${t("hub.nextRunStat", { when: formatUntil(t, soonestNextRun) })}`
                : t("hub.secSchedules")
            }
          />
          <StatCard num={doneThisWeek} label={t("hub.statDoneWeek")} />
        </div>

        {/* Needs you */}
        <section className="mt-8">
          <SectionHead title={t("hub.secNeedsYou")} count={needsYou.length} />
          {needsYou.length === 0 ? (
            <div className="rounded-xl border border-border px-4 py-3.5">
              <p className="text-sm font-medium text-muted-foreground">{t("hub.noNeedsYou")}</p>
              <p className="mt-1 text-[12.5px] text-muted-foreground">{t("hub.noNeedsYouDesc")}</p>
            </div>
          ) : (
            <>
              <p className="mb-3 text-[12.5px] text-muted-foreground">{t("hub.needsYouHint")}</p>
              <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
                {needsYou.map((item) => (
                  <NeedsYouCard
                    key={item.kind === "goal" ? item.goal.id : item.task.id}
                    item={item}
                    agentId={agentId}
                    onOpen={() => openItem(item)}
                  />
                ))}
              </div>
            </>
          )}
        </section>

        {/* Schedules */}
        <section className="mt-8">
          <SectionHead title={t("hub.secSchedules")} count={sortedJobs.length} />
          {sortedJobs.length === 0 ? (
            <p className="text-sm text-muted-foreground">{t("hub.noSchedules")}</p>
          ) : (
            <SchedulesTable jobs={sortedJobs} agentId={agentId} />
          )}
        </section>

        {/* Goals */}
        <section className="mt-8">
          <SectionHead
            title={t("hub.activeWork")}
            count={activeGoals.length}
            action={
              <div className="flex items-center gap-2">
                {historyGoalsCount > 0 && (
                  <Button
                    render={
                      <Link
                        to="/agents/$agentId/tasks/goals"
                        params={{ agentId }}
                        search={{ mode: "history" }}
                      />
                    }
                    variant="ghost"
                    size="xs"
                  >
                    {t("hub.openHistory")}
                  </Button>
                )}
                <Button
                  render={
                    <Link
                      to="/agents/$agentId/tasks/goals"
                      params={{ agentId }}
                      search={{ mode: "active" }}
                    />
                  }
                  variant="outline"
                  size="xs"
                >
                  {t("hub.openActiveGoals")}
                </Button>
              </div>
            }
          />
          {activeGoals.length === 0 ? (
            <p className="text-sm text-muted-foreground">{t("hub.noActiveGoals")}</p>
          ) : (
            <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
              {previewGoals.map((g) => (
                <GoalCard key={g.id} goal={g} onOpen={() => openItem({ kind: "goal", goal: g })} />
              ))}
            </div>
          )}
        </section>

        {/* Recently completed */}
        {recentDone.length > 0 && (
          <section className="mt-8">
            <SectionHead title={t("hub.secRecentDone")} />
            <div className="overflow-hidden rounded-xl border border-border">
              {recentDone.map((item) => {
                const id = item.kind === "goal" ? item.goal.id : item.task.id;
                const title = item.kind === "goal" ? item.goal.title : item.task.title;
                return (
                  <button
                    key={`${item.kind}:${id}`}
                    type="button"
                    onClick={() => openItem(item)}
                    className="flex w-full items-center gap-3 border-b border-border px-3.5 py-2.5 text-left text-[13px] last:border-b-0 hover:bg-muted/50"
                  >
                    <span className="min-w-0 flex-1 truncate font-medium">{title}</span>
                    <span className="shrink-0 font-mono text-xs text-chart-3">
                      {statusLabel(t, "done")}
                    </span>
                    <span className="shrink-0 font-mono text-xs text-muted-foreground">
                      {formatTime(itemUpdated(item))}
                    </span>
                  </button>
                );
              })}
            </div>
          </section>
        )}
      </div>
    </div>
  );
}

type NeedsYouItem = { kind: "goal"; goal: ComponentsGoal } | { kind: "task"; task: ComponentsTask };

function itemUpdated(item: NeedsYouItem): string {
  return item.kind === "goal" ? item.goal.updated_at : item.task.updated_at;
}

function StatCard({ num, label, tone }: { num: number; label: string; tone?: "attn" | "live" }) {
  return (
    <div
      className={cn(
        "rounded-xl border border-border px-4 py-3.5",
        tone === "attn" && "border-chart-4/30 bg-chart-4/[0.07]",
      )}
    >
      <div
        className={cn(
          "text-[26px] font-semibold tracking-tight",
          tone === "attn" && "text-chart-4",
          tone === "live" && num > 0 && "text-chart-3",
        )}
      >
        {num}
      </div>
      <div className="mt-0.5 truncate text-xs text-muted-foreground" title={label}>
        {label}
      </div>
    </div>
  );
}

function SectionHead({
  title,
  count,
  action,
}: {
  title: string;
  count?: number;
  action?: ReactNode;
}) {
  return (
    <div className="mb-3 flex items-baseline gap-2">
      <h2 className="text-sm font-semibold">{title}</h2>
      {count !== undefined && count > 0 && (
        <span className="rounded-full bg-muted px-2 py-px font-mono text-xs text-muted-foreground">
          {count}
        </span>
      )}
      {action && <div className="ml-auto">{action}</div>}
    </div>
  );
}

function NeedsYouCard({
  item,
  agentId,
  onOpen,
}: {
  item: NeedsYouItem;
  agentId: string;
  onOpen: () => void;
}) {
  const { t } = useI18n();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const [acting, setActing] = useState(false);

  const status = item.kind === "goal" ? item.goal.status : item.task.status;
  const isFail = status === "failed";
  const kindLabel = item.kind === "goal" ? t("hub.kindGoal") : t("hub.kindTask");
  const title = item.kind === "goal" ? item.goal.title : item.task.title;
  const desc = item.kind === "goal" ? item.goal.description : item.task.description;

  const handleRetry = async (e: React.MouseEvent) => {
    e.stopPropagation();
    if (item.kind !== "task") return;
    setActing(true);
    try {
      await reopenTask({ path: { taskId: item.task.id }, throwOnError: true });
      void qc.invalidateQueries({ queryKey: ["standalone-tasks"] });
    } finally {
      setActing(false);
    }
  };

  const handleOpenSession = (e: React.MouseEvent) => {
    e.stopPropagation();
    if (item.kind !== "task" || !item.task.session_id) return;
    void navigate({
      to: "/agents/$agentId/sessions/$sessionId",
      params: { agentId, sessionId: item.task.session_id },
    });
  };

  return (
    <div
      role="button"
      tabIndex={0}
      onClick={onOpen}
      onKeyDown={(e) => e.key === "Enter" && onOpen()}
      className={cn(
        "cursor-pointer rounded-xl border border-border border-l-[3px] px-4 py-3.5 hover:bg-muted/40",
        isFail ? "border-l-destructive" : "border-l-chart-4",
      )}
    >
      <div className="font-mono text-xs font-medium uppercase tracking-[0.08em] text-muted-foreground">
        {kindLabel} · {statusLabel(t, status)}
      </div>
      <div className="mt-1 text-sm font-semibold">{title}</div>
      <div className="mt-1 line-clamp-2 text-[12.5px] leading-relaxed text-muted-foreground">
        {desc || t("hub.updatedAt", { time: formatTime(itemUpdated(item)) })}
      </div>
      <div className="mt-2.5 flex gap-2">
        {item.kind === "task" && isFail && (
          <Button size="xs" loading={acting} onClick={handleRetry}>
            {t("hub.retry")}
          </Button>
        )}
        {item.kind === "task" && item.task.session_id && (
          <Button variant="outline" size="xs" onClick={handleOpenSession}>
            {t("hub.openSession")}
          </Button>
        )}
        {(item.kind === "goal" || (!isFail && !item.task.session_id)) && (
          <Button variant="outline" size="xs">
            {t("hub.view")}
          </Button>
        )}
      </div>
    </div>
  );
}

function SchedulesTable({ jobs, agentId }: { jobs: SchedulerJob[]; agentId: string }) {
  const { t } = useI18n();
  const navigate = useNavigate();
  const qc = useQueryClient();

  const runQueries = useQueries({
    queries: jobs.map((j) => schedulerJobRecentRunsOptions(j.agent_id || agentId, j.id)),
  });

  const toggleJob = async (job: SchedulerJob, enabled: boolean) => {
    await updateSchedulerJob({
      path: { agentId: job.agent_id || agentId, jobId: job.id },
      body: {
        name: job.name,
        message: job.message,
        cron: job.cron || "",
        every: job.every || "",
        at: job.at || "",
        session_mode: job.session_mode,
        enabled,
        agent_id: job.agent_id || agentId,
      },
      throwOnError: true,
    });
    void qc.invalidateQueries({ queryKey: ["agent-scheduler-jobs"] });
  };

  return (
    <Table variant="card">
      <TableHeader>
        <TableRow>
          <TableHead className="w-[32%]">{t("hub.colName")}</TableHead>
          <TableHead className="w-[18%]">{t("hub.colSchedule")}</TableHead>
          <TableHead>{t("hub.colRecentRuns")}</TableHead>
          <TableHead className="w-[18%]">{t("hub.colNextRun")}</TableHead>
          <TableHead className="w-14">{t("hub.colEnabled")}</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {jobs.map((job, i) => {
          const next = jobNextRunAt(job);
          return (
            <TableRow
              key={job.id}
              className="cursor-pointer"
              onClick={() =>
                navigate({
                  to: "/agents/$agentId/tasks/schedules/$scheduleId",
                  params: { agentId, scheduleId: job.id },
                })
              }
            >
              <TableCell className="font-medium">
                <span className="inline-flex items-center gap-1.5">
                  {job.name}
                  {job.template_key && (
                    <Badge size="sm" variant="secondary">
                      {t("automations.subscriptionBadge", {
                        key: job.template_key,
                      })}
                    </Badge>
                  )}
                </span>
              </TableCell>
              <TableCell className="font-mono text-[11.5px] text-muted-foreground">
                {humanScheduleText(t, job)}
              </TableCell>
              <TableCell>
                <RunSparkline runs={runQueries[i]?.data ?? []} />
              </TableCell>
              <TableCell className="text-[12.5px]">{next ? formatUntil(t, next) : "—"}</TableCell>
              <TableCell onClick={(e) => e.stopPropagation()}>
                <Switch checked={job.enabled} onCheckedChange={(v) => void toggleJob(job, v)} />
              </TableCell>
            </TableRow>
          );
        })}
      </TableBody>
    </Table>
  );
}

/** Last runs as colored dots, oldest → newest. */
function RunSparkline({ runs }: { runs: JobRun[] }) {
  const ordered = [...runs].reverse().slice(-10);
  if (!ordered.length) return <span className="text-xs text-muted-foreground">—</span>;
  return (
    <span className="inline-flex items-center gap-[3px]">
      {ordered.map((run) => (
        <i
          key={run.id}
          title={`${run.status}${run.error ? `: ${run.error}` : ""}`}
          className={cn(
            "size-2 rounded-sm",
            run.status === "running"
              ? "animate-pulse bg-chart-4"
              : run.status === "failed" || run.status === "error"
                ? "bg-destructive"
                : "bg-chart-3/85",
          )}
        />
      ))}
    </span>
  );
}

function GoalCard({ goal, onOpen }: { goal: ComponentsGoal; onOpen: () => void }) {
  const { t } = useI18n();
  const { data: graph } = useQuery(goalGraphOptions(goal.id));
  const r = rollup(graph?.tasks ?? []);

  return (
    <div
      role="button"
      tabIndex={0}
      onClick={onOpen}
      onKeyDown={(e) => e.key === "Enter" && onOpen()}
      className="cursor-pointer rounded-xl border border-border px-4 py-3.5 hover:bg-muted/40"
    >
      <div className="flex items-center gap-2">
        <span className={cn("size-1.5 shrink-0 rounded-full", statusMeta(goal.status).dot)} />
        <span className="min-w-0 flex-1 truncate text-sm font-semibold">{goal.title}</span>
        <span className="shrink-0 font-mono text-xs text-muted-foreground">
          {statusLabel(t, goal.status)}
        </span>
      </div>
      {goal.description && (
        <div className="mt-1 line-clamp-1 text-[12.5px] text-muted-foreground">
          {goal.description}
        </div>
      )}
      {r.total > 0 ? (
        <ProgressBar r={r} className="mt-3" />
      ) : goal.status === "done" ? (
        <div className="mt-3 rounded-lg border border-chart-3/25 bg-chart-3/10 px-3 py-2 font-mono text-xs text-chart-3">
          {t("hub.achievedAt", {
            time: formatTime(goal.completed_at ?? goal.updated_at),
          })}
        </div>
      ) : null}
      <div className="mt-2 flex flex-wrap gap-x-3.5 font-mono text-xs text-muted-foreground">
        {r.total > 0 && <span>{t("hub.goalDone", { done: r.done, total: r.total })}</span>}
        {r.blocked > 0 && (
          <span className="text-chart-4">{t("hub.goalBlocked", { n: r.blocked })}</span>
        )}
        {r.running > 0 && <span>{t("hub.goalRunning", { n: r.running })}</span>}
        <span>{t("hub.updatedAt", { time: formatTime(goal.updated_at) })}</span>
      </div>
    </div>
  );
}

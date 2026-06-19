import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import { queryOptions, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import {
  abandonDeliverable,
  listSchedulerJobRuns,
  reattemptDeliverable,
  updateSchedulerJob,
} from "@/lib/api-client";
import type { ComponentsDeliverable, JobRun } from "@/lib/api-client/types.gen";
import type { SchedulerJob } from "@/lib/types";
import { deliverableChildrenOptions, deliverablesOptions } from "@/lib/queries/deliverables";
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
import { jobNextRunAt } from "@/features/deliverables/types";
import {
  ProgressBar,
  blockReasonLabel,
  deliverableNeedsYou,
  deliverableStatusLabel,
  displayStatus,
  formatUntil,
  humanScheduleText,
  rollup,
  statusLabel,
  statusMeta,
} from "@/features/deliverables/lib";

// First page of a scheduler job's runs — enough for the overview sparkline
// without paging. Kept local so the new hub doesn't reach into the old tasks
// feature for the same shape.
function schedulerJobRecentRunsOptions(agentId: string, jobId: string) {
  return queryOptions({
    queryKey: ["scheduler-job-recent-runs", agentId, jobId],
    queryFn: async () => {
      const { data } = await listSchedulerJobRuns({
        path: { agentId, jobId },
        query: { page_size: 10 },
        throwOnError: true,
      });
      return data?.runs ?? [];
    },
    enabled: !!agentId && !!jobId,
  });
}

const TERMINAL = new Set(["accepted", "rejected_final", "abandoned", "cancelled"]);

export function OverviewPage() {
  const { t } = useI18n();
  const { agentId, projectId } = useParams({ strict: false }) as {
    agentId: string;
    projectId?: string;
  };
  const navigate = useNavigate();
  const { setHeaderActions } = useAppShell();

  const { data: deliverables = [] } = useQuery(deliverablesOptions(agentId));
  const { data: jobs = [] } = useQuery(agentSchedulerJobsOptions(agentId));

  useEffect(() => {
    setHeaderActions(
      <div className="flex items-center gap-1">
        <Button
          render={
            <Link
              to="/agents/$agentId/deliverables/new"
              params={{ agentId }}
              search={projectId ? { project_id: projectId } : {}}
            />
          }
          variant="outline"
          size="sm"
        >
          <Plus />
          <span className="max-sm:hidden">{t("deliverables.new")}</span>
        </Button>
      </div>,
    );
    return () => {
      setHeaderActions(null);
    };
  }, [setHeaderActions, t, agentId, projectId]);

  const needsYou = useMemo(
    () =>
      deliverables
        .filter(deliverableNeedsYou)
        .sort((a, b) => new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime()),
    [deliverables],
  );

  const runningCount = useMemo(
    () => deliverables.filter((d) => d.lifecycle === "active").length,
    [deliverables],
  );

  const doneThisWeek = useMemo(() => {
    const cutoff = Date.now() - 7 * 86_400_000;
    return deliverables.filter(
      (d) =>
        d.lifecycle === "accepted" && new Date(d.accepted_at ?? d.updated_at).getTime() >= cutoff,
    ).length;
  }, [deliverables]);

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

  const activeWork = useMemo(
    () =>
      deliverables
        .filter((d) => !TERMINAL.has(d.lifecycle) && !deliverableNeedsYou(d))
        .sort((a, b) => new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime()),
    [deliverables],
  );
  const previewWork = useMemo(() => activeWork.slice(0, 6), [activeWork]);

  const recentDone = useMemo(
    () =>
      deliverables
        .filter((d) => TERMINAL.has(d.lifecycle))
        .sort((a, b) => new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime())
        .slice(0, 5),
    [deliverables],
  );

  const openDeliverable = useCallback(
    (id: string) =>
      void navigate({
        to: "/agents/$agentId/deliverables/$deliverableId",
        params: { agentId, deliverableId: id },
      }),
    [navigate, agentId],
  );

  return (
    <div className="h-full min-h-0 overflow-y-auto bg-background">
      <div className="mx-auto max-w-[980px] px-6 py-6 pb-20 sm:px-8">
        {/* Stats */}
        <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
          <StatCard
            num={needsYou.length}
            label={t("deliverables.secNeedsYou")}
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
          <SectionHead title={t("deliverables.secNeedsYou")} count={needsYou.length} />
          {needsYou.length === 0 ? (
            <div className="rounded-xl border border-border px-4 py-3.5">
              <p className="text-sm font-medium text-muted-foreground">{t("hub.noNeedsYou")}</p>
              <p className="mt-1 text-[12.5px] text-muted-foreground">{t("hub.noNeedsYouDesc")}</p>
            </div>
          ) : (
            <>
              <p className="mb-3 text-[12.5px] text-muted-foreground">{t("hub.needsYouHint")}</p>
              <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
                {needsYou.map((d) => (
                  <NeedsYouCard
                    key={d.id}
                    deliverable={d}
                    agentId={agentId}
                    onOpen={() => openDeliverable(d.id)}
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

        {/* Active work */}
        <section className="mt-8">
          <SectionHead
            title={t("hub.activeWork")}
            count={activeWork.length}
            action={
              <Button
                render={<Link to="/agents/$agentId/deliverables/all" params={{ agentId }} />}
                variant="outline"
                size="xs"
              >
                {t("hub.viewAll")}
              </Button>
            }
          />
          {activeWork.length === 0 ? (
            <p className="text-sm text-muted-foreground">{t("hub.noActiveGoals")}</p>
          ) : (
            <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
              {previewWork.map((d) => (
                <DeliverableCard key={d.id} deliverable={d} onOpen={() => openDeliverable(d.id)} />
              ))}
            </div>
          )}
        </section>

        {/* Recently completed */}
        {recentDone.length > 0 && (
          <section className="mt-8">
            <SectionHead title={t("hub.secRecentDone")} />
            <div className="overflow-hidden rounded-xl border border-border">
              {recentDone.map((d) => {
                const s = displayStatus(d);
                return (
                  <button
                    key={d.id}
                    type="button"
                    onClick={() => openDeliverable(d.id)}
                    className="flex w-full items-center gap-3 border-b border-border px-3.5 py-2.5 text-left text-[13px] last:border-b-0 hover:bg-muted/50"
                  >
                    <span className="min-w-0 flex-1 truncate font-medium">{d.title}</span>
                    <span className="inline-flex shrink-0 items-center gap-1.5 font-mono text-xs text-muted-foreground">
                      <span className={cn("size-1.5 rounded-full", statusMeta(s).dot)} />
                      {statusLabel(t, s)}
                    </span>
                    <span className="shrink-0 font-mono text-xs text-muted-foreground">
                      {formatTime(d.accepted_at ?? d.updated_at)}
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

// hook copy + an inline affordance both follow from block_reason: a needs_verdict
// block routes to the Acceptance tab for the verdict, a budget block offers
// reattempt/abandon in place, and a dep block just opens the detail.
function hookLabel(t: ReturnType<typeof useI18n>["t"], d: ComponentsDeliverable): string {
  switch (d.block_reason) {
    case "needs_verdict":
      return t("deliverables.hookNeedsVerdict");
    case "budget_exhausted":
      return t("deliverables.hookBudget");
    case "dep":
      return t("deliverables.hookDep");
    default:
      return blockReasonLabel(t, d) ?? t("deliverables.statusBlocked");
  }
}

function NeedsYouCard({
  deliverable: d,
  agentId,
  onOpen,
}: {
  deliverable: ComponentsDeliverable;
  agentId: string;
  onOpen: () => void;
}) {
  const { t } = useI18n();
  const qc = useQueryClient();
  const [acting, setActing] = useState(false);

  const s = displayStatus(d);
  const isReview = s === "review";
  const isBudget = d.block_reason === "budget_exhausted";

  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: ["deliverables", agentId] });
    void qc.invalidateQueries({ queryKey: ["deliverable", d.id] });
  };

  const handleReattempt = async (e: React.MouseEvent) => {
    e.stopPropagation();
    setActing(true);
    try {
      await reattemptDeliverable({ path: { id: d.id }, throwOnError: true });
      invalidate();
    } finally {
      setActing(false);
    }
  };

  const handleAbandon = async (e: React.MouseEvent) => {
    e.stopPropagation();
    setActing(true);
    try {
      await abandonDeliverable({ path: { id: d.id }, body: {}, throwOnError: true });
      invalidate();
    } finally {
      setActing(false);
    }
  };

  return (
    <div
      role="button"
      tabIndex={0}
      onClick={onOpen}
      onKeyDown={(e) => e.key === "Enter" && onOpen()}
      className={cn(
        "cursor-pointer rounded-xl border border-border border-l-[3px] px-4 py-3.5 hover:bg-muted/40",
        isReview ? "border-l-primary" : "border-l-chart-4",
      )}
    >
      <div className="font-mono text-xs font-medium uppercase tracking-[0.08em] text-muted-foreground">
        {deliverableStatusLabel(t, d)} · {hookLabel(t, d)}
      </div>
      <div className="mt-1 text-sm font-semibold">{d.title}</div>
      <div className="mt-1 line-clamp-2 text-[12.5px] leading-relaxed text-muted-foreground">
        {d.intent || t("hub.updatedAt", { time: formatTime(d.updated_at) })}
      </div>
      <div className="mt-2.5 flex gap-2">
        {isReview ? (
          <Button
            size="xs"
            onClick={(e) => {
              e.stopPropagation();
              onOpen();
            }}
          >
            {t("deliverables.verdictSubmit")}
          </Button>
        ) : isBudget ? (
          <>
            <Button size="xs" loading={acting} onClick={handleReattempt}>
              {t("deliverables.reattempt")}
            </Button>
            <Button variant="outline" size="xs" loading={acting} onClick={handleAbandon}>
              {t("deliverables.abandon")}
            </Button>
          </>
        ) : (
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
                  to: "/agents/$agentId/deliverables/schedules/$scheduleId",
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

// A composite's progress comes from its children; a leaf has none, so the card
// shows its own status line instead of a bar.
function DeliverableCard({
  deliverable: d,
  onOpen,
}: {
  deliverable: ComponentsDeliverable;
  onOpen: () => void;
}) {
  const { t } = useI18n();
  const isComposite = d.kind === "composite";
  const { data: children = [] } = useQuery({
    ...deliverableChildrenOptions(isComposite ? d.id : undefined),
  });
  const r = rollup(children);
  const s = displayStatus(d);

  return (
    <div
      role="button"
      tabIndex={0}
      onClick={onOpen}
      onKeyDown={(e) => e.key === "Enter" && onOpen()}
      className="cursor-pointer rounded-xl border border-border px-4 py-3.5 hover:bg-muted/40"
    >
      <div className="flex items-center gap-2">
        <span className={cn("size-1.5 shrink-0 rounded-full", statusMeta(s).dot)} />
        <span className="min-w-0 flex-1 truncate text-sm font-semibold">{d.title}</span>
        <span className="shrink-0 font-mono text-xs text-muted-foreground">
          {statusLabel(t, s)}
        </span>
      </div>
      {d.intent && (
        <div className="mt-1 line-clamp-1 text-[12.5px] text-muted-foreground">{d.intent}</div>
      )}
      {r.total > 0 && <ProgressBar r={r} className="mt-3" />}
      <div className="mt-2 flex flex-wrap gap-x-3.5 font-mono text-xs text-muted-foreground">
        {r.total > 0 && (
          <span>{t("deliverables.requiredOf", { accepted: r.accepted, total: r.total })}</span>
        )}
        {r.blocked > 0 && (
          <span className="text-chart-4">
            {t("deliverables.rollupBlocked", { count: r.blocked })}
          </span>
        )}
        {r.active > 0 && <span>{t("deliverables.rollupActive", { count: r.active })}</span>}
        <span>{t("hub.updatedAt", { time: formatTime(d.updated_at) })}</span>
      </div>
    </div>
  );
}

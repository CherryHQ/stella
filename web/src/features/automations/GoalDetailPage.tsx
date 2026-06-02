import { useEffect, useMemo, useState } from "react";
import type { TFunction } from "i18next";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate, useParams, useSearch } from "@tanstack/react-router";
import {
  activateGoal,
  approveTaskReview,
  rejectTaskReview,
  requestChangesTaskReview,
  resolveTaskBlocker,
} from "@/lib/api-client";
import type { ComponentsDep, ComponentsTask } from "@/lib/api-client/types.gen";
import {
  goalGraphOptions,
  goalOptions,
  taskEventsOptions,
  taskReadinessOptions,
  taskReviewsOptions,
  taskRunsOptions,
} from "@/lib/queries/goals";
import { useI18n } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/messages";
import { formatTime } from "@/lib/time";
import { cn } from "@/lib/utils";
import { useAppShell } from "@/layouts/AppShell";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { ProgressBar, StatusDot, StatusPill, rollup, statusLabel } from "./lib";

export function GoalDetailPage() {
  const { t } = useI18n();
  const { setHeaderTitle, setHeaderActions } = useAppShell();
  const { agentId, goalId } = useParams({
    from: "/_app/agents/$agentId/automations/goals/$goalId",
  });
  const { task: selectedTaskId } = useSearch({
    from: "/_app/agents/$agentId/automations/goals/$goalId",
  });
  const navigate = useNavigate();
  const qc = useQueryClient();

  const { data: goal } = useQuery(goalOptions(goalId));
  const { data: graph } = useQuery(goalGraphOptions(goalId));
  const tasks = graph?.tasks ?? [];
  const r = useMemo(() => rollup(tasks), [tasks]);
  const selectedTask = selectedTaskId ? tasks.find((tk) => tk.id === selectedTaskId) : undefined;

  const selectTask = (id: string | null) =>
    void navigate({
      to: "/agents/$agentId/automations/goals/$goalId",
      params: { agentId, goalId },
      search: id ? { task: id } : {},
    });

  const activate = useMutation({
    mutationFn: () => activateGoal({ path: { goalId: goalId }, throwOnError: true }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["goal", goalId] });
      void qc.invalidateQueries({ queryKey: ["goals", agentId] });
    },
  });

  useEffect(() => {
    setHeaderTitle(
      <h1 className="truncate text-[15px] font-semibold tracking-[-0.01em]">
        {goal?.title ?? t("goals.title")}
      </h1>,
    );
    setHeaderActions(null);
    return () => {
      setHeaderTitle(null);
      setHeaderActions(null);
    };
  }, [goal?.title, setHeaderActions, setHeaderTitle, t]);

  return (
    <div className="flex h-full min-h-0 overflow-hidden bg-background">
      <div className="flex min-w-0 flex-1 flex-col overflow-hidden">
        <header className="flex h-14 shrink-0 items-center gap-3 border-b border-border px-5">
          <button
            onClick={() =>
              void navigate({
                to: "/agents/$agentId/automations",
                params: { agentId },
              })
            }
            className="flex items-center gap-1 text-sm text-primary hover:text-primary/80"
          >
            <ChevronLeft />
            {t("goals.backToGoals")}
          </button>
          {goal && (
            <>
              <span className="h-4 w-px bg-border" />
              <StatusPill status={goal.status} label={statusLabel(t, goal.status)} />
              <h1 className="min-w-0 flex-1 truncate font-serif text-lg tracking-tight">
                {goal.title}
              </h1>
              {goal.status === "draft" && (
                <Button size="sm" onClick={() => activate.mutate()} disabled={activate.isPending}>
                  {t("goals.activate")}
                </Button>
              )}
            </>
          )}
        </header>

        <div className="min-h-0 flex-1 overflow-y-auto">
          <div className="mx-auto max-w-[1000px] px-6 py-6">
            {goal?.description && (
              <p className="mb-5 max-w-2xl text-sm leading-relaxed text-muted-foreground">
                {goal.description}
              </p>
            )}

            {r.total > 0 && (
              <div className="mb-6 rounded-2xl border border-border bg-card p-4">
                <div className="mb-2 flex items-center justify-between">
                  <span className="font-mono text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
                    {t("goals.rollup")}
                  </span>
                  <span className="font-mono text-xs text-muted-foreground">
                    {r.done}/{r.total}
                  </span>
                </div>
                <ProgressBar r={r} />
                <div className="mt-3 flex flex-wrap gap-x-4 gap-y-1 font-mono text-[11px] text-muted-foreground">
                  {r.running > 0 && <span>{r.running} running</span>}
                  {r.reviewing > 0 && <span>{r.reviewing} reviewing</span>}
                  {r.blocked > 0 && (
                    <span className="text-amber-600 dark:text-amber-400">{r.blocked} blocked</span>
                  )}
                  {r.failed > 0 && <span className="text-destructive">{r.failed} failed</span>}
                </div>
              </div>
            )}

            <div className="mb-3 flex items-center gap-2.5">
              <span className="font-mono text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
                {t("goals.dagTitle")}
              </span>
              <span className="h-px flex-1 bg-border" />
            </div>

            {tasks.length === 0 ? (
              <p className="py-10 text-center text-sm text-muted-foreground">
                {t("goals.noTasks")}
              </p>
            ) : (
              <DagCanvas
                tasks={tasks}
                deps={graph?.deps ?? []}
                selectedId={selectedTaskId}
                onSelect={(id) => selectTask(id)}
                t={t}
              />
            )}
          </div>
        </div>
      </div>

      {selectedTask && (
        <TaskDrawer
          key={selectedTask.id}
          task={selectedTask}
          deps={(graph?.deps ?? []).filter((d) => d.task_id === selectedTask.id)}
          tasks={tasks}
          onClose={() => selectTask(null)}
        />
      )}
    </div>
  );
}

// ── DAG ──────────────────────────────────────────────────────────────

const NODE_W = 190;
const NODE_H = 64;
const COL_GAP = 70;
const ROW_GAP = 20;

function DagCanvas({
  tasks,
  deps,
  selectedId,
  onSelect,
  t,
}: {
  tasks: ComponentsTask[];
  deps: ComponentsDep[];
  selectedId?: string;
  onSelect: (id: string) => void;
  t: TFunction;
}) {
  const layout = useMemo(() => {
    const byId = new Map(tasks.map((tk) => [tk.id, tk]));
    const upstream = new Map<string, string[]>();
    for (const d of deps) {
      if (byId.has(d.task_id) && byId.has(d.dep_task_id)) {
        upstream.set(d.task_id, [...(upstream.get(d.task_id) ?? []), d.dep_task_id]);
      }
    }
    const depthCache = new Map<string, number>();
    const depth = (id: string, seen: Set<string>): number => {
      if (depthCache.has(id)) return depthCache.get(id)!;
      if (seen.has(id)) return 0;
      seen.add(id);
      const ups = upstream.get(id) ?? [];
      const d = ups.length ? Math.max(...ups.map((u) => depth(u, seen))) + 1 : 0;
      depthCache.set(id, d);
      return d;
    };
    const cols = new Map<number, ComponentsTask[]>();
    for (const tk of tasks) {
      const d = depth(tk.id, new Set());
      cols.set(d, [...(cols.get(d) ?? []), tk]);
    }
    const pos = new Map<string, { x: number; y: number }>();
    let maxRows = 0;
    for (const [col, items] of cols) {
      maxRows = Math.max(maxRows, items.length);
      items.forEach((tk, row) => {
        pos.set(tk.id, {
          x: col * (NODE_W + COL_GAP),
          y: row * (NODE_H + ROW_GAP),
        });
      });
    }
    const width = (Math.max(...cols.keys()) + 1) * (NODE_W + COL_GAP) - COL_GAP;
    const height = maxRows * (NODE_H + ROW_GAP) - ROW_GAP;
    return {
      pos,
      width: Math.max(width, NODE_W),
      height: Math.max(height, NODE_H),
    };
  }, [tasks, deps]);

  const edges = deps.filter((d) => layout.pos.has(d.task_id) && layout.pos.has(d.dep_task_id));

  return (
    <div className="overflow-x-auto rounded-2xl border border-border bg-muted/30 p-6">
      <div className="relative" style={{ width: layout.width, height: layout.height }}>
        <svg
          className="absolute inset-0 overflow-visible"
          width={layout.width}
          height={layout.height}
        >
          {edges.map((d, i) => {
            const from = layout.pos.get(d.dep_task_id)!;
            const to = layout.pos.get(d.task_id)!;
            const x1 = from.x + NODE_W;
            const y1 = from.y + NODE_H / 2;
            const x2 = to.x;
            const y2 = to.y + NODE_H / 2;
            const mx = (x1 + x2) / 2;
            return (
              <path
                key={i}
                d={`M ${x1} ${y1} C ${mx} ${y1}, ${mx} ${y2}, ${x2} ${y2}`}
                fill="none"
                className={d.dep_kind === "soft" ? "stroke-border" : "stroke-muted-foreground/40"}
                strokeWidth={1.5}
                strokeDasharray={d.dep_kind === "soft" ? "4 4" : undefined}
              />
            );
          })}
        </svg>
        {tasks.map((tk) => {
          const p = layout.pos.get(tk.id)!;
          return (
            <button
              key={tk.id}
              type="button"
              onClick={() => onSelect(tk.id)}
              style={{ left: p.x, top: p.y, width: NODE_W, height: NODE_H }}
              className={cn(
                "absolute flex flex-col justify-center gap-1 rounded-xl border bg-card px-3 text-left transition-shadow hover:shadow-md",
                selectedId === tk.id ? "border-primary ring-1 ring-primary" : "border-border",
              )}
            >
              <span className="truncate text-[13px] font-medium">{tk.title}</span>
              <span className="flex items-center gap-1.5">
                <StatusDot status={tk.status} />
                <span className="font-mono text-[10.5px] text-muted-foreground">
                  {statusLabel(t, tk.status)}
                </span>
              </span>
            </button>
          );
        })}
      </div>
    </div>
  );
}

// ── Task drawer ──────────────────────────────────────────────────────

type Tab = "overview" | "runs" | "reviews" | "blocker" | "events";
const TABS: { key: Tab; labelKey: MessageKey }[] = [
  { key: "overview", labelKey: "goals.tabOverview" },
  { key: "runs", labelKey: "goals.tabRuns" },
  { key: "reviews", labelKey: "goals.tabReviews" },
  { key: "blocker", labelKey: "goals.tabBlocker" },
  { key: "events", labelKey: "goals.tabEvents" },
];

function TaskDrawer({
  task,
  deps,
  tasks,
  onClose,
}: {
  task: ComponentsTask;
  deps: ComponentsDep[];
  tasks: ComponentsTask[];
  onClose: () => void;
}) {
  const { t } = useI18n();
  const [tab, setTab] = useState<Tab>("overview");

  return (
    <aside className="flex w-full max-w-[420px] shrink-0 flex-col overflow-hidden border-l border-border bg-card">
      <div className="flex shrink-0 items-start justify-between gap-3 border-b border-border px-5 py-4">
        <div className="min-w-0">
          <div className="mb-1.5 flex items-center gap-1.5">
            <StatusDot status={task.status} />
            <span className="font-mono text-[11px] capitalize text-muted-foreground">
              {statusLabel(t, task.status)}
            </span>
          </div>
          <h2 className="font-serif text-lg leading-tight tracking-tight">{task.title}</h2>
        </div>
        <button
          onClick={onClose}
          className="shrink-0 text-lg leading-none text-muted-foreground/60 hover:text-foreground"
        >
          ×
        </button>
      </div>

      <div className="flex shrink-0 gap-0.5 border-b border-border px-3">
        {TABS.map((tb) => (
          <button
            key={tb.key}
            onClick={() => setTab(tb.key)}
            className={cn(
              "border-b-2 px-2.5 py-2.5 text-xs font-medium transition-colors",
              tab === tb.key
                ? "border-primary text-foreground"
                : "border-transparent text-muted-foreground hover:text-foreground",
            )}
          >
            {t(tb.labelKey)}
          </button>
        ))}
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto p-5">
        {tab === "overview" && <OverviewTab task={task} deps={deps} tasks={tasks} />}
        {tab === "runs" && <RunsTab task={task} />}
        {tab === "reviews" && <ReviewsTab task={task} />}
        {tab === "blocker" && <BlockerTab task={task} />}
        {tab === "events" && <EventsTab task={task} />}
      </div>
    </aside>
  );
}

function Meta({ label, value, highlight }: { label: string; value: string; highlight?: boolean }) {
  return (
    <div className="flex items-center justify-between py-2">
      <span className="text-sm text-muted-foreground">{label}</span>
      <span
        className={cn("text-sm", highlight ? "font-medium text-destructive" : "text-foreground/70")}
      >
        {value}
      </span>
    </div>
  );
}

function OverviewTab({
  task,
  deps,
  tasks,
}: {
  task: ComponentsTask;
  deps: ComponentsDep[];
  tasks: ComponentsTask[];
}) {
  const { t } = useI18n();
  const { data: readiness } = useQuery(taskReadinessOptions(task.id));
  const titleOf = (id: string) => tasks.find((tk) => tk.id === id)?.title ?? id.slice(0, 8);

  return (
    <div className="space-y-5">
      {task.description && (
        <p className="text-sm leading-relaxed text-muted-foreground">{task.description}</p>
      )}

      {readiness && (
        <div className="rounded-xl border border-border bg-background p-3.5">
          <div className="mb-1.5 flex items-center justify-between">
            <span className="font-mono text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
              {t("goals.readiness")}
            </span>
            <span
              className={cn(
                "font-mono text-xs",
                readiness.dispatchable
                  ? "text-emerald-600 dark:text-emerald-400"
                  : "text-amber-600 dark:text-amber-400",
              )}
            >
              {readiness.state}
            </span>
          </div>
          {readiness.reasons && readiness.reasons.length > 0 ? (
            <ul className="mt-2 space-y-1.5">
              {readiness.reasons.map((reason, i) => (
                <li key={i} className="text-[12.5px] text-foreground/80">
                  <span className="font-mono text-muted-foreground">{reason.type}</span>
                  {reason.detail ? ` — ${reason.detail}` : ""}
                </li>
              ))}
            </ul>
          ) : (
            <p className="text-[12.5px] text-muted-foreground">{t("goals.noReasons")}</p>
          )}
        </div>
      )}

      <div className="divide-y divide-border">
        <Meta
          label={t("tasks.fieldPriority")}
          value={task.priority}
          highlight={task.priority === "urgent"}
        />
        <Meta label="Retries" value={`${task.retry_count}/${task.max_retries}`} />
        {task.deadline_at && <Meta label="Deadline" value={formatTime(task.deadline_at)} />}
        <Meta label={t("goals.colUpdated")} value={formatTime(task.updated_at)} />
      </div>

      <div>
        <div className="mb-2 font-mono text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
          {t("goals.deps")}
        </div>
        {deps.length === 0 ? (
          <p className="text-[12.5px] text-muted-foreground">{t("goals.noDeps")}</p>
        ) : (
          <ul className="space-y-1.5">
            {deps.map((d) => (
              <li key={d.dep_task_id} className="flex items-center gap-2 text-[12.5px]">
                {d.upstream_status && <StatusDot status={d.upstream_status} />}
                <span className="truncate">{titleOf(d.dep_task_id)}</span>
                <span className="ml-auto font-mono text-[10.5px] text-muted-foreground">
                  {d.dep_kind}
                </span>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}

function RunsTab({ task }: { task: ComponentsTask }) {
  const { t } = useI18n();
  const { data: runs = [] } = useQuery(taskRunsOptions(task.id));
  if (runs.length === 0) return <Empty text={t("goals.noRuns")} />;
  return (
    <ul className="space-y-2">
      {runs.map((run) => (
        <li key={run.id} className="rounded-xl border border-border bg-background p-3">
          <div className="flex items-center justify-between">
            <span className="flex items-center gap-2 text-sm font-medium">
              <StatusDot status={run.status === "completed" ? "done" : run.status} />
              {t("goals.attempt", { count: run.attempt_no })}
            </span>
            <span className="font-mono text-[11px] capitalize text-muted-foreground">
              {run.kind}
            </span>
          </div>
          <div className="mt-1.5 flex items-center justify-between font-mono text-[10.5px] text-muted-foreground">
            <span>{run.status}</span>
            <span>{formatTime(run.finished_at ?? run.started_at ?? run.created_at)}</span>
          </div>
          {run.error && <p className="mt-2 text-[12px] text-destructive">{run.error}</p>}
        </li>
      ))}
    </ul>
  );
}

function ReviewsTab({ task }: { task: ComponentsTask }) {
  const { t } = useI18n();
  const qc = useQueryClient();
  const { data: reviews = [] } = useQuery(taskReviewsOptions(task.id));
  const [feedback, setFeedback] = useState("");

  const active = reviews.find((rv) => rv.status === "requested" || rv.status === "in_progress");

  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: ["task-reviews", task.id] });
    void qc.invalidateQueries({ queryKey: ["task-readiness", task.id] });
    void qc.invalidateQueries({ queryKey: ["goal-graph"] });
  };

  const decide = useMutation({
    mutationFn: async (kind: "approve" | "reject" | "changes") => {
      if (!active) return;
      const opts = {
        path: { taskId: task.id, reviewId: active.id },
        body: { feedback: feedback.trim() || undefined },
        throwOnError: true as const,
      };
      if (kind === "approve") await approveTaskReview(opts);
      else if (kind === "reject") await rejectTaskReview(opts);
      else await requestChangesTaskReview(opts);
    },
    onSuccess: () => {
      setFeedback("");
      invalidate();
    },
  });

  return (
    <div className="space-y-4">
      {active && (
        <div className="rounded-xl border border-primary/30 bg-primary/[0.06] p-3.5">
          <div className="mb-2 font-mono text-[11px] font-semibold uppercase tracking-wider text-primary/80">
            {t("goals.reviewGate")}
          </div>
          {active.summary && (
            <p className="mb-3 text-[12.5px] text-foreground/80">{active.summary}</p>
          )}
          <Textarea
            value={feedback}
            onChange={(e) => setFeedback((e.target as HTMLTextAreaElement).value)}
            rows={3}
            placeholder={t("goals.feedbackPlaceholder")}
            className="mb-2.5 text-sm"
          />
          <div className="flex flex-wrap gap-2">
            <Button size="sm" disabled={decide.isPending} onClick={() => decide.mutate("approve")}>
              {t("tasks.approve")}
            </Button>
            <Button
              size="sm"
              variant="outline"
              disabled={decide.isPending}
              onClick={() => decide.mutate("changes")}
            >
              {t("goals.requestChanges")}
            </Button>
            <Button
              size="sm"
              variant="ghost"
              disabled={decide.isPending}
              onClick={() => decide.mutate("reject")}
              className="text-destructive"
            >
              {t("tasks.reject")}
            </Button>
          </div>
        </div>
      )}

      {reviews.length === 0 ? (
        <Empty text={t("goals.noReviews")} />
      ) : (
        <ul className="space-y-2">
          {reviews.map((rv) => (
            <li key={rv.id} className="rounded-xl border border-border bg-background p-3">
              <div className="flex items-center justify-between">
                <span className="font-mono text-[11px] capitalize text-muted-foreground">
                  {rv.reviewer_type}
                </span>
                <span className="font-mono text-[11px] capitalize text-muted-foreground">
                  {rv.status.replace(/_/g, " ")}
                </span>
              </div>
              {rv.feedback && (
                <p className="mt-1.5 text-[12.5px] text-foreground/80">{rv.feedback}</p>
              )}
              <span className="mt-1.5 block font-mono text-[10.5px] text-muted-foreground">
                {formatTime(rv.updated_at)}
              </span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function BlockerTab({ task }: { task: ComponentsTask }) {
  const { t } = useI18n();
  const qc = useQueryClient();
  const [resolution, setResolution] = useState("");

  const resolve = useMutation({
    mutationFn: () =>
      resolveTaskBlocker({
        path: { taskId: task.id, blockerId: task.active_blocker_id! },
        body: { resolution: resolution.trim() || undefined },
        throwOnError: true,
      }),
    onSuccess: () => {
      setResolution("");
      void qc.invalidateQueries({ queryKey: ["goal-graph"] });
      void qc.invalidateQueries({ queryKey: ["task-readiness", task.id] });
    },
  });

  if (!task.active_blocker_id) return <Empty text={t("goals.noBlocker")} />;

  return (
    <div className="space-y-3">
      <p className="text-sm text-foreground/80">{t("tasks.blockedMessage")}</p>
      <Textarea
        value={resolution}
        onChange={(e) => setResolution((e.target as HTMLTextAreaElement).value)}
        rows={4}
        placeholder={t("tasks.respondPlaceholder")}
        className="text-sm"
      />
      <Button size="sm" disabled={resolve.isPending} onClick={() => resolve.mutate()}>
        {t("goals.respond")}
      </Button>
    </div>
  );
}

function EventsTab({ task }: { task: ComponentsTask }) {
  const { t } = useI18n();
  const { data: events = [] } = useQuery(taskEventsOptions(task.id));
  if (events.length === 0) return <Empty text={t("goals.noEvents")} />;
  return (
    <ol className="relative space-y-4 border-l border-border pl-4">
      {events.map((ev) => (
        <li key={ev.id} className="relative">
          <span className="absolute -left-[21px] top-1 size-2 rounded-full bg-muted-foreground/40" />
          <div className="text-[12.5px] text-foreground/85">
            <span className="font-medium">{ev.event_type.replace(/_/g, " ")}</span>
            {ev.from_status && ev.to_status && (
              <span className="text-muted-foreground">
                {" "}
                · {ev.from_status} → {ev.to_status}
              </span>
            )}
          </div>
          <div className="mt-0.5 font-mono text-[10.5px] text-muted-foreground">
            {ev.actor_type} · {formatTime(ev.created_at)}
          </div>
        </li>
      ))}
    </ol>
  );
}

function Empty({ text }: { text: string }) {
  return <p className="py-8 text-center text-sm text-muted-foreground">{text}</p>;
}

function ChevronLeft() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" className="size-4">
      <path strokeLinecap="round" strokeLinejoin="round" d="M15.75 19.5 8.25 12l7.5-7.5" />
    </svg>
  );
}

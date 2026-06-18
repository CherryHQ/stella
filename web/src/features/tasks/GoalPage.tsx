import { lazy, Suspense, useCallback, useMemo, useState, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import {
  activateGoal,
  cancelGoal,
  approveGoalReview,
  requestChangesGoalReview,
  listGoalReviews,
  acceptGoalPlan,
  submitGoalPlanReview,
  approveGoalPlanReview,
  requestChangesGoalPlanReview,
  materializeGoalPlan,
} from "@/lib/api-client";
import type { ComponentsGoalPlan, ComponentsReview } from "@/lib/api-client/types.gen";
import { goalOptions, goalGraphOptions, goalPlanOptions } from "@/lib/queries/goals";
import { useI18n } from "@/lib/i18n";
import { formatTime } from "@/lib/time";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { Dialog, DialogPopup, DialogTitle } from "@/components/ui/dialog";
import {
  ProgressBar,
  StatusPill,
  policyLabel,
  priorityLabel,
  rollup,
  statusLabel,
  statusMeta,
} from "./lib";
import { AgentChip, DetailSection, DetailShell, MetaSep } from "./DetailShell";
import { PlanSection } from "./PlanView";
import { GoalPlanEditor } from "./GoalPlanEditor";

const GoalDetailPage = lazy(() =>
  import("./GoalDetailPage").then((m) => ({ default: m.GoalDetailPage })),
);

export function GoalPage() {
  const { t } = useI18n();
  const { agentId, goalId } = useParams({ strict: false }) as {
    agentId: string;
    goalId: string;
  };
  const qc = useQueryClient();
  const [dagOpen, setDagOpen] = useState(false);
  const [editorOpen, setEditorOpen] = useState(false);
  const [acting, setActing] = useState(false);

  const { data: goal, isError } = useQuery(goalOptions(goalId));
  const { data: plan } = useQuery(goalPlanOptions(goalId));
  const { data: graph } = useQuery(goalGraphOptions(goalId));
  const tasks = graph?.tasks ?? [];
  const deps = graph?.deps ?? [];
  const r = rollup(tasks);

  const titleById = useMemo(() => {
    const m = new Map<string, string>();
    for (const task of tasks) m.set(task.id, task.title);
    return m;
  }, [tasks]);

  const invalidate = useCallback(() => {
    void qc.invalidateQueries({ queryKey: ["goals"] });
    void qc.invalidateQueries({ queryKey: ["goal", goalId] });
    void qc.invalidateQueries({ queryKey: ["goal-plan", goalId] });
    void qc.invalidateQueries({ queryKey: ["goal-graph", goalId] });
  }, [qc, goalId]);

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
      <DetailShell agentId={agentId} kindLabel={t("hub.kindGoal")} title={t("hub.notFound")}>
        <div />
      </DetailShell>
    );
  }
  if (!goal) return null;

  // Unmet dependencies per blocked task, so the card explains *why* it waits.
  const blockReason = (taskId: string): string | null => {
    const waiting = deps
      .filter((d) => d.task_id === taskId && d.upstream_status !== "done")
      .map((d) => titleById.get(d.dep_task_id))
      .filter(Boolean);
    if (!waiting.length) return null;
    return t("hub.waitingOn", { deps: waiting.join(", ") });
  };

  return (
    <DetailShell
      agentId={agentId}
      kindLabel={t("hub.kindGoal")}
      title={goal.title}
      pill={<StatusPill status={goal.status} label={statusLabel(t, goal.status)} />}
      actions={
        <>
          {goal.status === "planned" && (
            <Button
              size="sm"
              loading={acting}
              onClick={() =>
                act(() => activateGoal({ path: { goalId: goal.id }, throwOnError: true }))
              }
            >
              {t("goals.activate")}
            </Button>
          )}
          {!["done", "failed", "cancelled"].includes(goal.status) && (
            <Button
              variant="ghost"
              size="sm"
              loading={acting}
              onClick={() =>
                act(() => cancelGoal({ path: { goalId: goal.id }, throwOnError: true }))
              }
            >
              {t("common.cancel")}
            </Button>
          )}
        </>
      }
    >
      {goal.description && (
        <p className="mt-2 text-sm leading-relaxed text-muted-foreground">{goal.description}</p>
      )}
      <div className="mt-2.5 flex flex-wrap items-center gap-x-3.5 gap-y-1.5 text-[12.5px] text-muted-foreground">
        <span>
          {t("hub.priority")}:{" "}
          <span className="font-medium text-foreground">{priorityLabel(t, goal.priority)}</span>
        </span>
        <MetaSep />
        <span>
          {t("hub.reviewPolicy")}: {policyLabel(t, goal.review_policy)}
        </span>
        <MetaSep />
        <AgentChip agentId={goal.agent_id || agentId} />
        <MetaSep />
        <span>{t("hub.createdAt", { time: formatTime(goal.created_at) })}</span>
      </div>

      {/* Plan authoring stays open for the whole non-terminal life of a goal, so
          a running goal can be replanned from the UI exactly as the CLI allows
          (CreateGoalPlan rejects only terminal goals). Pre-run states render the
          full plan; once running the progress/DAG below carries the detail, so we
          show just the replan entry here. */}
      {!["done", "failed", "cancelled"].includes(goal.status) && (
        <div className="mt-6">
          {["draft", "planning", "planned"].includes(goal.status) &&
            (plan ? (
              <PlanSection plan={plan} t={t} />
            ) : (
              goal.status === "draft" && (
                <p className="text-[13px] leading-relaxed text-muted-foreground">
                  {t("goals.planNoPlanYet")}
                </p>
              )
            ))}
          <div className="mt-3 flex flex-wrap gap-2">
            {plan && (
              <GoalPlanActions
                goalId={goal.id}
                activeReviewId={goal.active_review_id}
                plan={plan}
                acting={acting}
                run={act}
              />
            )}
            <Button
              variant={plan ? "outline" : "default"}
              size="sm"
              onClick={() => setEditorOpen(true)}
            >
              {plan ? t("goals.planEdit") : t("goals.planCreate")}
            </Button>
          </div>
        </div>
      )}

      {goal.status === "reviewing" && (
        <div className="mt-5 flex flex-wrap gap-2">
          <ReviewActions goalId={goal.id} onDone={invalidate} />
        </div>
      )}

      {r.total > 0 && (
        <DetailSection
          title={`${t("hub.progress")} · ${t("hub.goalDone", { done: r.done, total: r.total })}`}
        >
          <ProgressBar r={r} className="h-2" />
          <div className="mt-2 flex flex-wrap gap-x-3.5 font-mono text-xs text-muted-foreground">
            <span>{t("hub.goalDone", { done: r.done, total: r.total })}</span>
            {r.blocked > 0 && (
              <span className="text-chart-4">{t("hub.goalBlocked", { n: r.blocked })}</span>
            )}
            {r.running > 0 && <span>{t("hub.goalRunning", { n: r.running })}</span>}
            {r.reviewing > 0 && <span>{t("hub.goalReviewing", { n: r.reviewing })}</span>}
          </div>
        </DetailSection>
      )}

      {tasks.length > 0 && (
        <div className="mt-7">
          <div className="mb-2.5 flex items-baseline justify-between">
            <h3 className="font-mono text-xs font-semibold uppercase tracking-[0.08em] text-muted-foreground">
              {t("hub.subtasks")}
            </h3>
            <Button type="button" variant="link" size="xs" onClick={() => setDagOpen(true)}>
              {t("hub.viewDag")} →
            </Button>
          </div>
          <div className="overflow-hidden rounded-xl border border-border">
            {tasks.map((task) => {
              const reason = task.status === "blocked" ? blockReason(task.id) : null;
              const isHighlight = task.status === "blocked" || task.status === "reviewing";
              return (
                <Link
                  key={task.id}
                  to="/agents/$agentId/tasks/$taskId"
                  params={{ agentId, taskId: task.id }}
                  className="flex w-full items-center gap-3 border-b border-border px-3.5 py-3 text-left last:border-b-0 hover:bg-muted/50"
                >
                  <span
                    className={cn("size-2 shrink-0 rounded-full", statusMeta(task.status).dot)}
                  />
                  <span className="min-w-0 flex-1">
                    <span className="block truncate text-[13px] font-medium">{task.title}</span>
                    {reason && (
                      <span className="mt-0.5 block truncate text-xs text-muted-foreground">
                        {reason}
                      </span>
                    )}
                  </span>
                  <span
                    className={cn(
                      "shrink-0 font-mono text-xs",
                      isHighlight ? "text-chart-4" : "text-muted-foreground",
                    )}
                  >
                    {statusLabel(t, task.status)}
                  </span>
                </Link>
              );
            })}
          </div>
        </div>
      )}

      <GoalPlanEditor
        goalId={goal.id}
        plan={plan ?? null}
        open={editorOpen}
        onOpenChange={setEditorOpen}
        onSaved={invalidate}
        t={t}
      />

      <Dialog open={dagOpen} onOpenChange={setDagOpen}>
        <DialogPopup className="h-[85vh] max-w-[90vw] overflow-hidden p-0">
          <div className="flex h-full flex-col">
            <div className="flex items-center justify-between border-b border-border px-5 py-3">
              <DialogTitle className="text-base font-semibold">{t("hub.viewDag")}</DialogTitle>
            </div>
            <div className="min-h-0 flex-1 overflow-auto">
              <Suspense
                fallback={
                  <div className="flex items-center justify-center py-20">
                    <div className="size-4 animate-spin rounded-full border-2 border-muted-foreground/30 border-t-muted-foreground" />
                  </div>
                }
              >
                {dagOpen && <GoalDetailPage />}
              </Suspense>
            </div>
          </div>
        </DialogPopup>
      </Dialog>
    </DetailShell>
  );
}

// GoalPlanActions surfaces the single next step that moves a goal's pending plan
// edit forward: accept (or submit for review) the staged draft, decide an open
// plan review, then materialize. It is driven by the plan's pending state, not the
// goal status, so it works for a goal's first plan and for a replan staged on an
// already running goal alike; with no pending edit it renders nothing.
function GoalPlanActions({
  goalId,
  activeReviewId,
  plan,
  acting,
  run,
}: {
  goalId: string;
  activeReviewId?: string;
  plan: ComponentsGoalPlan;
  acting: boolean;
  run: (fn: () => Promise<unknown>) => Promise<void>;
}) {
  const { t } = useI18n();
  const path = { goalId };

  // A draft plan with no staged edit has nothing to accept/submit (e.g. a human
  // review was rejected and cleared the pending content). Surface no broken
  // action — the goal's "Edit plan" button is the way forward (#5).
  if (plan.status === "draft" && !plan.pending_content) return null;

  let buttons: ReactNode;
  if (plan.status === "draft") {
    buttons =
      plan.review_policy === "human" ? (
        <Button
          size="sm"
          loading={acting}
          onClick={() => run(() => submitGoalPlanReview({ path, throwOnError: true }))}
        >
          {t("goals.planSubmitReview")}
        </Button>
      ) : (
        <Button
          size="sm"
          loading={acting}
          onClick={() => run(() => acceptGoalPlan({ path, throwOnError: true }))}
        >
          {t("goals.planAccept")}
        </Button>
      );
  } else if (plan.status === "in_review") {
    if (!activeReviewId) return null;
    const reviewPath = { goalId, reviewId: activeReviewId };
    buttons = (
      <>
        <Button
          size="sm"
          loading={acting}
          onClick={() => run(() => approveGoalPlanReview({ path: reviewPath, throwOnError: true }))}
        >
          {t("goals.planApprove")}
        </Button>
        <Button
          variant="outline"
          size="sm"
          loading={acting}
          onClick={() =>
            run(() => requestChangesGoalPlanReview({ path: reviewPath, throwOnError: true }))
          }
        >
          {t("goals.requestChanges")}
        </Button>
      </>
    );
  } else {
    // accepted | approved. A staged edit (pending_content) is what's waiting to
    // be materialized — either a goal's first plan or a replan on an already
    // running goal. With nothing pending the plan is already built, so there's no
    // action to surface (Activate / progress takes over).
    if (!plan.pending_content) return null;
    buttons = (
      <Button
        size="sm"
        loading={acting}
        onClick={() => run(() => materializeGoalPlan({ path, throwOnError: true }))}
      >
        {t("goals.planMaterialize")}
      </Button>
    );
  }

  return <>{buttons}</>;
}

function ReviewActions({ goalId, onDone }: { goalId: string; onDone: () => void }) {
  const { t } = useI18n();
  const [feedback, setFeedback] = useState("");
  const [acting, setActing] = useState(false);
  const qc = useQueryClient();
  const { data: goalReviews = [] } = useQuery({
    queryKey: ["goal-reviews", goalId],
    queryFn: async () => {
      const { data } = await listGoalReviews({ path: { goalId }, throwOnError: true });
      return (data?.reviews ?? []) as ComponentsReview[];
    },
    enabled: !!goalId,
  });

  const activeReview = goalReviews.find(
    (r: ComponentsReview) => r.status === "requested" || r.status === "in_progress",
  );

  if (!activeReview) return null;

  const act = async (fn: () => Promise<unknown>) => {
    setActing(true);
    try {
      await fn();
      onDone();
      void qc.invalidateQueries({ queryKey: ["goal-reviews", goalId] });
    } finally {
      setActing(false);
    }
  };

  return (
    <>
      <Button
        size="sm"
        loading={acting}
        onClick={() =>
          act(() =>
            approveGoalReview({
              path: { goalId, reviewId: activeReview.id },
              body: { feedback },
              throwOnError: true,
            }),
          )
        }
      >
        {t("hub.approve")}
      </Button>
      <Button
        variant="outline"
        size="sm"
        loading={acting}
        onClick={() =>
          act(() =>
            requestChangesGoalReview({
              path: { goalId, reviewId: activeReview.id },
              body: { feedback },
              throwOnError: true,
            }),
          )
        }
      >
        {t("hub.requestChanges")}
      </Button>
      <div className="w-full">
        <Textarea
          value={feedback}
          onChange={(e) => setFeedback(e.target.value)}
          placeholder={t("goals.feedbackPlaceholder")}
          className="mt-2"
        />
      </div>
    </>
  );
}

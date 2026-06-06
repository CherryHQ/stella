import { lazy, Suspense, useCallback, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  activateGoal,
  cancelGoal,
  approveGoalReview,
  requestChangesGoalReview,
  listGoalReviews,
} from "@/lib/api-client";
import type { ComponentsGoal, ComponentsReview } from "@/lib/api-client/types.gen";
import { goalGraphOptions } from "@/lib/queries/goals";
import { useI18n } from "@/lib/i18n";
import { formatTime } from "@/lib/time";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { Dialog, DialogPopup, DialogTitle } from "@/components/ui/dialog";
import { StatusPill, ProgressBar, statusLabel, statusMeta, rollup } from "../lib";

const GoalDetailPage = lazy(() =>
  import("../GoalDetailPage").then((m) => ({ default: m.GoalDetailPage })),
);

export function GoalDetail({ goal }: { goal: ComponentsGoal }) {
  const { t } = useI18n();
  const qc = useQueryClient();
  const [dagOpen, setDagOpen] = useState(false);

  const { data: graph } = useQuery(goalGraphOptions(goal.id));
  const tasks = graph?.tasks ?? [];
  const r = rollup(tasks);

  const invalidate = useCallback(() => {
    void qc.invalidateQueries({ queryKey: ["goals"] });
    void qc.invalidateQueries({ queryKey: ["goal-graph", goal.id] });
  }, [qc, goal.id]);

  const [acting, setActing] = useState(false);

  const handleActivate = useCallback(async () => {
    setActing(true);
    try {
      await activateGoal({ path: { goalId: goal.id }, throwOnError: true });
      invalidate();
    } finally {
      setActing(false);
    }
  }, [goal.id, invalidate]);

  const handleCancel = useCallback(async () => {
    setActing(true);
    try {
      await cancelGoal({ path: { goalId: goal.id }, throwOnError: true });
      invalidate();
    } finally {
      setActing(false);
    }
  }, [goal.id, invalidate]);

  return (
    <div className="max-w-[680px] px-9 py-7">
      {/* Eyebrow */}
      <div className="mb-2 flex items-center gap-1.5">
        <span className="font-mono text-[10px] font-semibold uppercase tracking-[0.1em] text-violet-500">
          {t("hub.chipGoal")}
        </span>
        <span className="text-[10px] text-border">/</span>
        <StatusPill status={goal.status} label={statusLabel(t, goal.status)} />
        {goal.priority === "urgent" && (
          <span className="rounded-md border border-amber-500/25 bg-amber-500/10 px-2 py-0.5 font-mono text-[10px] font-medium text-amber-600 dark:text-amber-400">
            urgent
          </span>
        )}
      </div>

      <h2 className="font-serif text-[22px] font-semibold tracking-tight leading-snug">
        {goal.title}
      </h2>
      {goal.description && (
        <p className="mt-1.5 text-sm leading-relaxed text-muted-foreground">{goal.description}</p>
      )}

      {/* Actions */}
      <div className="mt-5 flex flex-wrap gap-2">
        {goal.status === "draft" && (
          <Button size="sm" loading={acting} onClick={handleActivate}>
            {t("goals.activate")}
          </Button>
        )}
        {goal.status === "reviewing" && <ReviewActions goalId={goal.id} onDone={invalidate} />}
        {!["done", "failed", "cancelled"].includes(goal.status) && (
          <Button variant="ghost" size="sm" loading={acting} onClick={handleCancel}>
            {t("common.cancel")}
          </Button>
        )}
      </div>

      <hr className="my-6 border-border" />

      {/* Properties */}
      <div className="grid grid-cols-2 gap-x-6 gap-y-4">
        <PropField label={t("goals.colStatus")}>
          <StatusPill status={goal.status} label={statusLabel(t, goal.status)} />
        </PropField>
        <PropField label="Priority">
          <span className="text-[13px] font-medium capitalize">{goal.priority}</span>
        </PropField>
        <PropField label="Agent">
          <span className="text-[13px] font-medium">{goal.agent_id || "—"}</span>
        </PropField>
        <PropField label="Review Policy">
          <span className="text-[13px] font-medium capitalize">{goal.review_policy}</span>
        </PropField>
        <PropField label="Created">
          <span className="font-mono text-xs">{formatTime(goal.created_at)}</span>
        </PropField>
        <PropField label="Updated">
          <span className="font-mono text-xs">{formatTime(goal.updated_at)}</span>
        </PropField>
      </div>

      {/* Tasks */}
      {tasks.length > 0 && (
        <div className="mt-6">
          <div className="mb-2.5 flex items-center justify-between border-b border-border pb-2">
            <span className="font-mono text-[10px] font-semibold uppercase tracking-[0.1em] text-muted-foreground">
              {t("hub.tasks")} ({r.done}/{r.total})
            </span>
            <button
              type="button"
              onClick={() => setDagOpen(true)}
              className="text-[11px] font-medium text-primary hover:underline"
            >
              {t("hub.viewDag")}
            </button>
          </div>
          <ProgressBar r={r} className="mb-1" />
          <div className="mb-3 flex justify-between">
            <span className="font-mono text-[10px] text-muted-foreground">
              {r.done} done{r.running ? `, ${r.running} running` : ""}
              {r.reviewing ? `, ${r.reviewing} reviewing` : ""}
            </span>
            <span className="font-mono text-[10px] text-muted-foreground">
              {r.total > 0 ? Math.round((r.done / r.total) * 100) : 0}%
            </span>
          </div>
          <div className="space-y-1">
            {tasks.map((task) => {
              const isHighlight = task.status === "reviewing" || task.status === "blocked";
              return (
                <div
                  key={task.id}
                  className={cn(
                    "flex items-center gap-2.5 rounded-lg border px-3 py-2",
                    isHighlight ? "border-primary/25 bg-primary/[0.04]" : "border-border/60",
                  )}
                >
                  <span
                    className={cn("size-1.5 shrink-0 rounded-full", statusMeta(task.status).dot)}
                  />
                  <span className="flex-1 truncate text-[12.5px]">{task.title}</span>
                  <span
                    className={cn(
                      "shrink-0 font-mono text-[10px]",
                      isHighlight ? "text-primary" : "text-muted-foreground",
                    )}
                  >
                    {statusLabel(t, task.status)}
                  </span>
                </div>
              );
            })}
          </div>
        </div>
      )}

      {/* DAG modal */}
      <Dialog open={dagOpen} onOpenChange={setDagOpen}>
        <DialogPopup className="h-[85vh] max-w-[90vw] overflow-hidden p-0">
          <div className="flex h-full flex-col">
            <div className="flex items-center justify-between border-b border-border px-5 py-3">
              <DialogTitle className="font-serif text-base font-semibold">
                {t("hub.viewDag")}
              </DialogTitle>
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
    </div>
  );
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

function PropField({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <div className="mb-1 text-[11px] text-muted-foreground">{label}</div>
      <div>{children}</div>
    </div>
  );
}

import { useCallback, useMemo, useState } from "react";
import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams, useSearch } from "@tanstack/react-router";
import {
  abandonGoal,
  activateGoal,
  approvePlan,
  cancelGoal,
  deleteGoal,
  reattemptGoal,
  rejectPlan,
  submitVerdict,
  unarchiveGoal,
  updateGoal,
  waiveEdge,
} from "@/lib/api-client";
import type {
  ComponentsAcceptanceContract,
  ComponentsAcceptanceEvent,
  ComponentsAttempt,
  ComponentsGoal,
  ComponentsEdge,
  ComponentsDecompositionContent,
  ComponentsProposedChild,
  ComponentsProposedEdge,
  ComponentsReadiness,
  ComponentsUpdateGoalRequest,
} from "@/lib/api-client/types.gen";
import { apiErrorMessage } from "@/lib/api-error";
import {
  goalAttemptsOptions,
  goalChildrenOptions,
  goalEdgesOptions,
  goalEventsOptions,
  goalOptions,
  goalReadinessOptions,
} from "@/lib/queries/goals";
import { useI18n } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/messages";
import { formatTime } from "@/lib/time";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogPanel,
  DialogPopup,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field";
import { Form } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Tabs, TabsList, TabsTab, TabsPanel } from "@/components/ui/tabs";
import { MarkdownPreview } from "@/components/MarkdownPreview";
import {
  ProgressBar,
  StatusDot,
  StatusPill,
  blockReasonLabel,
  goalStatusLabel,
  displayStatus,
  policyLabel,
  priorityLabel,
  rollup,
  statusLabel,
} from "@/features/goals/lib";
import { AgentChip, DetailSection, DetailShell, MetaSep } from "@/features/goals/DetailShell";
import { GoalTimeline } from "@/features/goals/GoalTimeline";
import { postGoalTimelineMessage } from "@/features/goals/useGoalTimelineMessage";
import { ToastContainer, useToast } from "@/hooks/use-toast";

type TabKey = "timeline" | "overview" | "children" | "attempts" | "acceptance" | "deps" | "plan";

export function GoalPage() {
  const { t } = useI18n();
  const { agentId, goalId } = useParams({ strict: false }) as {
    agentId: string;
    goalId: string;
  };
  const { tab } = useSearch({ strict: false }) as { tab?: TabKey };
  const navigate = useNavigate();
  const qc = useQueryClient();
  const { toasts, showToast } = useToast();
  const [acting, setActing] = useState(false);

  const { data: d, isError } = useQuery(goalOptions(goalId));

  const invalidate = useCallback(() => {
    void qc.invalidateQueries({ queryKey: ["goal", goalId] });
    void qc.invalidateQueries({ queryKey: ["goal-children", goalId] });
    void qc.invalidateQueries({ queryKey: ["goal-attempts", goalId] });
    void qc.invalidateQueries({ queryKey: ["goal-events", goalId] });
    void qc.invalidateQueries({ queryKey: ["goal-timeline", goalId] });
    void qc.invalidateQueries({ queryKey: ["goal-edges", goalId] });
    void qc.invalidateQueries({ queryKey: ["goal-readiness", goalId] });
    void qc.invalidateQueries({ queryKey: ["goals"] });
    void qc.invalidateQueries({ queryKey: ["goals-page"] });
  }, [qc, goalId]);

  const act = useCallback(
    async (fn: () => Promise<unknown>) => {
      setActing(true);
      try {
        await fn();
        invalidate();
        return null;
      } catch (e) {
        showToast(apiErrorMessage(e, t("goals.actionFailed")), "error");
        return e;
      } finally {
        setActing(false);
      }
    },
    [invalidate, showToast, t],
  );

  const goTab = (next: TabKey) =>
    void navigate({
      to: "/agents/$agentId/goals/$goalId",
      params: { agentId, goalId },
      search: { tab: next },
    });

  if (isError) {
    return (
      <DetailShell agentId={agentId} kindLabel={t("goals.kindLeaf")} title={t("goals.notFound")}>
        <div />
      </DetailShell>
    );
  }
  if (!d) return null;

  const isComposite = d.kind === "composite";
  const path = { id: d.id };
  const active = tab ?? "timeline";
  const retryEnvironment = () =>
    void act(async () => {
      try {
        const reattempt = await postGoalTimelineMessage(
          qc,
          d.id,
          t("goals.environmentRetryMessage"),
        );
        showToast(
          reattempt ? t("goals.timelineReattemptAuthorized") : t("goals.timelineMessageSaved"),
        );
      } catch {
        showToast(t("goals.timelinePostFailed"), "error");
      }
    });

  return (
    <DetailShell
      agentId={agentId}
      kindLabel={t(isComposite ? "goals.kindComposite" : "goals.kindLeaf")}
      title={d.title}
      pill={<StatusPill status={displayStatus(d)} label={goalStatusLabel(t, d)} />}
      actions={
        <HeaderActions
          d={d}
          acting={acting}
          act={act}
          path={path}
          onVerdict={() => goTab("acceptance")}
          onTimeline={() => goTab("timeline")}
          onContract={() => goTab("acceptance")}
          onEnvironmentRetry={retryEnvironment}
        />
      }
    >
      <div className="mt-2.5 flex flex-wrap items-center gap-x-3.5 gap-y-1.5 text-[12.5px] text-muted-foreground">
        <AgentChip agentId={d.agent_id || agentId} />
        <MetaSep />
        <span>{priorityLabel(t, d.priority)}</span>
        <MetaSep />
        <span>{t("hub.createdAt", { time: formatTime(d.created_at) })}</span>
        <MetaSep />
        <span>{t("hub.updatedAt", { time: formatTime(d.updated_at) })}</span>
        {d.parent_id && (
          <>
            <MetaSep />
            <Link
              to="/agents/$agentId/goals/$goalId"
              params={{ agentId, goalId: d.parent_id }}
              className="font-medium text-primary hover:underline"
            >
              {t("hub.parentGoal")} →
            </Link>
          </>
        )}
      </div>

      <Tabs value={active} onValueChange={(v) => goTab(v as TabKey)} className="mt-6">
        <TabsList variant="underline">
          <TabsTab value="timeline">{t("goals.tabTimeline")}</TabsTab>
          <TabsTab value="overview">{t("goals.tabOverview")}</TabsTab>
          {isComposite && <TabsTab value="children">{t("goals.tabChildren")}</TabsTab>}
          {!isComposite && <TabsTab value="attempts">{t("goals.tabAttempts")}</TabsTab>}
          <TabsTab value="acceptance">{t("goals.tabAcceptance")}</TabsTab>
          {!isComposite && <TabsTab value="deps">{t("goals.tabDeps")}</TabsTab>}
          {isComposite && <TabsTab value="plan">{t("goals.tabRevisions")}</TabsTab>}
        </TabsList>

        <TabsPanel value="timeline" className="mt-5">
          <GoalTimeline goalId={d.id} />
        </TabsPanel>
        <TabsPanel value="overview" className="mt-5">
          <OverviewTab
            d={d}
            agentId={agentId}
            acting={acting}
            act={act}
            onVerdict={() => goTab("acceptance")}
            onPlan={() => goTab("plan")}
            onTimeline={() => goTab("timeline")}
            onContract={() => goTab("acceptance")}
            onEnvironmentRetry={retryEnvironment}
          />
        </TabsPanel>
        {isComposite && (
          <TabsPanel value="children" className="mt-5">
            <ChildrenTab d={d} agentId={agentId} />
          </TabsPanel>
        )}
        {!isComposite && (
          <TabsPanel value="attempts" className="mt-5">
            <AttemptsTab id={d.id} />
          </TabsPanel>
        )}
        <TabsPanel value="acceptance" className="mt-5">
          <AcceptanceTab d={d} acting={acting} act={act} />
        </TabsPanel>
        {!isComposite && (
          <TabsPanel value="deps" className="mt-5">
            <DepsTab d={d} agentId={agentId} acting={acting} act={act} />
          </TabsPanel>
        )}
        {isComposite && (
          <TabsPanel value="plan" className="mt-5">
            <PlanTab d={d} agentId={agentId} acting={acting} act={act} />
          </TabsPanel>
        )}
      </Tabs>
      <ToastContainer messages={toasts} />
    </DetailShell>
  );
}

// ── Header actions ───────────────────────────────────────────────────

// Runs a goal action; on failure the error is toasted centrally and returned
// (null on success) so callers needing inline display don't re-catch.
type ActRun = (fn: () => Promise<unknown>) => Promise<unknown>;

type BlockActionKind =
  | "budget"
  | "environment"
  | "contract"
  | "review"
  | "plan"
  | "planning"
  | "other";

function blockActionKind(d: ComponentsGoal): BlockActionKind {
  if (d.block_reason === "env_unavailable") {
    return "environment";
  }
  if (d.block_reason === "contract_conflict") {
    return "contract";
  }
  if (d.block_reason === "budget_exhausted") return "budget";
  if (d.block_reason === "needs_verdict") return "review";
  if (d.block_reason === "needs_plan_approval") return "plan";
  if (d.block_reason === "planning_invalid") return "planning";
  return "other";
}

function HeaderActions({
  d,
  acting,
  act,
  path,
  onVerdict,
  onTimeline,
  onContract,
  onEnvironmentRetry,
}: {
  d: ComponentsGoal;
  acting: boolean;
  act: ActRun;
  path: { id: string };
  onVerdict: () => void;
  onTimeline: () => void;
  onContract: () => void;
  onEnvironmentRetry: () => void;
}) {
  const { t } = useI18n();
  const archived = !!d.archived_at;
  const lc = d.lifecycle;
  const reason = d.block_reason;
  const isComposite = d.kind === "composite";
  const canActivate = lc === "draft" && (!isComposite || !!d.planned_at);
  const needsPlanApproval =
    isComposite && lc === "blocked" && reason === "needs_plan_approval" && !archived;

  const cancelBtn = (
    <Button
      variant="ghost"
      size="sm"
      loading={acting}
      onClick={() => act(() => cancelGoal({ path, body: {}, throwOnError: true }))}
    >
      {t("goals.cancel")}
    </Button>
  );

  return (
    <>
      {d.active_attempt_id && (lc === "active" || lc === "pending") && (
        <span className="inline-flex items-center gap-1.5 self-center font-mono text-xs text-chart-2">
          <span className="size-1.5 animate-pulse rounded-full bg-chart-2" />
          {t("goals.attemptRunning")}
        </span>
      )}

      {needsPlanApproval && <PlanDecisionActions d={d} acting={acting} act={act} />}

      {lc === "draft" && canActivate && (
        <Button
          size="sm"
          loading={acting}
          onClick={() => act(() => activateGoal({ path, throwOnError: true }))}
        >
          {t("goals.activate")}
        </Button>
      )}

      {lc === "blocked" && reason === "needs_verdict" && (
        <Button size="sm" onClick={onVerdict}>
          {t("goals.verdictSubmit")}
        </Button>
      )}

      {lc === "blocked" && blockActionKind(d) === "budget" && (
        <>
          <Button
            size="sm"
            loading={acting}
            onClick={() => act(() => reattemptGoal({ path, throwOnError: true }))}
          >
            {t("goals.reattempt")}
          </Button>
          <Button variant="outline" size="sm" onClick={onTimeline}>
            {t("goals.timelineComment")}
          </Button>
          <Button
            variant="outline"
            size="sm"
            loading={acting}
            onClick={() => act(() => abandonGoal({ path, body: {}, throwOnError: true }))}
          >
            {t("goals.abandon")}
          </Button>
        </>
      )}

      {lc === "blocked" && blockActionKind(d) === "environment" && (
        <>
          <Button size="sm" loading={acting} onClick={onEnvironmentRetry}>
            {t("goals.environmentRetry")}
          </Button>
          <Button
            variant="outline"
            size="sm"
            onClick={() => window.alert(t("goals.reportAdminHint"))}
          >
            {t("goals.reportAdmin")}
          </Button>
        </>
      )}

      {lc === "blocked" && blockActionKind(d) === "planning" && (
        <Button
          size="sm"
          loading={acting}
          onClick={() => act(() => reattemptGoal({ path, throwOnError: true }))}
        >
          {t("goals.reattempt")}
        </Button>
      )}

      {lc === "blocked" && blockActionKind(d) === "contract" && (
        <Button variant="outline" size="sm" onClick={onContract}>
          {t("goals.editContract")}
        </Button>
      )}

      {!archived && lc === "done" && (
        <Button
          variant="outline"
          size="sm"
          loading={acting}
          onClick={() => act(() => deleteGoal({ path, throwOnError: true }))}
        >
          {t("goals.archive")}
        </Button>
      )}

      {archived && (
        <Button
          variant="outline"
          size="sm"
          loading={acting}
          onClick={() => act(() => unarchiveGoal({ path, throwOnError: true }))}
        >
          {t("goals.unarchive")}
        </Button>
      )}

      {["draft", "pending", "active"].includes(lc) && cancelBtn}
      {lc === "blocked" && cancelBtn}
    </>
  );
}

// ── Overview ─────────────────────────────────────────────────────────

function OverviewTab({
  d,
  agentId,
  acting,
  act,
  onVerdict,
  onPlan,
  onTimeline,
  onContract,
  onEnvironmentRetry,
}: {
  d: ComponentsGoal;
  agentId: string;
  acting: boolean;
  act: ActRun;
  onVerdict: () => void;
  onPlan: () => void;
  onTimeline: () => void;
  onContract: () => void;
  onEnvironmentRetry: () => void;
}) {
  const { t } = useI18n();
  const { data: readiness } = useQuery(goalReadinessOptions(d.id));
  const isComposite = d.kind === "composite";
  const blocked = d.lifecycle === "blocked";
  return (
    <div className="space-y-1">
      {d.intent && (
        <DetailSection title={t("goals.intent")}>
          <MarkdownPreview
            content={d.intent}
            className="text-[13.5px] leading-relaxed [&_ol]:pl-5 [&_ul]:pl-5"
          />
        </DetailSection>
      )}

      {blocked && (
        <DetailSection title={t("goals.blockedTitle")}>
          <div className="rounded-xl border border-chart-4/30 bg-chart-4/[0.06] px-4 py-3.5">
            <p className="text-[13px] font-medium text-chart-4">{blockReasonLabel(t, d)}</p>
            {d.block_reason === "needs_verdict" && (
              <Button size="sm" className="mt-3" onClick={onVerdict}>
                {t("goals.verdictSubmit")}
              </Button>
            )}
            {blockActionKind(d) === "budget" && (
              <div className="mt-3 flex flex-wrap gap-2">
                <Button
                  size="sm"
                  loading={acting}
                  onClick={() =>
                    act(() => reattemptGoal({ path: { id: d.id }, throwOnError: true }))
                  }
                >
                  {t("goals.reattempt")}
                </Button>
                <Button variant="outline" size="sm" onClick={onTimeline}>
                  {t("goals.timelineComment")}
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  loading={acting}
                  onClick={() =>
                    act(() => abandonGoal({ path: { id: d.id }, body: {}, throwOnError: true }))
                  }
                >
                  {t("goals.abandon")}
                </Button>
              </div>
            )}
            {blockActionKind(d) === "environment" && (
              <div className="mt-3 flex flex-wrap gap-2">
                <Button size="sm" loading={acting} onClick={onEnvironmentRetry}>
                  {t("goals.environmentRetry")}
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => window.alert(t("goals.reportAdminHint"))}
                >
                  {t("goals.reportAdmin")}
                </Button>
              </div>
            )}
            {blockActionKind(d) === "planning" && (
              <Button
                size="sm"
                className="mt-3"
                loading={acting}
                onClick={() => act(() => reattemptGoal({ path: { id: d.id }, throwOnError: true }))}
              >
                {t("goals.reattempt")}
              </Button>
            )}
            {blockActionKind(d) === "contract" && (
              <Button variant="outline" size="sm" className="mt-3" onClick={onContract}>
                {t("goals.editContract")}
              </Button>
            )}
            {d.block_reason === "needs_plan_approval" && (
              <Button size="sm" className="mt-3" onClick={onPlan}>
                {t("goals.tabRevisions")} →
              </Button>
            )}
          </div>
        </DetailSection>
      )}

      <DetailSection title={t("goals.tabOverview")}>
        <div className="grid grid-cols-2 gap-x-6 divide-border sm:grid-cols-3">
          <Meta label={t("goals.fieldPriority")} value={priorityLabel(t, d.priority)} />
          <Meta
            label={t("goals.fieldReviewPolicy")}
            value={policyLabel(t, d.review_policy ?? "none")}
          />
          <Meta
            label={t("goals.fieldKind")}
            value={t(isComposite ? "goals.kindComposite" : "goals.kindLeaf")}
          />
          <Meta label={t("goals.fieldCreated")} value={formatTime(d.created_at)} />
          <Meta label={t("goals.fieldUpdated")} value={formatTime(d.updated_at)} />
          {d.accepted_at && (
            <Meta label={t("goals.fieldAccepted")} value={formatTime(d.accepted_at)} />
          )}
          <Meta label={t("goals.fieldAttempts")} value={String(d.attempt_count ?? 0)} />
          <Meta label={t("goals.fieldDepth")} value={String(d.depth)} />
        </div>
      </DetailSection>

      <DetailSection title={t("goals.readiness")}>
        <ReadinessBlock readiness={readiness ?? null} />
      </DetailSection>

      {isComposite && (
        <div className="mt-4">
          <Button variant="link" size="xs" onClick={onPlan}>
            {t("goals.tabRevisions")} →
          </Button>
        </div>
      )}

      {isComposite ? (
        <CompositeDeliverables id={d.id} agentId={agentId} />
      ) : (
        d.accepted_output && (
          <DetailSection title={t("goals.outputTitle")}>
            <AcceptedOutputView output={d.accepted_output} />
          </DetailSection>
        )
      )}
    </div>
  );
}

// CompositeDeliverables aggregates a composite's deliverables in one place. A
// composite's own accepted_output is only a title-bearing rollup marker (its
// acceptance is DERIVED from children, never produced), so the actual work
// lives on the accepted children. This pulls each accepted child's frozen
// output up to the parent overview so the whole goal's result is readable
// without drilling into every leaf.
function CompositeDeliverables({ id, agentId }: { id: string; agentId: string }) {
  const { t } = useI18n();
  const { data: children = [] } = useQuery(goalChildrenOptions(id));
  const delivered = children.filter((c) => c.accepted_output);
  if (delivered.length === 0) return null;
  return (
    <DetailSection title={t("goals.deliverablesTitle")}>
      <div className="space-y-3">
        {delivered.map((c) => (
          <div key={c.id} className="rounded-xl border border-border p-3.5">
            <Link
              to="/agents/$agentId/goals/$goalId"
              params={{ agentId, goalId: c.id }}
              className="mb-2 flex items-center gap-2 text-[13px] font-medium hover:underline"
            >
              <StatusDot status={displayStatus(c)} />
              <span className="truncate">{c.title}</span>
            </Link>
            <AcceptedOutputView output={c.accepted_output!} />
          </div>
        ))}
      </div>
    </DetailSection>
  );
}

function ReadinessBlock({ readiness }: { readiness: ComponentsReadiness | null }) {
  const { t } = useI18n();
  if (!readiness) return <Empty text={t("goals.noReasons")} />;
  const stateKey: Record<ComponentsReadiness["state"], MessageKey> = {
    dispatchable: "goals.readinessDispatchable",
    waiting_deps: "goals.readinessWaitingDeps",
    blocked: "goals.readinessBlocked",
    active: "goals.readinessActive",
    terminal: "goals.readinessTerminal",
    draft: "goals.readinessDraft",
    composite: "goals.readinessComposite",
    unknown: "goals.readinessUnknown",
  };
  return (
    <div className="rounded-xl border border-border bg-background p-3.5">
      <span
        className={cn(
          "font-mono text-xs font-semibold",
          readiness.dispatchable ? "text-chart-3" : "text-chart-4",
        )}
      >
        {t(stateKey[readiness.state] ?? "goals.readinessUnknown")}
      </span>
      {readiness.reasons && readiness.reasons.length > 0 ? (
        <ul className="mt-2 space-y-1.5">
          {readiness.reasons.map((reason, i) => (
            <li
              key={`${reason.type ?? ""}:${reason.upstream_id ?? i}`}
              className="text-[12.5px] text-foreground"
            >
              {reason.type && (
                <span className="font-mono text-muted-foreground">{reason.type}</span>
              )}
              {reason.detail ? ` — ${reason.detail}` : ""}
            </li>
          ))}
        </ul>
      ) : (
        <p className="mt-2 text-[12.5px] text-muted-foreground">{t("goals.noReasons")}</p>
      )}
    </div>
  );
}

// ── Children ─────────────────────────────────────────────────────────

function ChildrenTab({ d, agentId }: { d: ComponentsGoal; agentId: string }) {
  const { t } = useI18n();
  const { data: children = [] } = useQuery(goalChildrenOptions(d.id));
  const r = useMemo(() => rollup(children), [children]);

  if (children.length === 0) return <Empty text={t("goals.noChildren")} />;

  return (
    <div className="space-y-4">
      <div>
        <div className="mb-2 flex items-center justify-between font-mono text-xs text-muted-foreground">
          <span className="font-semibold">{t("goals.childrenTitle")}</span>
          <span>
            {t("goals.requiredOf", {
              accepted: r.accepted,
              total: r.total,
            })}
          </span>
        </div>
        <ProgressBar r={r} className="h-2" />
      </div>
      <div className="overflow-hidden rounded-xl border border-border">
        {children.map((c) => (
          <Link
            key={c.id}
            to="/agents/$agentId/goals/$goalId"
            params={{ agentId, goalId: c.id }}
            className="flex w-full items-start gap-3 border-b border-border px-3.5 py-3 text-left last:border-b-0 hover:bg-muted/50"
          >
            <StatusDot status={displayStatus(c)} className="mt-1.5" />
            <span className="min-w-0 flex-1">
              <span className="block truncate text-[13px] font-medium">{c.title}</span>
              {c.intent && (
                <span className="mt-0.5 block truncate text-xs text-muted-foreground">
                  {c.intent}
                </span>
              )}
            </span>
            <span className="shrink-0 font-mono text-xs text-muted-foreground">
              {statusLabel(t, displayStatus(c))}
            </span>
          </Link>
        ))}
      </div>
    </div>
  );
}

// ── Attempts ─────────────────────────────────────────────────────────

const PURPOSE_KEY: Record<ComponentsAttempt["purpose"], MessageKey> = {
  execution: "goals.purposeExecution",
  decomposition: "goals.purposeDecomposition",
  review: "goals.purposeReview",
};

const ATTEMPT_STATUS_KEY: Record<ComponentsAttempt["status"], MessageKey> = {
  queued: "goals.attemptQueued",
  running: "goals.attemptRunning",
  submitted: "goals.attemptSubmitted",
  interrupted: "goals.attemptInterrupted",
  failed: "goals.attemptFailed",
  cancelled: "goals.attemptCancelled",
};

function AttemptsTab({ id }: { id: string }) {
  const { t } = useI18n();
  const { data: attempts = [] } = useQuery(goalAttemptsOptions(id));
  if (attempts.length === 0) return <Empty text={t("goals.noAttempts")} />;
  const sorted = [...attempts].sort((a, b) => b.attempt_no - a.attempt_no);
  return (
    <ul className="space-y-2">
      {sorted.map((a) => (
        <AttemptItem key={a.id} a={a} />
      ))}
    </ul>
  );
}

function AttemptItem({ a }: { a: ComponentsAttempt }) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const output = a.output && typeof a.output === "object" ? a.output : null;
  const hasOutput = !!output && Object.keys(output).length > 0;
  const hasGaps = !!a.gaps && Object.keys(a.gaps).length > 0;
  const canExpand = hasOutput || hasGaps || !!a.error;

  return (
    <li className="rounded-xl border border-border bg-background">
      <button
        type="button"
        disabled={!canExpand}
        onClick={() => setOpen((v) => !v)}
        className="flex w-full flex-col gap-1.5 px-3.5 py-3 text-left disabled:cursor-default"
      >
        <span className="flex w-full items-center justify-between gap-3">
          <span className="flex min-w-0 items-center gap-2">
            {canExpand && (
              <span
                className={cn(
                  "font-mono text-[10px] text-muted-foreground transition-transform",
                  open && "rotate-90",
                )}
              >
                ▶
              </span>
            )}
            <span className="text-sm font-medium">{t("goals.attemptNo", { n: a.attempt_no })}</span>
          </span>
          <span className="shrink-0 font-mono text-xs text-muted-foreground">
            {t(PURPOSE_KEY[a.purpose] ?? "goals.purposeExecution")}
          </span>
        </span>
        <span className="flex w-full items-center justify-between font-mono text-[10.5px] text-muted-foreground">
          <span>{t(ATTEMPT_STATUS_KEY[a.status] ?? "goals.attemptQueued")}</span>
          <span>{formatTime(a.finished_at ?? a.started_at ?? a.created_at)}</span>
        </span>
      </button>
      {open && canExpand && (
        <div className="space-y-3 border-t border-border px-3.5 py-3">
          {a.error && <p className="text-[12px] text-destructive">{a.error}</p>}
          {hasOutput && (
            <div>
              <div className="mb-1.5 font-mono text-[10.5px] font-semibold uppercase tracking-wide text-muted-foreground">
                {t("goals.outputResult")}
              </div>
              <div className="rounded-lg border border-border bg-muted/40 p-3">
                <JsonView value={output} />
              </div>
            </div>
          )}
          {hasGaps && (
            <div>
              <div className="mb-1.5 font-mono text-[10.5px] font-semibold text-muted-foreground">
                {t("goals.attemptGaps")}
              </div>
              <div className="rounded-lg border border-border bg-muted/40 p-3">
                <JsonView value={a.gaps} />
              </div>
            </div>
          )}
        </div>
      )}
    </li>
  );
}

// ── Acceptance ───────────────────────────────────────────────────────

function passingItemIds(events: ComponentsAcceptanceEvent[]): Set<string> {
  const ids = new Set<string>();
  for (const ev of events) if (ev.result === "pass") ids.add(ev.item_id);
  return ids;
}

// The pending judgment is the first judgment item with no passing event yet,
// preferring authority=human items. A goal parked at needs_verdict makes the
// human the adjudicator of last resort even for authority=agent items (e.g. a
// composite's own judgment item, which no review attempt will ever judge) —
// SubmitVerdict accepts a human verdict for any item and re-folds.
function pendingVerdictItemId(
  d: ComponentsGoal,
  events: ComponentsAcceptanceEvent[],
): string | null {
  const judgments = (d.acceptance_contract?.items ?? []).filter((it) => it.kind === "judgment");
  if (judgments.length === 0) return null;
  const passed = passingItemIds(events);
  const human = judgments.filter((it) => it.authority === "human");
  const firstPending = (pool: typeof judgments) => pool.find((it) => !passed.has(it.id));
  return (firstPending(human) ?? firstPending(judgments) ?? human[0] ?? judgments[0]).id;
}

// evaluatedOutputHash mirrors the backend's evaluatedAttempt (converge.go): the
// fold scopes a verdict to the active attempt's output if one is pointed, else
// the most recent submitted execution attempt — the artifact the human reviewed.
// A verdict must carry this exact hash as scope_hash or DeriveAcceptance drops it
// as stale (§4.2). "" when no reviewable output exists.
function evaluatedOutputHash(d: ComponentsGoal, attempts: ComponentsAttempt[]): string | undefined {
  const pick = d.active_attempt_id
    ? attempts.find((a) => a.id === d.active_attempt_id)
    : attempts
        .filter((a) => a.purpose === "execution" && a.status === "submitted")
        .sort((a, b) => b.attempt_no - a.attempt_no)[0];
  const hash = (pick?.output as { hash?: string } | undefined)?.hash;
  return hash || undefined;
}

function AcceptanceTab({ d, acting, act }: { d: ComponentsGoal; acting: boolean; act: ActRun }) {
  const { t } = useI18n();
  const { data: events = [] } = useQuery(goalEventsOptions(d.id));
  const { data: attempts = [] } = useQuery(goalAttemptsOptions(d.id));
  const needsVerdict = d.lifecycle === "blocked" && d.block_reason === "needs_verdict";
  const itemId = needsVerdict ? pendingVerdictItemId(d, events) : null;

  return (
    <div className="space-y-4">
      {needsVerdict && itemId && (
        <VerdictForm
          d={d}
          itemId={itemId}
          scopeHash={evaluatedOutputHash(d, attempts)}
          acting={acting}
          act={act}
          prompt={
            (d.acceptance_contract?.items ?? []).find((it) => it.id === itemId)?.prompt ?? null
          }
        />
      )}

      <ContractEditor d={d} acting={acting} act={act} />

      <DetailSection title={t("goals.acceptanceTitle")}>
        {events.length === 0 ? (
          <Empty text={t(d.kind === "composite" ? "goals.noEventsComposite" : "goals.noEvents")} />
        ) : (
          <ul className="space-y-2">
            {[...events]
              .sort((a, b) => b.seq - a.seq)
              .map((ev) => (
                <li key={ev.id} className="rounded-xl border border-border bg-background p-3.5">
                  <div className="flex items-center justify-between">
                    <span className="flex items-center gap-2 font-mono text-xs text-muted-foreground">
                      {t(
                        ev.item_kind === "judgment"
                          ? "goals.itemJudgment"
                          : "goals.itemDeterministic",
                      )}
                      <span
                        className={cn(
                          "font-semibold",
                          ev.result === "pass" ? "text-chart-3" : "text-destructive",
                        )}
                      >
                        {t(ev.result === "pass" ? "goals.resultPass" : "goals.resultFail")}
                      </span>
                    </span>
                    <span className="font-mono text-xs text-muted-foreground">
                      {t(
                        ev.authority === "human"
                          ? "goals.authorityHuman"
                          : ev.authority === "agent"
                            ? "goals.authorityAgent"
                            : "goals.authoritySystem",
                      )}
                    </span>
                  </div>
                  {ev.rationale && (
                    <p className="mt-1.5 text-[12.5px] text-foreground">{ev.rationale}</p>
                  )}
                  <span className="mt-1.5 block font-mono text-[10.5px] text-muted-foreground">
                    {formatTime(ev.created_at)}
                  </span>
                </li>
              ))}
          </ul>
        )}
      </DetailSection>
    </div>
  );
}

function ContractEditor({ d, acting, act }: { d: ComponentsGoal; acting: boolean; act: ActRun }) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState("");
  // A contract_conflict may live in the intent or the convergence policy rather
  // than the contract itself (e.g. an intent demanding more levels than
  // max_depth allows), so the resolve dialog exposes all three inputs the
  // planner folds together.
  const [intent, setIntent] = useState("");
  const [maxDepth, setMaxDepth] = useState("");
  const [error, setError] = useState<string | null>(null);
  const current = useMemo(
    () => JSON.stringify(d.acceptance_contract ?? {}, null, 2),
    [d.acceptance_contract],
  );
  const currentDepth = d.convergence_policy?.max_depth ?? 1;

  const reset = () => {
    setDraft(current);
    setIntent(d.intent ?? "");
    setMaxDepth(String(currentDepth));
    setError(null);
  };
  const setDialogOpen = (next: boolean) => {
    if (next) reset();
    setOpen(next);
  };
  const save = async () => {
    let contract: ComponentsAcceptanceContract;
    try {
      const parsed = JSON.parse(draft) as unknown;
      if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
        setError(t("goals.contractInvalidJson"));
        return;
      }
      contract = parsed as ComponentsAcceptanceContract;
    } catch {
      setError(t("goals.contractInvalidJson"));
      return;
    }
    const body: ComponentsUpdateGoalRequest = { acceptance_contract: contract };
    if (intent.trim() !== (d.intent ?? "").trim()) body.intent = intent;
    const depth = Number(maxDepth);
    if (Number.isInteger(depth) && depth >= 1 && depth !== currentDepth) {
      // PATCH replaces the whole policy, so overlay the edited knob onto the
      // current values instead of sending a partial body.
      body.convergence_policy = { ...d.convergence_policy, max_depth: depth };
    }
    const err = await act(() => updateGoal({ path: { id: d.id }, body, throwOnError: true }));
    if (err) {
      setError(apiErrorMessage(err, t("goals.contractSaveFailed")));
      return;
    }
    setOpen(false);
  };

  return (
    <DetailSection title={t("goals.contractTitle")}>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-[12.5px] text-muted-foreground">{t("goals.contractDescription")}</p>
        <Dialog open={open} onOpenChange={setDialogOpen}>
          <DialogTrigger render={<Button variant="outline" size="sm" />}>
            {t("goals.editContract")}
          </DialogTrigger>
          <DialogPopup>
            <DialogHeader>
              <DialogTitle>{t("goals.contractTitle")}</DialogTitle>
              <DialogDescription>{t("goals.contractJsonHelp")}</DialogDescription>
            </DialogHeader>
            <Form
              className="contents"
              onSubmit={(e) => {
                e.preventDefault();
                void save();
              }}
            >
              <DialogPanel className="space-y-4">
                <Field>
                  <FieldLabel>{t("goals.intent")}</FieldLabel>
                  <Textarea
                    name="intent"
                    value={intent}
                    onChange={(e) => setIntent(e.target.value)}
                    rows={4}
                  />
                  <FieldDescription>{t("goals.conflictIntentHelp")}</FieldDescription>
                </Field>
                <Field>
                  <FieldLabel>{t("goals.conflictMaxDepthLabel")}</FieldLabel>
                  <Input
                    name="max_depth"
                    type="number"
                    min={1}
                    value={maxDepth}
                    onChange={(e) => setMaxDepth(e.target.value)}
                    className="w-24"
                  />
                  <FieldDescription>{t("goals.conflictMaxDepthHelp")}</FieldDescription>
                </Field>
                <Field>
                  <FieldLabel>{t("goals.contractJsonLabel")}</FieldLabel>
                  <Textarea
                    name="acceptance_contract"
                    value={draft}
                    onChange={(e) => setDraft(e.target.value)}
                    rows={10}
                  />
                  <FieldDescription>{t("goals.contractJsonHelp")}</FieldDescription>
                  {/* Base UI Field.Error only renders on control validity failure;
                      server/parse errors are manual state, so render a plain element. */}
                  {error && <p className="text-xs text-destructive">{error}</p>}
                </Field>
              </DialogPanel>
              <DialogFooter>
                <Button type="button" variant="outline" onClick={() => setOpen(false)}>
                  {t("common.cancel")}
                </Button>
                <Button type="submit" loading={acting}>
                  {t("goals.contractSave")}
                </Button>
              </DialogFooter>
            </Form>
          </DialogPopup>
        </Dialog>
      </div>
    </DetailSection>
  );
}

function VerdictForm({
  d,
  itemId,
  scopeHash,
  prompt,
  acting,
  act,
}: {
  d: ComponentsGoal;
  itemId: string;
  scopeHash: string | undefined;
  prompt: string | null;
  acting: boolean;
  act: ActRun;
}) {
  const { t } = useI18n();
  const [rationale, setRationale] = useState("");
  const submit = (result: "pass" | "fail") =>
    act(async () => {
      await submitVerdict({
        path: { id: d.id },
        body: {
          item_id: itemId,
          result,
          rationale: rationale.trim() || undefined,
          scope_hash: scopeHash,
        },
        throwOnError: true,
      });
      setRationale("");
    });

  return (
    <div className="rounded-xl border border-primary/30 bg-primary/[0.06] p-4">
      <div className="font-mono text-xs font-semibold text-primary/80">
        {t("goals.verdictTitle")}
      </div>
      <p className="mt-1.5 text-[13px] text-foreground">{prompt || t("goals.verdictPrompt")}</p>
      <Textarea
        value={rationale}
        onChange={(e) => setRationale(e.target.value)}
        rows={3}
        placeholder={t("goals.verdictRationale")}
        className="mt-2.5 text-sm"
      />
      <div className="mt-2.5 flex flex-wrap gap-2">
        <Button size="sm" loading={acting} onClick={() => submit("pass")}>
          {t("goals.verdictPass")}
        </Button>
        <Button
          size="sm"
          variant="outline"
          loading={acting}
          className="text-destructive"
          onClick={() => submit("fail")}
        >
          {t("goals.verdictFail")}
        </Button>
      </div>
    </div>
  );
}

// ── Dependencies ─────────────────────────────────────────────────────

function DepsTab({
  d,
  agentId,
  acting,
  act,
}: {
  d: ComponentsGoal;
  agentId: string;
  acting: boolean;
  act: ActRun;
}) {
  const { t } = useI18n();
  const { data: edges = [] } = useQuery(goalEdgesOptions(d.id));
  if (edges.length === 0) return <Empty text={t("goals.noDeps")} />;
  return (
    <ul className="space-y-2">
      {edges.map((e) => (
        <EdgeRow key={e.upstream_id} d={d} edge={e} agentId={agentId} acting={acting} act={act} />
      ))}
    </ul>
  );
}

function EdgeRow({
  d,
  edge,
  agentId,
  acting,
  act,
}: {
  d: ComponentsGoal;
  edge: ComponentsEdge;
  agentId: string;
  acting: boolean;
  act: ActRun;
}) {
  const { t } = useI18n();
  const [reason, setReason] = useState("");
  const waived = !!edge.waived_at;
  const canWaive = !waived && edge.edge_kind === "hard";

  const onFailureKey: Record<ComponentsEdge["on_failure"], MessageKey> = {
    block: "goals.onFailureBlock",
    fail: "goals.onFailureFail",
    ignore: "goals.onFailureIgnore",
  };

  return (
    <li className="rounded-xl border border-border bg-background p-3.5">
      <div className="flex items-center gap-3">
        <Link
          to="/agents/$agentId/goals/$goalId"
          params={{ agentId, goalId: edge.upstream_id }}
          className="min-w-0 flex-1 truncate font-mono text-[12.5px] font-medium text-primary hover:underline"
        >
          {edge.upstream_id.slice(0, 8)}
        </Link>
        <span className="font-mono text-xs text-muted-foreground">
          {t(edge.edge_kind === "hard" ? "goals.edgeHard" : "goals.edgeSoft")}
        </span>
        <span className="font-mono text-xs text-muted-foreground">
          {t(onFailureKey[edge.on_failure] ?? "goals.onFailureBlock")}
        </span>
        {waived && <span className="font-mono text-xs text-chart-3">{t("goals.waived")}</span>}
      </div>
      {canWaive && (
        <div className="mt-2.5 flex flex-wrap items-center gap-2">
          <Input
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            placeholder={t("goals.waiveReasonPlaceholder")}
            className="h-8 max-w-xs text-sm"
          />
          <Button
            size="sm"
            variant="outline"
            loading={acting}
            onClick={() =>
              act(() =>
                waiveEdge({
                  path: { id: d.id, upstreamId: edge.upstream_id },
                  body: { reason: reason.trim() || undefined },
                  throwOnError: true,
                }),
              )
            }
          >
            {t("goals.waive")}
          </Button>
        </div>
      )}
    </li>
  );
}

// ── Plan (decomposition revisions) ───────────────────────────────────

const EDGE_ON_FAILURE_KEY: Record<NonNullable<ComponentsProposedEdge["on_failure"]>, MessageKey> = {
  block: "goals.onFailureBlock",
  fail: "goals.onFailureFail",
  ignore: "goals.onFailureIgnore",
};

function PlanTab({
  d,
  agentId,
  acting,
  act,
}: {
  d: ComponentsGoal;
  agentId: string;
  acting: boolean;
  act: ActRun;
}) {
  const { t } = useI18n();
  // The plan lives inline on the goal (DecompositionContent). It is empty for a
  // leaf or an unplanned composite, and holds the proposal while the composite
  // is parked at blocked(needs_plan_approval).
  const plan = (d.plan ?? {}) as ComponentsDecompositionContent;
  const children = plan.children ?? [];
  const edges = plan.edges ?? [];
  const { data: materializedChildren = [] } = useQuery(goalChildrenOptions(d.id));
  const childIDs = useMemo(
    () => new Set(materializedChildren.map((child) => child.id)),
    [materializedChildren],
  );
  const edgeQueries = useQueries({
    queries: materializedChildren.map((child) => goalEdgesOptions(child.id)),
  });
  const materializedEdges = edgeQueries
    .flatMap((query) => query.data ?? [])
    .filter((edge) => childIDs.has(edge.upstream_id));
  const needsApproval =
    d.lifecycle === "blocked" && d.block_reason === "needs_plan_approval" && !d.archived_at;

  if (children.length === 0 && materializedChildren.length === 0) {
    return <Empty text={t("goals.noRevisions")} />;
  }

  // Edges reference children by their stable `key`; show the human title instead
  // (falling back to the key if a child was dropped from the plan).
  const titleOf = (key: string) => children.find((c) => c.key === key)?.title ?? key;

  return (
    <div className="space-y-4">
      {needsApproval && (
        <div className="rounded-xl border border-primary/30 bg-primary/[0.06] p-4">
          <p className="text-[13px] font-medium text-primary/80">{t("goals.planAwaiting")}</p>
          <div className="mt-3">
            <PlanDecisionActions d={d} acting={acting} act={act} />
          </div>
        </div>
      )}

      {materializedChildren.length > 0 && (
        <MaterializedPlanDag
          children={materializedChildren}
          edges={materializedEdges}
          agentId={agentId}
        />
      )}

      {children.length > 0 && (
        <ul className="space-y-2">
          {children.map((c) => (
            <ProposedChildItem key={c.key} c={c} />
          ))}
        </ul>
      )}

      {edges.length > 0 && (
        <div>
          <div className="mb-1.5 font-mono text-[10.5px] font-semibold text-muted-foreground">
            {t("goals.planEdges", { count: edges.length })}
          </div>
          <ul className="space-y-1">
            {edges.map((e, i) => (
              <li
                key={`${e.upstream_key}-${e.downstream_key}-${i}`}
                className="flex flex-wrap items-center gap-x-1.5 gap-y-0.5 text-[11.5px] text-muted-foreground"
              >
                <span className="font-medium text-foreground">{titleOf(e.downstream_key)}</span>
                <span>{t("goals.planEdgeDep")}</span>
                <span className="font-medium text-foreground">{titleOf(e.upstream_key)}</span>
                <span className="font-mono text-[10px]">
                  {t(e.kind === "soft" ? "goals.planDepSoft" : "goals.planDepHard")}
                </span>
                {e.on_failure && e.on_failure !== "block" && (
                  <span className="font-mono text-[10px]">
                    {t(EDGE_ON_FAILURE_KEY[e.on_failure])}
                  </span>
                )}
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}

function MaterializedPlanDag({
  children,
  edges,
  agentId,
}: {
  children: ComponentsGoal[];
  edges: ComponentsEdge[];
  agentId: string;
}) {
  const { t } = useI18n();
  const incoming = new Map<string, ComponentsEdge[]>();
  for (const edge of edges) {
    const list = incoming.get(edge.goal_id) ?? [];
    list.push(edge);
    incoming.set(edge.goal_id, list);
  }
  const titleOf = (id: string) =>
    children.find((child) => child.id === id)?.title ?? id.slice(0, 8);
  const onFailureKey: Record<ComponentsEdge["on_failure"], MessageKey> = {
    block: "goals.onFailureBlock",
    fail: "goals.onFailureFail",
    ignore: "goals.onFailureIgnore",
  };

  return (
    <div>
      <div className="mb-2 font-mono text-[10.5px] font-semibold text-muted-foreground">
        {t("goals.planMaterializedDag")}
      </div>
      <ul className="space-y-2">
        {children.map((child) => {
          const deps = incoming.get(child.id) ?? [];
          return (
            <li key={child.id} className="rounded-lg border border-border bg-muted/30 px-3 py-2.5">
              <div className="flex flex-wrap items-center gap-2">
                <StatusDot status={displayStatus(child)} />
                <Link
                  to="/agents/$agentId/goals/$goalId"
                  params={{ agentId, goalId: child.id }}
                  className="min-w-0 flex-1 truncate text-[13px] font-medium hover:underline"
                >
                  {child.title}
                </Link>
                <span className="font-mono text-[10.5px] text-muted-foreground">
                  {statusLabel(t, displayStatus(child))}
                </span>
              </div>
              {deps.length > 0 ? (
                <ul className="mt-2 space-y-1 text-[11.5px] text-muted-foreground">
                  {deps.map((edge) => (
                    <li key={edge.upstream_id} className="flex flex-wrap items-center gap-1.5">
                      <span>{titleOf(edge.upstream_id)}</span>
                      <span>→</span>
                      <span className="font-medium text-foreground">{child.title}</span>
                      <span className="font-mono text-[10px]">
                        {t(edge.edge_kind === "soft" ? "goals.planDepSoft" : "goals.planDepHard")}
                      </span>
                      <span className="font-mono text-[10px]">
                        {t(onFailureKey[edge.on_failure] ?? "goals.onFailureBlock")}
                      </span>
                    </li>
                  ))}
                </ul>
              ) : (
                <p className="mt-1.5 text-[11.5px] text-muted-foreground">
                  {t("goals.planNoIncomingDeps")}
                </p>
              )}
            </li>
          );
        })}
      </ul>
    </div>
  );
}

function ProposedChildItem({ c }: { c: ComponentsProposedChild }) {
  const { t } = useI18n();
  return (
    <li className="rounded-lg border border-border bg-muted/30 px-3 py-2">
      <div className="flex items-center gap-2">
        <span className="min-w-0 flex-1 truncate text-[13px] font-medium">{c.title}</span>
        <span className="shrink-0 font-mono text-[10.5px] text-muted-foreground">
          {t(c.kind === "composite" ? "goals.kindComposite" : "goals.kindLeaf")}
        </span>
        {c.required === false && (
          <span className="shrink-0 font-mono text-[10.5px] text-muted-foreground">
            {t("goals.planOptional")}
          </span>
        )}
      </div>
      {c.intent && <p className="mt-1 text-xs text-muted-foreground">{c.intent}</p>}
      {c.acceptance_contract && <AcceptanceContractView contract={c.acceptance_contract} />}
    </li>
  );
}

function AcceptanceContractView({ contract }: { contract: ComponentsAcceptanceContract }) {
  const { t } = useI18n();
  const items = contract.items ?? [];
  if (items.length === 0) return null;

  return (
    <div className="mt-2 rounded-lg border border-border bg-background px-2.5 py-2">
      <div className="mb-1 flex items-center gap-2 font-mono text-[10px] uppercase tracking-wide text-muted-foreground">
        <span className="font-semibold">{t("goals.planContract")}</span>
        {contract.policy && <span>{contract.policy}</span>}
      </div>
      <ul className="space-y-1.5">
        {items.map((it) => (
          <li key={it.id} className="text-[11.5px] text-muted-foreground">
            <span className="flex items-center gap-1.5">
              <span className="font-mono">
                {t(
                  it.kind === "judgment" ? "goals.contractJudgment" : "goals.contractDeterministic",
                )}
              </span>
              {it.kind === "judgment" && it.authority && (
                <span className="font-mono text-[10px]">{it.authority}</span>
              )}
              {it.required === false && (
                <span className="font-mono text-[10px]">{t("goals.planOptional")}</span>
              )}
            </span>
            {it.command && (
              <code className="mt-0.5 block break-all font-mono text-[11px] text-foreground">
                {it.command}
              </code>
            )}
            {(it.rubric || it.prompt) && (
              <p className="mt-0.5 text-[11px]">{it.rubric ?? it.prompt}</p>
            )}
          </li>
        ))}
      </ul>
    </div>
  );
}

// PlanDecisionActions is the single plan gate: approve materializes children,
// reject sends the composite back to draft for re-decomposition. Shown only
// while blocked(needs_plan_approval).
function PlanDecisionActions({
  d,
  acting,
  act,
}: {
  d: ComponentsGoal;
  acting: boolean;
  act: ActRun;
}) {
  const { t } = useI18n();
  const path = { id: d.id };

  return (
    <div className="flex flex-wrap gap-2">
      <Button
        size="sm"
        loading={acting}
        onClick={() => act(() => approvePlan({ path, throwOnError: true }))}
      >
        {t("goals.revApprove")}
      </Button>
      <Button
        size="sm"
        variant="outline"
        loading={acting}
        className="text-destructive"
        onClick={() => act(() => rejectPlan({ path, body: {}, throwOnError: true }))}
      >
        {t("goals.revReject")}
      </Button>
    </div>
  );
}

// ── Accepted output ──────────────────────────────────────────────────

// AcceptedOutput is a fixed envelope (internal/goal/evidence.go) the OpenAPI
// types only model as an opaque record, so read its fields defensively. The
// human-facing summary leads; `result` is the genuinely arbitrary payload and
// renders through JsonView.
function AcceptedOutputView({ output }: { output: Record<string, unknown> }) {
  const { t } = useI18n();
  const str = (v: unknown) => (typeof v === "string" ? v : "");
  const summary = str(output.summary);
  const acceptedAt = str(output.accepted_at);
  const hash = str(output.hash);
  const result = output.result;
  const artifacts = Array.isArray(output.artifacts) ? output.artifacts : [];
  const hasResult = !!result && typeof result === "object" && Object.keys(result).length > 0;

  return (
    <div className="space-y-3">
      {summary && <p className="text-[13px] leading-relaxed text-foreground">{summary}</p>}

      {(acceptedAt || hash) && (
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-muted-foreground">
          {acceptedAt && (
            <span>{t("goals.outputAcceptedAt", { time: formatTime(acceptedAt) })}</span>
          )}
          {acceptedAt && hash && <MetaSep />}
          {hash && (
            <span className="font-mono">
              {t("goals.outputHash")} {hash.slice(0, 12)}
            </span>
          )}
        </div>
      )}

      {artifacts.length > 0 && (
        <div>
          <div className="mb-1.5 font-mono text-[10.5px] font-semibold uppercase tracking-wide text-muted-foreground">
            {t("goals.outputArtifacts")}
          </div>
          <ul className="space-y-1">
            {artifacts.map((a, i) => {
              const art = (a ?? {}) as Record<string, unknown>;
              const uri = str(art.uri);
              return (
                <li
                  key={i}
                  className="flex flex-wrap items-center gap-x-2 text-[11.5px] text-muted-foreground"
                >
                  <span className="font-mono text-foreground">{str(art.kind) || "artifact"}</span>
                  {uri && <span className="break-all font-mono text-[11px]">{uri}</span>}
                </li>
              );
            })}
          </ul>
        </div>
      )}

      {hasResult && (
        <div>
          <div className="mb-1.5 font-mono text-[10.5px] font-semibold uppercase tracking-wide text-muted-foreground">
            {t("goals.outputResult")}
          </div>
          <div className="rounded-xl border border-border bg-muted/40 p-3.5">
            <JsonView value={result} />
          </div>
        </div>
      )}
    </div>
  );
}

// Payload keys are agent-authored data fields. Common deliverable field names
// get a localized label; everything else humanizes the raw key (snake/kebab to
// spaced, capitalized) since arbitrary agent keys can't all go through i18n.
const FIELD_LABEL_KEY: Record<string, MessageKey> = {
  report: "goals.fieldReport",
  material: "goals.fieldMaterial",
  materials: "goals.fieldMaterial",
  result: "goals.fieldResult",
  results: "goals.fieldResult",
  summary: "goals.fieldSummary",
  analysis: "goals.fieldAnalysis",
  conclusion: "goals.fieldConclusion",
  conclusions: "goals.fieldConclusion",
  recommendation: "goals.fieldRecommendation",
  recommendations: "goals.fieldRecommendation",
  findings: "goals.fieldFindings",
  content: "goals.fieldContent",
  notes: "goals.fieldNotes",
  plan: "goals.fieldPlan",
  data: "goals.fieldData",
  details: "goals.fieldDetails",
};

function humanizeKey(key: string) {
  const s = key.replace(/[_-]+/g, " ").trim();
  return s.charAt(0).toUpperCase() + s.slice(1);
}

// JsonView renders arbitrary JSON as readable structure: objects as labelled
// rows, arrays as stacked cards, primitives as plain text — no raw dump.
function JsonView({ value }: { value: unknown }) {
  const { t } = useI18n();
  if (value === null || value === undefined) {
    return <span className="text-muted-foreground">—</span>;
  }
  if (Array.isArray(value)) {
    if (value.length === 0) return <span className="text-muted-foreground">[]</span>;
    return (
      <ul className="space-y-1.5">
        {value.map((v, i) => (
          <li key={i} className="rounded-lg border border-border bg-background px-2.5 py-1.5">
            <JsonView value={v} />
          </li>
        ))}
      </ul>
    );
  }
  if (typeof value === "object") {
    const entries = Object.entries(value as Record<string, unknown>);
    if (entries.length === 0) return <span className="text-muted-foreground">{"{}"}</span>;
    return (
      <dl className="space-y-1.5">
        {entries.map(([k, v]) => (
          <div key={k} className="flex flex-col gap-0.5 sm:flex-row sm:gap-3">
            <dt className="shrink-0 text-[11.5px] font-medium text-muted-foreground sm:w-36">
              {FIELD_LABEL_KEY[k.toLowerCase()]
                ? t(FIELD_LABEL_KEY[k.toLowerCase()])
                : humanizeKey(k)}
            </dt>
            <dd className="min-w-0 flex-1 text-[12.5px] text-foreground">
              <JsonView value={v} />
            </dd>
          </div>
        ))}
      </dl>
    );
  }
  if (typeof value === "string") {
    // Agent prose (reports, analyses) arrives as one string with newlines and
    // markdown; render it as markdown so headings/tables/lists are readable
    // instead of a raw wall of text. Short single-line values stay plain.
    if (value.includes("\n")) {
      return <MarkdownPreview content={value} className="text-[12.5px]" />;
    }
    return <span className="break-words text-[12.5px] text-foreground">{value}</span>;
  }
  return <span className="break-words text-[12.5px] text-foreground">{JSON.stringify(value)}</span>;
}

// ── Shared ───────────────────────────────────────────────────────────

function Meta({ label, value }: { label: string; value: string }) {
  return (
    <div className="py-2">
      <div className="font-mono text-[10.5px] uppercase tracking-wide text-muted-foreground">
        {label}
      </div>
      <div className="mt-0.5 text-[13px] text-foreground">{value}</div>
    </div>
  );
}

function Empty({ text }: { text: string }) {
  return <p className="py-8 text-center text-sm text-muted-foreground">{text}</p>;
}

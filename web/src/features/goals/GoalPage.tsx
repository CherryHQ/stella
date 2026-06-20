import { useCallback, useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams, useSearch } from "@tanstack/react-router";
import {
  abandonGoal,
  acceptRevision,
  activateGoal,
  approveRevision,
  cancelGoal,
  deleteGoal,
  materializeRevision,
  reattemptGoal,
  rejectRevision,
  requestChangesRevision,
  startDecomposition,
  submitRevisionReview,
  submitVerdict,
  unarchiveGoal,
  waiveEdge,
} from "@/lib/api-client";
import type {
  ComponentsAcceptanceContract,
  ComponentsAcceptanceEvent,
  ComponentsAttempt,
  ComponentsGoal,
  ComponentsEdge,
  ComponentsProposedEdge,
  ComponentsReadiness,
  ComponentsRevision,
} from "@/lib/api-client/types.gen";
import {
  goalAttemptsOptions,
  goalChildrenOptions,
  goalEdgesOptions,
  goalEventsOptions,
  goalOptions,
  goalReadinessOptions,
  goalRevisionsOptions,
} from "@/lib/queries/goals";
import { useI18n } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/messages";
import { formatTime } from "@/lib/time";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
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

type TabKey = "overview" | "children" | "attempts" | "acceptance" | "deps" | "plan";

export function GoalPage() {
  const { t } = useI18n();
  const { agentId, goalId } = useParams({ strict: false }) as {
    agentId: string;
    goalId: string;
  };
  const { tab } = useSearch({ strict: false }) as { tab?: TabKey };
  const navigate = useNavigate();
  const qc = useQueryClient();
  const [acting, setActing] = useState(false);

  const { data: d, isError } = useQuery(goalOptions(goalId));

  const invalidate = useCallback(() => {
    void qc.invalidateQueries({ queryKey: ["goal", goalId] });
    void qc.invalidateQueries({ queryKey: ["goal-children", goalId] });
    void qc.invalidateQueries({ queryKey: ["goal-attempts", goalId] });
    void qc.invalidateQueries({ queryKey: ["goal-events", goalId] });
    void qc.invalidateQueries({ queryKey: ["goal-edges", goalId] });
    void qc.invalidateQueries({ queryKey: ["goal-revisions", goalId] });
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
      } finally {
        setActing(false);
      }
    },
    [invalidate],
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
  const active = tab ?? "overview";

  return (
    <DetailShell
      agentId={agentId}
      kindLabel={t(isComposite ? "goals.kindComposite" : "goals.kindLeaf")}
      title={d.title}
      pill={<StatusPill status={displayStatus(d)} label={goalStatusLabel(t, d)} />}
      actions={
        <HeaderActions
          d={d}
          agentId={agentId}
          acting={acting}
          act={act}
          path={path}
          onVerdict={() => goTab("acceptance")}
          onWaive={() => goTab("deps")}
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
          <TabsTab value="overview">{t("goals.tabOverview")}</TabsTab>
          {isComposite && <TabsTab value="children">{t("goals.tabChildren")}</TabsTab>}
          {!isComposite && <TabsTab value="attempts">{t("goals.tabAttempts")}</TabsTab>}
          <TabsTab value="acceptance">{t("goals.tabAcceptance")}</TabsTab>
          {!isComposite && <TabsTab value="deps">{t("goals.tabDeps")}</TabsTab>}
          {isComposite && <TabsTab value="plan">{t("goals.tabRevisions")}</TabsTab>}
        </TabsList>

        <TabsPanel value="overview" className="mt-5">
          <OverviewTab
            d={d}
            acting={acting}
            act={act}
            onVerdict={() => goTab("acceptance")}
            onWaive={() => goTab("deps")}
            onPlan={() => goTab("plan")}
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
            <PlanTab d={d} acting={acting} act={act} />
          </TabsPanel>
        )}
      </Tabs>
    </DetailShell>
  );
}

// ── Header actions ───────────────────────────────────────────────────

type ActRun = (fn: () => Promise<unknown>) => Promise<void>;

function HeaderActions({
  d,
  agentId,
  acting,
  act,
  path,
  onVerdict,
  onWaive,
}: {
  d: ComponentsGoal;
  agentId: string;
  acting: boolean;
  act: ActRun;
  path: { id: string };
  onVerdict: () => void;
  onWaive: () => void;
}) {
  const { t } = useI18n();
  const archived = !!d.archived_at;
  const lc = d.lifecycle;
  const reason = d.block_reason;
  const isComposite = d.kind === "composite";
  const canActivate = lc === "draft" && (!isComposite || !!d.accepted_revision_id);

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
      {d.active_attempt_id && (lc === "active" || lc === "ready") && (
        <span className="inline-flex items-center gap-1.5 self-center font-mono text-xs text-chart-2">
          <span className="size-1.5 animate-pulse rounded-full bg-chart-2" />
          {t("goals.attemptRunning")}
        </span>
      )}

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

      {lc === "blocked" && reason === "budget_exhausted" && (
        <>
          <Button
            size="sm"
            loading={acting}
            onClick={() => act(() => reattemptGoal({ path, throwOnError: true }))}
          >
            {t("goals.reattempt")}
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

      {lc === "blocked" && reason === "dep" && (
        <Button variant="outline" size="sm" onClick={onWaive}>
          {t("goals.waive")}
        </Button>
      )}

      {d.session_id && (
        <Button
          render={
            <Link
              to="/agents/$agentId/sessions/$sessionId"
              params={{ agentId: d.agent_id || agentId, sessionId: d.session_id }}
            />
          }
          variant="outline"
          size="sm"
        >
          {t("goals.openSession")}
        </Button>
      )}

      {!archived && ["accepted", "rejected_final", "abandoned", "cancelled"].includes(lc) && (
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

      {["draft", "ready", "active"].includes(lc) && cancelBtn}
      {lc === "blocked" && cancelBtn}
    </>
  );
}

// ── Overview ─────────────────────────────────────────────────────────

function OverviewTab({
  d,
  acting,
  act,
  onVerdict,
  onWaive,
  onPlan,
}: {
  d: ComponentsGoal;
  acting: boolean;
  act: ActRun;
  onVerdict: () => void;
  onWaive: () => void;
  onPlan: () => void;
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
            {d.block_reason === "budget_exhausted" && (
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
            {d.block_reason === "dep" && (
              <Button variant="outline" size="sm" className="mt-3" onClick={onWaive}>
                {t("goals.waive")}
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

      {d.accepted_output && (
        <DetailSection title={t("goals.outputTitle")}>
          <pre className="max-h-72 overflow-auto rounded-xl border border-border bg-muted/40 p-3.5 text-[11.5px] leading-relaxed text-muted-foreground">
            {JSON.stringify(d.accepted_output, null, 2)}
          </pre>
        </DetailSection>
      )}
    </div>
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
              accepted: d.required_accepted ?? r.accepted,
              total: d.required_total ?? r.total,
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
        <li key={a.id} className="rounded-xl border border-border bg-background p-3.5">
          <div className="flex items-center justify-between">
            <span className="text-sm font-medium">{t("goals.attemptNo", { n: a.attempt_no })}</span>
            <span className="font-mono text-xs text-muted-foreground">
              {t(PURPOSE_KEY[a.purpose] ?? "goals.purposeExecution")}
            </span>
          </div>
          <div className="mt-1.5 flex items-center justify-between font-mono text-[10.5px] text-muted-foreground">
            <span>{t(ATTEMPT_STATUS_KEY[a.status] ?? "goals.attemptQueued")}</span>
            <span>{formatTime(a.finished_at ?? a.started_at ?? a.created_at)}</span>
          </div>
          {a.error && <p className="mt-2 text-[12px] text-destructive">{a.error}</p>}
          {a.gaps && Object.keys(a.gaps).length > 0 && (
            <div className="mt-2">
              <div className="mb-1 font-mono text-[10.5px] font-semibold text-muted-foreground">
                {t("goals.attemptGaps")}
              </div>
              <pre className="max-h-40 overflow-auto rounded-lg border border-border bg-muted/40 px-3 py-2 text-[11px] text-muted-foreground">
                {JSON.stringify(a.gaps, null, 2)}
              </pre>
            </div>
          )}
        </li>
      ))}
    </ul>
  );
}

// ── Acceptance ───────────────────────────────────────────────────────

function passingItemIds(events: ComponentsAcceptanceEvent[]): Set<string> {
  const ids = new Set<string>();
  for (const ev of events) if (ev.result === "pass") ids.add(ev.item_id);
  return ids;
}

// The pending human judgment is the first required judgment item whose authority
// is human that has no passing event yet; fall back to the first human-judgment
// item so the form always has a target while a verdict is pending.
function pendingVerdictItemId(
  d: ComponentsGoal,
  events: ComponentsAcceptanceEvent[],
): string | null {
  const items = d.acceptance_contract?.items ?? [];
  const human = items.filter((it) => it.kind === "judgment" && it.authority === "human");
  if (human.length === 0) return null;
  const passed = passingItemIds(events);
  return (human.find((it) => !passed.has(it.id)) ?? human[0]).id;
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

const REV_STATUS_KEY: Record<ComponentsRevision["status"], MessageKey> = {
  draft: "goals.revStatusDraft",
  in_review: "goals.revStatusInReview",
  accepted: "goals.revStatusAccepted",
  rejected: "goals.revStatusRejected",
  superseded: "goals.revStatusSuperseded",
};

const EDGE_ON_FAILURE_KEY: Record<NonNullable<ComponentsProposedEdge["on_failure"]>, MessageKey> = {
  block: "goals.onFailureBlock",
  fail: "goals.onFailureFail",
  ignore: "goals.onFailureIgnore",
};

function PlanTab({ d, acting, act }: { d: ComponentsGoal; acting: boolean; act: ActRun }) {
  const { t } = useI18n();
  const { data: revisions = [] } = useQuery(goalRevisionsOptions(d.id));
  const newest = useMemo(
    () => [...revisions].sort((a, b) => b.revision_no - a.revision_no)[0],
    [revisions],
  );

  if (revisions.length === 0) {
    return (
      <div className="space-y-4">
        <Empty text={t("goals.noRevisions")} />
        <div className="text-center">
          <Button
            size="sm"
            loading={acting}
            onClick={() =>
              act(() => startDecomposition({ path: { id: d.id }, throwOnError: true }))
            }
          >
            {t("goals.startDecomposition")}
          </Button>
        </div>
      </div>
    );
  }

  return (
    <div className="space-y-4">
      {newest && (
        <div className="flex flex-wrap gap-2">
          <RevisionActions d={d} rev={newest} acting={acting} act={act} />
        </div>
      )}
      <ul className="space-y-2">
        {[...revisions]
          .sort((a, b) => b.revision_no - a.revision_no)
          .map((rev) => (
            <RevisionItem key={rev.id} rev={rev} />
          ))}
      </ul>
    </div>
  );
}

function RevisionItem({ rev }: { rev: ComponentsRevision }) {
  const { t } = useI18n();
  const children = rev.content?.children ?? [];
  const edges = rev.content?.edges ?? [];
  const [open, setOpen] = useState(false);
  const canExpand = children.length > 0;
  // Edges reference children by their stable `key`; show the human title instead
  // (falling back to the key if a child was dropped from the revision).
  const titleOf = (key: string) => children.find((c) => c.key === key)?.title ?? key;

  return (
    <li className="rounded-xl border border-border bg-background">
      <button
        type="button"
        disabled={!canExpand}
        onClick={() => setOpen((v) => !v)}
        className="flex w-full items-center justify-between gap-3 px-3.5 py-3 text-left disabled:cursor-default"
      >
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
          <span className="text-sm font-medium">
            {t("goals.revisionNo", { n: rev.revision_no })}
          </span>
          <span className="font-mono text-[10.5px] text-muted-foreground">
            {t("goals.planChildren", { count: children.length })}
          </span>
        </span>
        <span className="shrink-0 font-mono text-xs text-muted-foreground">
          {t(REV_STATUS_KEY[rev.status] ?? "goals.revStatusDraft")}
        </span>
      </button>
      {open && canExpand && (
        <div className="space-y-3 border-t border-border px-3.5 py-3">
          <ul className="space-y-2">
            {children.map((c) => (
              <li key={c.key} className="rounded-lg border border-border bg-muted/30 px-3 py-2">
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
                {c.acceptance_contract && (
                  <AcceptanceContractView contract={c.acceptance_contract} />
                )}
              </li>
            ))}
          </ul>
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
      )}
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

function RevisionActions({
  d,
  rev,
  acting,
  act,
}: {
  d: ComponentsGoal;
  rev: ComponentsRevision;
  acting: boolean;
  act: ActRun;
}) {
  const { t } = useI18n();
  const path = { id: d.id, revId: rev.id };

  if (rev.status === "draft") {
    return rev.review_policy === "human" ? (
      <Button
        size="sm"
        loading={acting}
        onClick={() => act(() => submitRevisionReview({ path, throwOnError: true }))}
      >
        {t("goals.revSubmitReview")}
      </Button>
    ) : (
      <Button
        size="sm"
        loading={acting}
        onClick={() => act(() => acceptRevision({ path, throwOnError: true }))}
      >
        {t("goals.revAccept")}
      </Button>
    );
  }

  if (rev.status === "in_review") {
    return (
      <>
        <Button
          size="sm"
          loading={acting}
          onClick={() => act(() => approveRevision({ path, body: {}, throwOnError: true }))}
        >
          {t("goals.revApprove")}
        </Button>
        <Button
          size="sm"
          variant="outline"
          loading={acting}
          onClick={() => act(() => requestChangesRevision({ path, body: {}, throwOnError: true }))}
        >
          {t("goals.revRequestChanges")}
        </Button>
        <Button
          size="sm"
          variant="ghost"
          loading={acting}
          className="text-destructive"
          onClick={() => act(() => rejectRevision({ path, body: {}, throwOnError: true }))}
        >
          {t("goals.revReject")}
        </Button>
      </>
    );
  }

  if (rev.status === "accepted" && !rev.materialized_at) {
    return (
      <Button
        size="sm"
        loading={acting}
        onClick={() => act(() => materializeRevision({ path, throwOnError: true }))}
      >
        {t("goals.revMaterialize")}
      </Button>
    );
  }

  return null;
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

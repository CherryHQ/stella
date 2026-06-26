import { useCallback, useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { deleteWorkflow, instantiateWorkflow, updateWorkflow } from "@/lib/api-client";
import type { ComponentsGoal } from "@/lib/api-client/types.gen";
import { workflowOptions } from "@/lib/queries/workflows";
import { useI18n } from "@/lib/i18n";
import { formatTime } from "@/lib/time";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { useToast, ToastContainer } from "@/hooks/use-toast";
import { AgentChip, DetailSection, MetaSep } from "@/features/goals/DetailShell";
import { asFrozenPlan, planStats, WorkflowDetailShell } from "@/features/workflows/lib";
import { PlanTree } from "@/features/workflows/PlanTree";
import { ScheduleWorkflowDialog } from "@/features/workflows/ScheduleWorkflowDialog";

export function WorkflowPage() {
  const { t } = useI18n();
  const { agentId, workflowId } = useParams({ strict: false }) as {
    agentId: string;
    workflowId: string;
  };
  const navigate = useNavigate();
  const qc = useQueryClient();
  const { toasts, showToast } = useToast();

  const { data: w, isError } = useQuery(workflowOptions(workflowId));
  const [acting, setActing] = useState(false);
  const [editing, setEditing] = useState(false);
  const [scheduleOpen, setScheduleOpen] = useState(false);
  const [draftName, setDraftName] = useState("");
  const [draftIntent, setDraftIntent] = useState("");

  const plan = useMemo(() => asFrozenPlan(w?.plan), [w?.plan]);
  const stats = useMemo(() => planStats(plan), [plan]);

  const invalidate = useCallback(() => {
    void qc.invalidateQueries({ queryKey: ["workflow", workflowId] });
    void qc.invalidateQueries({ queryKey: ["workflows-page"] });
  }, [qc, workflowId]);

  const handleRun = useCallback(async () => {
    setActing(true);
    try {
      const { data } = await instantiateWorkflow({
        path: { id: workflowId },
        body: {},
        throwOnError: true,
      });
      const goal = data as ComponentsGoal;
      showToast(t("workflows.runStarted"));
      void navigate({
        to: "/agents/$agentId/goals/$goalId",
        params: { agentId, goalId: goal.id },
      });
    } catch (e) {
      showToast(e instanceof Error ? e.message : t("workflows.runFailed"), "error");
    } finally {
      setActing(false);
    }
  }, [workflowId, agentId, navigate, showToast, t]);

  const handleDelete = useCallback(async () => {
    setActing(true);
    try {
      await deleteWorkflow({ path: { id: workflowId }, throwOnError: true });
      invalidate();
      void navigate({ to: "/agents/$agentId/workflows", params: { agentId } });
    } catch (e) {
      showToast(e instanceof Error ? e.message : t("workflows.deleteFailed"), "error");
      setActing(false);
    }
  }, [workflowId, agentId, invalidate, navigate, showToast, t]);

  const startEdit = useCallback(() => {
    if (!w) return;
    setDraftName(w.name);
    setDraftIntent(w.intent ?? "");
    setEditing(true);
  }, [w]);

  const saveEdit = useCallback(async () => {
    setActing(true);
    try {
      await updateWorkflow({
        path: { id: workflowId },
        body: { name: draftName, intent: draftIntent },
        throwOnError: true,
      });
      invalidate();
      setEditing(false);
    } catch (e) {
      showToast(e instanceof Error ? e.message : t("workflows.saveFailed"), "error");
    } finally {
      setActing(false);
    }
  }, [workflowId, draftName, draftIntent, invalidate, showToast, t]);

  if (isError) {
    return (
      <WorkflowDetailShell agentId={agentId} title={t("workflows.notFound")}>
        <div />
      </WorkflowDetailShell>
    );
  }
  if (!w) return null;

  return (
    <WorkflowDetailShell
      agentId={agentId}
      title={
        editing ? (
          <Input
            value={draftName}
            onChange={(e) => setDraftName(e.target.value)}
            className="w-80"
            nativeInput
          />
        ) : (
          w.name
        )
      }
      pill={
        <Badge size="sm" variant="secondary">
          {t("workflows.versionLabel", { n: w.version })}
        </Badge>
      }
      actions={
        editing ? (
          <>
            <Button variant="outline" size="sm" onClick={() => setEditing(false)}>
              {t("common.cancel")}
            </Button>
            <Button size="sm" disabled={!draftName} loading={acting} onClick={saveEdit}>
              {t("common.save")}
            </Button>
          </>
        ) : (
          <>
            <Button size="sm" loading={acting} onClick={handleRun}>
              {t("workflows.run")}
            </Button>
            <Button variant="outline" size="sm" onClick={() => setScheduleOpen(true)}>
              {t("workflows.schedule")}
            </Button>
            <Button variant="outline" size="sm" onClick={startEdit}>
              {t("common.edit")}
            </Button>
            <Button
              variant="outline"
              size="sm"
              loading={acting}
              onClick={handleDelete}
              className="text-destructive hover:text-destructive"
            >
              {t("common.delete")}
            </Button>
          </>
        )
      }
    >
      <div className="mt-2.5 flex flex-wrap items-center gap-x-3.5 gap-y-1.5 text-[12.5px] text-muted-foreground">
        <AgentChip agentId={w.agent_id || agentId} />
        <MetaSep />
        <span>
          {t("workflows.planSummary", {
            nodes: stats.nodes,
            leaves: stats.leaves,
            composites: stats.composites,
          })}
        </span>
        {stats.semiFrozen > 0 && (
          <>
            <MetaSep />
            <span>{t("workflows.semiFrozenCount", { n: stats.semiFrozen })}</span>
          </>
        )}
        <MetaSep />
        <span>{t("hub.createdAt", { time: formatTime(w.created_at) })}</span>
        <MetaSep />
        <span>{t("hub.updatedAt", { time: formatTime(w.updated_at) })}</span>
        {w.source_goal_id && (
          <>
            <MetaSep />
            <Link
              to="/agents/$agentId/goals/$goalId"
              params={{ agentId, goalId: w.source_goal_id }}
              className="font-medium text-primary hover:underline"
            >
              {t("workflows.sourceGoal")} →
            </Link>
          </>
        )}
      </div>

      <DetailSection title={t("workflows.intentSection")}>
        {editing ? (
          <Textarea
            value={draftIntent}
            onChange={(e) => setDraftIntent(e.target.value)}
            placeholder={t("workflows.intentPlaceholder")}
            className="min-h-24"
          />
        ) : w.intent ? (
          <p className="whitespace-pre-wrap text-[13px] leading-relaxed text-muted-foreground">
            {w.intent}
          </p>
        ) : (
          <p className="text-[13px] text-muted-foreground">{t("workflows.noIntent")}</p>
        )}
      </DetailSection>

      <DetailSection title={t("workflows.planSection")}>
        {plan && plan.children.length > 0 ? (
          <PlanTree plan={plan} />
        ) : (
          <p className="text-[13px] text-muted-foreground">{t("workflows.emptyPlan")}</p>
        )}
      </DetailSection>

      {w.acceptance_contract && Object.keys(w.acceptance_contract).length > 0 && (
        <DetailSection title={t("workflows.contractSection")}>
          <pre className="overflow-x-auto rounded-xl border border-border bg-muted/40 px-4 py-3 font-mono text-xs text-muted-foreground">
            {JSON.stringify(w.acceptance_contract, null, 2)}
          </pre>
        </DetailSection>
      )}

      <ScheduleWorkflowDialog
        open={scheduleOpen}
        onOpenChange={setScheduleOpen}
        workflowId={workflowId}
        defaultName={w.name}
        onScheduled={() => showToast(t("workflows.scheduled"))}
      />
      <ToastContainer messages={toasts} />
    </WorkflowDetailShell>
  );
}

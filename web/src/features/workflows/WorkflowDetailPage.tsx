import { useEffect, useMemo, useState, type FormEvent, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { Badge } from "@/components/ui/badge";
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
import { Field, FieldDescription, FieldError, FieldLabel } from "@/components/ui/field";
import { Form } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { ConfirmDialog } from "@/features/settings/ConfirmDialog";
import { ToastContainer, useToast } from "@/hooks/use-toast";
import { useAppShell } from "@/layouts/AppShell";
import { deleteWorkflow, instantiateWorkflow } from "@/lib/api-client";
import type {
  ComponentsProposedChild,
  ComponentsProposedEdge,
  Workflow,
  WorkflowRun,
} from "@/lib/api-client/types.gen";
import { apiErrorMessage } from "@/lib/api-error";
import { useI18n } from "@/lib/i18n";
import { workflowOptions, workflowRunsOptions } from "@/lib/queries/workflows";
import { formatTime } from "@/lib/time";

type FrozenPlan = {
  children?: FrozenNode[];
  edges?: ComponentsProposedEdge[];
};

type FrozenNode = {
  child?: ComponentsProposedChild;
  plan?: FrozenPlan | null;
};

function asFrozenPlan(value: Workflow["payload"]): FrozenPlan {
  return value as FrozenPlan;
}

export function WorkflowDetailPage() {
  const { t } = useI18n();
  const { agentId, workflowId } = useParams({ strict: false }) as {
    agentId: string;
    workflowId: string;
  };
  const navigate = useNavigate();
  const qc = useQueryClient();
  const { setHeaderTitle, setHeaderActions } = useAppShell();
  const { toasts, showToast } = useToast();
  const { data: workflow, isLoading } = useQuery(workflowOptions(workflowId));
  const { data: runs = [] } = useQuery(workflowRunsOptions(workflowId, 10));
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [deleting, setDeleting] = useState(false);

  useEffect(() => {
    setHeaderTitle(
      <div className="min-w-0">
        <div className="truncate font-mono text-xs font-semibold text-muted-foreground">
          {t("workflows.title")}
        </div>
        <h1 className="truncate text-[15px] font-semibold tracking-[-0.01em]">
          {workflow?.name ?? t("common.loading")}
        </h1>
      </div>,
    );
    setHeaderActions(null);
    return () => setHeaderActions(null);
  }, [setHeaderActions, setHeaderTitle, t, workflow?.name]);

  const deleteCurrentWorkflow = async () => {
    if (!workflow || deleting) return;
    setDeleting(true);
    try {
      await deleteWorkflow({ path: { id: workflow.id }, throwOnError: true });
      await qc.invalidateQueries({ queryKey: ["workflows", agentId] });
      await qc.invalidateQueries({ queryKey: ["workflow", workflow.id] });
      showToast(t("workflows.deleteSuccess"));
      void navigate({ to: "/agents/$agentId/workflows", params: { agentId } });
    } catch (error) {
      showToast(apiErrorMessage(error, t("workflows.deleteFailed")), "error");
    } finally {
      setDeleting(false);
    }
  };

  if (isLoading) {
    return <div className="p-6 text-sm text-muted-foreground">{t("common.loading")}</div>;
  }
  if (!workflow) {
    return <div className="p-6 text-sm text-muted-foreground">{t("workflows.notFound")}</div>;
  }

  return (
    <div className="h-full min-h-0 overflow-y-auto bg-background">
      <div className="mx-auto flex max-w-[1100px] flex-col gap-6 px-6 py-7 pb-20 sm:px-8">
        <header className="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2">
              <h2 className="truncate text-xl font-semibold">{workflow.name}</h2>
              <Badge variant="secondary">
                {t("workflows.version", { version: workflow.version })}
              </Badge>
              <Badge variant={workflow.fully_frozen ? "success" : "warning"}>
                {t(workflow.fully_frozen ? "workflows.fullyFrozen" : "workflows.partlyFrozen")}
              </Badge>
            </div>
            <div className="mt-2 flex flex-wrap gap-2 text-sm text-muted-foreground">
              <span>{t("workflows.createdAt", { time: formatTime(workflow.created_at) })}</span>
              <span>·</span>
              <span>{t("workflows.ownerScope", { scope: workflow.owner_kind })}</span>
              {workflow.source_goal_id && (
                <>
                  <span>·</span>
                  <Link
                    to="/agents/$agentId/goals/$goalId"
                    params={{ agentId, goalId: workflow.source_goal_id }}
                    className="font-medium text-primary hover:underline"
                  >
                    {t("workflows.sourceGoal")}
                  </Link>
                </>
              )}
            </div>
          </div>
          <div className="flex gap-2">
            <RunWorkflowDialog
              workflow={workflow}
              agentId={agentId}
              onSuccess={(rootGoalId) => {
                void qc.invalidateQueries({ queryKey: ["workflow-runs", workflow.id] });
                showToast(t("workflows.runSuccess"));
                if (rootGoalId) {
                  void navigate({
                    to: "/agents/$agentId/goals/$goalId",
                    params: { agentId, goalId: rootGoalId },
                  });
                }
              }}
              onError={(message) => showToast(message, "error")}
            />
            <Button
              variant="destructive"
              size="sm"
              loading={deleting}
              onClick={() => setDeleteOpen(true)}
            >
              {t("common.delete")}
            </Button>
          </div>
        </header>

        <Section title={t("workflows.inputsSection")}>
          {workflow.inputs.length ? (
            <InputsTable workflow={workflow} />
          ) : (
            <Empty text={t("workflows.noInputs")} />
          )}
        </Section>

        <Section title={t("workflows.planSection")}>
          <FrozenPlanView plan={asFrozenPlan(workflow.payload)} />
        </Section>

        <Section title={t("workflows.runsTitle")}>
          <div className="mb-3">
            <Button
              variant="outline"
              size="sm"
              render={
                <Link
                  to="/agents/$agentId/goals/all"
                  params={{ agentId }}
                  search={{ workflow_id: workflow.id }}
                />
              }
            >
              {t("workflows.viewAllRuns")}
            </Button>
          </div>
          {runs.length ? (
            <RunsTable agentId={agentId} runs={runs} />
          ) : (
            <Empty text={t("workflows.noRuns")} />
          )}
        </Section>
      </div>
      <ConfirmDialog
        open={deleteOpen}
        onOpenChange={setDeleteOpen}
        title={t("workflows.deleteConfirm")}
        message={t("workflows.deleteConfirmDesc", { name: workflow.name })}
        onConfirm={deleteCurrentWorkflow}
        confirmLabel={t("common.delete")}
      />
      <ToastContainer messages={toasts} />
    </div>
  );
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="flex flex-col gap-3">
      <h3 className="text-lg font-semibold">{title}</h3>
      {children}
    </section>
  );
}

function InputsTable({ workflow }: { workflow: Workflow }) {
  const { t } = useI18n();
  return (
    <Table variant="card">
      <TableHeader>
        <TableRow>
          <TableHead>{t("workflows.inputName")}</TableHead>
          <TableHead>{t("workflows.inputRequired")}</TableHead>
          <TableHead>{t("workflows.inputDefault")}</TableHead>
          <TableHead>{t("workflows.inputDescription")}</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {workflow.inputs.map((input) => (
          <TableRow key={input.name}>
            <TableCell>{input.name}</TableCell>
            <TableCell>{input.required ? t("common.yes") : t("common.no")}</TableCell>
            <TableCell>
              {input.default || <span className="text-muted-foreground">{t("common.noData")}</span>}
            </TableCell>
            <TableCell>
              {input.description || (
                <span className="text-muted-foreground">{t("common.noData")}</span>
              )}
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}

function FrozenPlanView({ plan, depth = 0 }: { plan: FrozenPlan; depth?: number }) {
  const { t } = useI18n();
  const children = plan.children ?? [];
  const edges = plan.edges ?? [];
  if (!children.length) return <Empty text={t("workflows.planEmpty")} />;
  return (
    <div className="flex flex-col gap-3">
      <ul className="flex flex-col gap-3">
        {children.map((node, index) => {
          const child = node.child;
          if (!child) return null;
          const kind = child.kind ?? "leaf";
          return (
            <li key={child.key || index} className={`flex flex-col gap-2 ${depth ? "pl-4" : ""}`}>
              <div className="flex flex-wrap items-center gap-2">
                <span className="font-medium">{child.title}</span>
                <Badge variant="outline">{kind}</Badge>
                {child.required !== false && (
                  <Badge variant="secondary">{t("workflows.requiredMarker")}</Badge>
                )}
              </div>
              {kind === "composite" && !node.plan && (
                <p className="text-sm text-muted-foreground">{t("workflows.replannedAtRuntime")}</p>
              )}
              {kind === "composite" && node.plan && (
                <FrozenPlanView plan={node.plan} depth={depth + 1} />
              )}
            </li>
          );
        })}
      </ul>
      {edges.length > 0 && (
        <div className="flex flex-wrap gap-2 text-sm text-muted-foreground">
          {edges.map((edge, index) => (
            <span key={`${edge.downstream_key}-${edge.upstream_key}-${index}`}>
              {edge.downstream_key} ← {edge.upstream_key}
              {edge.kind ? ` · ${edge.kind}` : ""}
              {edge.on_failure ? ` · ${edge.on_failure}` : ""}
            </span>
          ))}
        </div>
      )}
    </div>
  );
}

function RunsTable({ agentId, runs }: { agentId: string; runs: WorkflowRun[] }) {
  const { t } = useI18n();
  return (
    <Table variant="card">
      <TableHeader>
        <TableRow>
          <TableHead>{t("workflows.colStatus")}</TableHead>
          <TableHead>{t("workflows.colCreated")}</TableHead>
          <TableHead>{t("workflows.colRootGoal")}</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {runs.map((run, index) => (
          <TableRow key={run.id}>
            <TableCell>
              <Badge
                variant={
                  run.status === "done"
                    ? "success"
                    : run.status === "failed"
                      ? "destructive"
                      : "info"
                }
              >
                {t(`workflows.runStatus.${run.status}` as const)}
              </Badge>
            </TableCell>
            <TableCell>{formatTime(run.created_at)}</TableCell>
            <TableCell>
              {run.root_goal_id ? (
                <Button
                  variant="link"
                  size="sm"
                  render={
                    <Link
                      to="/agents/$agentId/goals/$goalId"
                      params={{ agentId, goalId: run.root_goal_id }}
                    />
                  }
                >
                  {t("workflows.runNumber", { n: runs.length - index })}
                </Button>
              ) : (
                <span className="text-muted-foreground">{t("common.noData")}</span>
              )}
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}

function RunWorkflowDialog({
  workflow,
  agentId: _agentId,
  onSuccess,
  onError,
}: {
  workflow: Workflow;
  agentId: string;
  onSuccess: (rootGoalId?: string | null) => void;
  onError: (message: string) => void;
}) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const [pending, setPending] = useState(false);
  // Stable per dialog-open so a retry after a lost response resumes the same
  // run instead of minting a second goal tree.
  const [idemKey, setIdemKey] = useState(() => crypto.randomUUID());
  const [values, setValues] = useState<Record<string, string>>(() =>
    Object.fromEntries(workflow.inputs.map((input) => [input.name, input.default ?? ""])),
  );
  const requiredErrors = useMemo(
    () =>
      Object.fromEntries(
        workflow.inputs.map((input) => [
          input.name,
          input.required && !values[input.name]?.trim() ? t("workflows.inputValueRequired") : "",
        ]),
      ),
    [t, values, workflow.inputs],
  );
  const hasErrors = Object.values(requiredErrors).some(Boolean);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (hasErrors || pending) return;
    setPending(true);
    try {
      const { data } = await instantiateWorkflow({
        path: { id: workflow.id },
        body: { inputs: values, idempotency_key: idemKey },
        throwOnError: true,
      });
      setOpen(false);
      onSuccess(data?.root_goal_id);
    } catch (error) {
      onError(apiErrorMessage(error, t("workflows.runFailed")));
    } finally {
      setPending(false);
    }
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (next) setIdemKey(crypto.randomUUID());
        setOpen(next);
      }}
    >
      <DialogTrigger render={<Button size="sm" />}>{t("workflows.run")}</DialogTrigger>
      <DialogPopup>
        <DialogHeader>
          <DialogTitle>{t("workflows.run")}</DialogTitle>
          <DialogDescription>{t("workflows.runDesc", { name: workflow.name })}</DialogDescription>
        </DialogHeader>
        <Form onSubmit={submit}>
          <DialogPanel className="flex flex-col gap-4">
            {workflow.inputs.length ? (
              workflow.inputs.map((input) => (
                <Field key={input.name}>
                  <FieldLabel>
                    {input.name}
                    {input.required && (
                      <Badge variant="secondary">{t("workflows.requiredMarker")}</Badge>
                    )}
                  </FieldLabel>
                  <Input
                    value={values[input.name] ?? ""}
                    placeholder={input.default ?? ""}
                    onChange={(event) =>
                      setValues((prev) => ({ ...prev, [input.name]: event.target.value }))
                    }
                  />
                  {input.description && <FieldDescription>{input.description}</FieldDescription>}
                  {requiredErrors[input.name] && (
                    <FieldError>{requiredErrors[input.name]}</FieldError>
                  )}
                </Field>
              ))
            ) : (
              <p className="text-sm text-muted-foreground">{t("workflows.noInputs")}</p>
            )}
          </DialogPanel>
          <DialogFooter>
            <Button type="button" variant="ghost" size="sm" onClick={() => setOpen(false)}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" size="sm" loading={pending} disabled={hasErrors}>
              {t("workflows.run")}
            </Button>
          </DialogFooter>
        </Form>
      </DialogPopup>
    </Dialog>
  );
}

function Empty({ text }: { text: string }) {
  return <p className="py-8 text-center text-sm text-muted-foreground">{text}</p>;
}

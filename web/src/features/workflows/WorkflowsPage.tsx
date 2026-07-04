import { useQueries, useQuery } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import { History, Workflow as WorkflowIcon } from "lucide-react";
import { useEffect } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useAppShell } from "@/layouts/AppShell";
import { useI18n } from "@/lib/i18n";
import { workflowsOptions, workflowRunsOptions } from "@/lib/queries/workflows";
import { formatTime } from "@/lib/time";

export function WorkflowsPage() {
  const { t } = useI18n();
  const { agentId } = useParams({ strict: false }) as { agentId: string };
  const { setHeaderTitle, setHeaderActions } = useAppShell();
  const { data: workflows = [], isLoading } = useQuery(workflowsOptions(agentId));
  const runQueries = useQueries({
    queries: workflows.map((workflow) => workflowRunsOptions(workflow.id, 5)),
  });

  useEffect(() => {
    setHeaderTitle(
      <h1 className="truncate text-[15px] font-semibold tracking-[-0.01em]">
        {t("workflows.title")}
      </h1>,
    );
    setHeaderActions(null);
    return () => setHeaderActions(null);
  }, [setHeaderActions, setHeaderTitle, t]);

  if (isLoading) {
    return <div className="p-6 text-sm text-muted-foreground">{t("common.loading")}</div>;
  }

  if (!workflows.length) {
    return (
      <div className="flex h-full min-h-0 items-center justify-center p-6">
        <div className="max-w-md text-center">
          <WorkflowIcon className="mx-auto size-8 text-muted-foreground" />
          <h2 className="mt-3 text-lg font-semibold">{t("workflows.empty")}</h2>
          <p className="mt-2 text-sm text-muted-foreground">{t("workflows.emptyDesc")}</p>
        </div>
      </div>
    );
  }

  return (
    <div className="h-full min-h-0 overflow-y-auto bg-background">
      <div className="mx-auto max-w-[1100px] px-6 py-7 pb-20 sm:px-8">
        <Table variant="card">
          <TableHeader>
            <TableRow>
              <TableHead>{t("workflows.colName")}</TableHead>
              <TableHead>{t("workflows.colVersion")}</TableHead>
              <TableHead>{t("workflows.colFrozen")}</TableHead>
              <TableHead>{t("workflows.colCreated")}</TableHead>
              <TableHead>{t("workflows.colSource")}</TableHead>
              <TableHead>{t("workflows.colLatestRuns")}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {workflows.map((workflow, index) => {
              const runList = runQueries[index]?.data ?? { runs: [], total: 0 };
              return (
                <TableRow key={workflow.id}>
                  <TableCell>
                    <div className="min-w-0">
                      <Button
                        variant="link"
                        size="sm"
                        render={
                          <Link
                            to="/agents/$agentId/workflows/$workflowId"
                            params={{ agentId, workflowId: workflow.id }}
                          />
                        }
                      >
                        {workflow.name}
                      </Button>
                      <div className="truncate text-xs text-muted-foreground">{workflow.id}</div>
                    </div>
                  </TableCell>
                  <TableCell>{t("workflows.version", { version: workflow.version })}</TableCell>
                  <TableCell>
                    <Badge variant={workflow.fully_frozen ? "success" : "warning"}>
                      {t(
                        workflow.fully_frozen ? "workflows.fullyFrozen" : "workflows.partlyFrozen",
                      )}
                    </Badge>
                  </TableCell>
                  <TableCell>{formatTime(workflow.created_at)}</TableCell>
                  <TableCell>
                    {workflow.source_goal_id ? (
                      <Button
                        variant="link"
                        size="sm"
                        render={
                          <Link
                            to="/agents/$agentId/goals/$goalId"
                            params={{ agentId, goalId: workflow.source_goal_id }}
                          />
                        }
                      >
                        {t("workflows.sourceGoal")}
                      </Button>
                    ) : (
                      <span className="text-muted-foreground">{t("common.noData")}</span>
                    )}
                  </TableCell>
                  <TableCell>
                    <div className="flex max-w-[260px] flex-wrap gap-1.5">
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
                        <History />
                        {t("workflows.viewRuns")}
                      </Button>
                      {runList.runs.slice(0, 3).map((run, runIndex) =>
                        run.root_goal_id ? (
                          <Button
                            key={run.id}
                            variant="link"
                            size="sm"
                            render={
                              <Link
                                to="/agents/$agentId/goals/$goalId"
                                params={{ agentId, goalId: run.root_goal_id }}
                              />
                            }
                          >
                            {t("workflows.runNumber", { n: runList.total - runIndex })}
                          </Button>
                        ) : null,
                      )}
                    </div>
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      </div>
    </div>
  );
}

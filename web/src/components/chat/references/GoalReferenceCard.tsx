import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ArrowUpRight, Target } from "lucide-react";
import { goalGraphOptions, goalOptions } from "@/lib/queries/goals";
import { useI18n } from "@/lib/i18n";
import type { RenderableReference } from "@/lib/types";
import { StatusPill, statusLabel, statusMeta } from "@/features/tasks/lib";
import { ReferenceCardShell } from "./ReferenceCardShell";

export function GoalReferenceCard({ reference }: { reference: RenderableReference }) {
  const { t } = useI18n();
  const { data: goal, isError } = useQuery(goalOptions(reference.id));
  const { data: graph } = useQuery(goalGraphOptions(reference.id));

  const title = goal?.title ?? reference.preview?.title ?? reference.id;
  const status = goal?.status ?? reference.preview?.status;

  if (isError) {
    return (
      <ReferenceCardShell
        icon={Target}
        kind={t("references.goal")}
        title={t("references.deleted")}
        muted
      />
    );
  }

  // Subtask rollup — a single summary line, not the full DAG (design V2).
  const tasks = graph?.tasks ?? [];
  const done = tasks.filter((tk) => tk.status === "done").length;
  const running = tasks.filter((tk) => tk.status === "running").length;

  return (
    <div className="flex flex-col gap-1">
      <ReferenceCardShell
        icon={Target}
        kind={t("references.goal")}
        title={title}
        status={status ? <StatusPill status={status} label={statusLabel(t, status)} /> : undefined}
        action={
          goal?.agent_id ? (
            <Link
              to="/agents/$agentId/tasks/goals/$goalId"
              params={{ agentId: goal.agent_id, goalId: reference.id }}
              className="inline-flex shrink-0 items-center gap-1 rounded-lg px-2 py-1 font-mono text-xs text-muted-foreground hover:bg-muted hover:text-foreground"
            >
              {t("references.open")}
              <ArrowUpRight className="size-3.5" />
            </Link>
          ) : undefined
        }
      />
      {tasks.length > 0 && (
        <div className="flex items-center gap-3 pl-12 font-mono text-[11px] text-muted-foreground/70">
          <span>{t("references.goalTasks", { count: tasks.length })}</span>
          {done > 0 && (
            <span className="inline-flex items-center gap-1">
              <span className={`size-1.5 rounded-full ${statusMeta("done").dot}`} />
              {done}
            </span>
          )}
          {running > 0 && (
            <span className="inline-flex items-center gap-1">
              <span className={`size-1.5 rounded-full ${statusMeta("running").dot}`} />
              {running}
            </span>
          )}
        </div>
      )}
    </div>
  );
}

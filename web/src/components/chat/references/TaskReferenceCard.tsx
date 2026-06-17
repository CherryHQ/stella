import { queryOptions, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ArrowUpRight, ListTodo } from "lucide-react";
import { getTask } from "@/lib/api-client";
import type { ComponentsTask } from "@/lib/api-client/types.gen";
import { useI18n } from "@/lib/i18n";
import type { RenderableReference } from "@/lib/types";
import { StatusPill, statusLabel } from "@/features/tasks/lib";
import { pollWhileActive } from "./poll";
import { ReferenceCardShell } from "./ReferenceCardShell";

function taskOptions(taskId: string) {
  return queryOptions({
    queryKey: ["task", taskId],
    queryFn: async () => {
      const { data } = await getTask({ path: { taskId }, throwOnError: true });
      return data as ComponentsTask;
    },
    enabled: !!taskId,
    refetchInterval: pollWhileActive,
  });
}

export function TaskReferenceCard({ reference }: { reference: RenderableReference }) {
  const { t } = useI18n();
  const { data: task, isError } = useQuery(taskOptions(reference.id));

  // Live entity wins; preview is only a first-paint placeholder and is never
  // trusted for routing or existence.
  const title = task?.title ?? reference.preview?.title ?? reference.id;
  const status = task?.status ?? reference.preview?.status;

  if (isError) {
    return (
      <ReferenceCardShell
        icon={ListTodo}
        kind={t("references.task")}
        title={t("references.deleted")}
        muted
      />
    );
  }

  return (
    <ReferenceCardShell
      icon={ListTodo}
      kind={t("references.task")}
      title={title}
      status={status ? <StatusPill status={status} label={statusLabel(t, status)} /> : undefined}
      action={
        task?.agent_id ? (
          <Link
            to="/agents/$agentId/tasks/$taskId"
            params={{ agentId: task.agent_id, taskId: reference.id }}
            className="inline-flex shrink-0 items-center gap-1 rounded-lg px-2 py-1 font-mono text-xs text-muted-foreground hover:bg-muted hover:text-foreground"
          >
            {t("references.open")}
            <ArrowUpRight className="size-3.5" />
          </Link>
        ) : undefined
      }
    />
  );
}

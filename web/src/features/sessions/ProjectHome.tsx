import { useCallback, useEffect, useRef } from "react";
import { useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { createSession } from "@/lib/api-client/sdk.gen";
import type { ComponentsTask } from "@/lib/api-client/types.gen";
import type { Session } from "@/lib/types";
import { projectSessionsQueryOptions } from "@/lib/queries/sessions";
import { fetchAllTasks } from "@/lib/paginated";
import { formatTime } from "@/lib/time";
import { Button } from "@/components/ui/button";
import { StatusDot, statusLabel } from "@/features/automations/lib";
import { useAppShell } from "@/layouts/AppShell";
import { useI18n } from "@/lib/i18n";

const STATUS_ORDER = [
  "blocked",
  "reviewing",
  "running",
  "ready",
  "draft",
  "failed",
  "done",
  "cancelled",
];

export function ProjectHome() {
  const { agentId, projectId } = useParams({
    from: "/_app/agents/$agentId/projects/$projectId/",
  });
  const { tab: rawTab } = useSearch({ from: "/_app/agents/$agentId/projects/$projectId/" });
  const tab = rawTab === "tasks" ? "tasks" : "sessions";
  const navigate = useNavigate();
  const { t } = useI18n();
  const { setHeaderActions } = useAppShell();
  const queryClient = useQueryClient();
  const creating = useRef(false);

  const sessionsQuery = useQuery(projectSessionsQueryOptions(agentId, projectId));
  const projectSessions = (sessionsQuery.data ?? []).filter(
    (s) => !s.archived && s.kind !== "delegate" && s.kind !== "scheduler",
  );
  const { data: tasks = [], isLoading: tasksLoading } = useQuery({
    queryKey: ["project-tasks", agentId, projectId],
    queryFn: () => fetchAllTasks(agentId, projectId),
    enabled: !!agentId && !!projectId,
  });

  const mainSession = projectSessions.find((s) => s.kind === "main") ?? null;
  const mainSessionId = mainSession?.id;
  const orderedSessions = [
    ...(mainSession ? [mainSession] : []),
    ...projectSessions
      .filter((s) => s.id !== mainSessionId)
      .sort((a, b) => new Date(b.last_active).getTime() - new Date(a.last_active).getTime()),
  ];

  useEffect(() => {
    if (!sessionsQuery.isSuccess || mainSession || creating.current) return;
    creating.current = true;
    createSession({
      path: { agentId: agentId },
      body: { project_id: projectId, kind: "main" },
      throwOnError: true,
    })
      .then(async () => {
        await queryClient.invalidateQueries({ queryKey: ["sessions", agentId] });
      })
      .catch((err) => {
        console.error(err);
        creating.current = false;
      });
  }, [mainSession, sessionsQuery.isSuccess, agentId, projectId, queryClient]);

  useEffect(() => {
    setHeaderActions(
      <Button
        variant="outline"
        size="sm"
        onClick={() =>
          void navigate({
            to: "/agents/$agentId/tasks/new",
            params: { agentId },
            search: { project_id: projectId },
          })
        }
      >
        {t("tasks.new")}
      </Button>,
    );
    return () => setHeaderActions(null);
  }, [agentId, navigate, projectId, setHeaderActions, t]);

  const openTask = useCallback(
    (task: ComponentsTask) => {
      void navigate({
        to: "/agents/$agentId/tasks/$taskId",
        params: { agentId, taskId: task.id },
      });
    },
    [agentId, navigate],
  );

  const openSession = useCallback(
    (sessionId: string) => {
      void navigate({
        to: "/agents/$agentId/projects/$projectId/sessions/$sessionId",
        params: { agentId, projectId, sessionId },
      });
    },
    [agentId, projectId, navigate],
  );

  return (
    <div className="flex h-full min-h-0 flex-col overflow-hidden bg-background">
      <div className="min-h-0 flex-1 overflow-hidden">
        {tab === "tasks" && <ProjectTasks tasks={tasks} loading={tasksLoading} onOpen={openTask} />}
        {tab === "sessions" && (
          <ProjectSessions
            sessions={orderedSessions}
            mainSessionId={mainSessionId}
            onOpen={openSession}
          />
        )}
      </div>
    </div>
  );
}

function ProjectTasks({
  tasks,
  loading,
  onOpen,
}: {
  tasks: ComponentsTask[];
  loading: boolean;
  onOpen: (task: ComponentsTask) => void;
}) {
  const { t } = useI18n();
  const grouped = STATUS_ORDER.map((status) => ({
    status,
    tasks: tasks
      .filter((task) => task.status === status)
      .sort((a, b) => new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime()),
  })).filter((group) => group.tasks.length > 0);

  if (loading) {
    return <EmptyState title={t("projects.home.loadingTasks")} detail="" loading />;
  }
  if (tasks.length === 0) {
    return <EmptyState title={t("tasks.noTasks")} detail={t("tasks.noTasksDesc")} />;
  }

  return (
    <div className="h-full overflow-y-auto px-5 py-4">
      <div className="mx-auto max-w-5xl space-y-5">
        {grouped.map((group) => (
          <section key={group.status}>
            <div className="mb-2 flex items-center gap-2">
              <StatusDot
                status={group.status}
                className={group.status === "running" ? "animate-pulse" : undefined}
              />
              <h2 className="text-xs font-semibold uppercase tracking-[0.08em] text-muted-foreground">
                {statusLabel(t, group.status)}
              </h2>
              <span className="rounded bg-muted px-1.5 py-px text-xs text-muted-foreground">
                {group.tasks.length}
              </span>
            </div>
            <div className="divide-y divide-border rounded-md border border-border">
              {group.tasks.map((task) => (
                <button
                  key={task.id}
                  type="button"
                  onClick={() => onOpen(task)}
                  className="flex w-full items-center gap-3 px-3 py-2.5 text-left hover:bg-muted/50"
                >
                  <StatusDot
                    status={task.status}
                    className={task.status === "running" ? "animate-pulse" : undefined}
                  />
                  <div className="min-w-0 flex-1">
                    <div className="truncate text-sm font-medium">{task.title}</div>
                    {task.description && (
                      <div className="mt-0.5 truncate text-xs text-muted-foreground">
                        {task.description}
                      </div>
                    )}
                  </div>
                  <span className="shrink-0 font-mono text-xs text-muted-foreground">
                    {formatTime(task.updated_at)}
                  </span>
                </button>
              ))}
            </div>
          </section>
        ))}
      </div>
    </div>
  );
}

function ProjectSessions({
  sessions,
  mainSessionId,
  onOpen,
}: {
  sessions: Session[];
  mainSessionId?: string;
  onOpen: (sessionId: string) => void;
}) {
  const { t } = useI18n();
  if (sessions.length === 0) {
    return (
      <EmptyState
        title={t("projects.home.openingSessions")}
        detail={t("projects.home.preparingSession")}
      />
    );
  }
  return (
    <div className="h-full overflow-y-auto px-5 py-4">
      <div className="mx-auto max-w-5xl divide-y divide-border rounded-md border border-border">
        {sessions.map((session) => (
          <button
            key={session.id}
            type="button"
            onClick={() => onOpen(session.id)}
            className="flex w-full items-center gap-3 px-3 py-2.5 text-left hover:bg-muted/50"
          >
            <span className="grid size-8 shrink-0 place-items-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
              {session.id === mainSessionId ? "M" : "T"}
            </span>
            <div className="min-w-0 flex-1">
              <div className="truncate text-sm font-medium">
                {session.id === mainSessionId
                  ? t("projects.home.mainSession")
                  : session.title || session.id}
              </div>
              <div className="mt-0.5 truncate text-xs text-muted-foreground">{session.id}</div>
            </div>
            <span className="shrink-0 font-mono text-xs text-muted-foreground">
              {formatTime(session.last_active)}
            </span>
          </button>
        ))}
      </div>
    </div>
  );
}

function EmptyState({
  title,
  detail,
  loading = false,
}: {
  title: string;
  detail: string;
  loading?: boolean;
}) {
  return (
    <div className="flex h-full items-center justify-center p-6 text-center">
      <div>
        <p className="text-sm font-medium text-foreground">{title}</p>
        {detail && <p className="mt-1 text-xs text-muted-foreground">{detail}</p>}
        {loading && (
          <div className="mx-auto mt-4 size-4 animate-spin rounded-full border-2 border-muted-foreground/30 border-t-muted-foreground" />
        )}
      </div>
    </div>
  );
}

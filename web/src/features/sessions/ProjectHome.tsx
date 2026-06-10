import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useParams } from "@tanstack/react-router";
import { useInfiniteQuery, useQuery, useQueryClient } from "@tanstack/react-query";
import { createSession, getSessionWorkspace } from "@/lib/api-client/sdk.gen";
import type { ComponentsTask } from "@/lib/api-client/types.gen";
import type { Session, Workspace } from "@/lib/types";
import { sessionsInfiniteQueryOptions } from "@/lib/queries/sessions";
import { agentProjectsOptions } from "@/lib/queries/projects";
import { fetchAllTasks } from "@/lib/paginated";
import { formatTime } from "@/lib/time";
import { Button } from "@/components/ui/button";
import { StatusDot, statusLabel } from "@/features/automations/lib";
import { WorkspacePanel } from "./WorkspacePanel";
import { useI18n } from "@/lib/i18n";
import { cn } from "@/lib/utils";

type ProjectTab = "tasks" | "sessions" | "files";

const STATUS_ORDER = ["blocked", "reviewing", "running", "ready", "draft", "failed", "done"];

export function ProjectHome() {
  const { agentId, projectId } = useParams({
    from: "/_app/agents/$agentId/projects/$projectId/",
  });
  const navigate = useNavigate();
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const creating = useRef(false);

  const [tab, setTab] = useState<ProjectTab>("tasks");
  const [workspace, setWorkspace] = useState<Workspace | null>(null);
  const [workspaceLoading, setWorkspaceLoading] = useState(false);
  const [projectDir, setProjectDir] = useState("");

  const { data: projects = [] } = useQuery(agentProjectsOptions(agentId));
  const project = projects.find((p) => p.id === projectId);
  const sessionsQuery = useInfiniteQuery(sessionsInfiniteQueryOptions(agentId));
  const sessions = sessionsQuery.data?.pages.flatMap((p) => p.sessions) ?? [];
  const { data: tasks = [], isLoading: tasksLoading } = useQuery({
    queryKey: ["project-tasks", agentId, projectId],
    queryFn: async () => {
      const all = await fetchAllTasks(agentId);
      return all.filter((task) => task.project_id === projectId);
    },
    enabled: !!agentId && !!projectId,
  });

  const projectSessions = useMemo(
    () => sessions.filter((s) => s.project_id === projectId && !s.archived),
    [sessions, projectId],
  );
  const mainSession = useMemo(
    () => projectSessions.find((s) => !isTaskSession(s)) ?? null,
    [projectSessions],
  );
  const orderedSessions = useMemo(() => {
    const rest = projectSessions
      .filter((s) => s.id !== mainSession?.id)
      .sort((a, b) => new Date(b.last_active).getTime() - new Date(a.last_active).getTime());
    return mainSession ? [mainSession, ...rest] : rest;
  }, [mainSession, projectSessions]);

  useEffect(() => {
    if (sessionsQuery.isLoading || mainSession || creating.current) return;
    creating.current = true;
    createSession({
      path: { agentId: agentId },
      body: { project_id: projectId },
      throwOnError: true,
    })
      .then(async () => {
        await queryClient.invalidateQueries({ queryKey: ["sessions", agentId] });
      })
      .catch((err) => {
        console.error(err);
        creating.current = false;
      });
  }, [mainSession, sessionsQuery.isLoading, agentId, projectId, queryClient]);

  const loadWorkspace = useCallback(
    async (sid: string, scopePath?: string) => {
      setWorkspaceLoading(true);
      try {
        const { data } = await getSessionWorkspace({
          path: { agentId: agentId, sessionId: sid },
          query: { show_hidden: true, depth: 2, ...(scopePath ? { path: scopePath } : {}) },
          throwOnError: true,
        });
        setWorkspace(data);
        if (
          !scopePath &&
          project?.base_dir &&
          data.root &&
          project.base_dir.startsWith(data.root + "/")
        ) {
          const rel = project.base_dir.slice(data.root.length + 1);
          if (rel) setProjectDir(rel);
        }
      } catch (e) {
        console.error(e);
        setWorkspace(null);
      } finally {
        setWorkspaceLoading(false);
      }
    },
    [agentId, project?.base_dir],
  );

  useEffect(() => {
    if (tab === "files" && mainSession) {
      void loadWorkspace(mainSession.id, projectDir || undefined);
    }
  }, [tab, mainSession?.id, loadWorkspace, projectDir, mainSession]);

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
      <div className="border-b border-border px-5 py-4">
        <div className="flex min-w-0 items-center justify-between gap-3">
          <div className="min-w-0">
            <h1 className="truncate text-lg font-semibold tracking-[-0.01em]">
              {project?.name ?? "Project"}
            </h1>
            <p className="mt-1 truncate text-xs text-muted-foreground">
              {tasks.length} tasks · {projectSessions.length} sessions
            </p>
          </div>
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
          </Button>
        </div>
        <div className="mt-4 flex gap-1">
          {(["tasks", "sessions", "files"] as ProjectTab[]).map((value) => (
            <button
              key={value}
              type="button"
              onClick={() => setTab(value)}
              className={cn(
                "rounded-md px-3 py-1.5 text-sm font-medium transition-colors",
                tab === value
                  ? "bg-foreground text-background"
                  : "text-muted-foreground hover:bg-muted hover:text-foreground",
              )}
            >
              {value === "tasks"
                ? t("facets.tasks")
                : value === "sessions"
                  ? t("facets.conversation")
                  : t("facets.files")}
            </button>
          ))}
        </div>
      </div>

      <div className="min-h-0 flex-1 overflow-hidden">
        {tab === "tasks" && <ProjectTasks tasks={tasks} loading={tasksLoading} onOpen={openTask} />}
        {tab === "sessions" && (
          <ProjectSessions
            sessions={orderedSessions}
            mainSessionId={mainSession?.id}
            onOpen={openSession}
          />
        )}
        {tab === "files" && mainSession && (
          <WorkspacePanel
            agentID={agentId}
            sessionID={mainSession.id}
            workspace={workspace}
            workspaceLoading={workspaceLoading}
            onReload={loadWorkspace}
            projectDir={projectDir}
          />
        )}
        {tab === "files" && !mainSession && (
          <EmptyState
            title="Opening workspace…"
            detail="Stella is preparing the project session."
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
    return <EmptyState title="Loading tasks…" detail="" loading />;
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
              <StatusDot status={group.status} />
              <h2 className="text-xs font-semibold uppercase tracking-[0.08em] text-muted-foreground">
                {statusLabel(t, group.status)}
              </h2>
              <span className="rounded bg-muted px-1.5 py-px text-[11px] text-muted-foreground">
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
                  <StatusDot status={task.status} />
                  <div className="min-w-0 flex-1">
                    <div className="truncate text-sm font-medium">{task.title}</div>
                    {task.description && (
                      <div className="mt-0.5 truncate text-xs text-muted-foreground">
                        {task.description}
                      </div>
                    )}
                  </div>
                  <span className="shrink-0 font-mono text-[11px] text-muted-foreground">
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
  if (sessions.length === 0) {
    return (
      <EmptyState title="Opening sessions…" detail="Stella is preparing the project session." />
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
                {session.id === mainSessionId ? "Project main" : session.title || session.id}
              </div>
              <div className="mt-0.5 truncate text-xs text-muted-foreground">{session.id}</div>
            </div>
            <span className="shrink-0 font-mono text-[11px] text-muted-foreground">
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

function isTaskSession(session: Session): boolean {
  return session.channel === "task" || session.id.startsWith("task:");
}

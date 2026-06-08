import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { useInfiniteQuery, useQuery, useQueryClient } from "@tanstack/react-query";
import type { Agent, Session, Project } from "@/lib/types";
import {
  createProject,
  createSession as sdkCreateSession,
  deleteProject as sdkDeleteProject,
  getSessionWorkspace,
} from "@/lib/api-client/sdk.gen";
import { fetchAllTasks } from "@/lib/paginated";
import { cn } from "@/lib/utils";
import { getAgentColor } from "@/lib/agent-colors";
import type { ComponentsSession, TaskList } from "@/lib/api-client/types.gen";
import { useI18n } from "@/lib/i18n";
import { sessionsInfiniteQueryOptions } from "@/lib/queries/sessions";
import { agentProjectsOptions } from "@/lib/queries/projects";
import { Button } from "@/components/ui/button";
import { SectionLabel } from "@/components/AppSidebar";
import { useSidebar } from "@/components/ui/sidebar";
import { Input } from "@/components/ui/input";
import { GroupSection } from "@/features/groups/GroupSidebar";
import {
  Dialog,
  DialogPopup,
  DialogTitle,
  DialogFooter,
  DialogHeader,
  DialogDescription,
} from "@/components/ui/dialog";

interface Props {
  agents: Agent[];
  agentId: string;
  pathname: string;
  onAgentChange: (id: string) => void;
}

// ── helpers ──────────────────────────────────────────────────────────────────

function sessionTitle(s: Session): string {
  return s.title || "Untitled";
}

function sessionKindLabel(s: Session): string {
  if (s.kind === "main") return "main";
  if (s.kind === "task") return "task";
  if (s.kind === "scheduler") return "run";
  return "chat";
}

// ── icons ────────────────────────────────────────────────────────────────────

function ChevRight({ className }: { className?: string }) {
  return (
    <svg
      className={className}
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
    >
      <path d="m6 4 4 4-4 4" />
    </svg>
  );
}

function IconAutomation() {
  return (
    <svg className="size-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8">
      <path d="M13 2 3 14h8l-1 8 11-13h-8l0-7z" />
    </svg>
  );
}

function IconFolder() {
  return (
    <svg className="size-3" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8">
      <path d="M4 20h16a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.93a2 2 0 0 1-1.66-.9l-.82-1.2A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13c0 1.1.9 2 2 2z" />
    </svg>
  );
}

function IconMore() {
  return (
    <svg className="size-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
      <circle cx="5" cy="12" r="1" />
      <circle cx="12" cy="12" r="1" />
      <circle cx="19" cy="12" r="1" />
    </svg>
  );
}

function IconNewChat() {
  return (
    <svg
      className="size-3.5"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.85"
    >
      <path d="M5 6.5A2.5 2.5 0 0 1 7.5 4h6A2.5 2.5 0 0 1 16 6.5v3A2.5 2.5 0 0 1 13.5 12H9l-4 3v-8.5z" />
      <path d="M18 12v6" />
      <path d="M15 15h6" />
    </svg>
  );
}

function IconFolderProject() {
  return (
    <svg
      className="size-[18px]"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.75"
    >
      <path d="M3 7.5A2.5 2.5 0 0 1 5.5 5H10l2 2h6.5A2.5 2.5 0 0 1 21 9.5v7A2.5 2.5 0 0 1 18.5 19h-13A2.5 2.5 0 0 1 3 16.5z" />
      <path d="M3 10h18" />
    </svg>
  );
}

function IconSkills() {
  return (
    <svg className="size-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8">
      <path d="m12 3-1.912 5.813a2 2 0 0 1-1.275 1.275L3 12l5.813 1.912a2 2 0 0 1 1.275 1.275L12 21l1.912-5.813a2 2 0 0 1 1.275-1.275L21 12l-5.813-1.912a2 2 0 0 1-1.275-1.275z" />
    </svg>
  );
}

function IconMemory() {
  return (
    <svg className="size-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8">
      <path d="M12 2a7 7 0 0 1 7 7c0 2.38-1.19 4.47-3 5.74V17a2 2 0 0 1-2 2H10a2 2 0 0 1-2-2v-2.26C6.19 13.47 5 11.38 5 9a7 7 0 0 1 7-7z" />
      <path d="M10 21h4" />
    </svg>
  );
}

function relativeTime(dateStr: string): string {
  const now = Date.now();
  const then = new Date(dateStr).getTime();
  const diff = now - then;
  if (diff < 0) return "now";
  const minutes = Math.floor(diff / 60000);
  if (minutes < 1) return "now";
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h`;
  const days = Math.floor(hours / 24);
  if (days < 7) return `${days}d`;
  const weeks = Math.floor(days / 7);
  if (weeks < 5) return `${weeks}w`;
  const months = Math.floor(days / 30);
  return `${months}mo`;
}

// ── FolderTree & CreateProjectDialog (unchanged) ────────────────────────────

interface DirEntry {
  path: string;
  name: string;
  depth: number;
}

function parseDirs(paths: string[]): DirEntry[] {
  return paths
    .filter((p) => p.endsWith("/"))
    .map((p) => {
      const clean = p.replace(/\/$/, "");
      const parts = clean.split("/");
      return { path: clean, name: parts[parts.length - 1], depth: parts.length - 1 };
    });
}

function FolderTree({
  agentId,
  sessionId,
  selected,
  onSelect,
  onRootResolved,
}: {
  agentId: string;
  sessionId: string;
  selected: string;
  onSelect: (path: string) => void;
  onRootResolved?: (root: string) => void;
}) {
  const { t } = useI18n();
  const [dirs, setDirs] = useState<DirEntry[]>([]);
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    getSessionWorkspace({
      path: { agentId: agentId, sessionId: sessionId },
      query: { depth: 4 },
      throwOnError: true,
    }).then(
      ({ data: ws }) => {
        setDirs(parseDirs(ws.paths));
        if (ws.root) onRootResolved?.(ws.root);
        setLoading(false);
      },
      () => setLoading(false),
    );
  }, [agentId, sessionId, onRootResolved]);

  const toggle = (path: string) =>
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(path)) next.delete(path);
      else next.add(path);
      return next;
    });

  const visibleDirs = useMemo(() => {
    return dirs.filter((d) => {
      if (d.depth === 0) return true;
      const parentPath = d.path.split("/").slice(0, -1).join("/");
      return expanded.has(parentPath);
    });
  }, [dirs, expanded]);

  if (loading) {
    return <p className="px-2 py-3 text-xs text-muted-foreground">Loading folders…</p>;
  }
  if (dirs.length === 0) {
    return (
      <p className="px-2 py-3 text-xs text-muted-foreground">{t("sessions.sidebar.noFolders")}</p>
    );
  }

  return (
    <div className="max-h-48 overflow-y-auto">
      {visibleDirs.map((d) => {
        const hasChildren = dirs.some(
          (c) => c.depth === d.depth + 1 && c.path.startsWith(d.path + "/"),
        );
        const isSelected = selected === d.path;
        return (
          <div
            key={d.path}
            className={cn(
              "flex cursor-pointer items-center gap-1.5 rounded px-2 py-1 text-[12px] transition-colors",
              isSelected
                ? "bg-primary/10 text-primary"
                : "text-muted-foreground hover:bg-muted/50 hover:text-foreground",
            )}
            style={{ paddingLeft: `${d.depth * 16 + 8}px` }}
            onClick={() => onSelect(d.path)}
          >
            {hasChildren ? (
              <button
                type="button"
                className="flex-shrink-0 p-0"
                onClick={(e) => {
                  e.stopPropagation();
                  toggle(d.path);
                }}
              >
                <ChevRight
                  className={cn(
                    "size-2.5 text-muted-foreground transition-transform",
                    expanded.has(d.path) && "rotate-90",
                  )}
                />
              </button>
            ) : (
              <span className="size-2.5" />
            )}
            <IconFolder />
            <span className="truncate">{d.name}</span>
          </div>
        );
      })}
    </div>
  );
}

function CreateProjectDialog({
  agentId,
  sessionId,
  onCreated,
  onClose,
}: {
  agentId: string;
  sessionId: string;
  onCreated: () => void;
  onClose: () => void;
}) {
  const { t } = useI18n();
  const [userRoot, setUserRoot] = useState("");
  const [selectedDir, setSelectedDir] = useState("");
  const [name, setName] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");

  const folderName = selectedDir ? (selectedDir.split("/").pop() ?? "") : "";
  const effectiveName = name || folderName;
  const canSubmit = selectedDir !== "" && effectiveName !== "" && userRoot !== "" && !submitting;

  const handleRootResolved = useCallback((root: string) => setUserRoot(root), []);

  const handleSelect = useCallback((path: string) => {
    setSelectedDir(path);
    setName("");
  }, []);

  const handleSubmit = useCallback(async () => {
    if (!canSubmit) return;
    setSubmitting(true);
    setError("");
    try {
      const baseDir = `${userRoot}/${selectedDir}`;
      await createProject({
        path: { agentId: agentId },
        body: { name: effectiveName, base_dir: baseDir },
        throwOnError: true,
      });
      onCreated();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed to create project");
      setSubmitting(false);
    }
  }, [canSubmit, userRoot, selectedDir, effectiveName, agentId, onCreated]);

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <DialogPopup showCloseButton={false}>
        <DialogHeader>
          <DialogTitle>{t("sessions.sidebar.newProject")}</DialogTitle>
          <DialogDescription>{t("sessions.sidebar.selectFolder")}</DialogDescription>
        </DialogHeader>
        <div className="space-y-3 px-6 py-2">
          {sessionId ? (
            <div className="overflow-hidden rounded-lg border border-border">
              <FolderTree
                agentId={agentId}
                sessionId={sessionId}
                selected={selectedDir}
                onSelect={handleSelect}
                onRootResolved={handleRootResolved}
              />
            </div>
          ) : (
            <p className="text-xs text-muted-foreground">{t("sessions.sidebar.noActiveSession")}</p>
          )}
          <div>
            <label className="mb-1 block text-xs text-muted-foreground">
              {t("sessions.sidebar.projectName")}
            </label>
            <Input
              placeholder={folderName || "Select a folder above"}
              value={name}
              onChange={(e: React.ChangeEvent<HTMLInputElement>) => setName(e.target.value)}
              onKeyDown={(e: React.KeyboardEvent) => {
                if (e.key === "Enter") void handleSubmit();
              }}
            />
          </div>
          {error && <p className="text-xs text-destructive">{error}</p>}
        </div>
        <DialogFooter>
          <Button variant="ghost" size="sm" onClick={onClose}>
            Cancel
          </Button>
          <Button size="sm" disabled={!canSubmit} onClick={() => void handleSubmit()}>
            {submitting ? "Creating…" : "Create"}
          </Button>
        </DialogFooter>
      </DialogPopup>
    </Dialog>
  );
}

// ── nav item ────────────────────────────────────────────────────────────────

function NavItem({
  active,
  icon,
  label,
  badge,
  onClick,
}: {
  active: boolean;
  icon: React.ReactNode;
  label: string;
  badge?: number;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "flex h-[34px] w-full cursor-pointer items-center gap-2.5 rounded-lg px-2.5 text-[13px] transition-all duration-150 border",
        active
          ? "bg-muted font-semibold text-foreground border-border/60"
          : "text-muted-foreground hover:bg-muted/40 hover:text-foreground border-transparent",
      )}
    >
      <span className={cn("shrink-0", active ? "text-primary" : "text-muted-foreground")}>
        {icon}
      </span>
      <span className="flex-1 truncate text-left">{label}</span>
      {badge !== undefined && badge > 0 && (
        <span className="shrink-0 rounded-full bg-primary px-[7px] py-[2px] font-mono text-[10px] text-primary-foreground">
          {badge}
        </span>
      )}
    </button>
  );
}

// ── main component ───────────────────────────────────────────────────────────

export function AgentSidebarContent({ agents, agentId, pathname, onAgentChange }: Props) {
  const { t } = useI18n();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { setOpenMobile } = useSidebar();
  const closeMobile = useCallback(() => setOpenMobile(false), [setOpenMobile]);

  const [projectsOpen, setProjectsOpen] = useState(true);
  const [chatsOpen, setChatsOpen] = useState(false);
  const [showCreateProject, setShowCreateProject] = useState(false);
  const listRef = useRef<HTMLDivElement>(null);

  // ── data ─────────────────────────────────────────────────────────────────
  const sessionsQuery = useInfiniteQuery(sessionsInfiniteQueryOptions(agentId));
  const sessions = sessionsQuery.data?.pages.flatMap((p) => p.sessions) ?? [];

  const { data: projects = [] } = useQuery(agentProjectsOptions(agentId));
  const { data: taskList } = useQuery({
    queryKey: ["tasks", agentId],
    queryFn: async () => {
      const tasks = await fetchAllTasks(agentId);
      return { tasks } as TaskList;
    },
  });
  const taskAttentionCount =
    taskList?.tasks?.filter(
      (task) =>
        task.status === "blocked" || task.status === "reviewing" || task.status === "failed",
    ).length ?? 0;

  // ── active route detection ───────────────────────────────────────────────
  const isActive = (path: string) => pathname === path || pathname.startsWith(path + "/");
  const activeSessionId = pathname.match(/\/sessions\/([^/]+)/)?.[1] ?? "";
  const activeProjectId = pathname.match(/\/projects\/([^/]+)/)?.[1] ?? "";

  const homeSession = useMemo(() => {
    const active = sessions.filter((s) => !s.archived);
    return active.find((s) => s.kind === "main") ?? active[0] ?? null;
  }, [sessions]);

  const chatSessions = useMemo(() => {
    const active = sessions.filter((s) => !s.archived);
    const main = active.filter((s) => s.kind === "main");
    const rest = active.filter((s) => s.kind !== "main");
    return [...main, ...rest];
  }, [sessions]);

  // ── actions ──────────────────────────────────────────────────────────────
  const createSession = useCallback(async () => {
    const { data } = await sdkCreateSession({
      path: { agentId: agentId },
      throwOnError: true,
    });
    const sess = data as ComponentsSession;
    await queryClient.invalidateQueries({ queryKey: ["sessions", agentId] });
    closeMobile();
    void navigate({
      to: "/agents/$agentId/sessions/$sessionId",
      params: { agentId, sessionId: sess.id },
    });
  }, [agentId, queryClient, navigate, closeMobile]);

  const openProject = useCallback(
    async (projectId: string) => {
      closeMobile();
      const existing = sessions.find((s) => s.project_id === projectId && !s.archived);
      if (existing) {
        void navigate({
          to: "/agents/$agentId/projects/$projectId/sessions/$sessionId",
          params: { agentId, projectId, sessionId: existing.id },
        });
        return;
      }
      void navigate({
        to: "/agents/$agentId/projects/$projectId",
        params: { agentId, projectId },
      });
    },
    [agentId, sessions, navigate, closeMobile],
  );

  const deleteProject = useCallback(
    async (projectId: string) => {
      if (!window.confirm("Delete this project? Sessions will be kept.")) return;
      await sdkDeleteProject({
        path: { agentId: agentId, projectId },
        throwOnError: true,
      });
      await queryClient.invalidateQueries({ queryKey: ["projects", agentId] });
    },
    [agentId, queryClient],
  );

  // ── infinite scroll ──────────────────────────────────────────────────────
  const handleScroll = useCallback(() => {
    const el = listRef.current;
    if (!el || !sessionsQuery.hasNextPage || sessionsQuery.isFetchingNextPage) return;
    if (el.scrollHeight - el.scrollTop - el.clientHeight < 120) {
      void sessionsQuery.fetchNextPage();
    }
  }, [sessionsQuery]);

  return (
    <div className="flex min-h-0 w-full flex-col overflow-hidden">
      {/* ── Agents ──────────────────────────────────────────────────────── */}
      <div className="shrink-0 px-3">
        <SectionLabel>{t("sessions.sidebar.agents")}</SectionLabel>
        <div className="grid gap-1.5">
          {agents.map((ag, idx) => {
            const isCur = ag.id === agentId;
            return (
              <div
                key={ag.id}
                role="button"
                tabIndex={0}
                onClick={() => {
                  closeMobile();
                  onAgentChange(ag.id);
                }}
                onKeyDown={(e) => {
                  if (e.key === "Enter" || e.key === " ") {
                    e.preventDefault();
                    closeMobile();
                    onAgentChange(ag.id);
                  }
                }}
                className={cn(
                  "group grid min-h-[34px] cursor-pointer grid-cols-[1.5rem_minmax(0,1fr)] items-center gap-2.5 rounded-lg px-2.5 py-1 text-left transition-all duration-150 border",
                  isCur
                    ? "bg-muted font-semibold text-foreground border-border/60"
                    : "text-muted-foreground hover:bg-muted/40 hover:text-foreground border-transparent",
                )}
              >
                <span
                  className="grid size-6 place-items-center rounded-full text-[10px] font-bold text-white shrink-0"
                  style={{ background: getAgentColor(ag.id, idx).bg }}
                >
                  {ag.name[0]?.toUpperCase()}
                </span>
                <span className="min-w-0">
                  <span className="block truncate text-[13px] tracking-[-0.01em]">{ag.name}</span>
                </span>
              </div>
            );
          })}
          {agents.length === 0 && (
            <p className="px-2 py-2 text-xs text-muted-foreground">
              {t("sessions.sidebar.noAgents")}
            </p>
          )}
        </div>
      </div>

      {/* ── Scrollable nav ──────────────────────────────────────────────── */}
      <div ref={listRef} className="flex-1 overflow-y-auto px-3 pb-2" onScroll={handleScroll}>
        {/* ── Groups ───────────────────────────────────────────────────── */}
        <GroupSection />

        {/* ── Workspace ─────────────────────────────────────────────────── */}
        <div>
          <SectionLabel>{t("sessions.sidebar.workspace")}</SectionLabel>
          <div className="grid gap-0.5">
            <NavItem
              active={
                isActive(`/agents/${agentId}/automations`) || isActive(`/agents/${agentId}/tasks`)
              }
              icon={<IconAutomation />}
              label={t("sessions.sidebar.work")}
              badge={taskAttentionCount}
              onClick={() => {
                closeMobile();
                void navigate({ to: "/agents/$agentId/automations", params: { agentId } });
              }}
            />
            <NavItem
              active={isActive(`/agents/${agentId}/skills`)}
              icon={<IconSkills />}
              label={t("sessions.sidebar.skills")}
              onClick={() => {
                closeMobile();
                void navigate({ to: "/agents/$agentId/skills", params: { agentId } });
              }}
            />
            <NavItem
              active={isActive(`/agents/${agentId}/memories`)}
              icon={<IconMemory />}
              label={t("sessions.sidebar.memory")}
              onClick={() => {
                closeMobile();
                void navigate({ to: "/agents/$agentId/memories", params: { agentId } });
              }}
            />
          </div>
        </div>

        {/* ── Projects ─────────────────────────────────────────────── */}
        <section className="mt-3">
          <div className="flex h-[30px] items-center gap-1 pr-1">
            <button
              type="button"
              onClick={() => setProjectsOpen((v) => !v)}
              className="flex min-w-0 flex-1 items-center gap-2 rounded-[9px] px-2 py-1 font-mono text-[10px] text-muted-foreground hover:bg-foreground/[0.045] hover:text-muted-foreground"
            >
              <span>{t("sessions.sidebar.projects")}</span>
              <ChevRight
                className={cn(
                  "size-2.5 text-muted-foreground transition-transform duration-150",
                  projectsOpen && "rotate-90",
                )}
              />
            </button>
            <button
              type="button"
              onClick={() => setShowCreateProject(true)}
              className="grid size-6 place-items-center rounded-lg text-muted-foreground opacity-60 transition-all hover:bg-foreground/[0.055] hover:text-foreground hover:opacity-100"
              title={t("sessions.sidebar.newProject2")}
            >
              <IconMore />
            </button>
          </div>
          {projectsOpen && (
            <div className="grid gap-px">
              {(projects as Project[]).map((p) => {
                const isActiveProject = activeProjectId === p.id;
                return (
                  <div key={p.id} className="group/proj">
                    <button
                      type="button"
                      onClick={() => void openProject(p.id)}
                      className={cn(
                        "grid min-h-[32px] w-full grid-cols-[21px_minmax(0,1fr)_auto_auto] items-center gap-2 rounded-xl px-[7px] text-left text-[13px] font-medium tracking-[-0.016em] transition-colors border",
                        isActiveProject
                          ? "bg-muted text-foreground border-border"
                          : "text-muted-foreground hover:bg-muted/40 hover:text-foreground border-transparent",
                      )}
                    >
                      <span className={cn("opacity-90", isActiveProject && "text-foreground")}>
                        <IconFolderProject />
                      </span>
                      <span className="truncate">{p.name}</span>
                      <span className="font-mono text-[11px] text-muted-foreground">
                        {relativeTime(p.updated_at)}
                      </span>
                      <span
                        onClick={(e) => {
                          e.stopPropagation();
                          void deleteProject(p.id);
                        }}
                        className="grid size-6 place-items-center rounded-lg text-muted-foreground opacity-0 transition-all hover:bg-card hover:text-foreground group-hover/proj:opacity-70"
                      >
                        <IconMore />
                      </span>
                    </button>
                  </div>
                );
              })}
            </div>
          )}
        </section>
        {showCreateProject && (
          <CreateProjectDialog
            agentId={agentId}
            sessionId={homeSession?.id ?? ""}
            onCreated={() => {
              setShowCreateProject(false);
              void queryClient.invalidateQueries({ queryKey: ["projects", agentId] });
            }}
            onClose={() => setShowCreateProject(false)}
          />
        )}

        {/* ── Sessions ──────────────────────────────────────────────────── */}
        <section className="mt-3">
          <div className="flex h-[30px] items-center gap-1 pr-1">
            <button
              type="button"
              onClick={() => setChatsOpen((v) => !v)}
              className="flex min-w-0 flex-1 items-center gap-2 rounded-[9px] px-2 py-1 font-mono text-[10px] text-muted-foreground hover:bg-foreground/[0.045] hover:text-muted-foreground"
            >
              <span>{t("sessions.sidebar.sessions")}</span>
              <ChevRight
                className={cn(
                  "size-2.5 text-muted-foreground transition-transform duration-150",
                  chatsOpen && "rotate-90",
                )}
              />
            </button>
            <div className="flex items-center gap-0.5">
              <button
                type="button"
                onClick={() => void createSession()}
                className="grid size-6 place-items-center rounded-lg text-muted-foreground opacity-60 transition-all hover:bg-foreground/[0.055] hover:text-foreground hover:opacity-100"
                title="New temporary thread"
              >
                <IconNewChat />
              </button>
            </div>
          </div>
          {chatsOpen && (
            <div className="grid gap-px">
              {chatSessions.map((s) => (
                <button
                  key={s.id}
                  type="button"
                  onClick={() => {
                    closeMobile();
                    void navigate({
                      to: "/agents/$agentId/sessions/$sessionId",
                      params: { agentId, sessionId: s.id },
                    });
                  }}
                  className={cn(
                    "grid min-h-[27px] w-full grid-cols-[minmax(0,1fr)_auto_auto] items-center gap-2 rounded-lg px-[7px] text-left text-[13px] leading-snug tracking-[-0.012em] transition-colors border",
                    activeSessionId === s.id
                      ? "bg-muted text-primary border-border/50"
                      : "text-foreground hover:bg-muted/40 border-transparent",
                  )}
                >
                  <span className="truncate">{sessionTitle(s)}</span>
                  <span className="rounded-full bg-muted px-1.5 py-0.5 font-mono text-[9px] uppercase text-muted-foreground">
                    {sessionKindLabel(s)}
                  </span>
                  <time className="text-[12px] font-medium text-muted-foreground">
                    {relativeTime(s.last_active)}
                  </time>
                </button>
              ))}
              {chatSessions.length === 0 && (
                <p className="px-2 py-2 font-mono text-xs text-muted-foreground">
                  {t("sessions.sidebar.noChats")}
                </p>
              )}
              {sessionsQuery.isLoading && (
                <div className="flex items-center gap-2 px-2 py-1.5">
                  <div className="size-3 animate-spin rounded-full border border-muted-foreground/30 border-t-muted-foreground/70" />
                  <span className="font-mono text-xs text-muted-foreground">
                    {t("common.loading")}
                  </span>
                </div>
              )}
              {sessionsQuery.hasNextPage && !sessionsQuery.isFetchingNextPage && (
                <button
                  type="button"
                  onClick={() => void sessionsQuery.fetchNextPage()}
                  className="min-h-[28px] rounded-[10px] px-[7px] text-left text-[13px] text-muted-foreground transition-colors hover:bg-foreground/[0.045] hover:text-muted-foreground"
                >
                  Show more
                </button>
              )}
            </div>
          )}
        </section>
      </div>
    </div>
  );
}

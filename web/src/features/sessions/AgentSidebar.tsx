import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { useInfiniteQuery, useQuery, useQueryClient } from "@tanstack/react-query";
import type { Agent, Session, Project } from "@/lib/types";
import { api } from "@/lib/api";
import type { AgentTaskList } from "@/lib/api-client/types.gen";
import { cn } from "@/lib/utils";
import { useI18n } from "@/lib/i18n";
import { sessionsInfiniteQueryOptions } from "@/lib/queries/sessions";
import { agentSkillsOptions, agentMemoriesOptions } from "@/lib/queries/agents";
import { agentProjectsOptions } from "@/lib/queries/projects";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
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

const AVATAR_COLORS = [
  "linear-gradient(145deg, #111, #3d3d42)",
  "linear-gradient(145deg, #005fb8, #2997ff)",
  "linear-gradient(145deg, #2d6a4f, #52b788)",
  "linear-gradient(145deg, #7b2d8e, #b06ef5)",
  "linear-gradient(145deg, #b8860b, #e8b84b)",
  "linear-gradient(145deg, #b02020, #e05050)",
  "linear-gradient(145deg, #1a8a8a, #3bc9db)",
];

const PROJECT_COLORS = [
  "#0071e3",
  "#34c759",
  "#ff9f0a",
  "#af52de",
  "#ff3b30",
  "#5ac8fa",
  "#ffcc00",
];

function agentGradient(id: string, index: number): string {
  if (index < AVATAR_COLORS.length) return AVATAR_COLORS[index];
  let h = 0;
  for (let i = 0; i < id.length; i++) h = (h * 31 + id.charCodeAt(i)) & 0xffffff;
  return AVATAR_COLORS[h % AVATAR_COLORS.length];
}

function projectColor(index: number): string {
  return PROJECT_COLORS[index % PROJECT_COLORS.length];
}

function sessionTitle(s: Session): string {
  return s.title || "Untitled";
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

function IconTask() {
  return (
    <svg className="size-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8">
      <path d="m9 11 3 3L22 4" />
      <path d="M21 12v7a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11" />
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

function IconSkills() {
  return (
    <svg className="size-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8">
      <path d="M12 2v20M2 12h20M4.9 4.9l14.2 14.2M19.1 4.9 4.9 19.1" />
    </svg>
  );
}

function IconMemory() {
  return (
    <svg className="size-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8">
      <path d="M12 3a7 7 0 0 0-7 7c0 5 7 11 7 11s7-6 7-11a7 7 0 0 0-7-7z" />
      <circle cx="12" cy="10" r="2" />
    </svg>
  );
}

function IconSettings() {
  return (
    <svg
      className="size-3.5"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
    >
      <circle cx="12" cy="12" r="3" />
      <path d="M19.4 15a1.7 1.7 0 0 0 .34 1.88l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06A1.7 1.7 0 0 0 15 19.4a1.7 1.7 0 0 0-1 .6V20a2 2 0 1 1-4 0v-.08a1.7 1.7 0 0 0-1-.52 1.7 1.7 0 0 0-1.88.34l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06A1.7 1.7 0 0 0 4.6 15a1.7 1.7 0 0 0-.6-1H4a2 2 0 1 1 0-4h.08a1.7 1.7 0 0 0 .52-1 1.7 1.7 0 0 0-.34-1.88l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06A1.7 1.7 0 0 0 9 4.6a1.7 1.7 0 0 0 1-.6V4a2 2 0 1 1 4 0v.08a1.7 1.7 0 0 0 1 .52 1.7 1.7 0 0 0 1.88-.34l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06A1.7 1.7 0 0 0 19.4 9c.23.34.43.67.6 1H20a2 2 0 1 1 0 4h-.08c-.17.33-.37.66-.52 1z" />
    </svg>
  );
}

function IconPlus() {
  return (
    <svg
      className="size-2.5"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2.5"
    >
      <path d="M12 5v14M5 12h14" />
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

function IconTrash() {
  return (
    <svg className="size-3" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8">
      <path d="M3 6h18" />
      <path d="M19 6v14c0 1-1 2-2 2H7c-1 0-2-1-2-2V6" />
      <path d="M8 6V4c0-1 1-2 2-2h4c1 0 2 1 2 2v2" />
    </svg>
  );
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
  sessionId,
  selected,
  onSelect,
  onRootResolved,
}: {
  sessionId: string;
  selected: string;
  onSelect: (path: string) => void;
  onRootResolved?: (root: string) => void;
}) {
  const [dirs, setDirs] = useState<DirEntry[]>([]);
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    const enc = encodeURIComponent(sessionId);
    api<{ root: string; paths: string[] }>("GET", `/api/sessions/${enc}/workspace?depth=4`).then(
      (ws) => {
        setDirs(parseDirs(ws.paths));
        if (ws.root) onRootResolved?.(ws.root);
        setLoading(false);
      },
      () => setLoading(false),
    );
  }, [sessionId, onRootResolved]);

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
    return <p className="px-2 py-3 text-xs text-muted-foreground">No folders found</p>;
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
                    "size-2.5 text-muted-foreground/50 transition-transform",
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
      await api("POST", `/api/agents/${agentId}/projects`, {
        name: effectiveName,
        base_dir: baseDir,
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
          <DialogTitle>New Project</DialogTitle>
          <DialogDescription>Select a workspace folder for this agent.</DialogDescription>
        </DialogHeader>
        <div className="space-y-3 px-6 py-2">
          {sessionId ? (
            <div className="overflow-hidden rounded-lg border border-border">
              <FolderTree
                sessionId={sessionId}
                selected={selectedDir}
                onSelect={handleSelect}
                onRootResolved={handleRootResolved}
              />
            </div>
          ) : (
            <p className="text-xs text-muted-foreground">No active session to browse folders.</p>
          )}
          <div>
            <label className="mb-1 block text-xs text-muted-foreground">Project name</label>
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
        "flex h-[34px] w-full cursor-pointer items-center gap-2.5 rounded-[10px] px-2.5 text-[13px] transition-all duration-150",
        active
          ? "bg-primary/10 font-semibold text-primary"
          : "text-muted-foreground hover:bg-foreground/[0.045] hover:text-foreground",
      )}
    >
      <span className={cn("shrink-0", active ? "text-primary" : "text-muted-foreground/70")}>
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

// ── section label ───────────────────────────────────────────────────────────

function SectionLabel({ children }: { children: React.ReactNode }) {
  return (
    <div className="px-2 pb-1.5 pt-4 font-mono text-[10px] uppercase tracking-[0.08em] text-muted-foreground/60">
      {children}
    </div>
  );
}

// ── main component ───────────────────────────────────────────────────────────

export function AgentSidebar({ agents, agentId, pathname, onAgentChange }: Props) {
  const { t } = useI18n();
  const navigate = useNavigate();
  const queryClient = useQueryClient();

  const [openProjects, setOpenProjects] = useState<string[]>([]);
  const [archiveOpen, setArchiveOpen] = useState(false);
  const [historyKind, setHistoryKind] = useState("");
  const [showCreateProject, setShowCreateProject] = useState(false);
  const listRef = useRef<HTMLDivElement>(null);

  const isProjectOpen = (id: string) => openProjects.includes(id);
  const toggleProject = (id: string) =>
    setOpenProjects((prev) => (prev.includes(id) ? prev.filter((k) => k !== id) : [...prev, id]));

  // ── data ─────────────────────────────────────────────────────────────────
  const sessionsQuery = useInfiniteQuery(sessionsInfiniteQueryOptions(agentId));
  const sessions = sessionsQuery.data?.pages.flat() ?? [];

  const { data: _skills = [] } = useQuery(agentSkillsOptions(agentId));
  const { data: _memories = [] } = useQuery(agentMemoriesOptions(agentId));
  const { data: projects = [] } = useQuery(agentProjectsOptions(agentId));
  const { data: taskList } = useQuery({
    queryKey: ["tasks"],
    queryFn: () => api<AgentTaskList>("GET", "/api/tasks"),
  });
  const taskCount = taskList?.items?.length ?? 0;

  // ── active route detection ───────────────────────────────────────────────
  const isActive = (path: string) => pathname === path || pathname.startsWith(path + "/");
  const activeSessionId = pathname.match(/\/sessions\/([^/]+)/)?.[1] ?? "";
  const activeProjectId = pathname.match(/\/projects\/([^/]+)/)?.[1] ?? "";

  const homeSession = useMemo(() => {
    const active = sessions.filter((s) => !s.archived);
    return active.find((s) => s.kind === "main") ?? active[0] ?? null;
  }, [sessions]);

  const chatSessions = useMemo(() => {
    return sessions.filter((s) => {
      if (s.archived) return false;
      if (s.id === "main") return false;
      if (historyKind) return s.kind === historyKind;
      return true;
    });
  }, [sessions, historyKind]);

  const archivedCount = useMemo(
    () => sessions.filter((s) => !s.archived && s.kind !== "main").length,
    [sessions],
  );

  // ── actions ──────────────────────────────────────────────────────────────
  const createSession = useCallback(async () => {
    const sess = await api<Session>("POST", "/api/sessions", { agent_id: agentId });
    await queryClient.invalidateQueries({ queryKey: ["sessions", agentId] });
    void navigate({
      to: "/agents/$agentId/sessions/$sessionId",
      params: { agentId, sessionId: sess.id },
    });
  }, [agentId, queryClient, navigate]);

  const openProject = useCallback(
    async (projectId: string) => {
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
    [agentId, sessions, navigate],
  );

  const deleteProject = useCallback(
    async (projectId: string) => {
      if (!window.confirm("Delete this project? Sessions will be kept.")) return;
      await api("DELETE", `/api/agents/${agentId}/projects/${projectId}`);
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
    <aside className="flex h-full w-full flex-col overflow-hidden">
      {/* ── Agents ──────────────────────────────────────────────────────── */}
      <div className="shrink-0 px-3 pt-3">
        <SectionLabel>Agents</SectionLabel>
        <div className="grid gap-1.5">
          {agents.map((ag, idx) => {
            const isCur = ag.id === agentId;
            return (
              <button
                key={ag.id}
                type="button"
                onClick={() => onAgentChange(ag.id)}
                className={cn(
                  "group grid min-h-[46px] grid-cols-[2rem_minmax(0,1fr)_auto] items-center gap-2 rounded-[14px] px-[7px] py-1.5 text-left transition-all duration-150",
                  isCur
                    ? "bg-card text-foreground shadow-[0_0_0_0.5px_var(--border),0_6px_18px_rgba(29,29,31,0.04)]"
                    : "text-muted-foreground hover:bg-foreground/[0.045] hover:text-foreground",
                )}
              >
                <span
                  className="grid size-8 place-items-center rounded-[10px] text-[13px] font-bold text-white shadow-[0_6px_14px_rgba(0,0,0,0.14)]"
                  style={{ background: agentGradient(ag.id, idx) }}
                >
                  {ag.name[0]?.toUpperCase()}
                </span>
                <span className="min-w-0">
                  <span className="block truncate text-[13px] font-semibold tracking-[-0.01em] text-foreground">
                    {ag.name}
                  </span>
                </span>
                <button
                  type="button"
                  onClick={(e) => {
                    e.stopPropagation();
                    void navigate({ to: "/settings/agents" });
                  }}
                  className="grid size-7 place-items-center rounded-full text-muted-foreground/42 transition-all hover:bg-primary/10 hover:text-primary"
                  aria-label={`${ag.name} settings`}
                >
                  <IconSettings />
                </button>
              </button>
            );
          })}
          {agents.length === 0 && (
            <p className="px-2 py-2 text-xs text-muted-foreground">No agents yet.</p>
          )}
        </div>
      </div>

      {/* ── Scrollable nav ──────────────────────────────────────────────── */}
      <div ref={listRef} className="flex-1 overflow-y-auto px-3 pb-2" onScroll={handleScroll}>
        {/* ── Workspace ─────────────────────────────────────────────────── */}
        <div>
          <SectionLabel>Workspace</SectionLabel>
          <div className="grid gap-0.5">
            <NavItem
              active={isActive(`/agents/${agentId}/tasks`)}
              icon={<IconTask />}
              label={t("sessions.sidebar.tasks")}
              badge={taskCount}
              onClick={() => void navigate({ to: "/agents/$agentId/tasks", params: { agentId } })}
            />
            <NavItem
              active={isActive(`/agents/${agentId}/automations`)}
              icon={<IconAutomation />}
              label={t("sessions.sidebar.automations")}
              onClick={() =>
                void navigate({ to: "/agents/$agentId/automations", params: { agentId } })
              }
            />
            <NavItem
              active={isActive(`/agents/${agentId}/skills`)}
              icon={<IconSkills />}
              label={t("sessions.sidebar.skills")}
              onClick={() => void navigate({ to: "/agents/$agentId/skills", params: { agentId } })}
            />
            <NavItem
              active={
                isActive(`/agents/${agentId}/memories/soul`) ||
                isActive(`/agents/${agentId}/memories/profile`)
              }
              icon={<IconMemory />}
              label={t("sessions.sidebar.memory")}
              onClick={() =>
                void navigate({ to: "/agents/$agentId/memories/soul", params: { agentId } })
              }
            />
          </div>
        </div>

        {/* ── Project folders ──────────────────────────────────────────── */}
        <div>
          <div className="flex items-center justify-between pr-1">
            <SectionLabel>Project folders</SectionLabel>
            <button
              type="button"
              onClick={() => setShowCreateProject(true)}
              className="mt-2 p-0.5 text-muted-foreground/40 transition-colors hover:text-foreground"
              title="New project"
            >
              <IconPlus />
            </button>
          </div>
          <div className="grid gap-1">
            {(projects as Project[]).map((p, idx) => {
              const open = isProjectOpen(p.id);
              const isActiveProject = activeProjectId === p.id;
              const projectSessions = sessions.filter((s) => s.project_id === p.id && !s.archived);
              return (
                <div key={p.id} className="group/proj rounded-xl p-1.5 text-[12px]">
                  <div className="flex items-center">
                    <button
                      type="button"
                      className="flex min-h-[30px] min-w-0 flex-1 cursor-pointer items-center gap-2 text-left font-semibold text-foreground"
                      onClick={() => {
                        toggleProject(p.id);
                        void openProject(p.id);
                      }}
                    >
                      <ChevRight
                        className={cn(
                          "size-2.5 shrink-0 text-muted-foreground/50 transition-transform duration-150",
                          open && "rotate-90",
                        )}
                      />
                      <span
                        className="size-2.5 shrink-0 rounded-[3px]"
                        style={{ background: projectColor(idx) }}
                      />
                      <span
                        className={cn(
                          "truncate",
                          isActiveProject ? "text-primary" : "text-foreground",
                        )}
                      >
                        {p.name}
                      </span>
                    </button>
                    {(projects as Project[]).length > 1 && (
                      <button
                        type="button"
                        onClick={() => void deleteProject(p.id)}
                        className="p-0.5 text-muted-foreground/40 opacity-0 transition-all hover:text-destructive group-hover/proj:opacity-100"
                        title="Delete project"
                      >
                        <IconTrash />
                      </button>
                    )}
                  </div>
                  {open && projectSessions.length > 0 && (
                    <div className="ml-[30px] mt-1 grid gap-1 border-l border-border/70 pl-3">
                      {projectSessions.map((s) => {
                        const isActiveSess = activeSessionId === s.id;
                        return (
                          <button
                            key={s.id}
                            type="button"
                            onClick={() =>
                              void navigate({
                                to: "/agents/$agentId/projects/$projectId/sessions/$sessionId",
                                params: { agentId, projectId: p.id, sessionId: s.id },
                              })
                            }
                            className={cn(
                              "flex h-[25px] items-center rounded-[7px] px-2 text-left text-[11.5px] transition-colors",
                              isActiveSess
                                ? "bg-primary/10 text-primary"
                                : "text-muted-foreground hover:bg-primary/[0.07] hover:text-primary",
                            )}
                          >
                            {sessionTitle(s)}
                          </button>
                        );
                      })}
                    </div>
                  )}
                </div>
              );
            })}
          </div>
        </div>
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

        {/* ── Archive ──────────────────────────────────────────────────── */}
        <div>
          <SectionLabel>Archive</SectionLabel>
          <button
            type="button"
            onClick={() => setArchiveOpen((v) => !v)}
            className="flex h-8 w-full items-center gap-2 rounded-[10px] px-2.5 text-left text-[12px] text-muted-foreground transition-colors hover:bg-foreground/[0.045] hover:text-foreground"
          >
            <ChevRight
              className={cn(
                "size-2.5 shrink-0 text-muted-foreground/40 transition-transform duration-150",
                archiveOpen && "rotate-90",
              )}
            />
            <span className="flex-1">Archived sessions</span>
            <span className="font-mono text-[10px] text-muted-foreground/50">{archivedCount}</span>
          </button>
          {archiveOpen && (
            <div className="mt-1">
              <div className="mb-1 flex items-center gap-1 px-2">
                <select
                  value={historyKind}
                  onChange={(e) => setHistoryKind(e.target.value)}
                  className="rounded border border-border/50 bg-muted/50 px-2 py-0.5 font-mono text-xs text-muted-foreground focus:outline-none"
                >
                  <option value="">all</option>
                  <option value="chat">chat</option>
                  <option value="scheduler">scheduler</option>
                  <option value="task">task</option>
                </select>
              </div>
              {chatSessions.map((s) => (
                <button
                  key={s.id}
                  type="button"
                  onClick={() =>
                    void navigate({
                      to: "/agents/$agentId/sessions/$sessionId",
                      params: { agentId, sessionId: s.id },
                    })
                  }
                  className={cn(
                    "w-full truncate rounded-[9px] px-3 py-[7px] text-left text-[12px] leading-snug transition-colors",
                    activeSessionId === s.id
                      ? "bg-primary/10 text-primary"
                      : "text-muted-foreground hover:bg-foreground/[0.045] hover:text-foreground",
                  )}
                >
                  {sessionTitle(s)}
                </button>
              ))}
              {chatSessions.length === 0 && (
                <p className="px-3 py-2 font-mono text-xs text-muted-foreground">
                  {t("sessions.sidebar.noChats")}
                </p>
              )}
              {sessionsQuery.isLoading && (
                <div className="flex items-center gap-2 px-3 py-1.5">
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
                  className="w-full px-3 py-1.5 text-left font-mono text-xs text-muted-foreground transition-colors hover:text-foreground"
                >
                  {t("sessions.sidebar.loadMore")}
                </button>
              )}
              <div className="flex justify-end px-1 py-1">
                <button
                  type="button"
                  onClick={() => void createSession()}
                  className="flex items-center gap-1 font-mono text-[11px] text-muted-foreground/60 transition-colors hover:text-foreground"
                >
                  <IconPlus />
                  New
                </button>
              </div>
            </div>
          )}
        </div>
      </div>
    </aside>
  );
}

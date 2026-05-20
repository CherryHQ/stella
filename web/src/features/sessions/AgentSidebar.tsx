import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { useInfiniteQuery, useQuery, useQueryClient } from "@tanstack/react-query";
import type { Agent, Session, Project } from "@/lib/types";
import { api } from "@/lib/api";
import { formatTime } from "@/lib/time";
import { cn } from "@/lib/utils";
import { useI18n } from "@/lib/i18n";
import { sessionsInfiniteQueryOptions } from "@/lib/queries/sessions";
import { agentSkillsOptions, agentMemoriesOptions } from "@/lib/queries/agents";
import { agentProjectsOptions } from "@/lib/queries/projects";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogPopup,
  DialogTitle,
  DialogFooter,
  DialogHeader,
  DialogDescription,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";

interface Props {
  agents: Agent[];
  agentId: string;
  pathname: string;
  onAgentChange: (id: string) => void;
}

// ── helpers ──────────────────────────────────────────────────────────────────

const AVATAR_COLORS = ["#e07340", "#5b8cff", "#4fc98e", "#b06ef5", "#e8b84b", "#e05050", "#3bc9db"];

function agentColor(id: string): string {
  let h = 0;
  for (let i = 0; i < id.length; i++) h = (h * 31 + id.charCodeAt(i)) & 0xffffff;
  return AVATAR_COLORS[h % AVATAR_COLORS.length];
}

function sessionTitle(s: Session): string {
  return s.title || "Untitled";
}

// ── icons ────────────────────────────────────────────────────────────────────

function ChevRight({ className }: { className?: string }) {
  return (
    <svg
      className={className}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2.5"
    >
      <path d="m9 18 6-6-6-6" />
    </svg>
  );
}

function IconChat() {
  return (
    <svg
      className="w-3 h-3"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
    >
      <path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z" />
    </svg>
  );
}

function IconClock() {
  return (
    <svg
      className="w-3 h-3"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
    >
      <circle cx="12" cy="12" r="10" />
      <polyline points="12 6 12 12 16 14" />
    </svg>
  );
}

function IconTask() {
  return (
    <svg
      className="w-3 h-3"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
    >
      <path d="m9 11 3 3L22 4" />
      <path d="M21 12v7a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11" />
    </svg>
  );
}

function IconStar() {
  return (
    <svg
      className="w-3 h-3"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
    >
      <path d="m12 3-1.912 5.813a2 2 0 0 1-1.275 1.275L3 12l5.813 1.912a2 2 0 0 1 1.275 1.275L12 21l1.912-5.813a2 2 0 0 1 1.275-1.275L21 12l-5.813-1.912a2 2 0 0 1-1.275-1.275L12 3z" />
    </svg>
  );
}

function IconBrain() {
  return (
    <svg
      className="w-3 h-3"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
    >
      <path d="M9.5 2A2.5 2.5 0 0 1 12 4.5v15a2.5 2.5 0 0 1-4.96-.44 2.5 2.5 0 0 1-2.96-3.08 3 3 0 0 1-.34-5.58 2.5 2.5 0 0 1 1.32-4.24 2.5 2.5 0 0 1 1.44-1.17A2.5 2.5 0 0 1 9.5 2z" />
      <path d="M14.5 2A2.5 2.5 0 0 0 12 4.5v15a2.5 2.5 0 0 0 4.96-.44 2.5 2.5 0 0 0 2.96-3.08 3 3 0 0 0 .34-5.58 2.5 2.5 0 0 0-1.32-4.24 2.5 2.5 0 0 0-1.44-1.17A2.5 2.5 0 0 0 14.5 2z" />
    </svg>
  );
}

function IconPlus() {
  return (
    <svg
      className="w-2.5 h-2.5"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2.5"
    >
      <path d="M12 5v14M5 12h14" />
    </svg>
  );
}

function IconHome() {
  return (
    <svg
      className="w-3 h-3"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
    >
      <path d="m3 9 9-7 9 7v11a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" />
      <polyline points="9 22 9 12 15 12 15 22" />
    </svg>
  );
}

function IconFolder() {
  return (
    <svg
      className="w-3 h-3"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
    >
      <path d="M4 20h16a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.93a2 2 0 0 1-1.66-.9l-.82-1.2A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13c0 1.1.9 2 2 2z" />
    </svg>
  );
}

function IconTrash() {
  return (
    <svg
      className="w-3 h-3"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
    >
      <path d="M3 6h18" />
      <path d="M19 6v14c0 1-1 2-2 2H7c-1 0-2-1-2-2V6" />
      <path d="M8 6V4c0-1 1-2 2-2h4c1 0 2 1 2 2v2" />
    </svg>
  );
}

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
    return <p className="text-xs text-muted-foreground px-2 py-3">Loading folders…</p>;
  }
  if (dirs.length === 0) {
    return <p className="text-xs text-muted-foreground px-2 py-3">No folders found</p>;
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
              "flex items-center gap-1.5 px-2 py-1 cursor-pointer rounded text-[12px] transition-colors",
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
                className="p-0 flex-shrink-0"
                onClick={(e) => {
                  e.stopPropagation();
                  toggle(d.path);
                }}
              >
                <ChevRight
                  className={cn(
                    "w-2.5 h-2.5 text-muted-foreground/50 transition-transform",
                    expanded.has(d.path) && "rotate-90",
                  )}
                />
              </button>
            ) : (
              <span className="w-2.5" />
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
        <div className="px-6 py-2 space-y-3">
          {sessionId ? (
            <div className="border border-border rounded-lg overflow-hidden">
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
            <label className="text-xs text-muted-foreground mb-1 block">Project name</label>
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

// ── sub-components ───────────────────────────────────────────────────────────

function SectionHeader({
  icon,
  label,
  open,
  active,
  onToggle,
}: {
  icon: React.ReactNode;
  label: string;
  open?: boolean;
  active?: boolean;
  onToggle: () => void;
}) {
  const expandable = open !== undefined;
  return (
    <div
      className={cn(
        "group mx-3 flex cursor-pointer select-none items-center gap-2 rounded-[10px] px-2.5 py-2 transition-all duration-150",
        active
          ? "bg-accent text-accent-foreground"
          : "text-muted-foreground hover:bg-foreground/[0.045] hover:text-foreground",
      )}
      onClick={onToggle}
    >
      <span
        className={cn(
          "flex-shrink-0 transition-colors",
          active ? "text-primary" : "text-muted-foreground/70 group-hover:text-foreground",
        )}
      >
        {icon}
      </span>
      <span
        className={cn(
          "flex-1 text-[10px] font-mono font-semibold uppercase tracking-[0.08em] transition-colors",
          active
            ? "text-accent-foreground"
            : "text-muted-foreground/75 group-hover:text-foreground",
        )}
      >
        {label}
      </span>
      {expandable && (
        <ChevRight
          className={cn(
            "w-2.5 h-2.5 flex-shrink-0 text-muted-foreground/40 transition-transform duration-150",
            open && "rotate-90",
          )}
        />
      )}
    </div>
  );
}

function NavRow({
  active,
  icon,
  title,
  sub,
  meta,
  onClick,
  indent,
}: {
  active: boolean;
  icon?: React.ReactNode;
  title: string;
  sub?: string;
  meta?: string;
  onClick: () => void;
  indent?: boolean;
}) {
  return (
    <div
      onClick={onClick}
      className={cn(
        "mx-3 flex cursor-pointer items-center gap-2 rounded-[10px] px-2.5 py-1.5 transition-all duration-150",
        indent && "ml-8 border-l border-border/80 pl-3",
        active
          ? "bg-accent text-accent-foreground"
          : "text-muted-foreground hover:bg-foreground/[0.045] hover:text-foreground",
      )}
    >
      {icon && (
        <span className={cn("flex-shrink-0", active ? "text-primary" : "text-muted-foreground/70")}>
          {icon}
        </span>
      )}
      <div className="flex-1 min-w-0">
        <p
          className={cn(
            "text-[12px] truncate leading-snug",
            active ? "font-semibold text-accent-foreground" : "text-foreground/80",
          )}
        >
          {title}
        </p>
        {sub && (
          <p className="text-[10px] font-mono text-muted-foreground/50 truncate mt-0.5">{sub}</p>
        )}
      </div>
      {meta && (
        <span className="flex-shrink-0 text-[10px] font-mono text-muted-foreground/50">{meta}</span>
      )}
    </div>
  );
}

function SubFolder({
  label,
  count,
  open,
  onToggle,
  indent = 6,
}: {
  label: string;
  count: number;
  open: boolean;
  onToggle: () => void;
  indent?: number;
}) {
  return (
    <div
      className="group mx-3 flex cursor-pointer select-none items-center gap-1.5 rounded-md py-1 transition-colors duration-150 hover:bg-foreground/[0.035]"
      style={{ paddingLeft: `${indent * 4}px`, paddingRight: "10px" }}
      onClick={onToggle}
    >
      <ChevRight
        className={cn(
          "w-2 h-2 flex-shrink-0 text-muted-foreground/40 transition-transform duration-150",
          open && "rotate-90",
        )}
      />
      <svg
        className="w-3 h-3 flex-shrink-0 text-muted-foreground/50"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth="2"
      >
        <path d="M4 20h16a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.93a2 2 0 0 1-1.66-.9l-.82-1.2A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13c0 1.1.9 2 2 2z" />
      </svg>
      <span className="flex-1 text-[11px] text-muted-foreground/70 group-hover:text-foreground transition-colors truncate">
        {label}
      </span>
      <span className="text-[10px] font-mono text-muted-foreground/50 tabular-nums">{count}</span>
    </div>
  );
}

// ── main component ───────────────────────────────────────────────────────────

export function AgentSidebar({ agents, agentId, pathname, onAgentChange }: Props) {
  const { t } = useI18n();
  const navigate = useNavigate();
  const queryClient = useQueryClient();

  const [switcherOpen, setSwitcherOpen] = useState(false);
  const [search, setSearch] = useState("");
  const [openSections, setOpenSections] = useState<string[]>(["project"]);
  const [openFolders, setOpenFolders] = useState<string[]>([]);
  const [historyKind, setHistoryKind] = useState("");
  const [showCreateProject, setShowCreateProject] = useState(false);
  const listRef = useRef<HTMLDivElement>(null);

  const isFolderOpen = (key: string) => openFolders.includes(key);
  const toggleFolder = (key: string) =>
    setOpenFolders((prev) => (prev.includes(key) ? prev.filter((k) => k !== key) : [...prev, key]));

  const isOpen = (key: string) => openSections.includes(key);
  const toggleSection = (key: string) =>
    setOpenSections((prev) =>
      prev.includes(key) ? prev.filter((k) => k !== key) : [...prev, key],
    );

  // ── data ─────────────────────────────────────────────────────────────────
  const sessionsQuery = useInfiniteQuery(sessionsInfiniteQueryOptions(agentId));
  const sessions = sessionsQuery.data?.pages.flat() ?? [];

  const { data: skills = [] } = useQuery(agentSkillsOptions(agentId));
  const { data: memories = [] } = useQuery(agentMemoriesOptions(agentId));
  const { data: projects = [] } = useQuery(agentProjectsOptions(agentId));

  // ── active route detection ───────────────────────────────────────────────
  const isActive = (path: string) => pathname === path || pathname.startsWith(path + "/");
  const activeSessionId = pathname.match(/\/sessions\/([^/]+)/)?.[1] ?? "";
  const activeProjectId = pathname.match(/\/projects\/([^/]+)/)?.[1] ?? "";

  // ── derived lists ────────────────────────────────────────────────────────
  const selectedAgent = agents.find((a) => a.id === agentId);

  // The "home" session for the agent — prefer main, fall back to first chat.
  // Used by the home row so it navigates directly without a round-trip through AgentHome.
  const homeSession = useMemo(() => {
    const active = sessions.filter((s) => !s.archived);
    return active.find((s) => s.kind === "main") ?? active[0] ?? null;
  }, [sessions]);

  const chatSessions = useMemo(() => {
    const base = sessions.filter((s) => {
      if (s.archived) return false;
      if (s.id === "main") return false;
      if (historyKind) return s.kind === historyKind;
      return true;
    });
    if (search)
      return base.filter((s) => sessionTitle(s).toLowerCase().includes(search.toLowerCase()));
    return base;
  }, [sessions, search, historyKind]);

  const filteredSkills = useMemo(() => {
    if (!search) return skills;
    return skills.filter(
      (s) =>
        s.name.toLowerCase().includes(search.toLowerCase()) ||
        s.description.toLowerCase().includes(search.toLowerCase()),
    );
  }, [skills, search]);

  const agentSkills = useMemo(
    () => filteredSkills.filter((s) => s.scope === "agent"),
    [filteredSkills],
  );
  const userSkills = useMemo(
    () => filteredSkills.filter((s) => s.scope === "user"),
    [filteredSkills],
  );
  const systemSkills = useMemo(
    () => filteredSkills.filter((s) => s.scope === "system"),
    [filteredSkills],
  );

  const userMemory = useMemo(
    () => memories.find((m) => m.agent_id === agentId),
    [memories, agentId],
  );

  // ── create session ───────────────────────────────────────────────────────
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

  // ── close switcher on outside click ──────────────────────────────────────
  const switcherRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!switcherOpen) return;
    const handler = (e: MouseEvent) => {
      if (switcherRef.current && !switcherRef.current.contains(e.target as Node)) {
        setSwitcherOpen(false);
      }
    };
    document.addEventListener("mousedown", handler);
    return () => document.removeEventListener("mousedown", handler);
  }, [switcherOpen]);

  return (
    <aside className="flex flex-col overflow-hidden w-full h-full">
      {/* Agents */}
      <div ref={switcherRef} className="flex-shrink-0 px-3 pt-3">
        <div className="px-2 pb-1 text-[10px] font-mono uppercase tracking-[0.08em] text-muted-foreground/70">
          Agents
        </div>
        <div className="grid gap-1">
          {agents.map((ag) => {
            const c = agentColor(ag.id);
            const isCur = ag.id === agentId;
            return (
              <button
                key={ag.id}
                type="button"
                onClick={() => onAgentChange(ag.id)}
                className={cn(
                  "grid min-h-12 grid-cols-[2rem_minmax(0,1fr)_auto] items-center gap-2 rounded-[14px] px-2 py-1.5 text-left transition-all duration-150",
                  isCur
                    ? "bg-card text-foreground shadow-[0_0_0_1px_var(--border),0_6px_18px_rgba(29,29,31,0.04)]"
                    : "text-muted-foreground hover:bg-foreground/[0.045] hover:text-foreground",
                )}
              >
                <span
                  className="grid size-8 place-items-center rounded-[10px] text-[13px] font-bold text-white shadow-sm"
                  style={{ background: c }}
                >
                  {ag.name[0]?.toUpperCase()}
                </span>
                <span className="min-w-0">
                  <span className="block truncate text-[13px] font-semibold tracking-[-0.01em] text-foreground">
                    {ag.name}
                  </span>
                  <span className="block truncate font-mono text-[10.5px] leading-snug text-muted-foreground">
                    {(ag as { model?: string }).model || "Main session"}
                  </span>
                </span>
                {isCur && (
                  <span className="size-2 rounded-full bg-primary shadow-[0_0_0_3px_color-mix(in_oklch,var(--primary)_22%,transparent)]" />
                )}
              </button>
            );
          })}
          {agents.length === 0 && (
            <p className="px-2 py-2 text-xs text-muted-foreground">No agents yet.</p>
          )}
        </div>
      </div>

      {/* Search */}
      <div className="flex-shrink-0 px-3 pt-3">
        <div className="relative">
          <input
            type="text"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder={t("sessions.sidebar.search")}
            className="w-full rounded-full border border-border/70 bg-card/70 py-1.5 pr-3 pl-7 font-mono text-xs text-foreground shadow-sm transition-all duration-150 placeholder:text-muted-foreground/50 hover:bg-card focus:border-primary/40 focus:outline-none"
          />
          <svg
            className="absolute left-2 top-1/2 -translate-y-1/2 w-3 h-3 text-muted-foreground/50 pointer-events-none"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth="2.5"
          >
            <circle cx="11" cy="11" r="8" />
            <path d="m21 21-4.35-4.35" />
          </svg>
        </div>
      </div>

      {/* Scrollable nav */}
      <div ref={listRef} className="flex-1 overflow-y-auto pb-2" onScroll={handleScroll}>
        {/* Home row — standalone, above Tasks */}
        {!search && (
          <NavRow
            active={
              homeSession ? activeSessionId === homeSession.id : isActive(`/agents/${agentId}`)
            }
            icon={<IconHome />}
            title={selectedAgent?.name ?? "Home"}
            onClick={() => {
              const go = async () => {
                let sid = homeSession?.id;
                if (!sid) {
                  const sess = await api<Session>("POST", "/api/sessions", {
                    agent_id: agentId,
                    kind: "main",
                  });
                  await queryClient.invalidateQueries({ queryKey: ["sessions", agentId] });
                  sid = sess.id;
                }
                void navigate({
                  to: "/agents/$agentId/sessions/$sessionId",
                  params: { agentId, sessionId: sid },
                });
              };
              void go();
            }}
          />
        )}

        {/* Workspace */}
        {!search && (
          <div>
            <div className="px-5 pt-4 pb-1 text-[10px] font-mono uppercase tracking-[0.08em] text-muted-foreground/70">
              Workspace
            </div>
            <SectionHeader
              icon={<IconTask />}
              label={t("sessions.sidebar.tasks")}
              active={isActive(`/agents/${agentId}/tasks`)}
              onToggle={() => void navigate({ to: "/agents/$agentId/tasks", params: { agentId } })}
            />
            <SectionHeader
              icon={<IconClock />}
              label={t("sessions.sidebar.automations")}
              active={isActive(`/agents/${agentId}/automations`)}
              onToggle={() =>
                void navigate({ to: "/agents/$agentId/automations", params: { agentId } })
              }
            />
          </div>
        )}

        {/* Projects */}
        {!search && (
          <div>
            <div className="flex items-center">
              <div className="flex-1">
                <SectionHeader
                  icon={<IconFolder />}
                  label="Project folders"
                  open={isOpen("project")}
                  onToggle={() => toggleSection("project")}
                />
              </div>
              <button
                type="button"
                onClick={(e) => {
                  e.stopPropagation();
                  setShowCreateProject(true);
                }}
                className="mr-3 p-0.5 text-muted-foreground/40 hover:text-foreground transition-colors"
                title="New project"
              >
                <IconPlus />
              </button>
            </div>
            {isOpen("project") &&
              (projects as Project[]).map((p) => {
                return (
                  <div key={p.id} className="group/proj flex items-center">
                    <div className="flex-1 min-w-0">
                      <NavRow
                        active={activeProjectId === p.id}
                        icon={<IconFolder />}
                        title={p.name}
                        indent
                        onClick={() => void openProject(p.id)}
                      />
                    </div>
                    {(projects as Project[]).length > 1 && (
                      <button
                        type="button"
                        onClick={() => void deleteProject(p.id)}
                        className="mr-3 p-0.5 opacity-0 group-hover/proj:opacity-100 text-muted-foreground/40 hover:text-destructive transition-all"
                        title="Delete project"
                      >
                        <IconTrash />
                      </button>
                    )}
                  </div>
                );
              })}
          </div>
        )}
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

        {/* Skills */}
        {(filteredSkills.length > 0 || !search) && (
          <div>
            <SectionHeader
              icon={<IconStar />}
              label={t("sessions.sidebar.skills")}
              open={isOpen("skill")}
              onToggle={() => toggleSection("skill")}
            />
            {isOpen("skill") && (
              <>
                {(agentSkills.length > 0 || systemSkills.length > 0) && (
                  <>
                    <SubFolder
                      label={t("sessions.sidebar.builtin")}
                      count={agentSkills.length + systemSkills.length}
                      open={isFolderOpen("skill:builtin")}
                      onToggle={() => toggleFolder("skill:builtin")}
                    />
                    {isFolderOpen("skill:builtin") &&
                      [...systemSkills, ...agentSkills].map((s) => (
                        <NavRow
                          key={`${s.scope}:${s.id}`}
                          active={pathname.includes(`/skills/${s.id}`)}
                          icon={<IconStar />}
                          title={s.name}
                          sub={s.description}
                          indent
                          onClick={() =>
                            void navigate({
                              to: "/agents/$agentId/skills/$skillId",
                              params: { agentId, skillId: s.id },
                            })
                          }
                        />
                      ))}
                  </>
                )}
                {userSkills.map((s) => (
                  <NavRow
                    key={s.id}
                    active={pathname.includes(`/skills/${s.id}`)}
                    icon={<IconStar />}
                    title={s.name}
                    sub={s.description}
                    indent
                    onClick={() =>
                      void navigate({
                        to: "/agents/$agentId/skills/$skillId",
                        params: { agentId, skillId: s.id },
                      })
                    }
                  />
                ))}
                {userSkills.length === 0 &&
                  agentSkills.length === 0 &&
                  systemSkills.length === 0 && (
                    <p className="px-7 py-2 text-xs text-muted-foreground font-mono">
                      {t("sessions.sidebar.noSkills")}
                    </p>
                  )}
              </>
            )}
          </div>
        )}

        {/* Memory */}
        {!search && (
          <div>
            <SectionHeader
              icon={<IconBrain />}
              label={t("sessions.sidebar.memory")}
              open={isOpen("memory")}
              onToggle={() => toggleSection("memory")}
            />
            {isOpen("memory") && (
              <>
                <NavRow
                  active={isActive(`/agents/${agentId}/memories/soul`)}
                  icon={<IconBrain />}
                  title={t("sessions.sidebar.agentSoul")}
                  sub={t("sessions.sidebar.soulSubtitle")}
                  indent
                  onClick={() =>
                    void navigate({ to: "/agents/$agentId/memories/soul", params: { agentId } })
                  }
                />
                <NavRow
                  active={isActive(`/agents/${agentId}/memories/profile`)}
                  icon={<IconBrain />}
                  title={t("sessions.sidebar.userProfile")}
                  sub={
                    userMemory?.updated_at
                      ? formatTime(userMemory.updated_at)
                      : t("sessions.sidebar.noMemory")
                  }
                  indent
                  onClick={() =>
                    void navigate({
                      to: "/agents/$agentId/memories/profile",
                      params: { agentId },
                    })
                  }
                />
              </>
            )}
          </div>
        )}

        {/* History (was Chats - moved to bottom, collapsed by default) */}
        {(chatSessions.length > 0 || !search) && (
          <div>
            <SectionHeader
              icon={<IconChat />}
              label="Archive"
              open={isOpen("history")}
              onToggle={() => toggleSection("history")}
            />
            {isOpen("history") && (
              <>
                <div className="flex items-center gap-1 px-4 py-1">
                  <select
                    value={historyKind}
                    onChange={(e) => setHistoryKind(e.target.value)}
                    className="text-xs font-mono bg-muted/50 border border-border/50 rounded px-2 py-0.5 text-muted-foreground focus:outline-none"
                  >
                    <option value="">all</option>
                    <option value="chat">chat</option>
                    <option value="scheduler">scheduler</option>
                    <option value="task">task</option>
                  </select>
                </div>
                {chatSessions.map((s) => (
                  <NavRow
                    key={s.id}
                    active={activeSessionId === s.id}
                    icon={<IconChat />}
                    title={sessionTitle(s)}
                    meta={formatTime(s.last_active)}
                    indent
                    onClick={() =>
                      void navigate({
                        to: "/agents/$agentId/sessions/$sessionId",
                        params: { agentId, sessionId: s.id },
                      })
                    }
                  />
                ))}
                {chatSessions.length === 0 && (
                  <p className="px-7 py-2 text-xs text-muted-foreground font-mono">
                    {t("sessions.sidebar.noChats")}
                  </p>
                )}
                {sessionsQuery.isLoading && (
                  <div className="px-7 py-1.5 flex items-center gap-2">
                    <div className="w-3 h-3 border border-muted-foreground/30 border-t-muted-foreground/70 rounded-full animate-spin" />
                    <span className="text-xs font-mono text-muted-foreground">
                      {t("common.loading")}
                    </span>
                  </div>
                )}
                {sessionsQuery.hasNextPage && !sessionsQuery.isFetchingNextPage && (
                  <button
                    type="button"
                    onClick={() => void sessionsQuery.fetchNextPage()}
                    className="w-full px-7 py-1.5 text-xs text-muted-foreground hover:text-foreground font-mono text-left transition-colors"
                  >
                    {t("sessions.sidebar.loadMore")}
                  </button>
                )}
                <div className="flex justify-end px-3 py-1">
                  <button
                    type="button"
                    onClick={() => void createSession()}
                    className="flex items-center gap-1 text-[11px] font-mono text-muted-foreground/60 hover:text-foreground transition-colors"
                  >
                    <IconPlus />
                    New
                  </button>
                </div>
              </>
            )}
          </div>
        )}
      </div>
    </aside>
  );
}

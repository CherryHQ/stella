import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { useInfiniteQuery, useQuery, useQueryClient } from "@tanstack/react-query";
import type { Agent, Session, Project } from "@/lib/types";
import { api } from "@/lib/api";
import { formatTime } from "@/lib/time";
import { cn } from "@/lib/utils";
import { useI18n } from "@/lib/i18n";
import { sessionsInfiniteQueryOptions } from "@/lib/queries/sessions";
import {
  agentSchedulerJobsOptions,
  agentSkillsOptions,
  agentMemoriesOptions,
} from "@/lib/queries/agents";
import { agentProjectsOptions } from "@/lib/queries/projects";

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

function isTaskSession(s: Session): boolean {
  return s.source === "task";
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

function ChevDown({ className }: { className?: string }) {
  return (
    <svg
      className={className}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2.5"
    >
      <path d="m6 9 6 6 6-6" />
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

// ── sub-components ───────────────────────────────────────────────────────────

function SectionHeader({
  icon,
  label,
  count,
  open,
  active,
  onToggle,
  onAdd,
}: {
  icon: React.ReactNode;
  label: string;
  count: number;
  open?: boolean;
  active?: boolean;
  onToggle: () => void;
  onAdd?: () => void;
}) {
  const expandable = open !== undefined;
  return (
    <div
      className={cn(
        "flex items-center gap-1.5 px-3.5 pt-3 pb-1.5 cursor-pointer select-none group",
        active && "bg-sidebar-accent rounded-lg mx-2 px-3",
      )}
      onClick={onToggle}
    >
      {expandable ? (
        <ChevRight
          className={cn(
            "w-2.5 h-2.5 flex-shrink-0 text-muted-foreground/50 transition-transform duration-150",
            open && "rotate-90",
          )}
        />
      ) : (
        <span className="w-2.5 h-2.5 flex-shrink-0" />
      )}
      <span
        className={cn(
          "flex-shrink-0 transition-colors",
          active ? "text-primary" : "text-muted-foreground/60",
        )}
      >
        {icon}
      </span>
      <span
        className={cn(
          "flex-1 text-[10px] font-mono font-medium uppercase tracking-widest transition-colors",
          active ? "text-foreground" : "text-muted-foreground/70 group-hover:text-muted-foreground",
        )}
      >
        {label}
      </span>
      <span className="text-[10px] font-mono text-muted-foreground/50 tabular-nums">{count}</span>
      {onAdd && (
        <button
          type="button"
          onClick={(e) => {
            e.stopPropagation();
            onAdd();
          }}
          className="w-4 h-4 rounded flex items-center justify-center text-muted-foreground/40 hover:bg-muted hover:text-foreground transition-colors"
          title={`New ${label.toLowerCase().replace(/s$/, "")}`}
        >
          <IconPlus />
        </button>
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
}: {
  active: boolean;
  icon?: React.ReactNode;
  title: string;
  sub?: string;
  meta?: string;
  onClick: () => void;
}) {
  return (
    <div
      onClick={onClick}
      className={cn(
        "flex items-center gap-2 mx-2 px-2.5 py-1.5 rounded-lg cursor-pointer transition-all duration-150",
        active ? "bg-sidebar-accent" : "hover:bg-muted/50",
      )}
    >
      {icon && (
        <span className={cn("flex-shrink-0", active ? "text-primary" : "text-muted-foreground/60")}>
          {icon}
        </span>
      )}
      <div className="flex-1 min-w-0">
        <p
          className={cn(
            "text-[12px] truncate leading-snug",
            active ? "text-foreground font-medium" : "text-foreground/80",
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
      className="flex items-center gap-1.5 cursor-pointer select-none group py-1 mx-2 rounded-md hover:bg-muted/30 transition-colors duration-150"
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
  const [historySource, setHistorySource] = useState("");
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

  const { data: schedulerJobs = [] } = useQuery(agentSchedulerJobsOptions(agentId));
  const { data: skills = [] } = useQuery(agentSkillsOptions(agentId));
  const { data: memories = [] } = useQuery(agentMemoriesOptions(agentId));
  const { data: projects = [] } = useQuery(agentProjectsOptions(agentId));

  // ── active route detection ───────────────────────────────────────────────
  const isActive = (path: string) => pathname === path || pathname.startsWith(path + "/");
  const activeSessionId = pathname.match(/\/agents\/[^/]+\/sessions\/([^/]+)/)?.[1] ?? "";

  // ── derived lists ────────────────────────────────────────────────────────
  const selectedAgent = agents.find((a) => a.id === agentId);
  const color = selectedAgent ? agentColor(selectedAgent.id) : "#888";

  const chatSessions = useMemo(() => {
    const base = sessions.filter((s) => {
      if (s.archived) return false;
      if (s.id === "main") return false;
      if (historySource) return s.source === historySource;
      return true;
    });
    if (search)
      return base.filter((s) => sessionTitle(s).toLowerCase().includes(search.toLowerCase()));
    return base;
  }, [sessions, search, historySource]);

  const taskSessions = useMemo(
    () => sessions.filter((s) => !s.archived && isTaskSession(s)),
    [sessions],
  );

  const filteredJobs = useMemo(() => {
    if (!search) return schedulerJobs;
    return schedulerJobs.filter((j) => j.name.toLowerCase().includes(search.toLowerCase()));
  }, [schedulerJobs, search]);

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
      {/* Agent card */}
      <div ref={switcherRef} className="flex-shrink-0 mx-2.5 mt-2.5 relative">
        <div
          className={cn(
            "rounded-xl cursor-pointer overflow-hidden transition-colors",
            switcherOpen ? "bg-sidebar-accent" : "hover:bg-muted/40",
          )}
        >
          <div
            className="flex items-center gap-2.5 px-3 py-2.5 transition-colors"
            onClick={() => setSwitcherOpen((v) => !v)}
          >
            <div
              className="w-8 h-8 rounded-lg flex-shrink-0 flex items-center justify-center text-[13px] font-semibold font-mono"
              style={{ background: `${color}1a`, color }}
            >
              {selectedAgent?.name?.[0]?.toUpperCase() ?? "?"}
            </div>
            <div className="flex-1 min-w-0">
              <p className="text-[13px] font-semibold truncate leading-snug">
                {selectedAgent?.name ?? "Select agent"}
              </p>
              <p className="text-xs font-mono text-muted-foreground truncate">
                {(selectedAgent as { model?: string })?.model ?? ""}
              </p>
            </div>
            <ChevDown
              className={cn(
                "w-3 h-3 flex-shrink-0 text-muted-foreground/50 transition-transform duration-150",
                switcherOpen && "rotate-180",
              )}
            />
          </div>

          {switcherOpen && (
            <div className="border-t border-border/50 p-1.5 flex flex-col gap-0.5">
              {agents.map((ag) => {
                const c = agentColor(ag.id);
                const isCur = ag.id === agentId;
                return (
                  <div
                    key={ag.id}
                    onClick={() => {
                      onAgentChange(ag.id);
                      setSwitcherOpen(false);
                    }}
                    className={cn(
                      "flex items-center gap-2 px-2 py-1.5 rounded-md cursor-pointer transition-colors",
                      isCur ? "bg-primary/5" : "hover:bg-muted/60",
                    )}
                  >
                    <div
                      className="w-6 h-6 rounded-md flex-shrink-0 flex items-center justify-center text-[11px] font-semibold font-mono"
                      style={{ background: `${c}1a`, color: c }}
                    >
                      {ag.name[0]?.toUpperCase()}
                    </div>
                    <div className="flex-1 min-w-0">
                      <p className="text-[12px] font-medium truncate">{ag.name}</p>
                      <p className="text-xs font-mono text-muted-foreground truncate">
                        {(ag as { model?: string }).model ?? ""}
                      </p>
                    </div>
                    {isCur && (
                      <svg
                        className="w-3 h-3 flex-shrink-0 text-primary"
                        viewBox="0 0 24 24"
                        fill="none"
                        stroke="currentColor"
                        strokeWidth="3"
                      >
                        <path d="M20 6 9 17l-5-5" />
                      </svg>
                    )}
                  </div>
                );
              })}
            </div>
          )}
        </div>
      </div>

      {/* Search */}
      <div className="flex-shrink-0 px-2.5 pt-3">
        <div className="relative">
          <input
            type="text"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder={t("sessions.sidebar.search")}
            className="w-full pl-7 pr-3 py-1.5 text-xs font-mono rounded-lg bg-muted/50 border border-transparent hover:border-border focus:border-primary/40 focus:outline-none transition-all duration-150 text-foreground placeholder:text-muted-foreground/50"
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
        {/* Projects */}
        {!search && (
          <div>
            <SectionHeader
              icon={<IconFolder />}
              label="Projects"
              count={projects.length + 1}
              open={isOpen("project")}
              onToggle={() => toggleSection("project")}
            />
            {isOpen("project") && (
              <>
                <NavRow
                  active={isActive(`/agents/${agentId}`)}
                  icon={<IconHome />}
                  title={selectedAgent?.name ?? "Home"}
                  onClick={() => void navigate({ to: "/agents/$agentId", params: { agentId } })}
                />
                {(projects as Project[]).map((p) => (
                  <NavRow
                    key={p.id}
                    active={false}
                    icon={<IconFolder />}
                    title={p.name}
                    onClick={() => void navigate({ to: "/agents/$agentId", params: { agentId } })}
                  />
                ))}
              </>
            )}
          </div>
        )}

        {/* Tasks & Automations */}
        {!search && (
          <div>
            <SectionHeader
              icon={<IconTask />}
              label={t("sessions.sidebar.tasks")}
              count={taskSessions.length}
              active={isActive(`/agents/${agentId}/tasks`)}
              onToggle={() => void navigate({ to: "/agents/$agentId/tasks", params: { agentId } })}
            />
            <SectionHeader
              icon={<IconClock />}
              label={t("sessions.sidebar.automations")}
              count={filteredJobs.length}
              active={isActive(`/agents/${agentId}/automations`)}
              onToggle={() =>
                void navigate({ to: "/agents/$agentId/automations", params: { agentId } })
              }
            />
          </div>
        )}

        {/* Skills */}
        {(filteredSkills.length > 0 || !search) && (
          <div>
            <SectionHeader
              icon={<IconStar />}
              label={t("sessions.sidebar.skills")}
              count={filteredSkills.length}
              open={isOpen("skill")}
              onToggle={() => toggleSection("skill")}
              onAdd={() =>
                void navigate({ to: "/agents/$agentId/skills/new", params: { agentId } })
              }
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
              count={memories.length}
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
              label="History"
              count={chatSessions.length}
              open={isOpen("history")}
              onToggle={() => toggleSection("history")}
            />
            {isOpen("history") && (
              <>
                <div className="flex items-center gap-1 px-4 py-1">
                  <select
                    value={historySource}
                    onChange={(e) => setHistorySource(e.target.value)}
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

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { Agent, SchedulerJob, Session, Skill, UserMemory } from "@/lib/types";
import { formatTime } from "@/lib/time";
import { cn } from "@/lib/utils";

export type PanelKind = "chat" | "auto" | "task" | "skill" | "memory" | "settings";
export interface PanelSel {
  kind: PanelKind;
  id: string;
}

interface Props {
  agents: Agent[];
  selectedAgentId: string;
  onAgentChange: (id: string) => void;
  panelSel: PanelSel;
  onSelect: (sel: PanelSel) => void;
  openSections: string[];
  onToggleSection: (key: string) => void;
  sessions: Session[];
  sessionsLoading: boolean;
  sessionsHasMore: boolean;
  onLoadMoreSessions: () => void;
  schedulerJobs: SchedulerJob[];
  skills: Skill[];
  memories: UserMemory[];
  onCreateSession: () => void;
  onNavigateSettings: () => void;
}

// ── helpers ──────────────────────────────────────────────────────────────────

const AVATAR_COLORS = ["#e07340", "#5b8cff", "#4fc98e", "#b06ef5", "#e8b84b", "#e05050", "#3bc9db"];

function agentColor(id: string): string {
  let h = 0;
  for (let i = 0; i < id.length; i++) h = (h * 31 + id.charCodeAt(i)) & 0xffffff;
  return AVATAR_COLORS[h % AVATAR_COLORS.length];
}

function isSchedulerSession(s: Session): boolean {
  return Boolean(s.id && (s.id.startsWith("scheduler:") || s.id.includes(":scheduler:")));
}

function isTaskSession(s: Session): boolean {
  return Boolean(s.id?.startsWith("task:") || s.channel === "task");
}

function sessionTitle(s: Session): string {
  return s.title || "Untitled";
}

// ── icon components ───────────────────────────────────────────────────────────

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

function IconSettings() {
  return (
    <svg
      className="w-3.5 h-3.5"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
    >
      <path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z" />
      <circle cx="12" cy="12" r="3" />
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

// ── sub-components ────────────────────────────────────────────────────────────

function SectionHeader({
  icon,
  label,
  count,
  open,
  onToggle,
  onAdd,
}: {
  icon: React.ReactNode;
  label: string;
  count: number;
  open: boolean;
  onToggle: () => void;
  onAdd?: () => void;
}) {
  return (
    <div
      className="flex items-center gap-1.5 px-2.5 pt-2 pb-1 cursor-pointer select-none group"
      onClick={onToggle}
    >
      <ChevRight
        className={cn(
          "w-2.5 h-2.5 flex-shrink-0 text-muted-foreground/40 transition-transform duration-150",
          open && "rotate-90",
        )}
      />
      <span className="text-muted-foreground/60 flex-shrink-0">{icon}</span>
      <span className="flex-1 text-[10px] font-mono font-semibold uppercase tracking-wider text-muted-foreground/60 group-hover:text-muted-foreground transition-colors">
        {label}
      </span>
      <span className="text-[10px] font-mono text-muted-foreground/40 tabular-nums">{count}</span>
      {onAdd && (
        <button
          type="button"
          onClick={(e) => {
            e.stopPropagation();
            onAdd();
          }}
          className="w-4 h-4 rounded flex items-center justify-center text-muted-foreground/40 hover:bg-muted hover:text-muted-foreground transition-colors"
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
  badge,
  onClick,
}: {
  active: boolean;
  icon?: React.ReactNode;
  title: string;
  sub?: string;
  meta?: string;
  badge?: React.ReactNode;
  onClick: () => void;
}) {
  return (
    <div
      onClick={onClick}
      className={cn(
        "relative flex items-center gap-2 px-7 py-1.5 cursor-pointer transition-colors",
        active ? "bg-primary/5" : "hover:bg-muted/60",
      )}
    >
      {active && <span className="absolute left-0 top-1 bottom-1 w-0.5 rounded-r bg-primary" />}
      {icon && (
        <span
          className={cn("flex-shrink-0", active ? "text-primary/80" : "text-muted-foreground/50")}
        >
          {icon}
        </span>
      )}
      <div className="flex-1 min-w-0">
        <p
          className={cn(
            "text-[12.5px] truncate leading-snug",
            active ? "text-foreground font-medium" : "text-muted-foreground",
          )}
        >
          {title}
        </p>
        {sub && (
          <p className="text-[10px] font-mono text-muted-foreground/50 truncate mt-0.5">{sub}</p>
        )}
      </div>
      {meta && (
        <span className="flex-shrink-0 text-[10px] font-mono text-muted-foreground/40">{meta}</span>
      )}
      {badge}
    </div>
  );
}

function StatusBadge({ on }: { on: boolean }) {
  return (
    <span
      className={cn(
        "inline-flex items-center h-4 px-1.5 rounded-full text-[9.5px] font-mono font-medium flex-shrink-0",
        on ? "bg-green-500/10 text-green-500" : "bg-muted text-muted-foreground/50",
      )}
    >
      {on ? "on" : "off"}
    </span>
  );
}

// ── main component ────────────────────────────────────────────────────────────

export function SessionSidebar({
  agents,
  selectedAgentId,
  onAgentChange,
  panelSel,
  onSelect,
  openSections,
  onToggleSection,
  sessions,
  sessionsLoading,
  sessionsHasMore,
  onLoadMoreSessions,
  schedulerJobs,
  skills,
  memories,
  onCreateSession,
  onNavigateSettings,
}: Props) {
  const [switcherOpen, setSwitcherOpen] = useState(false);
  const [search, setSearch] = useState("");
  const listRef = useRef<HTMLDivElement>(null);

  const selectedAgent = agents.find((a) => a.id === selectedAgentId);
  const color = selectedAgent ? agentColor(selectedAgent.id) : "#888";

  // close switcher on outside click
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

  // session classification
  const chatSessions = useMemo(
    () =>
      sessions.filter((s) => {
        if (s.archived) return false;
        if (isSchedulerSession(s) || isTaskSession(s)) return false;
        if (search) return sessionTitle(s).toLowerCase().includes(search.toLowerCase());
        return true;
      }),
    [sessions, search],
  );

  const taskSessions = useMemo(
    () =>
      sessions.filter((s) => {
        if (s.archived) return false;
        if (!isTaskSession(s)) return false;
        if (search) return sessionTitle(s).toLowerCase().includes(search.toLowerCase());
        return true;
      }),
    [sessions, search],
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

  // infinite scroll
  const handleScroll = useCallback(() => {
    const el = listRef.current;
    if (!el || !sessionsHasMore || sessionsLoading) return;
    if (el.scrollHeight - el.scrollTop - el.clientHeight < 120) {
      onLoadMoreSessions();
    }
  }, [sessionsHasMore, sessionsLoading, onLoadMoreSessions]);

  const isOpen = (key: string) => openSections.includes(key);

  return (
    <aside className="flex flex-col overflow-hidden w-full h-full bg-background">
      {/* Agent card */}
      <div ref={switcherRef} className="flex-shrink-0 mx-2.5 mt-2.5 relative">
        <div
          className={cn(
            "rounded-lg border border-border bg-card cursor-pointer overflow-hidden",
            switcherOpen && "border-border",
          )}
        >
          <div
            className="flex items-center gap-2.5 px-3 py-2.5 hover:bg-muted/40 transition-colors"
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
              <p className="text-[10.5px] font-mono text-muted-foreground truncate">
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

          {/* Switcher dropdown */}
          {switcherOpen && (
            <div className="border-t border-border bg-card/80 p-1.5 flex flex-col gap-0.5">
              {agents.map((ag) => {
                const c = agentColor(ag.id);
                const isCur = ag.id === selectedAgentId;
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
                      <p className="text-[10px] font-mono text-muted-foreground/60 truncate">
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

      {/* New chat + search */}
      <div className="flex-shrink-0 px-2.5 pt-2 space-y-1">
        <button
          type="button"
          onClick={onCreateSession}
          className="w-full flex items-center gap-2 px-3 py-1.5 rounded-md text-[12.5px] font-medium text-muted-foreground hover:bg-muted/60 hover:text-foreground transition-colors"
        >
          <svg
            className="w-3.5 h-3.5"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth="2"
          >
            <path d="M12 5v14M5 12h14" />
          </svg>
          New chat
        </button>
        <div className="relative">
          <input
            type="text"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Search…"
            className="w-full pl-7 pr-3 py-1.5 text-xs font-mono rounded-md bg-transparent border border-transparent hover:border-border focus:border-border focus:outline-none transition-colors text-foreground placeholder:text-muted-foreground/40"
          />
          <svg
            className="absolute left-2 top-1/2 -translate-y-1/2 w-3 h-3 text-muted-foreground/40 pointer-events-none"
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
        {/* Chats */}
        {(chatSessions.length > 0 || !search) && (
          <div>
            <SectionHeader
              icon={<IconChat />}
              label="Chats"
              count={chatSessions.length}
              open={isOpen("chat")}
              onToggle={() => onToggleSection("chat")}
            />
            {isOpen("chat") &&
              chatSessions.map((s) => (
                <NavRow
                  key={s.id}
                  active={panelSel.kind === "chat" && panelSel.id === s.id}
                  icon={<IconChat />}
                  title={sessionTitle(s)}
                  meta={formatTime(s.last_active)}
                  onClick={() => onSelect({ kind: "chat", id: s.id })}
                />
              ))}
            {isOpen("chat") && chatSessions.length === 0 && (
              <p className="px-7 py-2 text-[11px] text-muted-foreground/40 font-mono">
                No chats yet.
              </p>
            )}
            {isOpen("chat") && sessionsLoading && (
              <div className="px-7 py-1.5 flex items-center gap-2">
                <div className="w-3 h-3 border border-muted-foreground/30 border-t-muted-foreground/70 rounded-full animate-spin" />
                <span className="text-[10px] font-mono text-muted-foreground/40">Loading…</span>
              </div>
            )}
          </div>
        )}

        {/* Automations */}
        {(filteredJobs.length > 0 || !search) && (
          <div>
            <SectionHeader
              icon={<IconClock />}
              label="Automations"
              count={filteredJobs.length}
              open={isOpen("auto")}
              onToggle={() => onToggleSection("auto")}
              onAdd={() => onSelect({ kind: "auto", id: "new" })}
            />
            {isOpen("auto") &&
              filteredJobs.map((j) => (
                <NavRow
                  key={j.id}
                  active={panelSel.kind === "auto" && panelSel.id === j.id}
                  icon={<IconClock />}
                  title={j.name}
                  sub={j.cron || (j.every ? "every " + j.every : "")}
                  badge={<StatusBadge on={j.enabled} />}
                  onClick={() => onSelect({ kind: "auto", id: j.id })}
                />
              ))}
            {isOpen("auto") && filteredJobs.length === 0 && (
              <p className="px-7 py-2 text-[11px] text-muted-foreground/40 font-mono">
                No automations yet.
              </p>
            )}
          </div>
        )}

        {/* Tasks */}
        {taskSessions.length > 0 && (
          <div>
            <SectionHeader
              icon={<IconTask />}
              label="Tasks"
              count={taskSessions.length}
              open={isOpen("task")}
              onToggle={() => onToggleSection("task")}
            />
            {isOpen("task") &&
              taskSessions.map((s) => (
                <NavRow
                  key={s.id}
                  active={panelSel.kind === "task" && panelSel.id === s.id}
                  icon={<IconTask />}
                  title={sessionTitle(s)}
                  meta={formatTime(s.last_active)}
                  onClick={() => onSelect({ kind: "task", id: s.id })}
                />
              ))}
          </div>
        )}

        {/* Skills */}
        {(filteredSkills.length > 0 || !search) && (
          <div>
            <SectionHeader
              icon={<IconStar />}
              label="Skills"
              count={filteredSkills.length}
              open={isOpen("skill")}
              onToggle={() => onToggleSection("skill")}
              onAdd={() => onSelect({ kind: "skill", id: "new" })}
            />
            {isOpen("skill") &&
              filteredSkills.map((s) => (
                <NavRow
                  key={s.id}
                  active={panelSel.kind === "skill" && panelSel.id === s.id}
                  icon={<IconStar />}
                  title={s.name}
                  sub={s.scope}
                  badge={
                    <span
                      className={cn(
                        "inline-flex items-center h-4 px-1.5 rounded-full text-[9.5px] font-mono font-medium flex-shrink-0",
                        s.scope === "agent"
                          ? "bg-blue-500/10 text-blue-500"
                          : s.scope === "user"
                            ? "bg-green-500/10 text-green-500"
                            : "bg-muted text-muted-foreground/60",
                      )}
                    >
                      {s.scope === "system" ? "built-in" : s.scope}
                    </span>
                  }
                  onClick={() => onSelect({ kind: "skill", id: s.id })}
                />
              ))}
            {isOpen("skill") && filteredSkills.length === 0 && (
              <p className="px-7 py-2 text-[11px] text-muted-foreground/40 font-mono">
                No skills yet.
              </p>
            )}
          </div>
        )}

        {/* Memory */}
        {!search && (
          <div>
            <SectionHeader
              icon={<IconBrain />}
              label="Memory"
              count={memories.length}
              open={isOpen("memory")}
              onToggle={() => onToggleSection("memory")}
            />
            {isOpen("memory") && (
              <NavRow
                active={panelSel.kind === "memory"}
                icon={<IconBrain />}
                title="Agent memory"
                sub={
                  memories[0]?.updated_at
                    ? "Updated " + formatTime(memories[0].updated_at)
                    : "No memory yet"
                }
                onClick={() => onSelect({ kind: "memory", id: selectedAgentId })}
              />
            )}
          </div>
        )}
      </div>

      {/* Footer */}
      <div className="flex-shrink-0 border-t border-border px-2.5 py-1.5 space-y-0.5">
        <button
          type="button"
          onClick={() => onSelect({ kind: "settings", id: selectedAgentId })}
          className={cn(
            "w-full flex items-center gap-2.5 px-3 py-1.5 rounded-md text-[12.5px] transition-colors",
            panelSel.kind === "settings"
              ? "text-primary bg-primary/5"
              : "text-muted-foreground hover:bg-muted/60 hover:text-foreground",
          )}
        >
          <IconSettings />
          Agent Settings
        </button>
        <button
          type="button"
          onClick={onNavigateSettings}
          className="w-full flex items-center gap-2.5 px-3 py-1.5 rounded-md text-[12.5px] text-muted-foreground hover:bg-muted/60 hover:text-foreground transition-colors"
        >
          <svg
            className="w-3.5 h-3.5"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.8"
          >
            <circle cx="12" cy="12" r="3" />
            <path d="M12 1v2M12 21v2M4.2 4.2l1.4 1.4M18.4 18.4l1.4 1.4M1 12h2M21 12h2M4.2 19.8l1.4-1.4M18.4 5.6l1.4-1.4" />
          </svg>
          App Settings
        </button>
      </div>
    </aside>
  );
}

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { Agent, Session } from "@/lib/types";
import { formatTime } from "@/lib/time";
import { cn } from "@/lib/utils";
import { buttonVariants } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
} from "@/components/ui/menu";

function channelLabel(ch: string | null | undefined): string {
  if (!ch) return "";
  const m = ch.match(/:channel:([^:]+)/);
  return m ? m[1] : ch;
}

interface Props {
  sessions: Session[];
  sessionsLoading: boolean;
  sessionsHasMore: boolean;
  selectedID: string | undefined;
  agents: Agent[];
  onSelect: (id: string) => void;
  onLoadMore: () => void;
  onCreateSession: (agentID: string) => Promise<void>;
}

export function SessionSidebar({
  sessions,
  sessionsLoading,
  sessionsHasMore,
  selectedID,
  agents,
  onSelect,
  onLoadMore,
  onCreateSession,
}: Props) {
  const [searchQuery, setSearchQuery] = useState("");
  const [selectedAgent, setSelectedAgent] = useState("");
  const [showArchived, setShowArchived] = useState(false);
  const [showScheduler, setShowScheduler] = useState(false);
  const listRef = useRef<HTMLDivElement>(null);

  const filteredSessions = useMemo(
    () =>
      sessions.filter((s) => {
        if (!showArchived && s.archived) return false;
        if (
          !showScheduler &&
          s.id &&
          (s.id.startsWith("scheduler:") || s.id.includes(":scheduler:"))
        )
          return false;
        if (selectedAgent && s.agent_id !== selectedAgent) return false;
        if (searchQuery.trim()) {
          const q = searchQuery.toLowerCase();
          return (
            (s.title || "").toLowerCase().includes(q) ||
            (s.channel || "").toLowerCase().includes(q) ||
            (s.agent_id || "").toLowerCase().includes(q)
          );
        }
        return true;
      }),
    [sessions, selectedAgent, showArchived, showScheduler, searchQuery],
  );

  const handleScroll = useCallback(() => {
    const el = listRef.current;
    if (!el || !sessionsHasMore || sessionsLoading) return;
    if (el.scrollHeight - el.scrollTop - el.clientHeight < 120) {
      onLoadMore();
    }
  }, [sessionsHasMore, sessionsLoading, onLoadMore]);

  useEffect(() => {
    const el = listRef.current;
    if (!sessionsHasMore || sessionsLoading) return;
    if (sessions.length > 0 && filteredSessions.length === 0) {
      onLoadMore();
      return;
    }
    if (el && el.scrollHeight <= el.clientHeight) {
      onLoadMore();
    }
  }, [sessions.length, filteredSessions.length, sessionsHasMore, sessionsLoading, onLoadMore]);

  const [creating, setCreating] = useState(false);

  const handleCreate = useCallback(
    async (agentID: string) => {
      setCreating(true);
      try {
        await onCreateSession(agentID);
      } finally {
        setCreating(false);
      }
    },
    [onCreateSession],
  );

  return (
    <aside className="flex flex-col overflow-hidden w-full h-full border-r border-border">
      {/* Header */}
      <div className="flex-shrink-0 bg-background">
        <div className="flex items-center justify-between px-4 py-3 border-b border-border">
          <span className="text-[9px] font-mono uppercase tracking-[0.12em] text-muted-foreground/60">
            Sessions
          </span>
          <DropdownMenu>
            <DropdownMenuTrigger
              disabled={agents.length === 0 || creating}
              className={cn(buttonVariants({ size: "xs" }), "cursor-pointer")}
              title={agents.length === 0 ? "No agents available" : "Create new session"}
            >
              <svg
                className="w-3 h-3 shrink-0"
                fill="none"
                viewBox="0 0 24 24"
                strokeWidth="2.5"
                stroke="currentColor"
              >
                <path strokeLinecap="round" strokeLinejoin="round" d="M12 4.5v15m7.5-7.5h-15" />
              </svg>
              {creating ? "Creating…" : "New"}
            </DropdownMenuTrigger>
            <DropdownMenuContent side="bottom" align="end" sideOffset={4}>
              <DropdownMenuGroup>
                <DropdownMenuLabel>Select agent</DropdownMenuLabel>
                {agents.map((a) => (
                  <DropdownMenuItem
                    key={a.id}
                    onClick={() => handleCreate(a.id)}
                    className="text-xs font-mono cursor-pointer"
                  >
                    {a.name || a.id}
                  </DropdownMenuItem>
                ))}
              </DropdownMenuGroup>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>

        {/* Search */}
        <div className="px-3 py-2.5 border-b border-border">
          <div className="relative">
            <input
              type="text"
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              placeholder="Search sessions…"
              className="w-full pl-7 pr-3 py-1.5 text-xs font-mono rounded-md border border-border bg-transparent focus:outline-none focus:border-primary/60 transition-colors"
            />
            <svg
              className="absolute left-2 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-muted-foreground/40 pointer-events-none"
              fill="none"
              viewBox="0 0 24 24"
              strokeWidth="2.5"
              stroke="currentColor"
            >
              <path
                strokeLinecap="round"
                strokeLinejoin="round"
                d="m21 21-5.197-5.197m0 0A7.5 7.5 0 1 0 5.196 5.196a7.5 7.5 0 0 0 10.607 10.607Z"
              />
            </svg>
          </div>
        </div>

        {/* Agent switcher */}
        <div className="px-3 py-2.5 border-b border-border space-y-2">
          <div className="flex items-center gap-2">
            <select
              value={selectedAgent}
              onChange={(e) => setSelectedAgent(e.target.value)}
              className="flex-1 text-xs font-mono border border-border rounded px-2 py-1.5 bg-background focus:outline-none focus:border-primary/60 transition-colors"
            >
              <option value="">All agents</option>
              {agents.map((a) => (
                <option key={a.id} value={a.id}>
                  {a.name || a.id}
                </option>
              ))}
            </select>
            <span className="text-[10px] font-mono text-muted-foreground/40 tabular-nums">
              {filteredSessions.length}
            </span>
          </div>
          <div className="flex items-center gap-4">
            <label className="flex items-center gap-1.5 cursor-pointer">
              <input
                type="checkbox"
                checked={showScheduler}
                onChange={(e) => setShowScheduler(e.target.checked)}
                className="w-3 h-3"
              />
              <span
                className={cn(
                  "text-[10px] font-mono select-none",
                  showScheduler ? "text-muted-foreground" : "text-muted-foreground/50",
                )}
              >
                Scheduled
              </span>
            </label>
            <label className="flex items-center gap-1.5 cursor-pointer">
              <input
                type="checkbox"
                checked={showArchived}
                onChange={(e) => setShowArchived(e.target.checked)}
                className="w-3 h-3"
              />
              <span
                className={cn(
                  "text-[10px] font-mono select-none",
                  showArchived ? "text-muted-foreground" : "text-muted-foreground/50",
                )}
              >
                Archived
              </span>
            </label>
          </div>
        </div>
      </div>

      {/* List */}
      <div ref={listRef} className="flex-1 overflow-y-auto" onScroll={handleScroll}>
        {filteredSessions.map((s) => (
          <div
            key={s.id}
            onClick={() => onSelect(s.id)}
            className={cn(
              "px-4 py-3 cursor-pointer transition-colors border-b border-border/50",
              selectedID === s.id ? "bg-primary/5" : "hover:bg-muted/70",
            )}
          >
            <p
              className={cn(
                "text-[13px] font-medium leading-snug truncate mb-1",
                selectedID === s.id
                  ? "text-primary"
                  : s.archived
                    ? "text-muted-foreground/40 italic"
                    : "text-foreground/80",
              )}
            >
              {s.title || "Untitled"}
            </p>
            <div className="flex items-center gap-0 text-[10px] font-mono text-muted-foreground/50 min-w-0">
              <span className="truncate">{channelLabel(s.channel) || "—"}</span>
              {s.agent_id && (
                <span className="shrink-0 px-1 text-muted-foreground/30">&middot;</span>
              )}
              {s.agent_id && <span className="truncate">{s.agent_id}</span>}
              <span className="ml-auto shrink-0 pl-2">{formatTime(s.last_active)}</span>
            </div>
          </div>
        ))}
        {filteredSessions.length === 0 && !sessionsLoading && (
          <div className="px-4 py-12 text-center">
            <p className="text-xs text-muted-foreground/40 font-mono">No sessions found.</p>
          </div>
        )}
        {sessionsLoading && (
          <div className="px-4 py-4 flex items-center justify-center gap-2">
            <div className="w-3 h-3 border border-muted-foreground/30 border-t-muted-foreground rounded-full animate-spin" />
            <span className="text-[10px] font-mono text-muted-foreground/50">Loading…</span>
          </div>
        )}
      </div>
    </aside>
  );
}

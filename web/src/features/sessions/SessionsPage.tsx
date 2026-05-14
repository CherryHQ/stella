import { useCallback, useEffect, useRef, useState } from "react";
import { useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import type { Agent, Session, Workspace } from "@/lib/types";
import { cn } from "@/lib/utils";
import { SessionSidebar } from "./SessionSidebar";
import { SessionDetail } from "./SessionDetail";
import { WorkspacePanel } from "./WorkspacePanel";

const RIGHT_MIN = 240;
const RIGHT_MAX_RATIO = 0.5;
const RIGHT_DEFAULT = 300;

export function SessionsPage() {
  const queryClient = useQueryClient();

  const [sessionDetail, setSessionDetail] = useState<Session | null>(null);
  const [agents, setAgents] = useState<Agent[]>([]);
  const [currentUserID, setCurrentUserID] = useState<number>(0);
  const [leftOpen, setLeftOpen] = useState(true);
  const [rightOpen, setRightOpen] = useState(true);

  // Workspace state
  const [workspace, setWorkspace] = useState<Workspace | null>(null);
  const [workspaceLoading, setWorkspaceLoading] = useState(false);

  // Resizable right panel
  const [rightWidth, setRightWidth] = useState(RIGHT_DEFAULT);
  const dragging = useRef(false);
  const containerRef = useRef<HTMLDivElement>(null);

  const onResizeStart = useCallback(
    (e: React.MouseEvent) => {
      e.preventDefault();
      dragging.current = true;
      const startX = e.clientX;
      const startW = rightWidth;

      const onMove = (ev: MouseEvent) => {
        if (!dragging.current) return;
        const delta = startX - ev.clientX;
        const maxW = (containerRef.current?.offsetWidth ?? 1200) * RIGHT_MAX_RATIO;
        setRightWidth(Math.max(RIGHT_MIN, Math.min(maxW, startW + delta)));
      };

      const onUp = () => {
        dragging.current = false;
        document.removeEventListener("mousemove", onMove);
        document.removeEventListener("mouseup", onUp);
        document.body.style.cursor = "";
        document.body.style.userSelect = "";
      };

      document.body.style.cursor = "col-resize";
      document.body.style.userSelect = "none";
      document.addEventListener("mousemove", onMove);
      document.addEventListener("mouseup", onUp);
    },
    [rightWidth],
  );

  const sessionsQuery = useInfiniteQuery({
    queryKey: ["sessions"],
    initialPageParam: 0,
    queryFn: ({ pageParam }) => api<Session[]>("GET", `/api/sessions?limit=10&offset=${pageParam}`),
    getNextPageParam: (lastPage, allPages) =>
      lastPage.length === 10 ? allPages.reduce((sum, page) => sum + page.length, 0) : undefined,
  });

  const sessions = sessionsQuery.data?.pages.flat() ?? [];

  const openSession = useCallback(async (sessionID: string, pushState = true) => {
    if (pushState) {
      history.pushState({ sessionID }, "", "/sessions/" + encodeURIComponent(sessionID));
    }
    try {
      const detail = await api<Session>("GET", `/api/sessions/${encodeURIComponent(sessionID)}`);
      setSessionDetail(detail);
    } catch (e) {
      console.error(e);
    }
  }, []);

  const createSession = useCallback(
    async (agentID: string) => {
      const sess = await api<Session>("POST", "/api/sessions", { agent_id: agentID });
      await queryClient.invalidateQueries({ queryKey: ["sessions"] });
      await openSession(sess.id);
    },
    [openSession, queryClient],
  );

  useEffect(() => {
    const init = async () => {
      await Promise.all([
        api<Agent[]>("GET", "/api/agents")
          .then((r) => setAgents(r ?? []))
          .catch(() => {}),
        api<{ id: number }>("GET", "/api/auth/me")
          .then((r) => {
            if (r?.id) setCurrentUserID(r.id);
          })
          .catch(() => {}),
      ]);
      const parts = window.location.pathname.split("/");
      const sessionID =
        parts.length >= 3 && parts[1] === "sessions" ? decodeURIComponent(parts[2]) : "";
      if (sessionID) {
        await openSession(sessionID, false);
      }
    };
    void init();
  }, [openSession]);

  useEffect(() => {
    const onPop = (e: PopStateEvent) => {
      const sid = (e.state as { sessionID?: string } | null)?.sessionID;
      if (sid) {
        void openSession(sid, false);
      } else {
        setSessionDetail(null);
      }
    };
    window.addEventListener("popstate", onPop);
    return () => window.removeEventListener("popstate", onPop);
  }, [openSession]);

  const loadWorkspace = useCallback(async (sid: string) => {
    setWorkspaceLoading(true);
    try {
      const data = await api<Workspace>(
        "GET",
        `/api/sessions/${encodeURIComponent(sid)}/workspace?show_hidden=true`,
      );
      setWorkspace(data);
    } catch (e) {
      console.error(e);
    } finally {
      setWorkspaceLoading(false);
    }
  }, []);

  useEffect(() => {
    if (rightOpen && sessionDetail) {
      loadWorkspace(sessionDetail.id).catch(console.error);
    }
    if (!sessionDetail) {
      setWorkspace(null);
    }
  }, [rightOpen, sessionDetail?.id, loadWorkspace, sessionDetail]);

  return (
    <div
      ref={containerRef}
      className="border-t border-border flex overflow-hidden"
      style={{ height: "calc(100vh - 3.5rem)" }}
    >
      {/* Left sidebar */}
      <div
        className={cn(
          "flex-shrink-0 transition-all duration-200 ease-out",
          leftOpen
            ? "w-[272px] min-w-[272px]"
            : "w-0 min-w-0 overflow-hidden opacity-0 pointer-events-none",
        )}
      >
        <SessionSidebar
          sessions={sessions}
          sessionsLoading={sessionsQuery.isFetchingNextPage || sessionsQuery.isLoading}
          sessionsHasMore={!!sessionsQuery.hasNextPage}
          selectedID={sessionDetail?.id}
          agents={agents}
          onSelect={openSession}
          onLoadMore={() => sessionsQuery.fetchNextPage()}
          onCreateSession={createSession}
        />
      </div>

      {/* Center panel */}
      <SessionDetail
        session={sessionDetail}
        currentUserID={currentUserID}
        onBack={() => {
          setSessionDetail(null);
          history.pushState(null, "", "/sessions");
        }}
        onSessionUpdate={(s) => setSessionDetail(s)}
        onToggleLeft={() => setLeftOpen((v) => !v)}
        onToggleRight={() => setRightOpen((v) => !v)}
      />

      {/* Right sidebar (workspace) — resizable */}
      <div
        className={cn(
          "flex-shrink-0 relative transition-[width,min-width,opacity] duration-200 ease-out",
          rightOpen
            ? "border-l border-border"
            : "w-0 min-w-0 overflow-hidden opacity-0 pointer-events-none",
        )}
        style={rightOpen ? { width: rightWidth, minWidth: RIGHT_MIN } : undefined}
      >
        {/* Drag handle */}
        {rightOpen && (
          <div
            onMouseDown={onResizeStart}
            className="absolute left-0 top-0 bottom-0 w-1 cursor-col-resize z-10 hover:bg-primary/10 active:bg-primary/20 transition-colors"
          />
        )}
        <WorkspacePanel
          sessionID={sessionDetail?.id ?? ""}
          workspace={workspace}
          workspaceLoading={workspaceLoading}
          onReload={loadWorkspace}
        />
      </div>
    </div>
  );
}

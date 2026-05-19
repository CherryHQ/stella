import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useParams } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";
import type { Session, Workspace } from "@/lib/types";
import { meQueryOptions } from "@/lib/queries/me";
import { agentProjectsOptions } from "@/lib/queries/projects";
import { useSidebarToggle } from "@/hooks/use-sidebar-toggle";
import { cn } from "@/lib/utils";
import { SessionDetail } from "./SessionDetail";
import { WorkspacePanel } from "./WorkspacePanel";

const RIGHT_MIN = 240;
const RIGHT_MAX_RATIO = 0.5;
const RIGHT_DEFAULT = 300;

export function SessionView() {
  const { agentId, sessionId, projectId } = useParams({ strict: false }) as {
    agentId: string;
    sessionId: string;
    projectId?: string;
  };
  const navigate = useNavigate();
  const { toggleSidebar } = useSidebarToggle();
  const { data: me } = useQuery(meQueryOptions);
  const currentUserID = (me as { id?: number } | undefined)?.id ?? 0;
  const { data: projects = [] } = useQuery(agentProjectsOptions(agentId));
  const project = useMemo(
    () => (projectId ? projects.find((p) => p.id === projectId) : undefined),
    [projects, projectId],
  );

  const [sessionDetail, setSessionDetail] = useState<Session | null>(null);
  const [rightOpen, setRightOpen] = useState(true);
  const [workspace, setWorkspace] = useState<Workspace | null>(null);
  const [workspaceLoading, setWorkspaceLoading] = useState(false);
  const [projectDir, setProjectDir] = useState("");

  const [rightWidth, setRightWidth] = useState(RIGHT_DEFAULT);
  const dragging = useRef(false);
  const containerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    let cancelled = false;
    const load = async () => {
      try {
        const detail = await api<Session>("GET", `/api/sessions/${encodeURIComponent(sessionId)}`);
        if (!cancelled) setSessionDetail(detail);
      } catch (e) {
        console.error(e);
      }
    };
    void load();
    return () => {
      cancelled = true;
    };
  }, [sessionId]);

  const loadWorkspace = useCallback(
    async (sid: string, scopePath?: string) => {
      setWorkspaceLoading(true);
      try {
        const params = new URLSearchParams({ show_hidden: "true", depth: "2" });
        if (scopePath) params.set("path", scopePath);
        const data = await api<Workspace>(
          "GET",
          `/api/sessions/${encodeURIComponent(sid)}/workspace?${params}`,
        );
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
      } catch {
        setWorkspace(null);
      } finally {
        setWorkspaceLoading(false);
      }
    },
    [project?.base_dir],
  );

  useEffect(() => {
    if (rightOpen && sessionDetail) {
      void loadWorkspace(sessionDetail.id, projectDir || undefined);
    }
  }, [rightOpen, sessionDetail?.id, loadWorkspace, projectDir, sessionDetail]);

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

  const showWorkspace = rightOpen;

  return (
    <div ref={containerRef} className="flex flex-1 min-w-0 overflow-hidden">
      <div className="flex-1 min-w-0 flex flex-col overflow-hidden">
        <SessionDetail
          session={sessionDetail}
          currentUserID={currentUserID}
          onBack={() => {
            void navigate({ to: "/agents/$agentId", params: { agentId } });
          }}
          onSessionUpdate={(s) => setSessionDetail(s)}
          onToggleLeft={toggleSidebar}
          onToggleRight={() => setRightOpen((v) => !v)}
        />
      </div>

      <div
        className={cn(
          "flex-shrink-0 relative transition-[width,min-width,opacity] duration-200 ease-out",
          showWorkspace
            ? "border-l border-border"
            : "w-0 min-w-0 overflow-hidden opacity-0 pointer-events-none",
        )}
        style={showWorkspace ? { width: rightWidth, minWidth: RIGHT_MIN } : undefined}
      >
        {showWorkspace && (
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
          projectDir={projectDir}
        />
      </div>
    </div>
  );
}

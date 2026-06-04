import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useParams } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { createSession, getSession, getSessionWorkspace } from "@/lib/api-client/sdk.gen";
import type { Session, Workspace } from "@/lib/types";
import { agentsQueryOptions } from "@/lib/queries/agents";
import { meQueryOptions } from "@/lib/queries/me";
import { agentProjectsOptions } from "@/lib/queries/projects";
import { cn } from "@/lib/utils";
import { Sheet, SheetPopup, SheetTitle, SheetDescription } from "@/components/ui/sheet";
import { InspectorPanel } from "./InspectorPanel";
import { SessionDetail } from "./SessionDetail";

const RIGHT_MIN = 280;
const RIGHT_MAX_RATIO = 0.45;
const RIGHT_DEFAULT = 360;
const RIGHT_AUTO_HIDE_WIDTH = 1180;

function defaultRightWidth(viewportWidth: number): number {
  if (viewportWidth >= 1800) return 440;
  if (viewportWidth >= 1440) return 400;
  return RIGHT_DEFAULT;
}

export function SessionView() {
  const { agentId, sessionId, projectId } = useParams({ strict: false }) as {
    agentId: string;
    sessionId: string;
    projectId?: string;
  };
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { data: me } = useQuery(meQueryOptions);
  const { data: agents = [] } = useQuery(agentsQueryOptions);
  const currentUserID = me?.id ?? "";
  const { data: projects = [] } = useQuery(agentProjectsOptions(agentId));
  const currentAgent = useMemo(() => agents.find((a) => a.id === agentId), [agents, agentId]);
  const project = useMemo(
    () => (projectId ? projects.find((p) => p.id === projectId) : undefined),
    [projects, projectId],
  );

  const [sessionDetail, setSessionDetail] = useState<Session | null>(null);
  const [rightOpen, setRightOpen] = useState(true);
  const [mobileSheetOpen, setMobileSheetOpen] = useState(false);
  const [workspace, setWorkspace] = useState<Workspace | null>(null);
  const [workspaceLoading, setWorkspaceLoading] = useState(false);
  const [projectDir, setProjectDir] = useState("");

  const [rightWidth, setRightWidth] = useState(RIGHT_DEFAULT);
  const dragging = useRef(false);
  const initializedRightPanel = useRef(false);
  const containerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (initializedRightPanel.current) return;
    initializedRightPanel.current = true;
    const viewportWidth = window.innerWidth;
    setRightWidth(defaultRightWidth(viewportWidth));
    setRightOpen(viewportWidth >= RIGHT_AUTO_HIDE_WIDTH);
  }, []);

  useEffect(() => {
    let cancelled = false;
    const load = async () => {
      try {
        const { data: detail } = await getSession({
          path: { agentId: agentId, sessionId: sessionId },
          throwOnError: true,
        });
        if (!cancelled) setSessionDetail(detail);
      } catch (e) {
        console.error(e);
      }
    };
    void load();
    return () => {
      cancelled = true;
    };
  }, [agentId, sessionId]);

  const loadWorkspace = useCallback(
    async (sid: string, scopePath?: string) => {
      setWorkspaceLoading(true);
      try {
        const { data } = await getSessionWorkspace({
          path: { agentId: agentId, sessionId: sid },
          query: { show_hidden: true, depth: 2, ...(scopePath ? { path: scopePath } : {}) },
          throwOnError: true,
        });
        const workspace = data;
        setWorkspace(workspace);
        if (
          !scopePath &&
          project?.base_dir &&
          workspace.root &&
          project.base_dir.startsWith(workspace.root + "/")
        ) {
          const rel = project.base_dir.slice(workspace.root.length + 1);
          if (rel) setProjectDir(rel);
        }
      } catch {
        setWorkspace(null);
      } finally {
        setWorkspaceLoading(false);
      }
    },
    [agentId, project?.base_dir],
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

  const createTemporarySession = useCallback(async () => {
    const { data } = await createSession({
      path: { agentId: agentId },
      body: { kind: "chat", ...(projectId ? { project_id: projectId } : {}) },
      throwOnError: true,
    });
    const session = data;
    await queryClient.invalidateQueries({ queryKey: ["sessions", agentId] });
    void navigate({
      to: "/agents/$agentId/sessions/$sessionId",
      params: { agentId, sessionId: session.id },
    });
  }, [agentId, navigate, projectId, queryClient]);

  const contextTitle = project?.name ?? currentAgent?.name ?? sessionDetail?.title;
  const contextSubtitle = project
    ? "Project main thread"
    : sessionDetail?.kind === "main"
      ? "Main thread"
      : `${sessionDetail?.kind ?? "chat"} session`;

  const showWorkspace = rightOpen;
  const toggleInspector = useCallback(() => {
    if (window.innerWidth < 768) {
      setMobileSheetOpen(true);
      return;
    }
    setRightOpen((v) => !v);
  }, []);

  return (
    <div ref={containerRef} className="relative flex h-full min-w-0 overflow-hidden bg-background">
      <div className="flex-1 min-w-0 flex flex-col overflow-hidden">
        <SessionDetail
          session={sessionDetail}
          currentUserID={currentUserID}
          onNewSession={() => void createTemporarySession()}
          onSessionUpdate={(s) => setSessionDetail(s)}
          onToggleWorkspace={toggleInspector}
          workspaceOpen={showWorkspace}
          contextTitle={contextTitle}
          contextSubtitle={contextSubtitle}
        />
      </div>

      <div
        className={cn(
          "stella-right-panel relative hidden flex-shrink-0 bg-card transition-[width,min-width,opacity] duration-200 ease-out md:block",
          showWorkspace
            ? "border-l border-border"
            : "w-0 min-w-0 overflow-hidden opacity-0 pointer-events-none",
        )}
        style={showWorkspace ? { width: rightWidth, minWidth: RIGHT_MIN } : undefined}
      >
        {showWorkspace && (
          <div
            onMouseDown={onResizeStart}
            className="absolute top-0 bottom-0 left-0 z-10 w-1.5 -translate-x-[3px] cursor-col-resize transition-colors hover:bg-primary/20 active:bg-primary/35"
          />
        )}
        <InspectorPanel
          agentID={agentId}
          sessionID={sessionDetail?.id ?? ""}
          session={sessionDetail}
          workspace={workspace}
          workspaceLoading={workspaceLoading}
          onReloadWorkspace={loadWorkspace}
          projectDir={projectDir}
        />
      </div>

      <Sheet open={mobileSheetOpen} onOpenChange={setMobileSheetOpen}>
        <SheetPopup
          side="right"
          showCloseButton={false}
          className="w-[85%] max-w-sm md:hidden bg-card border-l border-border"
        >
          <SheetTitle className="sr-only">Inspector</SheetTitle>
          <SheetDescription className="sr-only">
            Session workspace, work queue, and context
          </SheetDescription>
          <div className="flex h-full flex-col overflow-hidden">
            <InspectorPanel
              agentID={agentId}
              sessionID={sessionDetail?.id ?? ""}
              session={sessionDetail}
              workspace={workspace}
              workspaceLoading={workspaceLoading}
              onReloadWorkspace={loadWorkspace}
              projectDir={projectDir}
            />
          </div>
        </SheetPopup>
      </Sheet>
    </div>
  );
}

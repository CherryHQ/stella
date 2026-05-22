import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { getSession, getSessionWorkspace } from "@/lib/api-client/sdk.gen";
import type { Session, Workspace } from "@/lib/types";
import { meQueryOptions } from "@/lib/queries/me";
import { agentProjectsOptions } from "@/lib/queries/projects";
import { cn } from "@/lib/utils";
import { Sheet, SheetPopup, SheetTitle, SheetDescription } from "@/components/ui/sheet";
import { SessionDetail } from "./SessionDetail";
import { WorkspacePanel } from "./WorkspacePanel";

const RIGHT_MIN = 240;
const RIGHT_MAX_RATIO = 0.5;
const RIGHT_DEFAULT = 318;

export function SessionView() {
  const { agentId, sessionId, projectId } = useParams({ strict: false }) as {
    agentId: string;
    sessionId: string;
    projectId?: string;
  };
  const { draft } = useSearch({ strict: false }) as { draft?: string };
  const navigate = useNavigate();
  const { data: me } = useQuery(meQueryOptions);
  const currentUserID = (me as { id?: number } | undefined)?.id ?? 0;
  const { data: projects = [] } = useQuery(agentProjectsOptions(agentId));
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
  const containerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    let cancelled = false;
    const load = async () => {
      try {
        const { data: detail } = await getSession({
          path: { agentID: agentId, sessionID: sessionId },
          throwOnError: true,
        });
        if (!cancelled) setSessionDetail(detail as unknown as Session);
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
          path: { agentID: agentId, sessionID: sid },
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

  const showWorkspace = rightOpen;

  return (
    <div ref={containerRef} className="relative flex flex-1 min-w-0 overflow-hidden bg-card/70">
      <div className="flex-1 min-w-0 flex flex-col overflow-hidden">
        <SessionDetail
          session={sessionDetail}
          currentUserID={currentUserID}
          initialDraft={draft}
          onBack={() => {
            void navigate({ to: "/agents/$agentId", params: { agentId } });
          }}
          onSessionUpdate={(s) => setSessionDetail(s)}
          onToggleWorkspace={() => setMobileSheetOpen(true)}
        />
      </div>

      <button
        type="button"
        onClick={() => setRightOpen((v) => !v)}
        className="absolute top-3 z-30 hidden h-[34px] w-[18px] place-items-center rounded-full border border-border/60 bg-card text-muted-foreground/50 shadow-sm transition-all duration-200 hover:bg-accent hover:text-foreground md:grid"
        style={{ right: showWorkspace ? rightWidth - 9 : -9 }}
        aria-label={showWorkspace ? "Hide workspace" : "Show workspace"}
      >
        <svg
          className={cn("size-3 transition-transform", !showWorkspace && "rotate-180")}
          viewBox="0 0 16 16"
          fill="none"
          stroke="currentColor"
          strokeWidth="2"
        >
          <path d="m6 4 4 4-4 4" />
        </svg>
      </button>

      <div
        className={cn(
          "stella-right-panel relative hidden flex-shrink-0 bg-sidebar/70 transition-[width,min-width,opacity] duration-200 ease-out md:block",
          showWorkspace
            ? "border-l border-border/70"
            : "w-0 min-w-0 overflow-hidden opacity-0 pointer-events-none",
        )}
        style={showWorkspace ? { width: rightWidth, minWidth: RIGHT_MIN } : undefined}
      >
        {showWorkspace && (
          <div
            onMouseDown={onResizeStart}
            className="absolute top-4 bottom-4 left-0 z-10 w-2 -translate-x-1 cursor-col-resize rounded-full transition-colors hover:bg-primary/10 active:bg-primary/20"
          />
        )}
        <WorkspacePanel
          agentID={agentId}
          sessionID={sessionDetail?.id ?? ""}
          workspace={workspace}
          workspaceLoading={workspaceLoading}
          onReload={loadWorkspace}
          projectDir={projectDir}
        />
      </div>

      <Sheet open={mobileSheetOpen} onOpenChange={setMobileSheetOpen}>
        <SheetPopup side="right" showCloseButton={false} className="w-[85%] max-w-sm md:hidden">
          <SheetTitle className="sr-only">Workspace</SheetTitle>
          <SheetDescription className="sr-only">Session workspace files</SheetDescription>
          <div className="flex h-full flex-col overflow-hidden">
            <WorkspacePanel
              agentID={agentId}
              sessionID={sessionDetail?.id ?? ""}
              workspace={workspace}
              workspaceLoading={workspaceLoading}
              onReload={loadWorkspace}
              projectDir={projectDir}
            />
          </div>
        </SheetPopup>
      </Sheet>
    </div>
  );
}

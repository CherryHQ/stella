import { useCallback, useEffect, useState } from "react";
import { useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import type { Agent, Session, Workspace } from "@/lib/types";
import { cn } from "@/lib/utils";
import { SessionSidebar } from "./SessionSidebar";
import { SessionDetail } from "./SessionDetail";
import { WorkspacePanel } from "./WorkspacePanel";
import { FileEditorModal } from "./FileEditorModal";

export function SessionsPage() {
  const queryClient = useQueryClient();

  const [sessionDetail, setSessionDetail] = useState<Session | null>(null);
  const [agents, setAgents] = useState<Agent[]>([]);
  const [currentUserID, setCurrentUserID] = useState<number>(0);
  const [leftOpen, setLeftOpen] = useState(true);
  const [rightOpen, setRightOpen] = useState(true);

  // Workspace state (lifted from SessionDetail)
  const [workspace, setWorkspace] = useState<Workspace | null>(null);
  const [workspaceLoading, setWorkspaceLoading] = useState(false);
  const [fileEditor, setFileEditor] = useState<{
    open: boolean;
    path: string;
    content: string;
    language: string;
    saving: boolean;
    loading: boolean;
    previewMd: boolean;
  } | null>(null);

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

  // Workspace loading
  const loadWorkspace = useCallback(async (sid: string) => {
    setWorkspaceLoading(true);
    try {
      const data = await api<Workspace>(
        "GET",
        `/api/sessions/${encodeURIComponent(sid)}/workspace`,
      );
      setWorkspace(data);
    } catch (e) {
      console.error(e);
    } finally {
      setWorkspaceLoading(false);
    }
  }, []);

  // Load workspace when right panel opens or session changes while it's open
  useEffect(() => {
    if (rightOpen && sessionDetail) {
      loadWorkspace(sessionDetail.id).catch(console.error);
    }
    if (!sessionDetail) {
      setWorkspace(null);
    }
  }, [rightOpen, sessionDetail?.id, loadWorkspace, sessionDetail]);

  // File editor callbacks
  const enc = sessionDetail ? encodeURIComponent(sessionDetail.id) : "";

  const openFileEditor = useCallback(
    async (path: string) => {
      if (!sessionDetail) return;
      setFileEditor({
        open: true,
        path,
        content: "",
        language: "",
        saving: false,
        loading: true,
        previewMd: false,
      });
      try {
        const data = await api<{ content: string; language: string }>(
          "GET",
          `/api/sessions/${enc}/workspace/file-content?path=${encodeURIComponent(path)}`,
        );
        setFileEditor((prev) =>
          prev
            ? {
                ...prev,
                content: data.content ?? "",
                language: data.language ?? "",
                loading: false,
              }
            : null,
        );
      } catch (e) {
        console.error(e);
        setFileEditor(null);
      }
    },
    [sessionDetail, enc],
  );

  const saveFileEditor = useCallback(async () => {
    if (!fileEditor || !sessionDetail) return;
    setFileEditor((prev) => (prev ? { ...prev, saving: true } : null));
    try {
      await api("PUT", `/api/sessions/${enc}/workspace/file-content`, {
        path: fileEditor.path,
        content: fileEditor.content,
      });
    } finally {
      setFileEditor((prev) => (prev ? { ...prev, saving: false } : null));
    }
  }, [fileEditor, sessionDetail, enc]);

  return (
    <div
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

      {/* Right sidebar (workspace) */}
      <div
        className={cn(
          "flex-shrink-0 transition-all duration-200 ease-out",
          rightOpen
            ? "w-[272px] min-w-[272px] border-l border-border"
            : "w-0 min-w-0 overflow-hidden opacity-0 pointer-events-none",
        )}
      >
        <WorkspacePanel
          sessionID={sessionDetail?.id ?? ""}
          workspace={workspace}
          workspaceLoading={workspaceLoading}
          onWorkspaceChange={setWorkspace}
          onOpenFile={openFileEditor}
        />
      </div>

      {/* File editor modal */}
      {fileEditor?.open && (
        <FileEditorModal
          fileEditor={fileEditor}
          onClose={() => setFileEditor(null)}
          onSave={saveFileEditor}
          onChange={(content) => setFileEditor((prev) => (prev ? { ...prev, content } : null))}
          onTogglePreview={() =>
            setFileEditor((prev) => (prev ? { ...prev, previewMd: !prev.previewMd } : null))
          }
        />
      )}
    </div>
  );
}

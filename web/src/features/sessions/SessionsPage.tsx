import { useCallback, useEffect, useRef, useState } from "react";
import { useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate, useParams } from "@tanstack/react-router";
import { api } from "@/lib/api";
import type {
  Agent,
  BuiltinItem,
  SchedulerJob,
  SchedulerJobList,
  Session,
  Skill,
  UserMemory,
  Workspace,
} from "@/lib/types";
import { cn } from "@/lib/utils";
import { SessionSidebar, type PanelSel } from "./SessionSidebar";
import { SessionDetail } from "./SessionDetail";
import { WorkspacePanel } from "./WorkspacePanel";
import { AgentSettingsPanel } from "./panels/AgentSettingsPanel";
import { AutomationPanel } from "./panels/AutomationPanel";
import { MemoryPanel } from "./panels/MemoryPanel";
import { SkillPanel } from "./panels/SkillPanel";
import { SoulPanel } from "./panels/SoulPanel";

const RIGHT_MIN = 240;
const RIGHT_MAX_RATIO = 0.5;
const RIGHT_DEFAULT = 300;

export function SessionsPage() {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const params = useParams({ strict: false }) as { _splat?: string };

  // ── agent + panel state ──────────────────────────────────────────────────
  const [agents, setAgents] = useState<Agent[]>([]);
  const [selectedAgentId, setSelectedAgentId] = useState("");
  const [panelSel, setPanelSel] = useState<PanelSel>({ kind: "chat", id: "" });
  const [openSections, setOpenSections] = useState<string[]>([]);

  // ── per-agent data ───────────────────────────────────────────────────────
  const [schedulerJobs, setSchedulerJobs] = useState<SchedulerJob[]>([]);
  const [skills, setSkills] = useState<Skill[]>([]);
  const [memories, setMemories] = useState<UserMemory[]>([]);

  // ── session detail (for chat panel) ─────────────────────────────────────
  const [sessionDetail, setSessionDetail] = useState<Session | null>(null);
  const [currentUserID, setCurrentUserID] = useState<number>(0);

  // ── workspace (right panel, chat only) ──────────────────────────────────
  const [rightOpen, setRightOpen] = useState(true);
  const [workspace, setWorkspace] = useState<Workspace | null>(null);
  const [workspaceLoading, setWorkspaceLoading] = useState(false);

  // ── resizable right panel ────────────────────────────────────────────────
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

  // ── sessions query (per agent) ───────────────────────────────────────────
  const sessionsQuery = useInfiniteQuery({
    queryKey: ["sessions", selectedAgentId],
    initialPageParam: 0,
    queryFn: ({ pageParam }) => {
      const agentParam = selectedAgentId ? `&agent_id=${encodeURIComponent(selectedAgentId)}` : "";
      return api<Session[]>("GET", `/api/sessions?limit=20&offset=${pageParam}${agentParam}`);
    },
    getNextPageParam: (lastPage, allPages) =>
      lastPage.length === 20 ? allPages.reduce((sum, p) => sum + p.length, 0) : undefined,
    enabled: true,
  });

  const sessions = sessionsQuery.data?.pages.flat() ?? [];

  // ── load per-agent data ──────────────────────────────────────────────────
  const loadAgentData = useCallback(async (agentId: string) => {
    if (!agentId) return;
    try {
      const [jobs, agentSkills, userSkills, builtinSkills, allMemories] = await Promise.all([
        api<SchedulerJobList>("GET", "/api/scheduler/jobs").catch(() => ({ items: [] })),
        api<Skill[]>("GET", `/api/agents/${encodeURIComponent(agentId)}/skills`).catch(() => []),
        api<Skill[]>("GET", "/api/auth/profile/skills").catch(() => []),
        api<BuiltinItem[]>("GET", "/api/builtin/skill").catch(() => []),
        api<UserMemory[]>("GET", "/api/auth/profile/memories").catch(() => []),
      ]);
      setSchedulerJobs(
        (jobs.items ?? []).filter((j) => j.owner_kind === "system" || j.agent_id === agentId),
      );
      const systemSkills: Skill[] = (builtinSkills ?? []).map((b) => ({
        id: b.id,
        name: b.name,
        description: b.description ?? "",
        status: "active" as const,
        scope: "system" as const,
        disable_model_invocation: false,
      }));
      const normalizedAgentSkills = (agentSkills ?? []).map((s) => ({
        ...s,
        scope: "agent" as const,
      }));
      const normalizedUserSkills = (userSkills ?? []).map((s) => ({
        ...s,
        scope: "user" as const,
      }));
      const scopeOrder: Record<string, number> = { system: 0, agent: 1, user: 2 };
      const combined = [...systemSkills, ...normalizedAgentSkills, ...normalizedUserSkills];
      combined.sort((a, b) => {
        const diff = (scopeOrder[a.scope] ?? 9) - (scopeOrder[b.scope] ?? 9);
        return diff !== 0 ? diff : a.name.localeCompare(b.name);
      });
      setSkills(combined);
      setMemories((allMemories ?? []).filter((m) => m.agent_id === agentId));
    } catch (e) {
      console.error(e);
    }
  }, []);

  const refreshAgentData = useCallback(() => {
    void loadAgentData(selectedAgentId);
  }, [selectedAgentId, loadAgentData]);

  // ── init ─────────────────────────────────────────────────────────────────
  useEffect(() => {
    const init = async () => {
      await Promise.all([
        api<Agent[]>("GET", "/api/agents")
          .then((r) => {
            const list = r ?? [];
            setAgents(list);
            if (list.length > 0 && !selectedAgentId) {
              setSelectedAgentId(list[0].id);
            }
          })
          .catch(() => {}),
        api<{ id: number }>("GET", "/api/auth/me")
          .then((r) => {
            if (r?.id) setCurrentUserID(r.id);
          })
          .catch(() => {}),
      ]);
    };
    void init();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // load per-agent data when agent changes
  useEffect(() => {
    if (selectedAgentId) {
      void loadAgentData(selectedAgentId);
    }
  }, [selectedAgentId, loadAgentData]);

  // ── URL → session detail ─────────────────────────────────────────────────
  const loadSession = useCallback(
    async (sessionID: string) => {
      try {
        const detail = await api<Session>("GET", `/api/sessions/${encodeURIComponent(sessionID)}`);
        setSessionDetail(detail);
        // sync agent to session's agent when navigating via URL
        if (detail.agent_id && detail.agent_id !== selectedAgentId) {
          setSelectedAgentId(detail.agent_id);
        }
        setPanelSel({ kind: "chat", id: sessionID });
      } catch (e) {
        console.error(e);
      }
    },
    [selectedAgentId],
  );

  useEffect(() => {
    const sessionID = params._splat ? decodeURIComponent(params._splat) : "";
    if (sessionID) {
      void loadSession(sessionID);
    } else {
      setSessionDetail(null);
    }
  }, [loadSession, params._splat]);

  // ── agent switch ─────────────────────────────────────────────────────────
  const handleAgentChange = useCallback(
    (id: string) => {
      if (id === selectedAgentId) return;
      setSelectedAgentId(id);
      setOpenSections([]);
      setPanelSel({ kind: "chat", id: "" });
      setSessionDetail(null);
      void queryClient.invalidateQueries({ queryKey: ["sessions", id] });
      void navigate({ to: "/sessions" });
    },
    [selectedAgentId, queryClient, navigate],
  );

  // ── panel selection ──────────────────────────────────────────────────────
  const handleSelect = useCallback(
    (sel: PanelSel) => {
      setPanelSel(sel);
      if (sel.kind === "chat" || sel.kind === "task") {
        void navigate({ to: "/sessions/$", params: { _splat: sel.id } });
      }
    },
    [navigate],
  );

  const handleToggleSection = useCallback((key: string) => {
    setOpenSections((prev) =>
      prev.includes(key) ? prev.filter((k) => k !== key) : [...prev, key],
    );
  }, []);

  // ── create session ───────────────────────────────────────────────────────
  const createSession = useCallback(async () => {
    if (!selectedAgentId) return;
    const sess = await api<Session>("POST", "/api/sessions", { agent_id: selectedAgentId });
    await queryClient.invalidateQueries({ queryKey: ["sessions", selectedAgentId] });
    await navigate({ to: "/sessions/$", params: { _splat: sess.id } });
    setPanelSel({ kind: "chat", id: sess.id });
  }, [selectedAgentId, queryClient, navigate]);

  // ── workspace ────────────────────────────────────────────────────────────
  const loadWorkspace = useCallback(async (sid: string) => {
    setWorkspaceLoading(true);
    try {
      const data = await api<Workspace>(
        "GET",
        `/api/sessions/${encodeURIComponent(sid)}/workspace?show_hidden=true&depth=2`,
      );
      setWorkspace(data);
    } catch (e) {
      console.error(e);
    } finally {
      setWorkspaceLoading(false);
    }
  }, []);

  useEffect(() => {
    const isChatKind = panelSel.kind === "chat" || panelSel.kind === "task";
    if (rightOpen && isChatKind && sessionDetail) {
      void loadWorkspace(sessionDetail.id);
    }
    if (!sessionDetail) {
      setWorkspace(null);
    }
  }, [rightOpen, sessionDetail?.id, panelSel.kind, loadWorkspace, sessionDetail]);

  // ── derived ──────────────────────────────────────────────────────────────
  const isChatPanel = panelSel.kind === "chat" || panelSel.kind === "task";
  const showWorkspace = isChatPanel && rightOpen;

  return (
    <div
      ref={containerRef}
      className="border-t border-border flex overflow-hidden"
      style={{ height: "calc(100vh - 3.5rem)" }}
    >
      {/* Sidebar */}
      <div className="w-[268px] min-w-[268px] flex-shrink-0 border-r border-border">
        <SessionSidebar
          agents={agents}
          selectedAgentId={selectedAgentId}
          onAgentChange={handleAgentChange}
          panelSel={panelSel}
          onSelect={handleSelect}
          openSections={openSections}
          onToggleSection={handleToggleSection}
          sessions={sessions}
          sessionsLoading={sessionsQuery.isFetchingNextPage || sessionsQuery.isLoading}
          sessionsHasMore={!!sessionsQuery.hasNextPage}
          onLoadMoreSessions={() => sessionsQuery.fetchNextPage()}
          schedulerJobs={schedulerJobs}
          skills={skills}
          memories={memories}
          onCreateSession={() => void createSession()}
          onNavigateSettings={() => void navigate({ to: "/settings/agents" })}
        />
      </div>

      {/* Center panel */}
      <div className="flex-1 min-w-0 flex flex-col overflow-hidden">
        {isChatPanel ? (
          <SessionDetail
            session={sessionDetail}
            currentUserID={currentUserID}
            onBack={() => {
              setSessionDetail(null);
              setPanelSel({ kind: "chat", id: "" });
              void navigate({ to: "/sessions" });
            }}
            onSessionUpdate={(s) => setSessionDetail(s)}
            onToggleLeft={() => {}}
            onToggleRight={() => setRightOpen((v) => !v)}
          />
        ) : panelSel.kind === "auto" ? (
          <AutomationPanel
            key={panelSel.id}
            jobId={panelSel.id === "new" ? null : panelSel.id}
            agentId={selectedAgentId}
            onSaved={() => {
              refreshAgentData();
              if (panelSel.id === "new") setPanelSel({ kind: "auto", id: "" });
            }}
            onDeleted={() => {
              refreshAgentData();
              setPanelSel({ kind: "auto", id: "" });
            }}
          />
        ) : panelSel.kind === "skill" ? (
          <SkillPanel
            key={panelSel.id}
            skillId={panelSel.id === "new" ? null : panelSel.id}
            scope={skills.find((s) => s.id === panelSel.id)?.scope}
            agentId={selectedAgentId}
            onSaved={() => {
              refreshAgentData();
              if (panelSel.id === "new") setPanelSel({ kind: "skill", id: "" });
            }}
            onDeleted={() => {
              refreshAgentData();
              setPanelSel({ kind: "skill", id: "" });
            }}
          />
        ) : panelSel.kind === "memory" ? (
          <MemoryPanel key={selectedAgentId} agentId={selectedAgentId} />
        ) : panelSel.kind === "soul" ? (
          <SoulPanel key={selectedAgentId} agentId={selectedAgentId} />
        ) : panelSel.kind === "settings" ? (
          <AgentSettingsPanel key={selectedAgentId} agentId={selectedAgentId} />
        ) : (
          <div className="flex-1 flex items-center justify-center text-sm text-muted-foreground">
            Select a conversation or item from the sidebar.
          </div>
        )}
      </div>

      {/* Workspace (chat only) */}
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
        />
      </div>
    </div>
  );
}

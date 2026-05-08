import { useCallback, useEffect, useState } from "react";
import { useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import type { Agent, Session } from "@/lib/types";
import { SessionSidebar } from "./SessionSidebar";
import { SessionDetail } from "./SessionDetail";

export function SessionsPage() {
  const queryClient = useQueryClient();

  const [sessionDetail, setSessionDetail] = useState<Session | null>(null);
  const [agents, setAgents] = useState<Agent[]>([]);
  const [currentUserID, setCurrentUserID] = useState<number>(0);

  const sessionsQuery = useInfiniteQuery({
    queryKey: ["sessions"],
    initialPageParam: 0,
    queryFn: ({ pageParam }) =>
      api<Session[]>("GET", `/api/sessions?limit=10&offset=${pageParam}`),
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

  const deleteSession = useCallback(
    async (id: string) => {
      await api("DELETE", `/api/sessions/${encodeURIComponent(id)}`);
      await queryClient.invalidateQueries({ queryKey: ["sessions"] });
      if (sessionDetail?.id === id) {
        setSessionDetail(null);
        history.pushState(null, "", "/sessions");
      }
    },
    [queryClient, sessionDetail],
  );

  return (
    <div
      className="-mx-6 -mt-12 border-t border-border grid grid-cols-1 lg:grid-cols-[300px_1fr] overflow-hidden"
      style={{ height: "calc(100vh - 3.5rem)" }}
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
        onDeleteSession={deleteSession}
        hidden={!!sessionDetail}
      />
      <SessionDetail
        session={sessionDetail}
        currentUserID={currentUserID}
        onBack={() => {
          setSessionDetail(null);
          history.pushState(null, "", "/sessions");
        }}
        onSessionUpdate={(s) => setSessionDetail(s)}
      />
    </div>
  );
}

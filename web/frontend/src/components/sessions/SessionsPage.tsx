import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "@/lib/api";
import type { Agent, Session } from "@/lib/types";
import { SessionSidebar } from "./SessionSidebar";
import { SessionDetail } from "./SessionDetail";

export function SessionsPage() {
  const [sessions, setSessions] = useState<Session[]>([]);
  const [, setSessionsOffset] = useState(0);
  const [sessionsHasMore, setSessionsHasMore] = useState(false);
  const [sessionsLoading, setSessionsLoading] = useState(false);

  const [sessionDetail, setSessionDetail] = useState<Session | null>(null);
  const [agents, setAgents] = useState<Agent[]>([]);
  const [currentUserID, setCurrentUserID] = useState<number>(0);

  const offsetRef = useRef(0);
  const loadingRef = useRef(false);

  const loadSessions = useCallback(async (reset = false) => {
    if (loadingRef.current) return;
    loadingRef.current = true;
    setSessionsLoading(true);
    const offset = reset ? 0 : offsetRef.current;
    try {
      const batch = (await api<Session[]>("GET", `/api/sessions?limit=10&offset=${offset}`)) ?? [];
      setSessions((prev) => (offset === 0 ? batch : [...prev, ...batch]));
      const newOffset = offset + batch.length;
      offsetRef.current = newOffset;
      setSessionsOffset(newOffset);
      setSessionsHasMore(batch.length === 10);
    } catch (e) {
      console.error(e);
    } finally {
      loadingRef.current = false;
      setSessionsLoading(false);
    }
  }, []);

  const openSession = useCallback(
    async (sessionID: string, pushState = true) => {
      if (pushState) {
        history.pushState({ sessionID }, "", "/sessions/" + encodeURIComponent(sessionID));
      }
      try {
        const detail = await api<Session>("GET", `/api/sessions/${encodeURIComponent(sessionID)}`);
        setSessionDetail(detail);
      } catch (e) {
        console.error(e);
      }
    },
    [],
  );

  const createSession = useCallback(
    async (agentID: string) => {
      const sess = await api<Session>("POST", "/api/sessions", { agent_id: agentID });
      setSessions((prev) => [sess, ...prev]);
      setSessionsOffset((o) => o + 1);
      offsetRef.current += 1;
      await openSession(sess.id);
    },
    [openSession],
  );

  useEffect(() => {
    const init = async () => {
      await Promise.all([
        loadSessions(true),
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
      const sessionID = parts.length >= 3 && parts[1] === "sessions" ? decodeURIComponent(parts[2]) : "";
      if (sessionID) {
        await openSession(sessionID, false);
      }
    };
    void init();
  }, [loadSessions, openSession]);

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
      setSessions((prev) => prev.filter((s) => s.id !== id));
      offsetRef.current = Math.max(0, offsetRef.current - 1);
      if (sessionDetail?.id === id) {
        setSessionDetail(null);
        history.pushState(null, "", "/sessions");
      }
    },
    [sessionDetail],
  );

  return (
    <div
      className="-mx-6 -mt-12 border-t border-border grid grid-cols-1 lg:grid-cols-[300px_1fr] overflow-hidden"
      style={{ height: "calc(100vh - 3.5rem)" }}
    >
      <SessionSidebar
        sessions={sessions}
        sessionsLoading={sessionsLoading}
        sessionsHasMore={sessionsHasMore}
        selectedID={sessionDetail?.id}
        agents={agents}
        onSelect={openSession}
        onLoadMore={() => loadSessions(false)}
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

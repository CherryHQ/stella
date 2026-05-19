import { useEffect, useMemo, useRef } from "react";
import { useNavigate, useParams } from "@tanstack/react-router";
import { useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import type { Session } from "@/lib/types";
import { sessionsInfiniteQueryOptions } from "@/lib/queries/sessions";

export function ProjectHome() {
  const { agentId, projectId } = useParams({
    from: "/_app/agents/$agentId/projects/$projectId/",
  });
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const creating = useRef(false);

  const sessionsQuery = useInfiniteQuery(sessionsInfiniteQueryOptions(agentId));
  const sessions = sessionsQuery.data?.pages.flat() ?? [];

  const targetSession = useMemo(() => {
    return sessions.find((s) => s.project_id === projectId && !s.archived) ?? null;
  }, [sessions, projectId]);

  useEffect(() => {
    if (sessionsQuery.isLoading) return;
    if (targetSession) {
      void navigate({
        to: "/agents/$agentId/projects/$projectId/sessions/$sessionId",
        params: { agentId, projectId, sessionId: targetSession.id },
        replace: true,
      });
      return;
    }
    if (creating.current) return;
    creating.current = true;
    api<Session>("POST", "/api/sessions", {
      agent_id: agentId,
      project_id: projectId,
    })
      .then(async (sess) => {
        await queryClient.invalidateQueries({ queryKey: ["sessions", agentId] });
        void navigate({
          to: "/agents/$agentId/projects/$projectId/sessions/$sessionId",
          params: { agentId, projectId, sessionId: sess.id },
          replace: true,
        });
      })
      .catch((err) => {
        console.error(err);
        creating.current = false;
      });
  }, [targetSession, sessionsQuery.isLoading, agentId, projectId, navigate, queryClient]);

  return (
    <div className="flex-1 flex items-center justify-center">
      <div className="w-4 h-4 border-2 border-muted-foreground/30 border-t-muted-foreground rounded-full animate-spin" />
    </div>
  );
}

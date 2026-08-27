import { useEffect, useMemo, useRef } from "react";
import { useNavigate, useParams } from "@tanstack/react-router";
import { useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import { createSession } from "@/lib/api-client/sdk.gen";
import type { Session } from "@/lib/types";
import { sessionsInfiniteQueryOptions } from "@/lib/queries/sessions";

export function AgentHome() {
  const { agentId } = useParams({ from: "/_app/agents/$agentId/" });
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const creating = useRef(false);

  const sessionsQuery = useInfiniteQuery(sessionsInfiniteQueryOptions(agentId));
  const sessions = sessionsQuery.data?.pages.flatMap((p) => p.sessions) ?? [];

  const mainSession = useMemo(() => {
    const active = sessions.filter((s) => !s.archived);
    return active.find((s) => s.kind === "main") ?? null;
  }, [sessions]);

  useEffect(() => {
    if (sessionsQuery.isLoading) return;
    if (mainSession) {
      void navigate({
        to: "/agents/$agentId/sessions/$sessionId",
        params: { agentId, sessionId: mainSession.id },
        replace: true,
      });
      return;
    }
    if (creating.current) return;
    creating.current = true;
    createSession({
      path: { agentId: agentId },
      body: { kind: "main" },
      throwOnError: true,
    })
      .then(async ({ data }) => {
        // SAFETY: createSession returns the created session under data.
        const sess = data as Session;
        await queryClient.invalidateQueries({ queryKey: ["sessions", agentId] });
        void navigate({
          to: "/agents/$agentId/sessions/$sessionId",
          params: { agentId, sessionId: sess.id },
          replace: true,
        });
      })
      .catch((err) => {
        console.error(err);
        creating.current = false;
      });
  }, [agentId, mainSession, sessionsQuery.isLoading, navigate, queryClient]);

  return (
    <div className="flex flex-1 items-center justify-center bg-card/70">
      <div className="size-4 animate-spin rounded-full border-2 border-muted-foreground/30 border-t-muted-foreground" />
    </div>
  );
}

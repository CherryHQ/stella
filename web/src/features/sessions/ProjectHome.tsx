import { useEffect, useMemo, useRef } from "react";
import { useNavigate, useParams } from "@tanstack/react-router";
import { useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import { createSession } from "@/lib/api-client/sdk.gen";
import { unwrapApiData } from "@/lib/api-data";
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
    createSession({
      path: { agentID: agentId },
      body: { project_id: projectId },
      throwOnError: true,
    })
      .then(async ({ data }) => {
        const sess = unwrapApiData<Session>(data);
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
    <div className="flex flex-1 items-center justify-center bg-gradient-to-b from-card/80 to-card p-6">
      <div className="w-full max-w-md rounded-[24px] border border-border/80 bg-card p-6 text-center shadow-[0_22px_54px_rgba(29,29,31,0.10)]">
        <div className="mx-auto mb-4 grid size-12 place-items-center rounded-[16px] bg-foreground text-base font-bold text-background shadow-sm">
          S
        </div>
        <h1 className="text-xl font-semibold tracking-[-0.03em]">Opening project workspace…</h1>
        <p className="mt-2 text-sm leading-6 text-muted-foreground">
          Stella is preparing the durable project session and loading workspace context.
        </p>
        <div className="mx-auto mt-5 size-4 animate-spin rounded-full border-2 border-muted-foreground/30 border-t-muted-foreground" />
      </div>
    </div>
  );
}

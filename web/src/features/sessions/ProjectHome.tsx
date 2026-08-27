import { useEffect, useRef } from "react";
import { useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { createSession } from "@/lib/api-client/sdk.gen";
import type { Session } from "@/lib/types";
import { projectSessionsQueryOptions } from "@/lib/queries/sessions";
import { OverviewPage } from "@/features/goals/OverviewPage";

export function ProjectHome() {
  const { agentId, projectId } = useParams({
    from: "/_app/agents/$agentId/projects/$projectId/",
  });
  const { tab: rawTab } = useSearch({ from: "/_app/agents/$agentId/projects/$projectId/" });
  const tab = rawTab === "goals" ? "goals" : "conversation";
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const creating = useRef(false);

  const sessionsQuery = useQuery(projectSessionsQueryOptions(agentId, projectId));
  const projectSessions = (sessionsQuery.data ?? []).filter(
    (s) => !s.archived && s.kind !== "delegate" && s.kind !== "scheduler",
  );
  const mainSession = projectSessions.find((s) => s.kind === "main") ?? null;

  useEffect(() => {
    if (!sessionsQuery.isSuccess) return;
    if (mainSession) {
      if (tab === "conversation") {
        void navigate({
          to: "/agents/$agentId/projects/$projectId/sessions/$sessionId",
          params: { agentId, projectId, sessionId: mainSession.id },
          replace: true,
        });
      }
      return;
    }
    if (creating.current) return;
    creating.current = true;
    createSession({
      path: { agentId: agentId },
      body: { project_id: projectId, kind: "main" },
      throwOnError: true,
    })
      .then(async ({ data }) => {
        // SAFETY: createSession returns the created session under data.
        const session = data as Session;
        await queryClient.invalidateQueries({ queryKey: ["sessions", agentId] });
        if (tab === "conversation") {
          void navigate({
            to: "/agents/$agentId/projects/$projectId/sessions/$sessionId",
            params: { agentId, projectId, sessionId: session.id },
            replace: true,
          });
        }
      })
      .catch((err) => {
        console.error(err);
        creating.current = false;
      });
  }, [mainSession, sessionsQuery.isSuccess, agentId, projectId, queryClient, navigate, tab]);

  if (tab === "goals") return <OverviewPage />;

  return (
    <div className="flex flex-1 items-center justify-center bg-card/70">
      <div className="size-4 animate-spin rounded-full border-2 border-muted-foreground/30 border-t-muted-foreground" />
    </div>
  );
}

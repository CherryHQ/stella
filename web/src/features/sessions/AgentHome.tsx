import { useCallback, useEffect, useMemo } from "react";
import { useNavigate, useParams } from "@tanstack/react-router";
import { useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import type { Session } from "@/lib/types";
import { sessionsInfiniteQueryOptions } from "@/lib/queries/sessions";
import { useI18n } from "@/lib/i18n";

export function AgentHome() {
  const { agentId } = useParams({ from: "/_app/agents/$agentId/" });
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { t } = useI18n();

  const sessionsQuery = useInfiniteQuery(sessionsInfiniteQueryOptions(agentId));
  const sessions = sessionsQuery.data?.pages.flat() ?? [];

  // Prefer main session, then any chat session — never redirect to scheduler/task sessions.
  const targetSession = useMemo(() => {
    const active = sessions.filter((s) => !s.archived);
    return active.find((s) => s.kind === "main") ?? active.find((s) => s.kind === "chat") ?? null;
  }, [sessions]);

  useEffect(() => {
    if (!sessionsQuery.isLoading && targetSession) {
      void navigate({
        to: "/agents/$agentId/sessions/$sessionId",
        params: { agentId, sessionId: targetSession.id },
        replace: true,
      });
    }
  }, [targetSession, sessionsQuery.isLoading, agentId, navigate]);

  const createSession = useCallback(async () => {
    const sess = await api<Session>("POST", "/api/sessions", { agent_id: agentId });
    await queryClient.invalidateQueries({ queryKey: ["sessions", agentId] });
    void navigate({
      to: "/agents/$agentId/sessions/$sessionId",
      params: { agentId, sessionId: sess.id },
    });
  }, [agentId, queryClient, navigate]);

  if (sessionsQuery.isLoading) {
    return (
      <div className="flex-1 flex items-center justify-center">
        <div className="w-4 h-4 border-2 border-muted-foreground/30 border-t-muted-foreground rounded-full animate-spin" />
      </div>
    );
  }

  if (!targetSession) {
    return (
      <div className="flex-1 flex flex-col items-center justify-center gap-4">
        <p className="text-sm text-muted-foreground/60 font-mono">
          {t("sessions.sidebar.noChats")}
        </p>
        <button
          onClick={() => void createSession()}
          className="px-4 py-2 rounded-xl text-sm font-medium text-primary-foreground bg-primary hover:bg-primary/90 active:scale-[0.98] transition-all duration-150"
        >
          {t("sessions.sidebar.newChat")}
        </button>
      </div>
    );
  }

  return null;
}

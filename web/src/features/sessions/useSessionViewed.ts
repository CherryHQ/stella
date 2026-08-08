import { useCallback, useEffect } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { markSessionViewed } from "@/lib/api-client/sdk.gen";

/**
 * Clear sidebar attention when a session is actually on screen. Calling again
 * after stream completion closes the mount-vs-completion race: a user who
 * watched the turn finish should not receive an unread marker for that turn.
 */
export function useSessionViewed(agentId: string, sessionId: string) {
  const queryClient = useQueryClient();
  const markViewed = useCallback(async () => {
    if (!agentId || !sessionId) return;
    try {
      await markSessionViewed({ path: { agentId, sessionId }, throwOnError: true });
      await queryClient.invalidateQueries({ queryKey: ["sessions", agentId] });
    } catch (error) {
      console.error("[session viewed]", error);
    }
  }, [agentId, queryClient, sessionId]);

  useEffect(() => {
    void markViewed();
  }, [markViewed]);

  return markViewed;
}

import { useEffect, useRef } from "react";

type ChatStatus = "submitted" | "streaming" | "ready" | "error";

/**
 * Attach an idle chat view to any active server-side turn. Sending and watching
 * use separate SSE connections, so navigation, refresh, and transient network
 * loss can reconnect without owning the turn's lifetime.
 */
export function useSessionStreamResume(
  sessionId: string,
  enabled: boolean,
  status: ChatStatus,
  resumeStream: () => Promise<void>,
  recoveringDisconnect: boolean,
  clearError: () => void,
  onInitialCheck: () => void,
) {
  const statusRef = useRef(status);
  const resumingRef = useRef(false);
  const checkedSessionRef = useRef<string | null>(null);
  statusRef.current = status;

  useEffect(() => {
    if (!sessionId || !enabled) return;
    let cancelled = false;

    const tick = () => {
      if (cancelled || resumingRef.current) return;
      if (statusRef.current === "error" && recoveringDisconnect) {
        clearError();
        return;
      }
      if (statusRef.current !== "ready") return;
      resumingRef.current = true;
      void resumeStream().finally(() => {
        resumingRef.current = false;
        if (checkedSessionRef.current !== sessionId || recoveringDisconnect) {
          checkedSessionRef.current = sessionId;
          onInitialCheck();
        }
      });
    };

    tick();
    const timer = window.setInterval(tick, 3000);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [sessionId, enabled, resumeStream, recoveringDisconnect, clearError, onInitialCheck]);
}

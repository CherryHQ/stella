import { useEffect, useRef } from "react";

type ChatStatus = "submitted" | "streaming" | "ready" | "error";

/**
 * Attach an idle chat view to any active server-side turn. Sending and watching
 * use separate SSE connections, so navigation, refresh, and transient network
 * loss can reconnect without owning the turn's lifetime.
 *
 * The in-flight resume is token-scoped: a session switch releases the slot for
 * the new session's ticks, and the old attempt's cleanup can never clear a
 * newer request.
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
  const resumingRef = useRef<symbol | null>(null);
  const checkedSessionRef = useRef<string | null>(null);
  statusRef.current = status;

  useEffect(() => {
    if (!sessionId || !enabled) return;
    let cancelled = false;
    let deferredTimer: number | undefined;

    const tick = () => {
      if (cancelled || resumingRef.current !== null) return;
      if (statusRef.current === "error" && recoveringDisconnect) {
        clearError();
        return;
      }
      if (statusRef.current !== "ready") return;
      const token = Symbol(sessionId);
      resumingRef.current = token;
      void resumeStream().finally(() => {
        // A stale resume (session moved on, newer attempt took the slot) must
        // not free the slot or schedule a check against the wrong session.
        if (resumingRef.current !== token) return;
        resumingRef.current = null;
        if (cancelled) return;
        // AI SDK resolves resumeStream() for 204 and transport errors alike;
        // status is committed on the next render. Reconcile only from ready,
        // which means either a clean stream finish or no active stream.
        deferredTimer = window.setTimeout(() => {
          if (
            !cancelled &&
            statusRef.current === "ready" &&
            (checkedSessionRef.current !== sessionId || recoveringDisconnect)
          ) {
            checkedSessionRef.current = sessionId;
            onInitialCheck();
          }
        }, 0);
      });
    };

    tick();
    const timer = window.setInterval(tick, 3000);
    return () => {
      cancelled = true;
      resumingRef.current = null;
      window.clearInterval(timer);
      if (deferredTimer !== undefined) window.clearTimeout(deferredTimer);
    };
  }, [sessionId, enabled, resumeStream, recoveringDisconnect, clearError, onInitialCheck]);
}

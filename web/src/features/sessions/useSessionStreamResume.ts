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
  isRemoteRunActive: (() => boolean) | undefined,
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
    let deferredTimer: number | undefined;

    const tick = () => {
      if (cancelled || resumingRef.current) return;
      if (statusRef.current === "error" && (recoveringDisconnect || isRemoteRunActive?.())) {
        clearError();
        return;
      }
      if (statusRef.current !== "ready") return;
      resumingRef.current = true;
      void resumeStream().finally(() => {
        resumingRef.current = false;
        if (cancelled) return;
        // AI SDK resolves resumeStream() for 204 and transport errors alike;
        // status is committed on the next render. Reconcile only from ready,
        // which means either a clean stream finish or no active stream.
        deferredTimer = window.setTimeout(() => {
          if (!cancelled && statusRef.current === "ready") {
            const remoteRunActive = isRemoteRunActive?.() === true;
            if (
              !remoteRunActive &&
              checkedSessionRef.current === sessionId &&
              !recoveringDisconnect
            ) {
              return;
            }
            // Keep the one-shot check pending while another replica owns the
            // turn. This makes every 3s tick reconcile the persisted transcript
            // until the remote run's 503 disappears.
            checkedSessionRef.current = remoteRunActive ? null : sessionId;
            onInitialCheck();
          }
        }, 0);
      });
    };

    tick();
    const timer = window.setInterval(tick, 3000);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
      if (deferredTimer !== undefined) window.clearTimeout(deferredTimer);
    };
  }, [
    sessionId,
    enabled,
    resumeStream,
    isRemoteRunActive,
    recoveringDisconnect,
    clearError,
    onInitialCheck,
  ]);
}

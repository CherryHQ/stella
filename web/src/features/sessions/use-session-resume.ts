import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { UIMessage } from "ai";
import { ResumeSupersededError } from "@/lib/chat-transport";
import type { SessionTransportOptions } from "@/lib/chat-transport";

/**
 * HTTP status of a failed prefix fetch — the generated client's throwOnError
 * discards it, so prefix loaders wrap failures in this. `status` is undefined
 * for network drops (fetch never produced a response).
 */
export class ResumeHttpError extends Error {
  readonly status?: number;
  constructor(status?: number) {
    super(status === undefined ? "session prefix fetch failed" : `session prefix: HTTP ${status}`);
    this.status = status;
  }
}

// Only failures the same request can recover from: a dropped connection (no
// status) or a transient 5xx/408/429. Other 4xx (401/403/…) are answers, not
// outages — retrying them would spin forever.
function isRetryableStatus(status: number | undefined): boolean {
  return status === undefined || status === 408 || status === 429 || status >= 500;
}

export function isRetryablePrefixError(err: unknown): boolean {
  if (err instanceof ResumeHttpError) return isRetryableStatus(err.status);
  // A fetch that rejects outside the client's wrapper is a network drop.
  return err instanceof TypeError;
}

/**
 * One session's observe-resume state, shared by every chat surface that
 * watches durable turns.
 *
 * While a resume is live, `boundary` holds the observed turn's history
 * watermark: every history query caps at it so a mid-turn refetch cannot merge
 * the turn's own canonical rows over the replaying overlay. The pinned scope
 * names the observed turn (run id or "x"+token) so a reconnect re-observes
 * that exact turn — even after it finished and its execution row is gone —
 * until its durable terminal frame releases the pin.
 *
 * Neither the boundary nor the pin may be cleared on stream close alone: an
 * interrupted connection is not terminal evidence. Only the
 * `data-turn-terminal` frame (a durable fact), an explicit gone answer from
 * the server, or a session switch releases them.
 */
export function useSessionResume(options: {
  sessionId: string;
  // Fetches the visible canonical prefix for the boundary — same paging /
  // compacted-tail shape the component normally renders, capped server-side.
  loadPrefix: (boundary: number) => Promise<UIMessage[]>;
  setMessages: (messages: UIMessage[]) => void;
  // The observed turn's durable terminal arrived (or its record is gone):
  // lift the cap and reconcile canonical history authoritatively.
  onTerminal: () => void;
  // A stream ended without releasing a pin — an unscoped plain reply (slash
  // commands carry no turn identity) or a scoped EOF with no receipt. Refetch
  // canonical history under whatever cap is still held; the pin itself is
  // untouched.
  onRefresh?: () => void;
  // The current request failed during observe bootstrap, with its exact
  // retryable verdict — every failure reports, so a new non-retryable answer
  // (403) overwrites an older retryable one (503) instead of inheriting it.
  // Mid-stream drops are not reported here; the SDK's onFinish isDisconnect
  // already classifies them.
  onFailure?: (retryable: boolean) => void;
}) {
  const { sessionId } = options;
  const [boundary, setBoundary] = useState<number | null>(null);
  const optsRef = useRef(options);
  optsRef.current = options;
  // Resume state lives in refs: the transport captures its options object at
  // construction, so everything it reads must be stable across renders.
  const pinRef = useRef<string | undefined>(undefined);
  const genRef = useRef(0);
  // A verified durable terminal receipt waits here for the local stream to
  // finish — releasing the cap while the SDK is still consuming would let a
  // racing canonical refetch merge this turn's rows over its live overlay.
  const receiptRef = useRef<{ scope: string } | undefined>(undefined);
  const sessionRef = useRef(sessionId);
  // A session switch invalidates any in-flight bootstrap immediately — an old
  // session's callback must not seed into the new chat.
  if (sessionRef.current !== sessionId) {
    genRef.current++;
    sessionRef.current = sessionId;
    pinRef.current = undefined;
    receiptRef.current = undefined;
  }
  useEffect(() => {
    setBoundary(null);
    pinRef.current = undefined;
    receiptRef.current = undefined;
  }, [sessionId]);

  // The durable terminal receipt arrived (data-turn-terminal), or the pinned
  // turn's record is gone: release pin and cap, reconcile canonical history.
  const release = useCallback(() => {
    pinRef.current = undefined;
    receiptRef.current = undefined;
    setBoundary(null);
    optsRef.current.onTerminal();
  }, []);

  const transportOptions = useMemo<SessionTransportOptions>(
    () => ({
      onResumeReady: async (meta, signal) => {
        const forSession = sessionId;
        // Session check first: a callback from an old session must not burn a
        // generation the current session's in-flight seed still needs.
        if (sessionRef.current !== forSession || signal.aborted) {
          throw new ResumeSupersededError("stale session resume");
        }
        const gen = ++genRef.current;
        const stale = () =>
          gen !== genRef.current || sessionRef.current !== forSession || signal.aborted;
        const o = optsRef.current;
        // The stream's message identity doubles as the observe scope:
        // "msg-<runID>" pins runID, "msg-x<token>" pins the execution token.
        pinRef.current = meta.messageId.slice(4);
        // The GET resume alone owns the boundary: it is the only path that
        // reseeds from canonical history, so it is the only one that caps it.
        setBoundary(meta.historyBefore);
        let prefix: UIMessage[];
        try {
          prefix = meta.historyBefore > 0 ? await o.loadPrefix(meta.historyBefore) : [];
        } catch (err) {
          // A transient prefix failure keeps pin + boundary so the retry
          // re-enters the same observed turn — it is not a gone answer.
          // The verdict is reported either way so a non-retryable answer
          // (403) clears any retryable state an earlier attempt left.
          if (!stale()) o.onFailure?.(isRetryablePrefixError(err));
          throw err;
        }
        if (stale()) throw new ResumeSupersededError("stale session resume");
        o.setMessages([...prefix, { id: meta.messageId, role: "assistant" as const, parts: [] }]);
      },
      // A send's response headers register the new turn's identity: it
      // supersedes any in-flight bootstrap (gen bump) and pins the turn so
      // its durable terminal releases the cap and reconciles history.
      // Deliberately no boundary/reseed — the SDK already owns the just-sent
      // user message; bumping the boundary here would fire a history query
      // that merges the canonical user row over the optimistic one.
      onSendReady: (meta) => {
        if (sessionRef.current !== sessionId) return;
        genRef.current++;
        pinRef.current = meta.messageId.slice(4);
      },
      // The server explicitly answered "gone" for the scope this request
      // carried. Only that exact scope may release the pin — a superseded
      // request's late answer must not free a newer pin.
      onResumeGone: (requestScope) => {
        if (
          sessionRef.current === sessionId &&
          requestScope !== undefined &&
          pinRef.current === requestScope
        ) {
          release();
        }
      },
      // The observe request itself failed — report its exact retryable
      // verdict for the page's disconnect-recovery state. 0 is a network
      // drop; 4xx answers (401/403/…) are authorization facts, not outages.
      // A superseded request (its scope no longer pinned) reports nothing.
      onResumeFailed: (requestScope, status) => {
        if (sessionRef.current !== sessionId) return;
        if (requestScope !== undefined && requestScope !== pinRef.current) return;
        optsRef.current.onFailure?.(status === 0 || isRetryableStatus(status));
      },
      observeScope: () => pinRef.current,
    }),
    [sessionId, release],
  );

  // The durable terminal receipt travels as a transient data part; route it
  // from useChat's onData. It carries session+scope identity — a late frame
  // from an older observe must not count for the current turn's pin. The
  // receipt is only recorded here: releasing needs the local stream to be
  // done too, or a canonical refetch racing the still-open overlay merges
  // this turn's rows under authoritative=false and the dupes never leave.
  const handleDataPart = useCallback((part: { type?: string; data?: unknown }) => {
    if (part?.type !== "data-turn-terminal") return;
    // SAFETY: the wire shape is written by streamAgentEvents in
    // internal/server/sessions.go — session_id and scope are optional strings.
    const data = part.data as { session_id?: string; scope?: string } | undefined;
    const scope = data?.scope;
    if (
      data?.session_id !== sessionRef.current ||
      scope === undefined ||
      scope !== pinRef.current
    ) {
      return;
    }
    receiptRef.current = { scope };
  }, []);

  // The local stream settled. useChat fires onFinish from its finally —
  // errors included — and hands over the stream's actual message identity.
  // The receipt only releases when this stream's scope matches both the
  // receipt and the current pin: a late finish from an old stream must not
  // consume the newer turn's receipt, and a bare EOF proves nothing.
  const handleStreamEnd = useCallback(
    (message?: { id?: string }) => {
      const id = message?.id ?? "";
      const scope = id.startsWith("msg-") ? id.slice(4) : undefined;
      const receipt = receiptRef.current;
      if (scope !== undefined && receipt?.scope === scope && scope === pinRef.current) {
        receiptRef.current = undefined;
        release();
        return;
      }
      if (scope === undefined && pinRef.current === undefined) {
        // An unscoped stream (plain replies to slash commands mint a bare
        // uuid, never "msg-<scope>") registers no pin; its canonical rows are
        // already settled history, so reconcile them authoritatively.
        optsRef.current.onTerminal();
        return;
      }
      // A scoped end without a matching receipt, or a superseded stream's
      // late finish: the pin stays, but a canonical refetch under the cap
      // still converges any rows the ended stream left behind.
      optsRef.current.onRefresh?.();
    },
    [release],
  );

  // A new send supersedes any stale pinned turn: the turn it belonged to is
  // finished or finishing, and its canonical rows are history now. The gen
  // bump also kills any in-flight bootstrap whose late prefix would
  // otherwise overwrite the new send's messages.
  const clear = useCallback(() => {
    genRef.current++;
    pinRef.current = undefined;
    receiptRef.current = undefined;
    setBoundary(null);
  }, []);

  return { boundary, transportOptions, handleDataPart, handleStreamEnd, clear };
}

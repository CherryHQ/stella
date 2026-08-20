import { useEffect, useRef } from "react";
import type { GroupMessage } from "@/lib/api-client/types.gen";

// Exactly the states the dispatcher emits on a turn frame: an agent yielded
// (held), stayed quiet (silent), or blew up (failed). An accepted reply is a
// message frame, not a turn frame, so there is no "done" here. Widen this union
// the day the server emits a live progress state; the reducer already handles
// non-terminal states.
export type GroupTurnState = "held" | "silent" | "failed";

export interface GroupTurnEvent {
  agent_id: string;
  state: GroupTurnState;
  reason?: string;
}

interface Options {
  sinceSeq: number;
  onMessage: (message: GroupMessage) => void;
  onTurn: (turn: GroupTurnEvent) => void;
}

// The event log is canonical. EventSource reconnects automatically, and the
// sequence cursor makes reconnect replay safe after a dropped hub subscriber.
export function useGroupEvents(groupId: string, { sinceSeq, onMessage, onTurn }: Options) {
  const callbacks = useRef({ onMessage, onTurn });
  const initialSeq = useRef(sinceSeq);
  callbacks.current = { onMessage, onTurn };

  useEffect(() => {
    if (!groupId) return;
    const source = new EventSource(
      `/api/groups/${encodeURIComponent(groupId)}/events?since_seq=${Math.max(0, initialSeq.current)}`,
    );
    source.addEventListener("message", (event) => {
      try {
        callbacks.current.onMessage(JSON.parse(event.data) as GroupMessage);
      } catch {
        // A malformed best-effort frame must not kill durable replay on reconnect.
      }
    });
    source.addEventListener("turn", (event) => {
      try {
        callbacks.current.onTurn(JSON.parse(event.data) as GroupTurnEvent);
      } catch {
        // See message frame handling above.
      }
    });
    return () => source.close();
  }, [groupId]);
}

import { useEffect, useRef } from "react";
import type { GroupMessage } from "@/lib/api-client/types.gen";

// Exactly the states the dispatcher emits on a turn frame, mirroring the
// GroupTurnEvent schema in api/spec/domain/groups. "running" is the live one: an
// agent is generating right now, and it holds until one terminal state replaces
// it -- the agent's reply was published ("done"), it yielded ("held"), it stayed
// quiet ("silent"), or it blew up ("failed"). A fresh subscriber is handed a
// "running" snapshot on connect, so this state survives a reload.
export type GroupTurnState = "running" | "held" | "silent" | "failed" | "done";

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
        // SAFETY: the SSE message frame's JSON body is the GroupMessage shape; malformed frames are caught below.
        callbacks.current.onMessage(JSON.parse(event.data) as GroupMessage);
      } catch {
        // A malformed best-effort frame must not kill durable replay on reconnect.
      }
    });
    source.addEventListener("turn", (event) => {
      try {
        // SAFETY: the SSE turn frame's JSON body is the GroupTurnEvent shape.
        callbacks.current.onTurn(JSON.parse(event.data) as GroupTurnEvent);
      } catch {
        // See message frame handling above.
      }
    });
    return () => source.close();
  }, [groupId]);
}

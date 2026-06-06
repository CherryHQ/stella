import type { GroupMessage } from "@/lib/api-client/types.gen";

export interface GroupStreamMessage {
  agentId: string;
  agentName: string;
  messageId: string;
  text: string;
  reasoning: string;
  done: boolean;
}

export interface GroupStreamCallbacks {
  onAgentStart: (agentId: string, agentName: string, messageId: string) => void;
  onTextDelta: (agentId: string, delta: string) => void;
  onReasoningDelta: (agentId: string, delta: string) => void;
  onAgentEnd: (agentId: string) => void;
  onFinish: () => void;
  onError: (error: string) => void;
}

export async function sendGroupMessage(
  groupId: string,
  content: string,
  callbacks: GroupStreamCallbacks,
  signal?: AbortSignal,
): Promise<void> {
  const res = await fetch(`/api/groups/${encodeURIComponent(groupId)}/messages`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ content }),
    signal,
  });

  if (!res.ok) {
    const err = await res.text().catch(() => "request failed");
    callbacks.onError(err);
    return;
  }

  const reader = res.body?.getReader();
  if (!reader) {
    callbacks.onError("no response body");
    return;
  }

  const decoder = new TextDecoder();
  let buffer = "";

  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;

      buffer += decoder.decode(value, { stream: true });
      const lines = buffer.split("\n");
      buffer = lines.pop() ?? "";

      for (const line of lines) {
        if (!line.startsWith("data: ")) continue;
        const payload = line.slice(6);
        if (payload === "[DONE]") {
          callbacks.onFinish();
          return;
        }

        try {
          const evt = JSON.parse(payload) as Record<string, string>;
          switch (evt.type) {
            case "agent-start":
              callbacks.onAgentStart(evt.agentId, evt.agentName, evt.messageId);
              break;
            case "text-delta":
              if (evt.delta) callbacks.onTextDelta(evt.agentId ?? "", evt.delta);
              break;
            case "reasoning-delta":
              if (evt.delta) callbacks.onReasoningDelta(evt.agentId ?? "", evt.delta);
              break;
            case "agent-end":
              callbacks.onAgentEnd(evt.agentId);
              break;
            case "finish":
              callbacks.onFinish();
              return;
            case "error":
              callbacks.onError(evt.errorText ?? "unknown error");
              break;
          }
        } catch {
          // skip malformed JSON
        }
      }
    }
  } finally {
    reader.releaseLock();
  }
}

export function groupMessageToDisplay(m: GroupMessage): {
  id: string;
  role: "user" | "assistant";
  agentId?: string;
  agentName?: string;
  content: string;
  timestamp: string;
} {
  return {
    id: m.id,
    role: m.actor_type === "human" ? "user" : "assistant",
    agentId: m.actor_type === "agent" ? m.actor_id : undefined,
    agentName: m.actor_type === "agent" ? m.actor_name : undefined,
    content: m.content,
    timestamp: m.created_at,
  };
}

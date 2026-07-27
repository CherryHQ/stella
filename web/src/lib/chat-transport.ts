import { DefaultChatTransport } from "ai";
import type { UIMessage } from "ai";
import type { GroupMessage } from "@/lib/api-client/types.gen";
import type { Message, RenderableReference } from "./types";

export function createSessionTransport(agentId: string, sessionId: string) {
  const base = `/api/agents/${encodeURIComponent(agentId)}/sessions/${encodeURIComponent(sessionId)}`;
  return new DefaultChatTransport({
    api: `${base}/messages`,
    prepareSendMessagesRequest: ({ messages }) => {
      const last = messages[messages.length - 1];
      const parts = last.parts
        .map((p) => {
          if (p.type === "text") return { type: "text" as const, text: p.text };
          // File parts carry a workspace path the server resolves; the browser's
          // media type rides along as a hint only.
          if (p.type === "file")
            return { type: "file" as const, url: p.url, mimeType: p.mediaType };
          return null;
        })
        .filter(Boolean);
      return { body: { parts } };
    },
    // Read-only resume: watch a turn started elsewhere (server-driven
    // scheduler/task/delegate turns, or another tab) via the events SSE
    // endpoint. It answers 204 when no turn is in flight, which the SDK treats
    // as "nothing to resume".
    prepareReconnectToStreamRequest: () => ({ api: `${base}/events` }),
  });
}

export function createGroupTransport(groupId: string) {
  return new DefaultChatTransport({
    api: `/api/groups/${encodeURIComponent(groupId)}/messages`,
    prepareSendMessagesRequest: ({ messages }) => {
      const last = messages[messages.length - 1];
      const text = last.parts
        .filter(
          (p): p is Extract<(typeof last.parts)[number], { type: "text" }> => p.type === "text",
        )
        .map((p) => p.text)
        .join("");
      return { body: { content: text, client_message_id: last.id } };
    },
  });
}

export interface AgentInfoData {
  agentId: string;
  agentName: string;
  agentSessionId?: string;
}

export function groupMessagesToUIMessages(
  messages: GroupMessage[],
  agentNameMap: Map<string, string>,
): UIMessage[] {
  const result: UIMessage[] = [];
  let i = 0;

  while (i < messages.length) {
    const msg = messages[i];

    if (msg.actor_type === "human") {
      result.push({
        id: `grp-${msg.id}`,
        role: "user",
        parts: [{ type: "text" as const, text: msg.content }],
        metadata: { timestamp: msg.created_at },
      });
      i++;

      const agentParts: UIMessage["parts"] = [];
      let firstAgentId = "";

      while (i < messages.length && messages[i].actor_type === "agent") {
        const agentMsg = messages[i];
        const agentName =
          agentNameMap.get(agentMsg.actor_id) ?? agentMsg.actor_name ?? agentMsg.actor_id;

        if (!firstAgentId) firstAgentId = agentMsg.id;

        agentParts.push({ type: "step-start" as const });
        agentParts.push({
          type: "data-agent-info",
          id: `ai-${agentMsg.id}`,
          data: {
            agentId: agentMsg.actor_id,
            agentName,
            agentSessionId: agentMsg.agent_session_id,
          },
        } as UIMessage["parts"][number]);

        if (agentMsg.reasoning) {
          agentParts.push({ type: "reasoning", text: agentMsg.reasoning, providerMetadata: {} });
        }
        if (agentMsg.content) {
          agentParts.push({ type: "text" as const, text: agentMsg.content });
        }

        i++;
      }

      if (agentParts.length > 0) {
        result.push({
          id: `grp-ast-${firstAgentId}`,
          role: "assistant",
          parts: agentParts,
          metadata: { timestamp: messages[i - 1]?.created_at },
        });
      }
    } else {
      const agentMsg = messages[i];
      const agentName =
        agentNameMap.get(agentMsg.actor_id) ?? agentMsg.actor_name ?? agentMsg.actor_id;
      const parts: UIMessage["parts"] = [
        { type: "step-start" as const },
        {
          type: "data-agent-info",
          id: `ai-${agentMsg.id}`,
          data: {
            agentId: agentMsg.actor_id,
            agentName,
            agentSessionId: agentMsg.agent_session_id,
          },
        } as UIMessage["parts"][number],
      ];
      if (agentMsg.reasoning) {
        parts.push({ type: "reasoning", text: agentMsg.reasoning, providerMetadata: {} });
      }
      if (agentMsg.content) {
        parts.push({ type: "text" as const, text: agentMsg.content });
      }
      result.push({
        id: `grp-ast-${agentMsg.id}`,
        role: "assistant",
        parts,
        metadata: { timestamp: agentMsg.created_at },
      });
      i++;
    }
  }

  return result;
}

/**
 * Merge standalone role:"tool" messages into the preceding assistant message's
 * tool_call blocks so that the conversion to UIMessage never produces orphan
 * tool-result rows. Returns a new array (no mutation).
 */
export function mergeToolResults(messages: Message[]): Message[] {
  const out: Message[] = [];
  for (const m of messages) {
    if (m.role === "tool" && m.tool_call_id) {
      const prev = out.length > 0 ? out[out.length - 1] : undefined;
      if (prev?.role === "assistant" && prev.blocks) {
        prev.blocks = prev.blocks.map((block) => {
          if (block.type === "tool_call" && block.id === m.tool_call_id && !block.result) {
            return {
              ...block,
              status: "done" as const,
              result: {
                tool_call_id: m.tool_call_id!,
                content: m.content ?? "",
                is_error: false,
                ...(m.references && m.references.length > 0 ? { references: m.references } : {}),
              },
            };
          }
          return block;
        });
        continue;
      }
    }
    out.push({ ...m });
  }
  return out;
}

// Tiny non-cryptographic content hash. Used to derive a stable historical ID
// that does not depend on the message's position in the list. Without this,
// fetching an older page shifts every index and the dedup in setChatMessages
// breaks — every already-loaded message would be added a second time.
function stableContentKey(m: Message): string {
  const sig = m.blocks ? JSON.stringify(m.blocks) : (m.content ?? "");
  let h = 5381;
  for (let i = 0; i < sig.length; i++) {
    h = (h * 33) ^ sig.charCodeAt(i);
  }
  return (h >>> 0).toString(36);
}

export function messageToUIMessage(m: Message): UIMessage {
  const parts: UIMessage["parts"] = [];

  if (m.blocks && m.blocks.length > 0) {
    for (const block of m.blocks) {
      switch (block.type) {
        case "text":
          parts.push({ type: "text", text: block.text });
          break;
        case "thinking":
          if (block.thinking) {
            parts.push({ type: "reasoning", text: block.thinking, providerMetadata: {} });
          }
          break;
        case "tool_call": {
          parts.push({
            type: "dynamic-tool",
            toolName: block.name,
            toolCallId: block.id,
            state: block.result
              ? block.result.is_error
                ? "output-error"
                : "output-available"
              : "input-available",
            input: block.arguments ?? {},
            ...(block.result && !block.result.is_error ? { output: block.result.content } : {}),
            ...(block.result?.is_error ? { errorText: block.result.content } : {}),
          } as UIMessage["parts"][number]);
          // Re-emit references as a data part so history rehydration feeds the
          // exact same channel as the live SSE stream — uiMessageToMessage reads
          // `data-tool-references` for both, so there is one rendering path.
          const refs = block.result?.references;
          if (refs && refs.length > 0) {
            parts.push({
              type: "data-tool-references",
              id: block.id,
              data: { toolCallId: block.id, references: refs },
            } as unknown as UIMessage["parts"][number]);
          }
          break;
        }
      }
    }
  } else if (m.content) {
    parts.push({ type: "text", text: m.content });
  }

  return {
    id: m.id ?? `hist-${m.timestamp}-${m.role}-${stableContentKey(m)}`,
    role: m.role === "tool" ? "assistant" : m.role,
    parts,
    metadata: {
      timestamp: m.timestamp,
      token_count: m.token_count,
      model: m.model,
    },
  };
}

type AnyToolPart = {
  type: string;
  toolCallId: string;
  toolName?: string;
  state: string;
  input?: unknown;
  output?: unknown;
  errorText?: string;
};

function isToolPart(
  part: UIMessage["parts"][number],
): part is AnyToolPart & UIMessage["parts"][number] {
  return part.type === "dynamic-tool" || part.type.startsWith("tool-");
}

function extractToolName(part: AnyToolPart): string {
  if (part.toolName) return part.toolName;
  if (part.type.startsWith("tool-") && part.type.length > 5) return part.type.slice(5);
  return "";
}

export function uiMessageToMessage(m: UIMessage): Message {
  const blocks: Message["blocks"] = [];
  let content = "";

  // Renderable references arrive as out-of-band `data-tool-references` parts
  // (the live counterpart of the stored `references[]`); collect them up front
  // and attach to the matching tool block so cards show mid-stream.
  const refsByTool = new Map<string, RenderableReference[]>();
  for (const part of m.parts) {
    if (part.type === "data-tool-references") {
      const data = (part as unknown as { data?: { toolCallId?: string; references?: unknown } })
        .data;
      if (data?.toolCallId && Array.isArray(data.references)) {
        refsByTool.set(data.toolCallId, data.references as RenderableReference[]);
      }
    }
  }

  for (const part of m.parts) {
    switch (part.type) {
      case "text":
        blocks.push({ type: "text", text: part.text });
        content += part.text;
        break;
      case "file": {
        // Mirror the marker the server stores beside the attachment, so the
        // message just sent renders its thumbnail the same way the reloaded
        // one does.
        const marker = `[file: ${part.url}]`;
        blocks.push({ type: "text", text: marker });
        content += content ? `\n${marker}` : marker;
        break;
      }
      case "reasoning":
        blocks.push({ type: "thinking", thinking: part.text });
        break;
      default:
        if (isToolPart(part)) {
          const hasOutput = part.state === "output-available" || part.state === "output-error";
          const outputContent = hasOutput
            ? part.state === "output-error"
              ? (part.errorText ?? "error")
              : typeof part.output === "string"
                ? part.output
                : JSON.stringify(part.output)
            : undefined;
          const references = refsByTool.get(part.toolCallId);
          blocks.push({
            type: "tool_call",
            id: part.toolCallId,
            name: extractToolName(part),
            arguments: (part.input as Record<string, unknown>) ?? {},
            status: hasOutput ? "done" : "running",
            ...(hasOutput
              ? {
                  result: {
                    tool_call_id: part.toolCallId,
                    content: outputContent!,
                    is_error: part.state === "output-error",
                    ...(references && references.length > 0 ? { references } : {}),
                  },
                }
              : {}),
          });
        }
        break;
    }
  }

  const meta = (m.metadata ?? {}) as Record<string, unknown>;

  return {
    id: m.id,
    role: m.role === "system" ? "assistant" : m.role,
    content: content || undefined,
    blocks: blocks.length > 0 ? blocks : undefined,
    timestamp: (meta.timestamp as string) || "",
    token_count: meta.token_count as number | undefined,
    model: meta.model as string | undefined,
    streaming: m.parts.some((p) => (p as Record<string, unknown>).state === "streaming"),
  };
}

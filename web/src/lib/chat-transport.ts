import { DefaultChatTransport } from "ai";
import type { UIMessage } from "ai";
import type { Message } from "./types";

export function createSessionTransport(sessionId: string) {
  return new DefaultChatTransport({
    api: `/api/sessions/${encodeURIComponent(sessionId)}/messages`,
    prepareSendMessagesRequest: ({ messages }) => {
      const last = messages[messages.length - 1];
      const parts = last.parts
        .map((p) => {
          if (p.type === "text") return { type: "text" as const, text: p.text };
          if (p.type === "file" && "url" in p)
            return { type: "file" as const, url: p.url, mimeType: p.mediaType };
          return null;
        })
        .filter(Boolean);
      return { body: { parts } };
    },
  });
}

let msgSeq = 0;

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
        case "tool_call":
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
          break;
      }
    }
  } else if (m.content) {
    parts.push({ type: "text", text: m.content });
  }

  return {
    id: `${m.timestamp}-${m.role}-${msgSeq++}`,
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

  for (const part of m.parts) {
    switch (part.type) {
      case "text":
        blocks.push({ type: "text", text: part.text });
        content += part.text;
        break;
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
    role: m.role === "system" ? "assistant" : m.role,
    content: content || undefined,
    blocks: blocks.length > 0 ? blocks : undefined,
    timestamp: (meta.timestamp as string) || new Date().toISOString(),
    token_count: meta.token_count as number | undefined,
    model: meta.model as string | undefined,
  };
}

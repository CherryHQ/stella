import { forwardRef, useMemo } from "react";
import type { UIMessage } from "ai";
import type { ContentBlock } from "@/lib/types";
import type { AgentInfoData } from "@/lib/chat-transport";
import { ChatTranscript, type TranscriptMessage } from "@/components/chat/ChatTranscript";

interface Props {
  messages: UIMessage[];
  loading?: boolean;
  onScroll?: () => void;
  agentNames?: Map<string, string>;
  uploadAgentId?: string;
  uploadSessionId?: string;
}

export const GroupTranscript = forwardRef<HTMLDivElement, Props>(function GroupTranscript(
  { messages, loading, onScroll, agentNames, uploadAgentId, uploadSessionId },
  ref,
) {
  const transcriptMessages = useMemo(
    () => uiMessagesToTranscriptMessages(messages, agentNames),
    [messages, agentNames],
  );

  return (
    <ChatTranscript
      ref={ref}
      messages={transcriptMessages}
      loading={loading}
      onScroll={onScroll}
      fileAgentId={uploadAgentId}
      fileSessionId={uploadSessionId}
      agentNames={agentNames}
    />
  );
});

function uiMessagesToTranscriptMessages(
  messages: UIMessage[],
  agentNames?: Map<string, string>,
): TranscriptMessage[] {
  const result: TranscriptMessage[] = [];

  for (const msg of messages) {
    if (msg.role === "user") {
      const text = msg.parts
        .filter(
          (p): p is Extract<(typeof msg.parts)[number], { type: "text" }> => p.type === "text",
        )
        .map((p) => p.text)
        .join("");
      // SAFETY: msg.metadata is an open object literal; timestamp is read defensively for display.
      const timestamp = (msg.metadata as Record<string, unknown> | undefined)?.timestamp as
        | string
        | undefined;
      result.push({
        id: msg.id,
        role: "user",
        content: text,
        timestamp,
      });
      continue;
    }

    const steps = splitBySteps(msg.parts);

    if (steps.length === 0) {
      const text = msg.parts
        .filter(
          (p): p is Extract<(typeof msg.parts)[number], { type: "text" }> => p.type === "text",
        )
        .map((p) => p.text)
        .join("");
      result.push({
        id: msg.id,
        role: "assistant",
        content: text,
        blocks: partsToBlocks(msg.parts),
        // SAFETY: a UI part is an open object whose state field is a string when present.
        streaming: msg.parts.some((p) => (p as Record<string, unknown>).state === "streaming"),
      });
      continue;
    }

    for (let si = 0; si < steps.length; si++) {
      const stepParts = steps[si];
      const info = extractAgentInfo(stepParts);
      const agentId = info?.agentId;
      const agentName = agentId
        ? (agentNames?.get(agentId) ?? info?.agentName ?? agentId)
        : undefined;

      const text = stepParts
        .filter(
          (p): p is Extract<(typeof msg.parts)[number], { type: "text" }> => p.type === "text",
        )
        .map((p) => p.text)
        .join("");

      result.push({
        id: `${msg.id}-step-${si}`,
        role: "assistant",
        content: text,
        blocks: partsToBlocks(stepParts),
        agentName,
        agentId,
        // SAFETY: a UI part is an open object whose state field is a string when present.
        streaming: stepParts.some((p) => (p as Record<string, unknown>).state === "streaming"),
      });
    }
  }

  return result;
}

function splitBySteps(parts: UIMessage["parts"]): UIMessage["parts"][] {
  const steps: UIMessage["parts"][] = [];
  let current: UIMessage["parts"] | null = null;

  for (const part of parts) {
    if (part.type === "step-start") {
      if (current !== null) steps.push(current);
      current = [];
      continue;
    }
    if (current !== null) {
      current.push(part);
    }
  }
  if (current !== null && current.length > 0) steps.push(current);
  return steps;
}

function extractAgentInfo(parts: UIMessage["parts"]): AgentInfoData | null {
  for (const part of parts) {
    if (part.type === "data-agent-info") {
      // SAFETY: a data-agent-info part always carries the AgentInfoData payload.
      return (part as unknown as { data: AgentInfoData }).data;
    }
  }
  return null;
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

function partsToBlocks(parts: UIMessage["parts"]): ContentBlock[] {
  const blocks: ContentBlock[] = [];
  for (const part of parts) {
    switch (part.type) {
      case "text":
        if (part.text) blocks.push({ type: "text", text: part.text });
        break;
      case "reasoning":
        blocks.push({ type: "thinking", thinking: part.text });
        break;
      default:
        if (part.type === "dynamic-tool" || part.type.startsWith("tool-")) {
          // SAFETY: the dynamic-tool/tool-* parts share the AnyToolPart shape by construction.
          const tp = part as unknown as AnyToolPart;
          const hasOutput = tp.state === "output-available" || tp.state === "output-error";
          blocks.push({
            type: "tool_call",
            id: tp.toolCallId,
            name: tp.toolName ?? "",
            // SAFETY: the tool input is arbitrary JSON written by the tool call; rendered defensively.
            arguments: (tp.input as Record<string, unknown>) ?? {},
            status: hasOutput ? "done" : "running",
            result: hasOutput
              ? {
                  tool_call_id: tp.toolCallId,
                  content:
                    tp.state === "output-error"
                      ? (tp.errorText ?? "error")
                      : typeof tp.output === "string"
                        ? tp.output
                        : JSON.stringify(tp.output ?? ""),
                  is_error: tp.state === "output-error",
                }
              : undefined,
          });
        }
        break;
    }
  }
  return blocks;
}

import { forwardRef, useMemo } from "react";
import type { Message } from "@/lib/types";
import { useQuery } from "@tanstack/react-query";
import { agentsQueryOptions } from "@/lib/queries/agents";
import { ChatTranscript, type TranscriptMessage } from "@/components/chat/ChatTranscript";

interface Props {
  messages: Message[];
  messagesLoading: boolean;
  onScroll: () => void;
  agentId: string;
  sessionId: string;
  activeStreaming?: boolean;
}

export const Transcript = forwardRef<HTMLDivElement, Props>(function Transcript(
  { messages, messagesLoading, onScroll, agentId, sessionId, activeStreaming },
  ref,
) {
  const { data: agents = [] } = useQuery(agentsQueryOptions);
  const agentName = agents.find((a) => a.id === agentId)?.name ?? "Agent";

  const transcriptMessages = useMemo((): TranscriptMessage[] => {
    const filtered = messages.filter((m) => m.role !== "tool");
    const merged = mergeConsecutiveMessages(filtered);
    const lastAssistantIndex = activeStreaming
      ? merged.findLastIndex((m) => m.role === "assistant")
      : -1;
    return merged.map((msg, i) => ({
      id: msg.id ?? `${msg.timestamp}-${msg.role}-${i}`,
      role: msg.role as "user" | "assistant",
      content: msg.content,
      timestamp: msg.timestamp,
      agentName,
      agentId,
      blocks: msg.blocks ?? (msg.content ? [{ type: "text" as const, text: msg.content }] : []),
      model: msg.model,
      tokenCount: msg.token_count,
      streaming: msg.streaming || i === lastAssistantIndex,
    }));
  }, [messages, agentName, agentId, activeStreaming]);

  return (
    <ChatTranscript
      ref={ref}
      messages={transcriptMessages}
      loading={messagesLoading}
      onScroll={onScroll}
      fileAgentId={agentId}
      fileSessionId={sessionId}
    />
  );
});

function mergeConsecutiveMessages(messages: Message[]): Message[] {
  const result: Message[] = [];

  for (const msg of messages) {
    if (
      result.length > 0 &&
      result[result.length - 1].role === "assistant" &&
      msg.role === "assistant"
    ) {
      const prev = result[result.length - 1];
      const prevBlocks = prev.blocks ?? [];
      const currBlocks = msg.blocks ?? [];

      let mergedBlocks = [...prevBlocks];
      if (currBlocks.length > 0) {
        mergedBlocks.push(...currBlocks);
      } else if (msg.content) {
        mergedBlocks.push({ type: "text", text: msg.content });
      }

      result[result.length - 1] = {
        ...prev,
        blocks: mergedBlocks,
        token_count: (prev.token_count ?? 0) + (msg.token_count ?? 0),
        timestamp: msg.timestamp || prev.timestamp,
        model: msg.model || prev.model,
        streaming: prev.streaming || msg.streaming,
      };
    } else {
      const blocks = msg.blocks ?? [];
      if (blocks.length === 0 && msg.content && msg.role !== "tool") {
        result.push({
          ...msg,
          blocks: [{ type: "text", text: msg.content }],
        });
      } else {
        result.push({ ...msg });
      }
    }
  }

  return result;
}

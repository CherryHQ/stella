import { forwardRef, useMemo } from "react";
import type { ContentBlock } from "@/lib/types";
import { ChatTranscript, type TranscriptMessage } from "@/components/chat/ChatTranscript";
import type { ToolCallState } from "./group-transport";

export interface DisplayMessage {
  id: string;
  role: "user" | "assistant";
  agentId?: string;
  agentName?: string;
  content: string;
  reasoning?: string;
  toolCalls?: ToolCallState[];
  agentSessionId?: string;
  timestamp?: string;
  streaming?: boolean;
}

interface Props {
  messages: DisplayMessage[];
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
    (): TranscriptMessage[] =>
      messages.map((msg) => ({
        id: msg.id,
        role: msg.role,
        content: msg.content,
        timestamp: msg.timestamp,
        agentName: msg.agentName,
        agentId: msg.agentId,
        blocks: toBlocks(msg),
        streaming: msg.streaming,
        agentSessionId: msg.agentSessionId,
      })),
    [messages],
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

function toBlocks(msg: DisplayMessage): ContentBlock[] {
  const blocks: ContentBlock[] = [];
  if (msg.reasoning) blocks.push({ type: "thinking", thinking: msg.reasoning });
  if (msg.toolCalls) {
    for (const tc of msg.toolCalls) {
      blocks.push({
        type: "tool_call",
        id: tc.id,
        name: tc.name,
        arguments: tc.input,
        status: tc.output !== undefined ? "done" : "running",
        result:
          tc.output !== undefined
            ? { tool_call_id: tc.id, content: tc.output, is_error: tc.isError ?? false }
            : undefined,
      });
    }
  }
  if (msg.content) blocks.push({ type: "text", text: msg.content });
  return blocks;
}

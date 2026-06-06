import { forwardRef, useMemo } from "react";
import type { ContentBlock } from "@/lib/types";
import { cn } from "@/lib/utils";
import { UserMessage } from "./UserMessage";
import { AssistantMessage } from "./AssistantMessage";

export interface TranscriptMessage {
  id: string;
  role: "user" | "assistant";
  content?: string;
  timestamp?: string;
  agentName?: string;
  agentId?: string;
  blocks?: ContentBlock[];
  model?: string;
  tokenCount?: number;
  streaming?: boolean;
  agentSessionId?: string;
}

interface Props {
  messages: TranscriptMessage[];
  loading?: boolean;
  onScroll?: () => void;
  fileAgentId?: string;
  fileSessionId?: string;
  agentNames?: Map<string, string>;
}

interface Annotated extends TranscriptMessage {
  sameRoleAsPrev: boolean;
  showTimestamp: boolean;
}

function annotate(messages: TranscriptMessage[]): Annotated[] {
  return messages.map((msg, i) => ({
    ...msg,
    sameRoleAsPrev: i > 0 && messages[i - 1].role === msg.role,
    showTimestamp: i === messages.length - 1 || messages[i + 1].role !== msg.role,
  }));
}

export const ChatTranscript = forwardRef<HTMLDivElement, Props>(function ChatTranscript(
  { messages, loading, onScroll, fileAgentId, fileSessionId, agentNames },
  ref,
) {
  const processed = useMemo(() => annotate(messages), [messages]);

  return (
    <div
      ref={ref}
      className="stella-transcript-scroll flex-1 overflow-y-auto bg-background px-4 py-6 sm:px-8 sm:py-8"
      onScroll={onScroll}
    >
      {loading && messages.length > 0 && (
        <div className="mb-6 flex items-center justify-center gap-2">
          <div className="size-3 animate-spin rounded-full border border-muted-foreground/30 border-t-muted-foreground" />
          <span className="font-mono text-[10px] text-muted-foreground">
            Loading earlier messages…
          </span>
        </div>
      )}
      {messages.length === 0 && !loading && (
        <div className="py-20 text-center">
          <p className="font-mono text-xs text-muted-foreground/60">Empty session.</p>
        </div>
      )}
      <div className="mx-auto max-w-3xl space-y-8">
        {processed.map((msg, idx) => (
          <div
            key={msg.id}
            className={cn(
              msg.sameRoleAsPrev ? "-mt-2" : "",
              idx > 0 && msg.role === "user" && !msg.sameRoleAsPrev
                ? "border-t border-border/40 pt-8 mt-8"
                : "",
            )}
          >
            {msg.role === "user" ? (
              <UserMessage
                msg={{ content: msg.content, timestamp: msg.timestamp }}
                agentId={fileAgentId}
                sessionId={fileSessionId}
                agentNames={agentNames}
                sameRoleAsPrev={msg.sameRoleAsPrev}
              />
            ) : (
              <AssistantMessage
                agentName={msg.agentName || "Agent"}
                agentId={msg.agentId || "default"}
                blocks={msg.blocks ?? []}
                timestamp={msg.timestamp}
                model={msg.model}
                tokenCount={msg.tokenCount}
                streaming={msg.streaming}
                showTimestamp={msg.showTimestamp}
                sameRoleAsPrev={msg.sameRoleAsPrev}
                agentSessionId={msg.agentSessionId}
              />
            )}
          </div>
        ))}
      </div>
    </div>
  );
});

import { forwardRef, memo, type ReactNode } from "react";
import { useI18n } from "@/lib/i18n";
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
  /* Rendered inside the scroll container above the messages (e.g. epoch summaries). */
  header?: ReactNode;
}

interface MessageListProps {
  messages: TranscriptMessage[];
  fileAgentId?: string;
  fileSessionId?: string;
  agentNames?: Map<string, string>;
  /* Layout classes for the list wrapper (width, centering); styling stays internal. */
  className?: string;
}

// Memoized per-message row: with stable TranscriptMessage identities from the
// upstream caches, a stream update re-renders only the tail row instead of the
// whole history. Neighbor-derived flags are passed as primitives so the memo
// comparison stays shallow.
const MessageRow = memo(function MessageRow({
  msg,
  sameRoleAsPrev,
  showTimestamp,
  fileAgentId,
  fileSessionId,
  agentNames,
}: {
  msg: TranscriptMessage;
  sameRoleAsPrev: boolean;
  showTimestamp: boolean;
  fileAgentId?: string;
  fileSessionId?: string;
  agentNames?: Map<string, string>;
}) {
  // content-visibility lets the browser skip layout/paint for rows far outside
  // the viewport; contain-intrinsic-size keeps scrollHeight (and thus scroll
  // restoration when paging in older history) stable while they're skipped.
  return (
    <div
      className={cn(
        "min-w-0 [content-visibility:auto] [contain-intrinsic-size:auto_80px]",
        sameRoleAsPrev ? "-mt-3" : "",
      )}
    >
      {msg.role === "user" ? (
        <UserMessage
          msg={{ content: msg.content, blocks: msg.blocks, timestamp: msg.timestamp }}
          agentId={fileAgentId}
          sessionId={fileSessionId}
          agentNames={agentNames}
          sameRoleAsPrev={sameRoleAsPrev}
          showTimestamp={showTimestamp}
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
          showTimestamp={showTimestamp}
          sameRoleAsPrev={sameRoleAsPrev}
          agentSessionId={msg.agentSessionId}
        />
      )}
    </div>
  );
});

/** The bare message column, reusable wherever a transcript needs to render (chat, epoch raw view). */
export function MessageList({
  messages,
  fileAgentId,
  fileSessionId,
  agentNames,
  className,
}: MessageListProps) {
  return (
    <div className={cn("min-w-0 space-y-6", className)}>
      {messages.map((msg, i) => (
        <MessageRow
          key={msg.id}
          msg={msg}
          sameRoleAsPrev={
            i > 0 && messages[i - 1].role === msg.role && messages[i - 1].agentId === msg.agentId
          }
          showTimestamp={i === messages.length - 1 || messages[i + 1].role !== msg.role}
          fileAgentId={fileAgentId}
          fileSessionId={fileSessionId}
          agentNames={agentNames}
        />
      ))}
    </div>
  );
}

export const ChatTranscript = forwardRef<HTMLDivElement, Props>(function ChatTranscript(
  { messages, loading, onScroll, fileAgentId, fileSessionId, agentNames, header },
  ref,
) {
  const { t } = useI18n();

  return (
    // `log` is the role for a running conversation: assistive tech announces
    // messages as they arrive without stealing focus, which `alert` would.
    // It carries an implicit `aria-live="polite"`, so that is not restated.
    <div
      ref={ref}
      role="log"
      aria-label={t("sessions.transcript.label")}
      className="stella-transcript-scroll min-w-0 flex-1 overflow-y-auto bg-background px-4 py-6 sm:px-8 sm:py-8"
      onScroll={onScroll}
    >
      {loading && messages.length > 0 && (
        <div className="mb-6 flex items-center justify-center gap-2">
          <div className="size-3 animate-spin rounded-full border border-muted-foreground/30 border-t-muted-foreground" />
          <span className="font-mono text-xs text-muted-foreground">
            {t("sessions.transcript.loadingEarlier")}
          </span>
        </div>
      )}
      {header}
      {messages.length === 0 && !loading && !header && (
        <div className="py-20 text-center">
          <p className="font-mono text-xs text-muted-foreground">
            {t("sessions.transcript.empty")}
          </p>
        </div>
      )}
      <MessageList
        messages={messages}
        fileAgentId={fileAgentId}
        fileSessionId={fileSessionId}
        agentNames={agentNames}
        className="mx-auto w-full max-w-[var(--chat-column)]"
      />
    </div>
  );
});

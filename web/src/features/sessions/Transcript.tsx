import { forwardRef, useMemo, useState } from "react";
import type { Message } from "@/lib/types";
import { useQuery } from "@tanstack/react-query";
import { Archive, ChevronDown, MessageSquareText } from "lucide-react";
import { agentsQueryOptions } from "@/lib/queries/agents";
import { sessionSummaryOptions } from "@/lib/queries/session-context";
import { getSessionMessages } from "@/lib/api-client/sdk.gen";
import type { SessionContextItem, SessionContextMessage } from "@/lib/api-client/types.gen";
import { ChatTranscript, type TranscriptMessage } from "@/components/chat/ChatTranscript";
import { Button } from "@/components/ui/button";
import { useI18n } from "@/lib/i18n";
import { cn } from "@/lib/utils";

interface Props {
  messages: Message[];
  messagesLoading: boolean;
  onScroll: () => void;
  agentId: string;
  sessionId: string;
  activeStreaming?: boolean;
  contextItems?: SessionContextItem[];
  contextLoading?: boolean;
}

export const Transcript = forwardRef<HTMLDivElement, Props>(function Transcript(
  {
    messages,
    messagesLoading,
    onScroll,
    agentId,
    sessionId,
    activeStreaming,
    contextItems = [],
    contextLoading = false,
  },
  ref,
) {
  const { data: agents = [] } = useQuery(agentsQueryOptions);
  const agentName = agents.find((a) => a.id === agentId)?.name ?? "Agent";
  const hasSummaries = contextItems.some((item) => item.type === "summary");

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

  if (hasSummaries) {
    return (
      <div
        ref={ref}
        onScroll={onScroll}
        className="min-h-0 flex-1 overflow-y-auto bg-background px-4 py-4"
      >
        <div className="mx-auto flex w-full max-w-3xl flex-col gap-3">
          {contextItems.map((item) =>
            item.type === "summary" && item.summary ? (
              <SummaryCard
                key={`summary:${item.summary.id}`}
                agentId={agentId}
                sessionId={sessionId}
                item={item}
              />
            ) : item.message ? (
              <ContextMessageBubble
                key={`message:${item.message.id}`}
                message={item.message}
                agentName={agentName}
              />
            ) : null,
          )}
        </div>
      </div>
    );
  }

  return (
    <ChatTranscript
      ref={ref}
      messages={transcriptMessages}
      loading={messagesLoading || contextLoading}
      onScroll={onScroll}
      fileAgentId={agentId}
      fileSessionId={sessionId}
    />
  );
});

function SummaryCard({
  agentId,
  sessionId,
  item,
}: {
  agentId: string;
  sessionId: string;
  item: SessionContextItem;
}) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const [showMessages, setShowMessages] = useState(false);
  const summary = item.summary;
  const summaryQuery = useQuery({
    ...sessionSummaryOptions(agentId, sessionId, summary?.id ?? ""),
    enabled: open && !!summary?.id,
  });
  const from = summaryQuery.data?.message_seq_from;
  const to = summaryQuery.data?.message_seq_to;
  const rangeReady = typeof from === "number" && typeof to === "number";
  const messagesQuery = useQuery({
    queryKey: ["session-summary-messages", agentId, sessionId, from, to],
    queryFn: async () => {
      const { data } = await getSessionMessages({
        path: { agentId, sessionId },
        query: { seq_from: from as number, seq_to: to as number },
        throwOnError: true,
      });
      return data.messages;
    },
    enabled: showMessages && rangeReady,
  });

  if (!summary) return null;

  return (
    <section className="rounded-lg border border-border bg-muted/20">
      <button
        type="button"
        className="flex w-full items-start gap-3 px-4 py-3 text-left"
        onClick={() => setOpen((value) => !value)}
      >
        <span className="grid size-8 shrink-0 place-items-center rounded-md border border-border bg-background text-muted-foreground">
          <Archive className="size-4" />
        </span>
        <span className="min-w-0 flex-1">
          <span className="block text-xs font-semibold text-foreground">
            {t("sessions.epoch.summary")}
          </span>
          <span className="mt-1 block line-clamp-2 text-xs leading-relaxed text-muted-foreground">
            {summary.content}
          </span>
          <span className="mt-2 block font-mono text-[10px] text-muted-foreground">
            {summary.descendant_count} {t("sessions.epoch.messages")} ·{" "}
            {formatNumber(summary.source_message_token_count)} → {formatNumber(summary.token_count)}
          </span>
        </span>
        <ChevronDown
          className={cn("mt-1 size-4 shrink-0 transition-transform", open && "rotate-180")}
        />
      </button>
      {open && (
        <div className="border-t border-border px-4 py-3">
          <p className="whitespace-pre-wrap text-xs leading-relaxed text-foreground">
            {summaryQuery.data?.summary.content ?? summary.content}
          </p>
          <div className="mt-3 flex items-center gap-2">
            <Button variant="outline" size="xs" onClick={() => setShowMessages((value) => !value)}>
              <MessageSquareText className="size-3.5" />
              {t("sessions.epoch.originalMessages")}
            </Button>
          </div>
          {showMessages && (
            <div className="mt-3 space-y-2">
              {messagesQuery.isLoading ? (
                <p className="text-xs text-muted-foreground">{t("common.loading")}</p>
              ) : (
                messagesQuery.data?.map((message, index) => (
                  <pre
                    key={index}
                    className="whitespace-pre-wrap rounded-md border border-border bg-background p-2 text-xs text-muted-foreground"
                  >
                    {formatRawMessage(message)}
                  </pre>
                ))
              )}
            </div>
          )}
        </div>
      )}
    </section>
  );
}

function ContextMessageBubble({
  message,
  agentName,
}: {
  message: SessionContextMessage;
  agentName: string;
}) {
  const isUser = message.role === "user";
  return (
    <div className={cn("flex", isUser ? "justify-end" : "justify-start")}>
      <div
        className={cn(
          "max-w-[78%] rounded-lg border px-3 py-2 text-sm leading-relaxed",
          isUser ? "border-primary/20 bg-primary text-primary-foreground" : "border-border bg-card",
        )}
      >
        {!isUser && (
          <div className="mb-1 text-[10px] font-medium text-muted-foreground">{agentName}</div>
        )}
        <div className="whitespace-pre-wrap">{message.content}</div>
      </div>
    </div>
  );
}

function formatNumber(value: number): string {
  if (value >= 1000) return `${(value / 1000).toFixed(1)}k`;
  return String(value);
}

function formatRawMessage(message: Record<string, unknown>): string {
  if (typeof message.content === "string") return message.content;
  return JSON.stringify(message.blocks ?? message, null, 2);
}

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

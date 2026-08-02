import { forwardRef, useMemo, useRef, useState } from "react";
import type { Message } from "@/lib/types";
import { useQuery } from "@tanstack/react-query";
import { Archive, ChevronDown, MessageSquareText } from "lucide-react";
import { agentsQueryOptions } from "@/lib/queries/agents";
import { sessionSummaryOptions } from "@/lib/queries/session-context";
import { getSessionMessages } from "@/lib/api-client/sdk.gen";
import type { SessionContextItem } from "@/lib/api-client/types.gen";
import {
  ChatTranscript,
  MessageList,
  type TranscriptMessage,
} from "@/components/chat/ChatTranscript";
import { useI18n } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import { mergeToolResults, sessionMessagesToMessages } from "@/lib/chat-transport";

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

  // Identity-preserving pipeline: SessionDetail hands us Message objects that
  // are stable across stream updates, and these caches keep the merged and
  // mapped outputs stable too, so only the streaming tail produces new objects
  // and the memoized rows downstream skip everything else.
  const mergeCacheRef = useRef(new WeakMap<Message, { members: Message[]; merged: Message }>());
  const tmCacheRef = useRef(new WeakMap<Message, TmCacheEntry>());

  const transcriptMessages = useMemo((): TranscriptMessage[] => {
    const filtered = messages.filter((m) => m.role !== "tool");
    const merged = mergeConsecutiveMessagesCached(filtered, mergeCacheRef.current);
    const lastAssistantIndex = activeStreaming
      ? merged.findLastIndex((m) => m.role === "assistant")
      : -1;
    return merged.map((msg, i) => {
      const streaming = Boolean(msg.streaming || i === lastAssistantIndex);
      const hit = tmCacheRef.current.get(msg);
      if (
        hit &&
        hit.agentName === agentName &&
        hit.agentId === agentId &&
        hit.streaming === streaming
      )
        return hit.out;
      const out: TranscriptMessage = {
        id: msg.id ?? `${msg.timestamp}-${msg.role}-${i}`,
        role: msg.role as "user" | "assistant",
        content: msg.content,
        timestamp: msg.timestamp,
        agentName,
        agentId,
        blocks: msg.blocks ?? (msg.content ? [{ type: "text" as const, text: msg.content }] : []),
        model: msg.model,
        tokenCount: msg.token_count,
        streaming,
      };
      tmCacheRef.current.set(msg, { agentName, agentId, streaming, out });
      return out;
    });
  }, [messages, agentName, agentId, activeStreaming]);

  // Compacted epochs render as summary cards above the live tail; the tail
  // messages themselves flow through the regular chat pipeline so tool calls,
  // markdown, and streaming render identically to an uncompacted session.
  const epochHeader = hasSummaries ? (
    <div className="mx-auto mb-8 flex w-full max-w-3xl flex-col gap-3">
      {contextItems.map((item) =>
        item.type === "summary" && item.summary ? (
          <SummaryCard
            key={`summary:${item.summary.id}`}
            agentId={agentId}
            agentName={agentName}
            sessionId={sessionId}
            item={item}
          />
        ) : null,
      )}
    </div>
  ) : undefined;

  return (
    <ChatTranscript
      ref={ref}
      messages={transcriptMessages}
      loading={messagesLoading || contextLoading}
      onScroll={onScroll}
      fileAgentId={agentId}
      fileSessionId={sessionId}
      header={epochHeader}
    />
  );
});

function SummaryCard({
  agentId,
  agentName,
  sessionId,
  item,
}: {
  agentId: string;
  agentName: string;
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

  const rawTranscript = useMemo<TranscriptMessage[]>(() => {
    const raw = mergeToolResults(sessionMessagesToMessages(messagesQuery.data));
    const filtered = raw.filter((m) => m.role !== "tool");
    return mergeConsecutiveMessages(filtered).map((msg, i) => ({
      id: msg.id ?? `${msg.timestamp}-${msg.role}-${i}`,
      role: msg.role as "user" | "assistant",
      content: msg.content,
      timestamp: msg.timestamp,
      agentName,
      agentId,
      blocks: msg.blocks ?? (msg.content ? [{ type: "text" as const, text: msg.content }] : []),
      model: msg.model,
      tokenCount: msg.token_count,
    }));
  }, [messagesQuery.data, agentName, agentId]);

  if (!summary) return null;

  return (
    <section className="rounded-lg border border-border bg-muted/20">
      <button
        type="button"
        className="flex w-full items-center gap-2.5 px-4 py-2.5 text-left"
        onClick={() => setOpen((value) => !value)}
      >
        <Archive className="size-4 shrink-0 text-muted-foreground" />
        <span className="shrink-0 text-xs font-semibold text-foreground">
          {t("sessions.epoch.summary")}
        </span>
        <span className="min-w-0 flex-1 truncate font-mono text-xs text-muted-foreground">
          {t("sessions.epoch.messageCount", { count: summary.descendant_count })} ·{" "}
          {formatNumber(summary.source_message_token_count)} → {formatNumber(summary.token_count)}
        </span>
        <ChevronDown
          className={cn(
            "size-4 shrink-0 text-muted-foreground transition-transform",
            open && "rotate-180",
          )}
        />
      </button>
      {open && (
        <div className="border-t border-border px-4 py-3">
          <p className="whitespace-pre-wrap text-xs leading-relaxed text-foreground">
            {summaryQuery.data?.summary.content ?? summary.content}
          </p>
          {(summaryQuery.data?.children.length ?? 0) > 0 && (
            <div className="mt-3 space-y-2">
              {summaryQuery.data?.children.map((child) => (
                <div key={child.id} className="rounded-md border border-border bg-background p-2">
                  <p className="line-clamp-3 whitespace-pre-wrap text-xs leading-relaxed text-muted-foreground">
                    {child.content}
                  </p>
                </div>
              ))}
            </div>
          )}
          <button
            type="button"
            onClick={() => setShowMessages((value) => !value)}
            className="mt-3 flex cursor-pointer items-center gap-1.5 font-mono text-xs text-muted-foreground/70 transition-colors hover:text-foreground"
          >
            <MessageSquareText className="size-3.5 shrink-0 text-muted-foreground/60" />
            <span>{t("sessions.epoch.originalMessages")}</span>
            <ChevronDown
              className={cn(
                "size-3.5 shrink-0 text-muted-foreground/40 transition-transform",
                showMessages && "rotate-180",
              )}
            />
          </button>
          {showMessages && (
            <div className="mt-3 max-h-[60vh] overflow-y-auto rounded-md border border-border bg-background/40 px-4 py-3">
              {messagesQuery.isLoading ? (
                <p className="text-xs text-muted-foreground">{t("common.loading")}</p>
              ) : rawTranscript.length > 0 ? (
                <MessageList
                  messages={rawTranscript}
                  fileAgentId={agentId}
                  fileSessionId={sessionId}
                />
              ) : (
                <p className="text-xs text-muted-foreground">{t("sessions.transcript.empty")}</p>
              )}
            </div>
          )}
        </div>
      )}
    </section>
  );
}

function formatNumber(value: number): string {
  if (value >= 1000) return `${(value / 1000).toFixed(1)}k`;
  return String(value);
}

interface TmCacheEntry {
  agentName: string;
  agentId: string;
  streaming: boolean;
  out: TranscriptMessage;
}

// Same semantics as mergeConsecutiveMessages, but reuses the merged output for
// any run of messages whose member identities are unchanged, so a stream
// update only rebuilds the tail group. Cache is keyed on the run's first
// source message.
function mergeConsecutiveMessagesCached(
  messages: Message[],
  cache: WeakMap<Message, { members: Message[]; merged: Message }>,
): Message[] {
  const result: Message[] = [];
  let run: Message[] = [];

  const flush = () => {
    if (run.length === 0) return;
    const members = run;
    run = [];
    const hit = cache.get(members[0]);
    if (
      hit &&
      hit.members.length === members.length &&
      hit.members.every((m, i) => m === members[i])
    ) {
      result.push(hit.merged);
      return;
    }
    const merged = mergeConsecutiveMessages(members)[0];
    cache.set(members[0], { members, merged });
    result.push(merged);
  };

  for (const msg of messages) {
    if (msg.role === "assistant") {
      run.push(msg);
    } else {
      flush();
      run.push(msg);
      flush();
    }
  }
  flush();
  return result;
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

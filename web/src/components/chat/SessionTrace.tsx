import { useCallback, useId, useState } from "react";
import { ChevronRight } from "lucide-react";
import { getSessionMessages } from "@/lib/api-client/sdk.gen";
import { mergeToolResults, sessionMessagesToMessages } from "@/lib/chat-transport";
import { useI18n } from "@/lib/i18n";
import type { Message, ContentBlock } from "@/lib/types";
import { AssistantMessage } from "./AssistantMessage";

interface Props {
  agentId: string;
  agentName: string;
  sessionId: string;
  matchContent: string;
}

export function SessionTrace({ agentId, agentName, sessionId, matchContent }: Props) {
  const [expanded, setExpanded] = useState(false);
  const [blocks, setBlocks] = useState<ContentBlock[] | null>(null);
  const [loading, setLoading] = useState(false);
  const panelId = useId();
  const { t } = useI18n();

  const load = useCallback(async () => {
    if (blocks !== null) {
      setExpanded(true);
      return;
    }
    setLoading(true);
    setExpanded(true);
    try {
      const { data } = await getSessionMessages({
        path: { agentId, sessionId },
        query: { limit: 200, skip: 0 },
        throwOnError: true,
      });
      const msgs = mergeToolResults(sessionMessagesToMessages(data?.messages));
      const found = findMatchingTurn(msgs, matchContent);
      setBlocks(found);
    } catch {
      setBlocks([]);
    } finally {
      setLoading(false);
    }
  }, [agentId, sessionId, matchContent, blocks]);

  const toggle = useCallback(() => {
    if (expanded) {
      setExpanded(false);
    } else {
      void load();
    }
  }, [expanded, load]);

  if (!expanded) {
    return (
      <button
        type="button"
        aria-expanded={false}
        onClick={toggle}
        className="flex items-center gap-1.5 text-xs font-mono text-muted-foreground hover:text-foreground transition-colors cursor-pointer mt-1"
      >
        <ChevronRight className="size-3" />
        <span>{t("chat.viewTrace")}</span>
      </button>
    );
  }

  return (
    <div className="mt-2 space-y-2">
      <button
        type="button"
        aria-expanded={true}
        aria-controls={panelId}
        onClick={toggle}
        className="flex items-center gap-1.5 text-xs font-mono text-muted-foreground hover:text-foreground transition-colors cursor-pointer"
      >
        <ChevronRight className="size-3 rotate-90 transition-transform" />
        <span>{t("chat.executionTrace")}</span>
      </button>
      <div id={panelId} className="space-y-2">
        {loading && (
          <div className="flex items-center gap-2 pl-4">
            <div className="size-3 animate-spin rounded-full border border-muted-foreground/30 border-t-muted-foreground" />
            <span className="text-xs font-mono text-muted-foreground">Loading…</span>
          </div>
        )}
        {blocks !== null && blocks.length > 0 && (
          <div className="border-l border-border/40 pl-3 ml-1">
            <AssistantMessage
              agentName={agentName}
              agentId={agentId}
              blocks={blocks}
              sameRoleAsPrev
            />
          </div>
        )}
        {blocks !== null && blocks.length === 0 && !loading && (
          <p className="text-xs font-mono text-muted-foreground pl-4">
            {t("chat.noExecutionDetails")}
          </p>
        )}
      </div>
    </div>
  );
}

function findMatchingTurn(messages: Message[], matchContent: string): ContentBlock[] {
  const turns = splitIntoTurns(messages);
  const needle = matchContent.trim();

  for (let i = turns.length - 1; i >= 0; i--) {
    const turn = turns[i];
    const textBlocks = turn.filter((b) => b.type === "text");
    // SAFETY: a block typed text always carries its .text string.
    const turnText = textBlocks.map((b) => (b as { text: string }).text).join("");
    if (turnText.trim() === needle) {
      return turn.filter((b) => b.type === "thinking" || b.type === "tool_call");
    }
  }

  if (turns.length > 0) {
    const last = turns[turns.length - 1];
    return last.filter((b) => b.type === "thinking" || b.type === "tool_call");
  }
  return [];
}

function splitIntoTurns(messages: Message[]): ContentBlock[][] {
  const turns: ContentBlock[][] = [];
  let current: ContentBlock[] = [];

  for (const msg of messages) {
    if (msg.role === "user") {
      if (current.length > 0) {
        turns.push(current);
        current = [];
      }
      continue;
    }
    if (msg.role === "assistant" && msg.blocks) {
      current.push(...msg.blocks);
    }
  }
  if (current.length > 0) {
    turns.push(current);
  }
  return turns;
}

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { listGroupMessages } from "@/lib/api-client/sdk.gen";
import type { GroupMessage } from "@/lib/api-client/types.gen";
import { groupMembersQueryOptions } from "@/lib/queries/groups";
import { Button } from "@/components/ui/button";
import { GroupTranscript, type DisplayMessage } from "./GroupTranscript";
import { sendGroupMessage, groupMessageToDisplay } from "./group-transport";

interface Props {
  groupId: string;
}

export function GroupChat({ groupId }: Props) {
  const queryClient = useQueryClient();
  const { data: members = [] } = useQuery(groupMembersQueryOptions(groupId));

  const [historicalMessages, setHistoricalMessages] = useState<GroupMessage[]>([]);
  const [streamingMessages, setStreamingMessages] = useState<Map<string, DisplayMessage>>(
    new Map(),
  );
  const [userInput, setUserInput] = useState("");
  const [pendingUserMessage, setPendingUserMessage] = useState<DisplayMessage | null>(null);
  const [isStreaming, setIsStreaming] = useState(false);
  const [loading, setLoading] = useState(true);
  const transcriptRef = useRef<HTMLDivElement>(null);
  const abortRef = useRef<AbortController | null>(null);
  const currentAgentRef = useRef<string>("");

  // Track @mention state
  const [mentionQuery, setMentionQuery] = useState<string | null>(null);
  const inputRef = useRef<HTMLTextAreaElement>(null);

  const loadMessages = useCallback(async () => {
    setLoading(true);
    try {
      const { data } = await listGroupMessages({
        path: { groupId },
        query: { page_size: 50 },
        throwOnError: true,
      });
      setHistoricalMessages((data?.messages as GroupMessage[]) ?? []);
    } finally {
      setLoading(false);
    }
  }, [groupId]);

  useEffect(() => {
    void loadMessages();
    setStreamingMessages(new Map());
    setPendingUserMessage(null);
    setIsStreaming(false);
  }, [groupId, loadMessages]);

  const scrollToBottom = useCallback(() => {
    setTimeout(() => {
      if (transcriptRef.current) {
        transcriptRef.current.scrollTop = transcriptRef.current.scrollHeight;
      }
    }, 0);
  }, []);

  const displayMessages = useMemo((): DisplayMessage[] => {
    const msgs: DisplayMessage[] = [...historicalMessages].reverse().map(groupMessageToDisplay);
    if (pendingUserMessage) {
      msgs.push(pendingUserMessage);
    }
    for (const sm of streamingMessages.values()) {
      msgs.push(sm);
    }
    return msgs;
  }, [historicalMessages, streamingMessages, pendingUserMessage]);

  const handleSend = useCallback(async () => {
    const content = userInput.trim();
    if (!content || isStreaming) return;

    setUserInput("");
    setMentionQuery(null);
    setIsStreaming(true);

    const userMsg: DisplayMessage = {
      id: `pending-user-${Date.now()}`,
      role: "user",
      content,
      timestamp: new Date().toISOString(),
    };
    setPendingUserMessage(userMsg);
    scrollToBottom();

    const abort = new AbortController();
    abortRef.current = abort;

    await sendGroupMessage(
      groupId,
      content,
      {
        onAgentStart: (agentId, agentName, messageId) => {
          currentAgentRef.current = agentId;
          setStreamingMessages((prev) => {
            const next = new Map(prev);
            next.set(agentId, {
              id: messageId,
              role: "assistant",
              agentId,
              agentName,
              content: "",
              reasoning: "",
              streaming: true,
            });
            return next;
          });
          scrollToBottom();
        },
        onTextDelta: (agentId, delta) => {
          const id = agentId || currentAgentRef.current;
          setStreamingMessages((prev) => {
            const next = new Map(prev);
            const existing = next.get(id);
            if (existing) {
              next.set(id, { ...existing, content: existing.content + delta });
            }
            return next;
          });
          scrollToBottom();
        },
        onReasoningDelta: (agentId, delta) => {
          const id = agentId || currentAgentRef.current;
          setStreamingMessages((prev) => {
            const next = new Map(prev);
            const existing = next.get(id);
            if (existing) {
              next.set(id, {
                ...existing,
                reasoning: (existing.reasoning || "") + delta,
              });
            }
            return next;
          });
        },
        onAgentEnd: (agentId) => {
          setStreamingMessages((prev) => {
            const next = new Map(prev);
            const existing = next.get(agentId);
            if (existing) {
              next.set(agentId, { ...existing, streaming: false });
            }
            return next;
          });
        },
        onFinish: () => {
          setIsStreaming(false);
          setPendingUserMessage(null);
          setStreamingMessages(new Map());
          void loadMessages();
          void queryClient.invalidateQueries({ queryKey: ["groups"] });
        },
        onError: (error) => {
          console.error("Group chat error:", error);
          setIsStreaming(false);
        },
      },
      abort.signal,
    );
  }, [userInput, isStreaming, groupId, scrollToBottom, loadMessages, queryClient]);

  const handleStop = useCallback(() => {
    abortRef.current?.abort();
    setIsStreaming(false);
  }, []);

  // @mention filtering
  const mentionCandidates = useMemo(() => {
    if (mentionQuery === null) return [];
    const q = mentionQuery.toLowerCase();
    return members.filter(
      (m) =>
        m.agent_id.toLowerCase().includes(q) ||
        (m.agent_name && m.agent_name.toLowerCase().includes(q)),
    );
  }, [mentionQuery, members]);

  const handleInputChange = useCallback((e: React.ChangeEvent<HTMLTextAreaElement>) => {
    const val = e.target.value;
    setUserInput(val);

    const pos = e.target.selectionStart ?? val.length;
    const before = val.slice(0, pos);
    const atMatch = before.match(/@(\S*)$/);
    setMentionQuery(atMatch ? atMatch[1] : null);
  }, []);

  const insertMention = useCallback(
    (agentId: string) => {
      const textarea = inputRef.current;
      if (!textarea) return;

      const pos = textarea.selectionStart ?? userInput.length;
      const before = userInput.slice(0, pos);
      const after = userInput.slice(pos);
      const atIdx = before.lastIndexOf("@");
      if (atIdx < 0) return;

      const newVal = before.slice(0, atIdx) + `@${agentId} ` + after;
      setUserInput(newVal);
      setMentionQuery(null);
      textarea.focus();
    },
    [userInput],
  );

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <GroupTranscript ref={transcriptRef} messages={displayMessages} loading={loading} />
      <div className="relative border-t border-border bg-background p-3">
        {mentionQuery !== null && mentionCandidates.length > 0 && (
          <div className="absolute bottom-full left-3 right-3 mb-1 max-h-40 overflow-y-auto rounded-lg border border-border bg-popover p-1 shadow-lg">
            {mentionCandidates.map((m) => (
              <button
                key={m.agent_id}
                type="button"
                onClick={() => insertMention(m.agent_id)}
                className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm text-foreground transition-colors hover:bg-muted"
              >
                <span className="font-medium">{m.agent_name || m.agent_id}</span>
                <span className="text-xs text-muted-foreground">@{m.agent_id}</span>
              </button>
            ))}
          </div>
        )}
        <div className="flex gap-2">
          <textarea
            ref={inputRef}
            value={userInput}
            onChange={handleInputChange}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !e.shiftKey) {
                e.preventDefault();
                void handleSend();
              }
            }}
            placeholder="Message the group... (@ to mention)"
            rows={1}
            className="min-w-0 flex-1 resize-none rounded-md border border-input bg-background px-3 py-2 text-sm outline-hidden focus:ring-2 focus:ring-ring"
          />
          {isStreaming ? (
            <Button size="sm" variant="outline" onClick={handleStop}>
              Stop
            </Button>
          ) : (
            <Button size="sm" disabled={!userInput.trim()} onClick={() => void handleSend()}>
              Send
            </Button>
          )}
        </div>
      </div>
    </div>
  );
}

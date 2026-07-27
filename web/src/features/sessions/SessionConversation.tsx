import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useInfiniteQuery } from "@tanstack/react-query";
import { useChat } from "@ai-sdk/react";
import { getSessionMessages } from "@/lib/api-client/sdk.gen";
import type { Message } from "@/lib/types";
import {
  createSessionTransport,
  mergeToolResults,
  messageToUIMessage,
  uiMessageToMessage,
} from "@/lib/chat-transport";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogHeader,
  DialogPanel,
  DialogPopup,
  DialogTitle,
} from "@/components/ui/dialog";
import { Transcript } from "./Transcript";
import { ChatErrorNotice } from "@/components/chat/ChatErrorNotice";
import { useI18n } from "@/lib/i18n";

interface Props {
  agentId: string;
  sessionId: string;
  placeholder?: string;
  className?: string;
  bodyClassName?: string;
  after?: string;
  before?: string;
  inline?: boolean;
}

export function SessionConversation({
  agentId,
  sessionId,
  placeholder = "Ask Stella about this…",
  className = "",
  bodyClassName = "h-[28rem]",
  after,
  before,
  inline,
}: Props) {
  const { t } = useI18n();
  const [userInput, setUserInput] = useState("");
  const [mobileOpen, setMobileOpen] = useState(false);
  const transcriptRef = useRef<HTMLDivElement>(null);
  const initialScrollSessionRef = useRef<string | null>(null);
  const transport = useMemo(() => createSessionTransport(agentId, sessionId), [agentId, sessionId]);

  const {
    messages: chatMessages,
    sendMessage: chatSendMessage,
    setMessages: setChatMessages,
    status: chatStatus,
    stop: chatStop,
    error: chatError,
  } = useChat({
    id: `conv-${sessionId}`,
    transport,
    onError: (err) => console.error("[session conversation chat]", err),
  });

  const isStreaming = chatStatus === "streaming" || chatStatus === "submitted";

  const messagesQuery = useInfiniteQuery({
    queryKey: ["session-messages", agentId, sessionId, after, before],
    initialPageParam: 0,
    queryFn: async ({ pageParam }) => {
      const { data } = await getSessionMessages({
        path: { agentId: agentId, sessionId: sessionId },
        query: {
          limit: 20,
          skip: pageParam,
          ...(after ? { after } : {}),
          ...(before ? { before } : {}),
        },
        throwOnError: true,
      });
      return (data?.messages as unknown as Message[] | undefined) ?? [];
    },
    getNextPageParam: (lastPage, allPages) =>
      lastPage.length === 20 ? allPages.reduce((sum, page) => sum + page.length, 0) : undefined,
  });

  const historicalIDsRef = useRef(new Set<string>());

  useEffect(() => {
    if (!messagesQuery.data) return;
    const merged = mergeToolResults([...messagesQuery.data.pages].reverse().flat());
    if (merged.length === 0) return;
    const uiMessages = merged.map(messageToUIMessage);
    const newIDs = new Set(uiMessages.map((m) => m.id));
    historicalIDsRef.current = newIDs;
    setChatMessages((prev) => {
      const liveSlice = prev.filter((m) => !newIDs.has(m.id));
      return [...uiMessages, ...liveSlice];
    });
  }, [messagesQuery.data, setChatMessages]);

  const messages = useMemo(() => chatMessages.map(uiMessageToMessage), [chatMessages]);

  useEffect(() => {
    initialScrollSessionRef.current = null;
    historicalIDsRef.current = new Set();
    setChatMessages([]);
  }, [sessionId, setChatMessages]);

  useEffect(() => {
    if (!messagesQuery.isSuccess || initialScrollSessionRef.current === sessionId) return;
    initialScrollSessionRef.current = sessionId;
    setTimeout(() => {
      if (transcriptRef.current) {
        transcriptRef.current.scrollTop = transcriptRef.current.scrollHeight;
      }
    }, 0);
  }, [messagesQuery.isSuccess, sessionId]);

  const loadOlderMessages = useCallback(async () => {
    if (!transcriptRef.current || !messagesQuery.hasNextPage || messagesQuery.isFetching) return;
    const el = transcriptRef.current;
    if (el.scrollTop > 60) return;
    const prevHeight = el.scrollHeight;
    await messagesQuery.fetchNextPage();
    setTimeout(() => {
      el.scrollTop = el.scrollHeight - prevHeight;
    }, 0);
  }, [messagesQuery]);

  const sendMessage = useCallback(() => {
    const content = userInput.trim();
    if (!content || isStreaming) return;

    setUserInput("");
    setTimeout(() => {
      if (transcriptRef.current) {
        transcriptRef.current.scrollTop = transcriptRef.current.scrollHeight;
      }
    }, 0);

    void chatSendMessage({ text: content });
  }, [userInput, isStreaming, chatSendMessage]);

  const renderBody = () => (
    <div className="flex h-full min-h-0 flex-col">
      <Transcript
        ref={transcriptRef}
        messages={messages}
        messagesLoading={messagesQuery.isLoading || messagesQuery.isFetchingNextPage}
        onScroll={() => void loadOlderMessages()}
        agentId={agentId}
        sessionId={sessionId}
        activeStreaming={isStreaming}
      />
      <ChatErrorNotice error={chatError} />
      <div className="flex flex-col gap-2 border-t border-border p-2 sm:flex-row sm:p-3">
        <input
          value={userInput}
          onChange={(e) => setUserInput(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !e.shiftKey) {
              e.preventDefault();
              sendMessage();
            }
          }}
          placeholder={placeholder}
          className="min-w-0 flex-1 rounded-md border border-input bg-background px-3 py-2 text-sm outline-hidden focus:ring-2 focus:ring-ring"
        />
        {isStreaming ? (
          <Button
            size="sm"
            variant="outline"
            className="w-full sm:w-auto"
            onClick={() => chatStop()}
          >
            Stop
          </Button>
        ) : (
          <Button
            size="sm"
            className="w-full sm:w-auto"
            disabled={!userInput.trim()}
            onClick={sendMessage}
          >
            Continue
          </Button>
        )}
      </div>
    </div>
  );

  if (inline) {
    return (
      <div className={`flex flex-col overflow-hidden ${className}`}>
        <div className={`flex flex-col ${bodyClassName}`}>{renderBody()}</div>
      </div>
    );
  }

  return (
    <>
      <div className="overflow-hidden rounded-2xl border border-border bg-background sm:hidden">
        <div className="flex items-center justify-between gap-3 px-3 py-3">
          <div className="min-w-0">
            <div className="text-sm font-semibold">{t("sessions.conversation")}</div>
            <div className="truncate font-mono text-xs text-muted-foreground">{sessionId}</div>
          </div>
          <Button size="sm" onClick={() => setMobileOpen(true)}>
            Open
          </Button>
        </div>
      </div>

      <div
        className={`hidden overflow-hidden rounded-2xl border border-border bg-background sm:flex sm:flex-col ${className}`}
      >
        <div className="flex items-center justify-between gap-3 border-b border-border px-3 py-2">
          <div className="min-w-0">
            <div className="text-sm font-semibold">{t("sessions.conversation")}</div>
            <div className="truncate font-mono text-xs text-muted-foreground">{sessionId}</div>
          </div>
          <a
            href={`/sessions/${encodeURIComponent(sessionId)}`}
            className="inline-flex h-8 shrink-0 items-center rounded-md border border-input bg-popover px-2 text-xs font-medium text-foreground shadow-xs/5 hover:bg-accent/50"
          >
            Full view
          </a>
        </div>
        <div className={`flex flex-col ${bodyClassName}`}>{renderBody()}</div>
      </div>

      <Dialog open={mobileOpen} onOpenChange={setMobileOpen}>
        <DialogPopup className="h-[85vh] max-w-3xl" showCloseButton>
          <DialogHeader>
            <DialogTitle>{t("sessions.conversation")}</DialogTitle>
            <div className="truncate font-mono text-xs text-muted-foreground">{sessionId}</div>
          </DialogHeader>
          <DialogPanel className="flex min-h-0 flex-1 flex-col p-0" scrollFade={false}>
            {renderBody()}
          </DialogPanel>
        </DialogPopup>
      </Dialog>
    </>
  );
}

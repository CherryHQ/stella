import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import { useChat } from "@ai-sdk/react";
import type { UIMessage } from "ai";
import { getSessionMessages, stopSession } from "@/lib/api-client/sdk.gen";
import {
  createSessionTransport,
  mergeToolResults,
  messageToUIMessage,
  reconcileHistoryUIMessages,
  sessionMessagesToMessages,
  uiMessageToMessage,
} from "@/lib/chat-transport";
import { ResumeHttpError, useSessionResume } from "./use-session-resume";
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
import { useSessionStreamResume } from "./useSessionStreamResume";

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
  const queryClient = useQueryClient();
  const [userInput, setUserInput] = useState("");
  const [mobileOpen, setMobileOpen] = useState(false);
  const [resumeEnabled, setResumeEnabled] = useState(true);
  // The session whose canonical hydrate has actually been applied to chat
  // state — an identity, not a boolean, so a stale "ready" can never leak
  // across a session switch. Query success alone can precede the apply; an
  // empty history still counts (nothing to apply).
  const [hydratedSession, setHydratedSession] = useState<string | null>(null);
  const [recoveringDisconnect, setRecoveringDisconnect] = useState(false);
  const transcriptRef = useRef<HTMLDivElement>(null);
  const initialScrollSessionRef = useRef<string | null>(null);
  const loadPrefixRef = useRef<((boundary: number) => Promise<UIMessage[]>) | undefined>(undefined);
  const historyAuthoritativeRef = useRef(false);
  const refreshPersistedHistory = useCallback(() => {
    void queryClient.invalidateQueries({
      queryKey: ["session-messages", agentId, sessionId],
    });
  }, [queryClient, agentId, sessionId]);
  const reconcilePersistedHistory = useCallback(() => {
    historyAuthoritativeRef.current = true;
    refreshPersistedHistory();
  }, [refreshPersistedHistory]);
  const completeReconnectCheck = useCallback(() => {
    setRecoveringDisconnect(false);
    reconcilePersistedHistory();
  }, [reconcilePersistedHistory]);
  const resume = useSessionResume({
    sessionId,
    loadPrefix: (b: number) => loadPrefixRef.current?.(b) ?? Promise.resolve([]),
    setMessages: (m: UIMessage[]) => setChatMessages(m),
    onTerminal: reconcilePersistedHistory,
    onRefresh: refreshPersistedHistory,
    // The resume lifecycle reports each observe failure's exact retryable
    // verdict; the poll treats it exactly like a disconnect recovery.
    onFailure: setRecoveringDisconnect,
  });
  const resumeBoundary = resume.boundary;
  const transport = useMemo(
    () => createSessionTransport(agentId, sessionId, resume.transportOptions),
    [agentId, sessionId, resume.transportOptions],
  );
  // Navigation tears down only the local observe connection — including a
  // bootstrap fetch still awaiting its response. The server turn lives on.
  useEffect(() => () => transport.close(), [transport]);

  const {
    messages: chatMessages,
    sendMessage: chatSendMessage,
    setMessages: setChatMessages,
    status: chatStatus,
    resumeStream: chatResume,
    clearError: chatClearError,
    error: chatError,
  } = useChat({
    id: `conv-${sessionId}`,
    transport,
    // Batch SSE deltas: without this every token re-renders the transcript.
    experimental_throttle: 50,
    onError: (err) => console.error("[session conversation chat]", err),
    onData: resume.handleDataPart,
    onFinish: ({ message, isDisconnect }) => {
      setRecoveringDisconnect(isDisconnect);
      // The cap lifts only when a verified terminal receipt was recorded in
      // onData AND this stream's message identity matches it — a bare EOF or
      // a late finish from a superseded stream proves nothing.
      resume.handleStreamEnd(message);
    },
  });

  const isStreaming = chatStatus === "streaming" || chatStatus === "submitted";
  const isStreamingRef = useRef(isStreaming);
  isStreamingRef.current = isStreaming;

  const messagesQuery = useInfiniteQuery({
    queryKey: ["session-messages", agentId, sessionId, after, before, resumeBoundary],
    initialPageParam: 0,
    queryFn: async ({ pageParam }) => {
      const { data } = await getSessionMessages({
        path: { agentId: agentId, sessionId: sessionId },
        query: {
          limit: 20,
          skip: pageParam,
          ...(after ? { after } : undefined),
          ...(before ? { before } : undefined),
          ...(resumeBoundary !== null ? { snapshot_seq: resumeBoundary } : undefined),
        },
        throwOnError: true,
      });
      return sessionMessagesToMessages(data?.messages);
    },
    getNextPageParam: (lastPage, allPages) =>
      lastPage.length === 20 ? allPages.reduce((sum, page) => sum + page.length, 0) : undefined,
  });

  useSessionStreamResume(
    sessionId,
    resumeEnabled && hydratedSession === sessionId,
    chatStatus,
    chatResume,
    recoveringDisconnect,
    chatClearError,
    completeReconnectCheck,
  );

  // Resume prefix: canonical rows capped at the turn's watermark, fetched
  // through the same paged/filtered shape the live query uses.
  loadPrefixRef.current = async (boundary: number) => {
    if (boundary <= 0) return [];
    // No throwOnError: the generated error drops the status, and the resume
    // lifecycle needs it to tell a retryable 5xx/drop from a 4xx answer.
    const res = await getSessionMessages({
      path: { agentId, sessionId },
      query: {
        limit: 20,
        skip: 0,
        snapshot_seq: boundary,
        ...(after ? { after } : undefined),
        ...(before ? { before } : undefined),
      },
    });
    if (res.error !== undefined || res.data === undefined) {
      throw new ResumeHttpError(res.response?.status);
    }
    return mergeToolResults(sessionMessagesToMessages(res.data?.messages)).map(messageToUIMessage);
  };

  const historicalIDsRef = useRef(new Set<string>());

  useEffect(() => {
    if (!messagesQuery.isSuccess) return;
    if (messagesQuery.data) {
      const merged = mergeToolResults([...messagesQuery.data.pages].reverse().flat());
      if (merged.length === 0) {
        setHydratedSession(sessionId);
        return;
      }
      const uiMessages = merged.map(messageToUIMessage);
      const newIDs = new Set(uiMessages.map((m) => m.id));
      historicalIDsRef.current = newIDs;
      const authoritative = historyAuthoritativeRef.current && !isStreamingRef.current;
      if (authoritative) historyAuthoritativeRef.current = false;
      setChatMessages((prev) =>
        reconcileHistoryUIMessages(
          uiMessages,
          prev.filter((message) => !newIDs.has(message.id)),
          { authoritative },
        ),
      );
    }
    setHydratedSession(sessionId);
  }, [messagesQuery.isSuccess, messagesQuery.data, sessionId, setChatMessages]);

  const messages = useMemo(() => chatMessages.map(uiMessageToMessage), [chatMessages]);

  useEffect(() => {
    setResumeEnabled(true);
    setRecoveringDisconnect(false);
    initialScrollSessionRef.current = null;
    historicalIDsRef.current = new Set();
    resume.clear();
    setChatMessages([]);
  }, [sessionId, setChatMessages, resume.clear]);

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

    setResumeEnabled(true);
    setRecoveringDisconnect(false);
    setUserInput("");
    setTimeout(() => {
      if (transcriptRef.current) {
        transcriptRef.current.scrollTop = transcriptRef.current.scrollHeight;
      }
    }, 0);

    // A new send supersedes any pinned resume state — the observed turn's
    // canonical rows are history once the next turn starts.
    resume.clear();
    void chatSendMessage({ text: content });
  }, [userInput, isStreaming, chatSendMessage, resume.clear]);

  const stopActiveTurn = useCallback(() => {
    setResumeEnabled(false);
    setRecoveringDisconnect(false);
    void stopSession({
      path: { agentId, sessionId },
      throwOnError: true,
    }).catch((err) => {
      console.error("[session conversation stop]", err);
      setResumeEnabled(true);
    });
  }, [agentId, sessionId]);

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
            if (e.nativeEvent.isComposing || e.keyCode === 229) return;
            if (e.key === "Enter" && !e.shiftKey) {
              e.preventDefault();
              sendMessage();
            }
          }}
          placeholder={placeholder}
          className="min-w-0 flex-1 rounded-md border border-input bg-background px-3 py-2 text-sm outline-hidden focus:ring-2 focus:ring-ring"
        />
        {isStreaming ? (
          <Button size="sm" variant="outline" className="w-full sm:w-auto" onClick={stopActiveTurn}>
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

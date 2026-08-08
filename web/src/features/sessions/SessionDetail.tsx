import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useInfiniteQuery, useQuery, useQueryClient } from "@tanstack/react-query";
import { useChat } from "@ai-sdk/react";
import type { UIMessage } from "ai";
import { Link } from "@tanstack/react-router";
import { AlertCircle, Download, MessageSquarePlus, PanelRight } from "lucide-react";
import { useI18n } from "@/lib/i18n";
import {
  getSession,
  getSessionMessages,
  stopSession,
  uploadWorkspaceFile,
} from "@/lib/api-client/sdk.gen";
import { agentSkillsOptions, agentsQueryOptions } from "@/lib/queries/agents";
import { inboxQueryOptions } from "@/lib/queries/inbox";
import { sessionContextItemsOptions } from "@/lib/queries/session-context";
import { fetchAllSessionMessages } from "@/lib/paginated";
import type { Message, Session } from "@/lib/types";
import { sessionDisplayTitle } from "@/lib/session-title";
import { ChatPane } from "@/components/chat/ChatPane";
import { ChatErrorNotice } from "@/components/chat/ChatErrorNotice";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/menu";
import { Tooltip, TooltipPopup, TooltipTrigger } from "@/components/ui/tooltip";
import { useToast } from "@/hooks/use-toast";
import {
  downloadTextFile,
  exportFileName,
  messagesToJSONL,
  messagesToMarkdown,
} from "./exportSession";
import {
  createSessionTransport,
  mergeToolResults,
  messageToUIMessage,
  reconcileHistoryUIMessages,
  sessionMessagesToMessages,
  uiMessageToMessage,
} from "@/lib/chat-transport";
import { useAppShell } from "@/layouts/AppShell";
import { BUILTIN_COMMANDS, ChatComposer } from "./ChatComposer";
import { takePendingMessage } from "./pendingMessage";
import { ChatWidthToggle } from "@/components/chat/ChatWidthToggle";
import { SessionInfoPopover } from "./SessionInfoPopover";
import { Transcript } from "./Transcript";
import { useFileAttachments } from "./useFileAttachments";
import { useSessionStreamResume } from "./useSessionStreamResume";
import { useSessionViewed } from "./useSessionViewed";

const PAGE_SIZE = 50;
// Auto-fill (below) pulls older pages until the transcript overflows; cap how
// many it may pull so a pathological history can't trigger an unbounded fetch
// loop on mount. Scroll-up paging remains unlimited.
const MAX_AUTO_FILL_PAGES = 3;

const inboxKindLabels = {
  blocked: "inbox.kind.blocked",
  review: "inbox.kind.review",
  failed: "inbox.kind.failed",
} as const;

interface Props {
  session: Session | null;
  currentUserID: string;
  onNewSession?: () => void;
  onSessionUpdate: (s: Session) => void;
  onToggleWorkspace?: () => void;
  workspaceOpen?: boolean;
}

export function SessionDetail({
  session,
  currentUserID,
  onNewSession,
  onSessionUpdate,
  onToggleWorkspace,
  workspaceOpen,
}: Props) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { showToast } = useToast();
  const [exporting, setExporting] = useState(false);
  const [exportMenuOpen, setExportMenuOpen] = useState(false);
  const [resumeEnabled, setResumeEnabled] = useState(true);
  const [recoveringDisconnect, setRecoveringDisconnect] = useState(false);
  const { data: agentsList = [] } = useQuery(agentsQueryOptions);

  const transcriptRef = useRef<HTMLDivElement>(null);
  const sessionIDRef = useRef<string | null>(null);
  const initialScrollSessionRef = useRef<string | null>(null);
  const autoFillPagesRef = useRef(0);
  const shouldAutoScrollRef = useRef(true);

  const sessionId = session?.id ?? "";
  const agentId = session?.agent_id ?? "";

  const uploadFn = useCallback(
    async (file: File): Promise<string> => {
      const { data } = await uploadWorkspaceFile({
        path: { agentId, sessionId },
        body: { file },
        throwOnError: true,
      });
      return data.path;
    },
    [agentId, sessionId],
  );
  const { attachments, selectFiles, removeAttachment, clearAttachments, buildMessageParts } =
    useFileAttachments(uploadFn);

  const { data: skills = [] } = useQuery(agentSkillsOptions(agentId));
  const { data: inbox } = useQuery(inboxQueryOptions(agentId, 5));
  const contextItemsQuery = useQuery(sessionContextItemsOptions(agentId, sessionId));
  const hasContextSummaries =
    contextItemsQuery.data?.items.some((item) => item.type === "summary") ?? false;
  const attentionItems = inbox?.items ?? [];
  const composerSkills = useMemo(
    () => [
      ...BUILTIN_COMMANDS,
      ...skills.map((s) => ({ name: s.name, description: s.description })),
    ],
    [skills],
  );

  const transport = useMemo(
    () => (session ? createSessionTransport(session.agent_id, session.id) : undefined),
    [session?.agent_id, session?.id],
  );
  const markViewed = useSessionViewed(agentId, sessionId);
  const historyAuthoritativeRef = useRef(false);
  const reconcilePersistedHistory = useCallback(() => {
    historyAuthoritativeRef.current = true;
    void queryClient.invalidateQueries({ queryKey: ["session-messages", sessionId] });
    void queryClient.invalidateQueries({ queryKey: ["session-tail-messages", sessionId] });
  }, [queryClient, sessionId]);
  const completeReconnectCheck = useCallback(() => {
    setRecoveringDisconnect(false);
    reconcilePersistedHistory();
  }, [reconcilePersistedHistory]);

  const {
    messages: chatMessages,
    sendMessage: chatSendMessage,
    setMessages: setChatMessages,
    status: chatStatus,
    stop: chatStop,
    resumeStream: chatResume,
    clearError: chatClearError,
    error: chatError,
  } = useChat({
    id: session?.id ?? "empty",
    transport,
    // Batch SSE deltas: without this every token re-renders the transcript.
    experimental_throttle: 50,
    onError: (err) => console.error("[session chat]", err),
    onFinish: ({ isAbort, isDisconnect, isError }) => {
      setRecoveringDisconnect(isDisconnect);
      if (!isDisconnect) void markViewed();
      if (!isAbort && !isDisconnect && !isError) reconcilePersistedHistory();
    },
  });

  const isStreaming = chatStatus === "streaming" || chatStatus === "submitted";
  const isStreamingRef = useRef(isStreaming);
  isStreamingRef.current = isStreaming;

  // The server titles an untitled session from its first user message *during*
  // the turn (internal/agent/runtime/chat.go), so nothing on the client knows
  // the new title until the turn ends. Re-read the session and drop the cached
  // session lists once streaming settles; without this the header and the
  // sidebar both sit on "untitled" until a full reload.
  const wasStreamingRef = useRef(false);
  useEffect(() => {
    if (isStreaming) {
      wasStreamingRef.current = true;
      return;
    }
    if (!wasStreamingRef.current) return;
    wasStreamingRef.current = false;
    if (!agentId || !sessionId) return;
    let cancelled = false;
    void (async () => {
      try {
        const { data } = await getSession({
          path: { agentId, sessionId },
          throwOnError: true,
        });
        if (!cancelled) onSessionUpdate(data);
      } catch (err) {
        // A stale title is better than tearing down the transcript.
        console.error("[session refresh]", err);
      }
      void queryClient.invalidateQueries({ queryKey: ["sessions", agentId] });
    })();
    return () => {
      cancelled = true;
    };
  }, [isStreaming, agentId, sessionId, onSessionUpdate, queryClient]);

  // Every turn is server-owned once admitted. An idle view polls the read-only
  // events stream so navigation, refresh, connection loss, and turns started
  // elsewhere all converge on the same reconnect path.
  useSessionStreamResume(
    sessionId,
    resumeEnabled,
    chatStatus,
    chatResume,
    recoveringDisconnect,
    chatClearError,
    completeReconnectCheck,
  );

  const messagesQuery = useInfiniteQuery({
    queryKey: ["session-messages", session?.id],
    // Paged history is only for uncompacted sessions; compacted ones load
    // their tail via the seq-range query below. If the context-items request
    // fails we fall back to plain paging rather than showing nothing.
    enabled:
      !!session &&
      (contextItemsQuery.isError || (contextItemsQuery.isSuccess && !hasContextSummaries)),
    initialPageParam: 0,
    queryFn: async ({ pageParam }) => {
      const { data } = await getSessionMessages({
        path: { agentId: agentId, sessionId: sessionId },
        query: { limit: PAGE_SIZE, skip: pageParam },
        throwOnError: true,
      });
      return sessionMessagesToMessages(data?.messages);
    },
    getNextPageParam: (lastPage, allPages) =>
      lastPage.length === PAGE_SIZE
        ? allPages.reduce((sum, page) => sum + page.length, 0)
        : undefined,
  });

  // In a compacted session the live tail is the message-type context items;
  // fetch them as full messages so they render through the normal chat
  // pipeline instead of as raw context blobs.
  const tailSeqRange = useMemo(() => {
    const items = contextItemsQuery.data?.items;
    if (!items) return null;
    const seqs = items
      .filter((item) => item.type === "message" && item.message)
      .map((item) => item.message!.seq);
    if (seqs.length === 0) return null;
    return { from: Math.min(...seqs), to: Math.max(...seqs) };
  }, [contextItemsQuery.data]);

  const tailQuery = useQuery({
    queryKey: ["session-tail-messages", session?.id, tailSeqRange?.from, tailSeqRange?.to],
    enabled: !!session && hasContextSummaries && !!tailSeqRange,
    queryFn: async () => {
      const { data } = await getSessionMessages({
        path: { agentId, sessionId },
        query: { seq_from: tailSeqRange!.from, seq_to: tailSeqRange!.to },
        throwOnError: true,
      });
      return sessionMessagesToMessages(data?.messages);
    },
  });

  const historyMessages = useMemo(() => {
    if (hasContextSummaries) return tailQuery.data ?? null;
    if (!messagesQuery.data) return null;
    return [...messagesQuery.data.pages].reverse().flat();
  }, [hasContextSummaries, tailQuery.data, messagesQuery.data]);

  const historicalIDsRef = useRef(new Set<string>());

  useEffect(() => {
    if (!historyMessages) return;
    const merged = mergeToolResults(historyMessages);
    if (merged.length === 0) return;
    const uiMessages = merged.map(messageToUIMessage);
    // Accumulate every id history has ever produced. Incremental page loads can
    // momentarily orphan a tool result at a page boundary — its assistant lives
    // in an older, not-yet-loaded page, so it renders as a standalone assistant
    // text bubble and is assigned an id here. Once that older page arrives the
    // result merges into its tool_call block and drops out of the current id
    // set; filtering liveSlice on the current set alone would keep the stale
    // text copy forever (duplicated, un-collapsed tool output). Excluding
    // everything history has ever owned drops it.
    for (const m of uiMessages) historicalIDsRef.current.add(m.id);
    const authoritative = historyAuthoritativeRef.current && !isStreamingRef.current;
    if (authoritative) historyAuthoritativeRef.current = false;
    setChatMessages((prev) =>
      reconcileHistoryUIMessages(
        uiMessages,
        prev.filter((message) => !historicalIDsRef.current.has(message.id)),
        { authoritative },
      ),
    );
  }, [historyMessages, setChatMessages]);

  // Convert UIMessage -> Message with a per-object cache so unchanged messages
  // keep their output identity across stream updates — that identity is what
  // lets the memoized transcript rows below skip re-rendering. The entry
  // revalidates on parts count + tail text length in case the SDK ever grows a
  // message in place instead of replacing it.
  const uiToMsgCache = useRef(
    new WeakMap<UIMessage, { partsLen: number; tailLen: number; out: Message }>(),
  );
  const messages = useMemo(
    () =>
      chatMessages.map((m) => {
        const tail = m.parts[m.parts.length - 1] as { text?: string } | undefined;
        const tailLen = tail?.text?.length ?? 0;
        const hit = uiToMsgCache.current.get(m);
        if (hit && hit.partsLen === m.parts.length && hit.tailLen === tailLen) return hit.out;
        const out = uiMessageToMessage(m);
        uiToMsgCache.current.set(m, { partsLen: m.parts.length, tailLen, out });
        return out;
      }),
    [chatMessages],
  );

  useEffect(() => {
    setResumeEnabled(true);
    setRecoveringDisconnect(false);
    if (!session) {
      setChatMessages([]);
      clearAttachments();
      return;
    }
    sessionIDRef.current = session.id;
    initialScrollSessionRef.current = null;
    shouldAutoScrollRef.current = true;
    historicalIDsRef.current = new Set();
    autoFillPagesRef.current = 0;
  }, [session?.id]);

  const historyReady = hasContextSummaries
    ? !tailSeqRange || tailQuery.isSuccess
    : messagesQuery.isSuccess;

  useEffect(() => {
    if (!session || !historyReady || initialScrollSessionRef.current === session.id) return;
    initialScrollSessionRef.current = session.id;
    shouldAutoScrollRef.current = true;
    setTimeout(() => {
      if (transcriptRef.current)
        transcriptRef.current.scrollTop = transcriptRef.current.scrollHeight;
    }, 0);
  }, [session, historyReady]);

  // The transcript loads older pages on scroll-up, but that can only fire once
  // the column overflows. A newest page made entirely of assistant/tool turns
  // collapses into a single short bubble that never fills the viewport, so the
  // scroll trigger stays dead and every earlier page is unreachable. Keep
  // pulling older pages until the column overflows (or runs out), pinned to the
  // bottom so the newest turn stays in view.
  useEffect(() => {
    if (hasContextSummaries) return;
    const el = transcriptRef.current;
    if (!el) return;
    if (!messagesQuery.isSuccess || messagesQuery.isFetchingNextPage || !messagesQuery.hasNextPage)
      return;
    if (el.scrollHeight > el.clientHeight) return;
    if (autoFillPagesRef.current >= MAX_AUTO_FILL_PAGES) return;
    autoFillPagesRef.current += 1;
    void messagesQuery.fetchNextPage().then(() => {
      requestAnimationFrame(() => {
        if (transcriptRef.current)
          transcriptRef.current.scrollTop = transcriptRef.current.scrollHeight;
      });
    });
  }, [
    hasContextSummaries,
    messages.length,
    messagesQuery.isSuccess,
    messagesQuery.isFetchingNextPage,
    messagesQuery.hasNextPage,
    messagesQuery.fetchNextPage,
  ]);

  // Auto-scroll to bottom as new messages stream in (if the user is already
  // near the bottom). Keyed on length + tail content size rather than the
  // array identity: the messages array is rebuilt on every stream update, and
  // reading scrollHeight forces a reflow of the whole transcript.
  const lastMessage = messages[messages.length - 1];
  const lastMessageSize = lastMessage
    ? (lastMessage.content?.length ?? 0) +
      (lastMessage.blocks?.reduce(
        (sum, b) =>
          sum +
          (b.type === "text"
            ? b.text.length
            : b.type === "thinking"
              ? (b.thinking?.length ?? 0)
              : 1),
        0,
      ) ?? 0)
    : 0;
  useEffect(() => {
    if (!transcriptRef.current) return;
    const el = transcriptRef.current;
    if (shouldAutoScrollRef.current) {
      requestAnimationFrame(() => {
        el.scrollTop = el.scrollHeight;
      });
    }
  }, [messages.length, lastMessageSize]);

  const loadOlderMessages = useCallback(async () => {
    // Compacted sessions have no older pages to load: everything before the
    // tail is folded into the epoch summaries shown above the transcript.
    if (
      !session ||
      hasContextSummaries ||
      !transcriptRef.current ||
      !messagesQuery.hasNextPage ||
      messagesQuery.isFetching
    )
      return;
    const el = transcriptRef.current;
    if (el.scrollTop > 60) return;
    const prevHeight = el.scrollHeight;
    await messagesQuery.fetchNextPage();
    setTimeout(() => {
      if (el) el.scrollTop = el.scrollHeight - prevHeight;
    }, 0);
  }, [session, hasContextSummaries, messagesQuery]);

  const handleTranscriptScroll = useCallback(() => {
    void loadOlderMessages();
    if (transcriptRef.current) {
      const el = transcriptRef.current;
      const isAtBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 50;
      shouldAutoScrollRef.current = isAtBottom;
    }
  }, [loadOlderMessages]);

  const sendMessage = useCallback(
    async (input: string) => {
      if ((!input.trim() && attachments.length === 0) || isStreaming || !session) return;
      if (attachments.some((a) => a.uploading)) return;

      const parts = buildMessageParts(input);
      setResumeEnabled(true);
      setRecoveringDisconnect(false);
      clearAttachments();
      shouldAutoScrollRef.current = true;
      setTimeout(() => {
        if (transcriptRef.current)
          transcriptRef.current.scrollTop = transcriptRef.current.scrollHeight;
      }, 0);

      void chatSendMessage({ parts, metadata: { timestamp: new Date().toISOString() } });
    },
    [isStreaming, session, attachments, buildMessageParts, clearAttachments, chatSendMessage],
  );

  const stopActiveTurn = useCallback(() => {
    if (!session) return;
    // Block automatic reconnect until another message is sent. The explicit
    // action cancels server work; chatStop only detaches this local reader.
    setResumeEnabled(false);
    setRecoveringDisconnect(false);
    void chatStop();
    void stopSession({
      path: { agentId: session.agent_id, sessionId: session.id },
      throwOnError: true,
    })
      .then(() => markViewed())
      .catch((err) => {
        console.error("[session stop]", err);
        setResumeEnabled(true);
      });
  }, [session, chatStop, markViewed]);

  // A thread started from the home composer arrives with its first message
  // parked in memory. Claim it once the session is loaded; `takePendingMessage`
  // hands it out exactly once, so a reload never re-sends it.
  const pendingSentRef = useRef<string | null>(null);
  useEffect(() => {
    if (!session || pendingSentRef.current === session.id) return;
    const text = takePendingMessage(session.id);
    if (!text) return;
    pendingSentRef.current = session.id;
    void sendMessage(text);
  }, [session, sendMessage]);

  const exportSessionAs = useCallback(
    async (format: "jsonl" | "md") => {
      if (!session || exporting || isStreaming) return;
      setExporting(true);
      try {
        const exportedAt = new Date().toISOString();
        const all = await fetchAllSessionMessages(session.agent_id, session.id, {
          before: exportedAt,
        });
        if (all.length === 0) {
          showToast(t("sessions.export.empty"), "error");
          return;
        }
        const agentName = agentsList.find((a) => a.id === session.agent_id)?.name ?? "Agent";
        const exportMeta = {
          session,
          agentName,
          exportedAt,
        };
        const isJsonl = format === "jsonl";
        const body = isJsonl
          ? messagesToJSONL(all, exportMeta)
          : messagesToMarkdown(all, exportMeta);
        const filename = exportFileName(session, isJsonl ? "jsonl" : "md");
        const mime = isJsonl ? "application/x-ndjson" : "text/markdown";
        downloadTextFile(filename, body, mime);
        showToast(t("sessions.export.success", { count: all.length }), "success");
      } catch (e) {
        showToast(
          t("sessions.export.failed", { error: (e as Error).message ?? String(e) }),
          "error",
        );
      } finally {
        setExporting(false);
      }
    },
    [session, exporting, isStreaming, agentsList, showToast, t],
  );

  const { setHeaderTitle, setHeaderActions, setHeaderPanelToggle } = useAppShell();

  // A main session *is* the agent (or project) conversation, so its title only
  // repeats the breadcrumb — "Anna / Anna". Only a branched thread earns a tail.
  const titleText =
    session && session.kind !== "main"
      ? sessionDisplayTitle(session.title, t("sessions.untitled"))
      : "";
  useEffect(() => {
    setHeaderTitle(
      titleText ? (
        <h1 className="truncate text-[15px] font-semibold tracking-[-0.01em]">{titleText}</h1>
      ) : null,
    );
    // The shell outlives this page: leaving a stale title behind is what made a
    // session's crumb linger on the profile page.
    return () => setHeaderTitle(null);
  }, [titleText, setHeaderTitle]);

  const exportDisabled = exporting || isStreaming;

  useEffect(() => {
    setHeaderActions(
      session ? (
        <div className="flex items-center gap-1 shrink-0">
          <DropdownMenu
            open={exportMenuOpen && !exportDisabled}
            onOpenChange={(next) => {
              if (exportDisabled) return;
              setExportMenuOpen(next);
            }}
          >
            <Tooltip>
              <TooltipTrigger
                render={
                  <DropdownMenuTrigger
                    render={
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        aria-disabled={exportDisabled || undefined}
                        data-disabled={exportDisabled || undefined}
                        className="aria-disabled:cursor-not-allowed aria-disabled:opacity-50"
                        aria-label={t("sessions.export.button")}
                      >
                        <Download />
                      </Button>
                    }
                  />
                }
              />
              <TooltipPopup side="bottom">
                {exporting
                  ? t("sessions.export.exporting")
                  : isStreaming
                    ? t("sessions.export.streamingDisabled")
                    : t("sessions.export.button")}
              </TooltipPopup>
            </Tooltip>
            <DropdownMenuContent align="end">
              <DropdownMenuItem onClick={() => void exportSessionAs("jsonl")}>
                {t("sessions.export.jsonl")}
              </DropdownMenuItem>
              <DropdownMenuItem onClick={() => void exportSessionAs("md")}>
                {t("sessions.export.markdown")}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
          {onNewSession && (
            <Tooltip>
              <TooltipTrigger
                render={
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    onClick={onNewSession}
                    aria-label={t("sessions.startThread")}
                  >
                    <MessageSquarePlus />
                  </Button>
                }
              />
              <TooltipPopup side="bottom">{t("sessions.startThread")}</TooltipPopup>
            </Tooltip>
          )}
          <ChatWidthToggle />
          <SessionInfoPopover session={session} />
        </div>
      ) : null,
    );
    return () => setHeaderActions(null);
  }, [
    session,
    onNewSession,
    setHeaderActions,
    exporting,
    exportSessionAs,
    exportDisabled,
    exportMenuOpen,
    t,
  ]);

  // The workspace toggle is a layout control, not a page action: it rides at the
  // header's far edge, mirroring the sidebar trigger on the other side.
  useEffect(() => {
    if (!onToggleWorkspace) {
      setHeaderPanelToggle(null);
      return;
    }
    setHeaderPanelToggle(
      <Tooltip>
        <TooltipTrigger
          render={
            <Button
              variant="ghost"
              size="icon-sm"
              onClick={onToggleWorkspace}
              aria-pressed={workspaceOpen}
              aria-label={t("sessions.inspector.files")}
            >
              <PanelRight />
            </Button>
          }
        />
        <TooltipPopup side="bottom">{t("sessions.inspector.files")}</TooltipPopup>
      </Tooltip>,
    );
    return () => setHeaderPanelToggle(null);
  }, [onToggleWorkspace, workspaceOpen, setHeaderPanelToggle, t]);

  if (!session) {
    return (
      <div className="flex-1 min-w-0 flex flex-col overflow-hidden">
        <div className="flex-1 flex flex-col items-center justify-center gap-3">
          <p className="text-sm text-muted-foreground">{t("sessions.selectSession")}</p>
          <p className="text-xs text-muted-foreground font-mono">{t("sessions.createNewHint")}</p>
        </div>
      </div>
    );
  }
  const composer =
    session.user_id === currentUserID ? (
      <ChatComposer
        onSend={(text) => void sendMessage(text)}
        onStop={stopActiveTurn}
        isStreaming={isStreaming}
        placeholder={t("sessions.composer.placeholder")}
        attachments={attachments}
        onFileSelect={(files) => void selectFiles(files)}
        onRemoveAttachment={removeAttachment}
        skills={composerSkills}
      />
    ) : null;

  // A brand-new thread has nothing to scroll: show the agent and the composer
  // in the middle of the column instead of an empty transcript with a composer
  // docked to the bottom. Gated on the history query having settled so the
  // centered state can never flash while messages are still loading, and
  // limited to hand-started threads — a "main" conversation is never empty in
  // spirit even when it has no rows yet.
  const isBlankThread =
    session.kind === "chat" && historyReady && messages.length === 0 && !!composer;

  if (isBlankThread) {
    const agentName = agentsList.find((a) => a.id === session.agent_id)?.name ?? "";
    return (
      <>
        <div className="flex min-h-0 min-w-0 flex-1 flex-col items-center justify-center overflow-y-auto bg-background">
          <div className="w-full max-w-[var(--chat-column)]">
            {agentName && (
              <h2 className="px-4 pb-4 text-center text-xl font-semibold sm:px-8">{agentName}</h2>
            )}
            {composer}
          </div>
        </div>
      </>
    );
  }

  return (
    <>
      <ChatPane
        banner={
          attentionItems.length > 0 ? (
            <div className="border-b border-border/70 bg-muted/20 px-3 py-2">
              <div className="flex items-center gap-2 overflow-x-auto">
                <span className="inline-flex shrink-0 items-center gap-1.5 text-xs font-medium text-muted-foreground">
                  <AlertCircle className="size-3.5" />
                  {t("inbox.needsYou")}
                </span>
                {attentionItems.map((item) => (
                  <Link
                    key={item.id}
                    to={item.target_path}
                    className="inline-flex h-7 max-w-[220px] shrink-0 items-center gap-1.5 rounded-md border border-border/70 bg-background px-2 text-left text-xs transition-colors hover:bg-muted"
                  >
                    <span className="truncate">{item.title}</span>
                    <span className="shrink-0 text-xs uppercase text-muted-foreground">
                      {t(inboxKindLabels[item.kind])}
                    </span>
                  </Link>
                ))}
              </div>
            </div>
          ) : null
        }
        transcript={
          <Transcript
            ref={transcriptRef}
            messages={messages}
            messagesLoading={
              hasContextSummaries
                ? tailQuery.isLoading
                : messagesQuery.isLoading || messagesQuery.isFetchingNextPage
            }
            contextItems={contextItemsQuery.data?.items}
            contextLoading={contextItemsQuery.isLoading}
            onScroll={handleTranscriptScroll}
            agentId={agentId}
            sessionId={sessionId}
            activeStreaming={isStreaming}
          />
        }
        notice={<ChatErrorNotice error={chatError} />}
        composer={composer}
      />
    </>
  );
}

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { useChat } from "@ai-sdk/react";
import type { UIMessage } from "ai";
import { Link } from "@tanstack/react-router";
import {
  AlertCircle,
  Download,
  MessageSquarePlus,
  PanelRightClose,
  PanelRightOpen,
} from "lucide-react";
import { useI18n } from "@/lib/i18n";
import { getSessionMessages, uploadWorkspaceFile } from "@/lib/api-client/sdk.gen";
import { agentSkillsOptions, agentsQueryOptions } from "@/lib/queries/agents";
import { inboxQueryOptions } from "@/lib/queries/inbox";
import { sessionContextItemsOptions } from "@/lib/queries/session-context";
import { fetchAllSessionMessages } from "@/lib/paginated";
import type { Message, Session } from "@/lib/types";
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
import { useToast, ToastContainer } from "@/hooks/use-toast";
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
import { Transcript } from "./Transcript";
import { useFileAttachments } from "./useFileAttachments";

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
  contextTitle?: string;
}

export function SessionDetail({
  session,
  currentUserID,
  onNewSession,
  onToggleWorkspace,
  workspaceOpen,
  contextTitle,
}: Props) {
  const { t } = useI18n();
  const { toasts, showToast } = useToast();
  const [exporting, setExporting] = useState(false);
  const [exportMenuOpen, setExportMenuOpen] = useState(false);
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

  const {
    messages: chatMessages,
    sendMessage: chatSendMessage,
    setMessages: setChatMessages,
    status: chatStatus,
    stop: chatStop,
    resumeStream: chatResume,
    error: chatError,
  } = useChat({
    id: session?.id ?? "empty",
    transport,
    // Batch SSE deltas: without this every token re-renders the transcript.
    experimental_throttle: 50,
    onError: (err) => console.error("[session chat]", err),
  });

  const isStreaming = chatStatus === "streaming" || chatStatus === "submitted";

  // Server-driven sessions (scheduler/task/delegate) run turns that carry no
  // HTTP request of their own, so the normal send-stream never sees them. Poll
  // the read-only events stream to watch such a turn live: resumeStream()
  // attaches when one is in flight and no-ops (204) otherwise. Fire it only
  // while idle so we never double-connect, and read status via a ref to keep
  // the interval stable.
  const isInternalKind =
    session?.kind === "scheduler" || session?.kind === "task" || session?.kind === "delegate";
  const chatStatusRef = useRef(chatStatus);
  chatStatusRef.current = chatStatus;
  // resumeStream() is async and status only flips to "streaming" once the SSE
  // connection parses its first frame; guard with an in-flight ref so a tick
  // firing inside that window can't open a second concurrent stream.
  const resumingRef = useRef(false);
  useEffect(() => {
    if (!session || !isInternalKind) return;
    let cancelled = false;
    const tick = () => {
      if (cancelled || resumingRef.current || chatStatusRef.current !== "ready") return;
      resumingRef.current = true;
      void chatResume().finally(() => {
        resumingRef.current = false;
      });
    };
    tick();
    const timer = setInterval(tick, 3000);
    return () => {
      cancelled = true;
      clearInterval(timer);
    };
  }, [session?.id, isInternalKind, chatResume]);

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
    setChatMessages((prev) =>
      reconcileHistoryUIMessages(
        uiMessages,
        prev.filter((message) => !historicalIDsRef.current.has(message.id)),
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

  const { setHeaderTitle, setHeaderActions } = useAppShell();

  const titleText = session ? contextTitle || session.title || t("sessions.untitled") : "";
  useEffect(() => {
    setHeaderTitle(
      titleText ? (
        <h1 className="truncate text-[15px] font-semibold tracking-[-0.01em]">{titleText}</h1>
      ) : null,
    );
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
          {onToggleWorkspace && (
            <Button
              variant="ghost"
              size="icon-sm"
              onClick={onToggleWorkspace}
              aria-label={workspaceOpen ? t("sessions.hideInspector") : t("sessions.showInspector")}
              title={workspaceOpen ? t("sessions.hideInspector") : t("sessions.showInspector")}
            >
              {workspaceOpen ? <PanelRightClose /> : <PanelRightOpen />}
            </Button>
          )}
        </div>
      ) : null,
    );
  }, [
    session,
    onNewSession,
    onToggleWorkspace,
    workspaceOpen,
    setHeaderActions,
    exporting,
    exportSessionAs,
    exportDisabled,
    exportMenuOpen,
    t,
  ]);

  if (!session) {
    return (
      <div className="flex-1 min-w-0 flex flex-col overflow-hidden">
        <div className="flex-1 flex flex-col items-center justify-center gap-3">
          <p className="text-sm text-muted-foreground/70">{t("sessions.selectSession")}</p>
          <p className="text-xs text-muted-foreground/40 font-mono">
            {t("sessions.createNewHint")}
          </p>
        </div>
      </div>
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
        composer={
          session.user_id === currentUserID ? (
            <ChatComposer
              onSend={(text) => void sendMessage(text)}
              onStop={() => chatStop()}
              isStreaming={isStreaming}
              placeholder={t("sessions.composer.placeholder")}
              attachments={attachments}
              onFileSelect={(files) => void selectFiles(files)}
              onRemoveAttachment={removeAttachment}
              skills={composerSkills}
            />
          ) : null
        }
      />
      <ToastContainer messages={toasts} />
    </>
  );
}

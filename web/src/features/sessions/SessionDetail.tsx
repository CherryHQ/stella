import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { useChat } from "@ai-sdk/react";
import { useNavigate } from "@tanstack/react-router";
import {
  AlertCircle,
  Download,
  MessageCircleDashed,
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
  uiMessageToMessage,
} from "@/lib/chat-transport";
import { useAppShell } from "@/layouts/AppShell";
import { BUILTIN_COMMANDS, ChatComposer } from "./ChatComposer";
import { Transcript } from "./Transcript";
import { useFileAttachments } from "./useFileAttachments";

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
  contextSubtitle?: string;
}

export function SessionDetail({
  session,
  currentUserID,
  onNewSession,
  onToggleWorkspace,
  workspaceOpen,
  contextTitle,
  contextSubtitle,
}: Props) {
  const { t } = useI18n();
  const navigate = useNavigate();
  const [userInput, setUserInput] = useState("");
  const { toasts, showToast } = useToast();
  const [exporting, setExporting] = useState(false);
  const [exportMenuOpen, setExportMenuOpen] = useState(false);
  const { data: agentsList = [] } = useQuery(agentsQueryOptions);

  const transcriptRef = useRef<HTMLDivElement>(null);
  const sessionIDRef = useRef<string | null>(null);
  const initialScrollSessionRef = useRef<string | null>(null);
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
  const { attachments, selectFiles, removeAttachment, clearAttachments, buildMessageText } =
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
  } = useChat({
    id: session?.id ?? "empty",
    transport,
  });

  const isStreaming = chatStatus === "streaming" || chatStatus === "submitted";

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
        query: { limit: 20, skip: pageParam },
        throwOnError: true,
      });
      return (data?.messages as unknown as Message[] | undefined) ?? [];
    },
    getNextPageParam: (lastPage, allPages) =>
      lastPage.length === 20 ? allPages.reduce((sum, page) => sum + page.length, 0) : undefined,
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
      return (data?.messages as unknown as Message[] | undefined) ?? [];
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
    const newIDs = new Set(uiMessages.map((m) => m.id));
    historicalIDsRef.current = newIDs;
    setChatMessages((prev) => {
      const liveSlice = prev.filter((m) => !newIDs.has(m.id));
      return [...uiMessages, ...liveSlice];
    });
  }, [historyMessages, setChatMessages]);

  const messages = useMemo(() => chatMessages.map(uiMessageToMessage), [chatMessages]);

  useEffect(() => {
    if (!session) {
      setChatMessages([]);
      clearAttachments();
      return;
    }
    sessionIDRef.current = session.id;
    initialScrollSessionRef.current = null;
    shouldAutoScrollRef.current = true;
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

  // Auto-scroll to bottom as new messages stream in (if the user is already near the bottom)
  useEffect(() => {
    if (!transcriptRef.current) return;
    const el = transcriptRef.current;
    if (shouldAutoScrollRef.current) {
      requestAnimationFrame(() => {
        el.scrollTop = el.scrollHeight;
      });
    }
  }, [messages]);

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
    async (overrideText?: string) => {
      const input = overrideText ?? userInput;
      if ((!input.trim() && attachments.length === 0) || isStreaming || !session) return;
      if (attachments.some((a) => a.uploading)) return;

      const text = buildMessageText(input);
      setUserInput("");
      clearAttachments();
      shouldAutoScrollRef.current = true;
      setTimeout(() => {
        if (transcriptRef.current)
          transcriptRef.current.scrollTop = transcriptRef.current.scrollHeight;
      }, 0);

      void chatSendMessage({ text });
    },
    [
      userInput,
      isStreaming,
      session,
      attachments,
      buildMessageText,
      clearAttachments,
      chatSendMessage,
    ],
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
  const subtitleText = session
    ? contextSubtitle ||
      t("sessions.messagesChannel", {
        count: messages.length,
        channel: channelLabel(session.channel) || t("sessions.chat"),
      })
    : "";

  useEffect(() => {
    setHeaderTitle(
      titleText ? (
        <div className="min-w-0">
          <h1 className="truncate text-[15px] font-semibold tracking-[-0.01em]">{titleText}</h1>
          <p className="mt-0.5 truncate text-[11px] text-muted-foreground">{subtitleText}</p>
        </div>
      ) : null,
    );
  }, [titleText, subtitleText, setHeaderTitle]);

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
                        className="rounded-full text-muted-foreground aria-disabled:cursor-not-allowed aria-disabled:opacity-50"
                        aria-label={t("sessions.export.button")}
                      >
                        <Download className="size-3.5" />
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
                    size="xs"
                    onClick={onNewSession}
                    className="hidden h-7 w-7 rounded-full p-0 text-muted-foreground sm:inline-flex"
                    aria-label={t("sessions.startThread")}
                  >
                    <MessageCircleDashed className="size-3.5" />
                  </Button>
                }
              />
              <TooltipPopup side="bottom">{t("sessions.startThread")}</TooltipPopup>
            </Tooltip>
          )}
          {onToggleWorkspace && (
            <Button
              variant="ghost"
              size="xs"
              onClick={onToggleWorkspace}
              className="h-7 w-7 rounded-full p-0 text-muted-foreground"
              title={workspaceOpen ? t("sessions.hideInspector") : t("sessions.showInspector")}
            >
              {workspaceOpen ? (
                <PanelRightClose className="size-3.5" />
              ) : (
                <PanelRightOpen className="size-3.5" />
              )}
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
          <p className="text-[11px] text-muted-foreground/40 font-mono">
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
                  <button
                    key={item.id}
                    type="button"
                    className="inline-flex h-7 max-w-[220px] shrink-0 items-center gap-1.5 rounded-md border border-border/70 bg-background px-2 text-left text-xs transition-colors hover:bg-muted"
                    onClick={() => void navigate({ to: item.target_path })}
                  >
                    <span className="truncate">{item.title}</span>
                    <span className="shrink-0 text-[10px] uppercase text-muted-foreground">
                      {t(inboxKindLabels[item.kind])}
                    </span>
                  </button>
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
        composer={
          session.user_id === currentUserID ? (
            <ChatComposer
              value={userInput}
              onChange={setUserInput}
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

function channelLabel(ch: string | null | undefined): string {
  if (!ch) return "";
  const m = ch.match(/:channel:([^:]+)/);
  return m ? m[1] : ch;
}

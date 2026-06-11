import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { useChat } from "@ai-sdk/react";
import { Download, MessageCircleDashed, PanelRightClose, PanelRightOpen } from "lucide-react";
import { useI18n } from "@/lib/i18n";
import { getSessionMessages, uploadWorkspaceFile } from "@/lib/api-client/sdk.gen";
import { agentSkillsOptions, agentsQueryOptions } from "@/lib/queries/agents";
import { fetchAllSessionMessages } from "@/lib/paginated";
import type { Message, Session } from "@/lib/types";
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
  const [userInput, setUserInput] = useState("");
  const { toasts, showToast } = useToast();
  const [exporting, setExporting] = useState(false);
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
    enabled: !!session,
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
    if (!session) {
      setChatMessages([]);
      clearAttachments();
      return;
    }
    sessionIDRef.current = session.id;
    initialScrollSessionRef.current = null;
    shouldAutoScrollRef.current = true;
  }, [session?.id]);

  useEffect(() => {
    if (!session || !messagesQuery.isSuccess || initialScrollSessionRef.current === session.id)
      return;
    initialScrollSessionRef.current = session.id;
    shouldAutoScrollRef.current = true;
    setTimeout(() => {
      if (transcriptRef.current)
        transcriptRef.current.scrollTop = transcriptRef.current.scrollHeight;
    }, 0);
  }, [session, messagesQuery.isSuccess]);

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
    if (
      !session ||
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
  }, [session, messagesQuery]);

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
      if (!session || exporting) return;
      setExporting(true);
      try {
        const all = await fetchAllSessionMessages(session.agent_id, session.id);
        if (all.length === 0) {
          showToast(t("sessions.export.empty"), "error");
          return;
        }
        const agentName = agentsList.find((a) => a.id === session.agent_id)?.name ?? "Agent";
        const exportMeta = {
          session,
          agentName,
          exportedAt: new Date().toISOString(),
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
    [session, exporting, agentsList, showToast, t],
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

  useEffect(() => {
    setHeaderActions(
      session ? (
        <div className="flex items-center gap-1 shrink-0">
          <DropdownMenu>
            <Tooltip>
              <TooltipTrigger
                render={
                  <DropdownMenuTrigger
                    aria-label={t("sessions.export.button")}
                    disabled={exporting}
                    className="grid size-7 shrink-0 cursor-pointer place-items-center rounded-full text-muted-foreground outline-none hover:bg-muted hover:text-foreground disabled:cursor-not-allowed disabled:opacity-50"
                  >
                    <Download className="size-3.5" />
                  </DropdownMenuTrigger>
                }
              />
              <TooltipPopup side="bottom">
                {exporting ? t("sessions.export.exporting") : t("sessions.export.button")}
              </TooltipPopup>
            </Tooltip>
            <DropdownMenuContent align="end">
              <DropdownMenuItem disabled={exporting} onClick={() => void exportSessionAs("jsonl")}>
                {t("sessions.export.jsonl")}
              </DropdownMenuItem>
              <DropdownMenuItem disabled={exporting} onClick={() => void exportSessionAs("md")}>
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
    <div className="flex-1 min-w-0 flex flex-col overflow-hidden bg-background">
      <div className="flex-1 min-h-0 flex overflow-hidden">
        <div className="flex-1 min-w-0 flex flex-col overflow-hidden">
          {/* Transcript */}
          <Transcript
            ref={transcriptRef}
            messages={messages}
            messagesLoading={messagesQuery.isLoading || messagesQuery.isFetchingNextPage}
            onScroll={handleTranscriptScroll}
            agentId={agentId}
            sessionId={sessionId}
            activeStreaming={isStreaming}
          />

          {/* Message input */}
          {session.user_id === currentUserID && (
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
          )}
        </div>
      </div>
      <ToastContainer messages={toasts} />
    </div>
  );
}

function channelLabel(ch: string | null | undefined): string {
  if (!ch) return "";
  const m = ch.match(/:channel:([^:]+)/);
  return m ? m[1] : ch;
}

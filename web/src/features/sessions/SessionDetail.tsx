import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useInfiniteQuery } from "@tanstack/react-query";
import { useChat } from "@ai-sdk/react";
import {
  Menu,
  MessageCircleDashed,
  PanelLeftClose,
  PanelLeftOpen,
  PanelRightClose,
  PanelRightOpen,
  ArrowUp,
  Paperclip,
} from "lucide-react";
import { useI18n } from "@/lib/i18n";
import { getSessionMessages, uploadWorkspaceFile } from "@/lib/api-client/sdk.gen";
import type { Message, Session } from "@/lib/types";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipPopup, TooltipTrigger } from "@/components/ui/tooltip";
import {
  createSessionTransport,
  mergeToolResults,
  messageToUIMessage,
  uiMessageToMessage,
} from "@/lib/chat-transport";
import { Transcript } from "./Transcript";

interface Props {
  session: Session | null;
  currentUserID: string;
  onNewSession?: () => void;
  onSessionUpdate: (s: Session) => void;
  onToggleSidebar?: () => void;
  onOpenMobileSidebar?: () => void;
  sidebarCollapsed?: boolean;
  onToggleWorkspace?: () => void;
  workspaceOpen?: boolean;
  contextTitle?: string;
  contextSubtitle?: string;
}

export function SessionDetail({
  session,
  currentUserID,
  onNewSession,
  onToggleSidebar,
  onOpenMobileSidebar,
  sidebarCollapsed,
  onToggleWorkspace,
  workspaceOpen,
  contextTitle,
  contextSubtitle,
}: Props) {
  const { t } = useI18n();
  const [userInput, setUserInput] = useState("");
  const [attachments, setAttachments] = useState<
    { name: string; path: string; uploading: boolean }[]
  >([]);

  const transcriptRef = useRef<HTMLDivElement>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const sessionIDRef = useRef<string | null>(null);
  const initialScrollSessionRef = useRef<string | null>(null);
  const shouldAutoScrollRef = useRef(true);

  const sessionId = session?.id ?? "";
  const agentId = session?.agent_id ?? "";

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
      setAttachments([]);
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

  const handleFileSelect = useCallback(
    async (files: FileList | null) => {
      if (!files || files.length === 0 || !session) return;
      for (const file of Array.from(files)) {
        const placeholder = { name: file.name, path: "", uploading: true };
        setAttachments((prev) => [...prev, placeholder]);
        try {
          const { data: res } = await uploadWorkspaceFile({
            path: { agentId: agentId, sessionId: sessionId },
            body: { file },
            throwOnError: true,
          });
          setAttachments((prev) =>
            prev.map((a) => (a === placeholder ? { ...a, path: res.path, uploading: false } : a)),
          );
        } catch (e) {
          console.error("upload failed:", e);
          setAttachments((prev) => prev.filter((a) => a !== placeholder));
        }
      }
    },
    [session, agentId, sessionId],
  );

  const removeAttachment = useCallback((idx: number) => {
    setAttachments((prev) => prev.filter((_, i) => i !== idx));
  }, []);

  const sendMessage = useCallback(async () => {
    if ((!userInput.trim() && attachments.length === 0) || isStreaming || !session) return;
    const uploading = attachments.some((a) => a.uploading);
    if (uploading) return;

    const filePaths = attachments.filter((a) => a.path).map((a) => a.path);
    const parts: string[] = [];
    for (const p of filePaths) {
      parts.push(`[file: ${p}]`);
    }
    if (userInput.trim()) parts.push(userInput.trim());
    const text = parts.join("\n");

    setUserInput("");
    setAttachments([]);
    shouldAutoScrollRef.current = true;
    setTimeout(() => {
      if (transcriptRef.current)
        transcriptRef.current.scrollTop = transcriptRef.current.scrollHeight;
    }, 0);

    void chatSendMessage({ text });
  }, [userInput, isStreaming, session, attachments, chatSendMessage]);

  if (!session) {
    return (
      <div className="flex-1 min-w-0 flex flex-col overflow-hidden">
        <div className="flex-shrink-0 h-12 border-b border-border/60 bg-background" />
        <div className="flex-1 flex flex-col items-center justify-center gap-3">
          <p className="text-sm text-muted-foreground/70">Select a session from the sidebar</p>
          <p className="text-[11px] text-muted-foreground/40 font-mono">
            or create a new one with + New
          </p>
        </div>
      </div>
    );
  }

  return (
    <div className="flex-1 min-w-0 flex flex-col overflow-hidden bg-gradient-to-b from-card/80 to-card">
      {/* Header */}
      <div className="flex h-12 flex-shrink-0 items-center border-b border-border/70 bg-card/65 px-4 backdrop-blur-xl">
        <div className="flex items-center gap-2.5 w-full min-w-0">
          {onToggleSidebar && (
            <Button
              variant="ghost"
              size="xs"
              onClick={onToggleSidebar}
              className="hidden h-7 w-7 shrink-0 rounded-full p-0 text-muted-foreground md:inline-flex"
              title={sidebarCollapsed ? "Show sidebar" : "Hide sidebar"}
            >
              {sidebarCollapsed ? (
                <PanelLeftOpen className="size-3.5" />
              ) : (
                <PanelLeftClose className="size-3.5" />
              )}
            </Button>
          )}
          {onOpenMobileSidebar && (
            <Button
              variant="ghost"
              size="xs"
              onClick={onOpenMobileSidebar}
              className="h-7 w-7 shrink-0 rounded-full p-0 text-muted-foreground md:hidden"
            >
              <Menu className="size-4" />
            </Button>
          )}
          <div className="min-w-0 flex-1">
            <h1 className="truncate text-[15px] font-semibold tracking-[-0.01em]">
              {contextTitle || session.title || "Untitled session"}
            </h1>
            <p className="mt-0.5 truncate text-[11px] text-muted-foreground">
              {contextSubtitle ||
                `${messages.length} messages · ${channelLabel(session.channel) || "chat"}`}
            </p>
          </div>
          <div className="flex items-center gap-1 shrink-0">
            {onNewSession && (
              <Tooltip>
                <TooltipTrigger
                  render={
                    <Button
                      variant="ghost"
                      size="xs"
                      onClick={onNewSession}
                      className="hidden h-7 w-7 rounded-full p-0 text-muted-foreground sm:inline-flex"
                      aria-label="Start temporary thread"
                    >
                      <MessageCircleDashed className="size-3.5" />
                    </Button>
                  }
                />
                <TooltipPopup side="bottom">Start temporary thread</TooltipPopup>
              </Tooltip>
            )}
            {onToggleWorkspace && (
              <Button
                variant="ghost"
                size="xs"
                onClick={onToggleWorkspace}
                className="h-7 w-7 rounded-full p-0 text-muted-foreground"
                title={workspaceOpen ? "Hide inspector" : "Show inspector"}
              >
                {workspaceOpen ? (
                  <PanelRightClose className="size-3.5" />
                ) : (
                  <PanelRightOpen className="size-3.5" />
                )}
              </Button>
            )}
          </div>
        </div>
      </div>

      <div className="flex-1 min-h-0 flex overflow-hidden">
        <div className="flex-1 min-w-0 flex flex-col overflow-hidden">
          {/* Transcript */}
          <Transcript
            ref={transcriptRef}
            messages={messages}
            messagesLoading={messagesQuery.isLoading || messagesQuery.isFetchingNextPage}
            onScroll={handleTranscriptScroll}
          />

          {/* Message input */}
          {session.user_id === currentUserID && (
            <div className="flex-shrink-0 bg-gradient-to-b from-card/0 to-card px-4 pt-3 pb-4 sm:px-8">
              <input
                ref={fileInputRef}
                type="file"
                multiple
                className="hidden"
                onChange={(e) => {
                  handleFileSelect(e.target.files).catch(console.error);
                  e.target.value = "";
                }}
              />
              <div
                className={cn(
                  "mx-auto max-w-4xl rounded-[28px] border bg-card/70 backdrop-blur-lg transition-all duration-200 flex flex-col p-1 shadow-[0_8px_30px_rgb(0,0,0,0.04)] dark:shadow-[0_8px_30px_rgb(0,0,0,0.18)]",
                  isStreaming
                    ? "border-primary/45"
                    : "border-border/80 focus-within:border-primary/50 focus-within:ring-2 focus-within:ring-primary/10",
                )}
                onDragOver={(e) => {
                  e.preventDefault();
                  e.stopPropagation();
                }}
                onDrop={(e) => {
                  e.preventDefault();
                  e.stopPropagation();
                  if (!isStreaming) handleFileSelect(e.dataTransfer.files).catch(console.error);
                }}
              >
                {attachments.length > 0 && (
                  <div className="flex flex-wrap gap-1.5 px-4 pt-3 pb-1">
                    {attachments.map((a, i) => (
                      <span
                        key={i}
                        className={cn(
                          "inline-flex items-center gap-1.5 text-[11px] font-mono rounded-full px-3 py-1 max-w-48 border",
                          a.uploading
                            ? "bg-muted/50 text-muted-foreground/50 border-border"
                            : "bg-primary/5 text-primary border-primary/20",
                        )}
                      >
                        {a.uploading ? (
                          <div className="w-3 h-3 border border-muted-foreground/30 border-t-muted-foreground rounded-full animate-spin shrink-0" />
                        ) : (
                          <Paperclip className="w-3 h-3 shrink-0 text-primary/70" />
                        )}
                        <span className="truncate">{a.name}</span>
                        {!a.uploading && (
                          <button
                            onClick={() => removeAttachment(i)}
                            className="text-muted-foreground/50 hover:text-foreground cursor-pointer shrink-0 font-bold ml-0.5"
                          >
                            ×
                          </button>
                        )}
                      </span>
                    ))}
                  </div>
                )}
                <textarea
                  value={userInput}
                  onChange={(e) => setUserInput(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" && !e.shiftKey) {
                      e.preventDefault();
                      sendMessage().catch(console.error);
                    }
                  }}
                  onInput={(e) => {
                    const el = e.currentTarget;
                    el.style.height = "auto";
                    el.style.height = Math.min(el.scrollHeight, 160) + "px";
                  }}
                  onPaste={(e) => {
                    const files = e.clipboardData.files;
                    if (files.length > 0 && !isStreaming) {
                      e.preventDefault();
                      handleFileSelect(files).catch(console.error);
                    }
                  }}
                  placeholder={t("sessions.composer.placeholder")}
                  className={cn(
                    "w-full resize-none overflow-y-auto border-0 bg-transparent px-4 pt-3.5 pb-2 text-[15px] leading-relaxed focus:outline-none placeholder:text-muted-foreground/40 text-foreground",
                  )}
                  style={{ minHeight: 44, maxHeight: 160 }}
                  rows={1}
                  disabled={isStreaming}
                />
                <div className="flex items-center justify-between px-3 pb-2 pt-1.5 border-t border-border/10">
                  <div className="flex items-center gap-1.5">
                    {!isStreaming && (
                      <button
                        type="button"
                        onClick={() => fileInputRef.current?.click()}
                        className="text-muted-foreground/70 hover:text-foreground hover:bg-muted/40 transition-colors p-1.5 rounded-full w-8 h-8 flex items-center justify-center cursor-pointer"
                        title="Attach files"
                      >
                        <Paperclip className="w-4 h-4" />
                      </button>
                    )}
                    {!isStreaming && (
                      <span className="text-[10px] font-mono text-muted-foreground/35 select-none pl-1">
                        ↵ send · ⇧↵ new line
                      </span>
                    )}
                    {isStreaming && (
                      <span className="text-[10px] font-mono text-primary/60 select-none animate-pulse pl-1">
                        generating…
                      </span>
                    )}
                  </div>
                  <div className="flex items-center gap-2">
                    {isStreaming && (
                      <button
                        type="button"
                        onClick={() => chatStop()}
                        className="text-destructive hover:bg-destructive/10 bg-destructive/5 border border-destructive/20 font-semibold text-xs rounded-full px-3.5 h-8 transition-colors flex items-center gap-1 cursor-pointer"
                      >
                        <div className="w-2.5 h-2.5 bg-destructive rounded-sm" />
                        <span>Stop</span>
                      </button>
                    )}
                    {!isStreaming && (
                      <button
                        type="button"
                        disabled={
                          (!userInput.trim() && attachments.length === 0) ||
                          attachments.some((a) => a.uploading)
                        }
                        onClick={() => sendMessage().catch(console.error)}
                        className={cn(
                          "w-8 h-8 rounded-full flex items-center justify-center transition-all cursor-pointer",
                          (!userInput.trim() && attachments.length === 0) ||
                            attachments.some((a) => a.uploading)
                            ? "bg-muted text-muted-foreground/30 cursor-not-allowed"
                            : "bg-primary text-primary-foreground hover:bg-primary/95 hover:scale-[1.03] active:scale-[0.97]",
                        )}
                        title="Send message"
                      >
                        <ArrowUp className="w-4.5 h-4.5 stroke-[2.5]" />
                      </button>
                    )}
                  </div>
                </div>
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

function channelLabel(ch: string | null | undefined): string {
  if (!ch) return "";
  const m = ch.match(/:channel:([^:]+)/);
  return m ? m[1] : ch;
}

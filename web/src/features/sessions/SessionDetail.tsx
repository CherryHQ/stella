import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useInfiniteQuery } from "@tanstack/react-query";
import { useChat } from "@ai-sdk/react";
import {
  MessageCircleDashed,
  PanelLeftClose,
  PanelLeftOpen,
  PanelRightClose,
  PanelRightOpen,
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
  onBack: () => void;
  onNewSession?: () => void;
  onSessionUpdate: (s: Session) => void;
  onToggleSidebar?: () => void;
  sidebarCollapsed?: boolean;
  onToggleWorkspace?: () => void;
  workspaceOpen?: boolean;
  contextTitle?: string;
  contextSubtitle?: string;
}

export function SessionDetail({
  session,
  currentUserID,
  onBack,
  onNewSession,
  onToggleSidebar,
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
        path: { agentID: agentId, sessionID: sessionId },
        query: { limit: 20, skip: pageParam },
        throwOnError: true,
      });
      return (data as unknown as Message[]) ?? [];
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
  }, [session?.id]);

  useEffect(() => {
    if (!session || !messagesQuery.isSuccess || initialScrollSessionRef.current === session.id)
      return;
    initialScrollSessionRef.current = session.id;
    setTimeout(() => {
      if (transcriptRef.current)
        transcriptRef.current.scrollTop = transcriptRef.current.scrollHeight;
    }, 0);
  }, [session, messagesQuery.isSuccess]);

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
  }, [loadOlderMessages]);

  const handleFileSelect = useCallback(
    async (files: FileList | null) => {
      if (!files || files.length === 0 || !session) return;
      for (const file of Array.from(files)) {
        const placeholder = { name: file.name, path: "", uploading: true };
        setAttachments((prev) => [...prev, placeholder]);
        try {
          const { data: res } = await uploadWorkspaceFile({
            path: { agentID: agentId, sessionID: sessionId },
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
          <button
            onClick={onBack}
            className="lg:hidden text-xs text-muted-foreground hover:text-foreground cursor-pointer shrink-0"
          >
            ←
          </button>
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
                  "relative mx-auto max-w-4xl overflow-hidden rounded-[22px] border bg-card shadow-[0_18px_42px_rgba(29,29,31,0.09)] transition-all duration-150",
                  isStreaming
                    ? "border-primary/40"
                    : "border-border/80 focus-within:border-primary/50 focus-within:shadow-[0_0_0_4px_color-mix(in_oklch,var(--primary)_10%,transparent),0_18px_42px_rgba(29,29,31,0.09)]",
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
                  <div className="flex flex-wrap gap-1.5 px-4 pt-3">
                    {attachments.map((a, i) => (
                      <span
                        key={i}
                        className={cn(
                          "inline-flex items-center gap-1 text-[11px] font-mono rounded-lg px-2 py-1 max-w-48 border",
                          a.uploading
                            ? "bg-muted/50 text-muted-foreground/50 border-border"
                            : "bg-primary/5 text-primary border-primary/20",
                        )}
                      >
                        {a.uploading ? (
                          <div className="w-3 h-3 border border-muted-foreground/30 border-t-muted-foreground rounded-full animate-spin shrink-0" />
                        ) : (
                          <svg
                            className="w-3 h-3 shrink-0"
                            fill="none"
                            viewBox="0 0 24 24"
                            strokeWidth="2"
                            stroke="currentColor"
                          >
                            <path
                              strokeLinecap="round"
                              strokeLinejoin="round"
                              d="m18.375 12.739-7.693 7.693a4.5 4.5 0 0 1-6.364-6.364l10.94-10.94A3 3 0 1 1 19.5 7.372L8.552 18.32m.009-.01-.01.01m5.699-9.941-7.81 7.81a1.5 1.5 0 0 0 2.112 2.13"
                            />
                          </svg>
                        )}
                        <span className="truncate">{a.name}</span>
                        {!a.uploading && (
                          <button
                            onClick={() => removeAttachment(i)}
                            className="text-muted-foreground/50 hover:text-foreground cursor-pointer shrink-0"
                          >
                            x
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
                    "w-full resize-none overflow-y-auto border-0 bg-transparent px-4 pb-11 text-sm leading-relaxed focus:outline-none",
                    attachments.length > 0 ? "pt-2" : "pt-3",
                  )}
                  style={{ minHeight: 52, maxHeight: 160 }}
                  rows={1}
                  disabled={isStreaming}
                />
                <div className="pointer-events-none absolute right-3 bottom-2.5 left-4 flex items-center justify-between">
                  {!isStreaming && (
                    <span className="text-[10px] font-mono text-muted-foreground/30 select-none">
                      ↵ send · ⇧↵ new line
                    </span>
                  )}
                  {isStreaming && (
                    <span className="text-[10px] font-mono text-primary/50 select-none">
                      generating…
                    </span>
                  )}
                  <div className="flex items-center gap-1 pointer-events-auto">
                    {!isStreaming && (
                      <Button
                        size="xs"
                        variant="ghost"
                        onClick={() => fileInputRef.current?.click()}
                        className="text-muted-foreground rounded-lg"
                        title="Attach files"
                      >
                        <svg
                          className="w-4 h-4"
                          fill="none"
                          viewBox="0 0 24 24"
                          strokeWidth="1.8"
                          stroke="currentColor"
                        >
                          <path
                            strokeLinecap="round"
                            strokeLinejoin="round"
                            d="m18.375 12.739-7.693 7.693a4.5 4.5 0 0 1-6.364-6.364l10.94-10.94A3 3 0 1 1 19.5 7.372L8.552 18.32m.009-.01-.01.01m5.699-9.941-7.81 7.81a1.5 1.5 0 0 0 2.112 2.13"
                          />
                        </svg>
                      </Button>
                    )}
                    {isStreaming && (
                      <Button
                        size="xs"
                        variant="ghost"
                        onClick={() => chatStop()}
                        className="text-destructive gap-1 rounded-lg"
                      >
                        Stop
                      </Button>
                    )}
                    {!isStreaming && (
                      <Button
                        size="sm"
                        disabled={
                          (!userInput.trim() && attachments.length === 0) ||
                          attachments.some((a) => a.uploading)
                        }
                        onClick={() => sendMessage().catch(console.error)}
                        className="gap-1.5 rounded-full active:scale-[0.98] transition-transform"
                      >
                        <svg className="w-4 h-4" viewBox="0 0 24 24" fill="currentColor">
                          <path d="M3.478 2.405a.75.75 0 0 0-.926.94l2.432 7.905H13.5a.75.75 0 0 1 0 1.5H4.984l-2.432 7.905a.75.75 0 0 0 .926.94 60.519 60.519 0 0 0 18.445-8.986.75.75 0 0 0 0-1.218A60.517 60.517 0 0 0 3.478 2.405Z" />
                        </svg>
                        {t("sessions.composer.send")}
                      </Button>
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

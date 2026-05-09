import { useCallback, useEffect, useRef, useState } from "react";
import { useInfiniteQuery } from "@tanstack/react-query";
import { useI18n } from "@/lib/i18n";
import { api } from "@/lib/api";
import { formatTime } from "@/lib/time";
import type { Message, Session, Tool } from "@/lib/types";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Transcript } from "./Transcript";

interface Props {
  session: Session | null;
  currentUserID: number;
  onBack: () => void;
  onSessionUpdate: (s: Session) => void;
  onToggleLeft: () => void;
  onToggleRight: () => void;
}

export function SessionDetail({
  session,
  currentUserID,
  onBack,
  onToggleLeft,
  onToggleRight,
}: Props) {
  const { t } = useI18n();
  const [liveMessages, setLiveMessages] = useState<Message[]>([]);
  const [systemPrompt, setSystemPrompt] = useState("");
  const [tools, setTools] = useState<Tool[]>([]);
  const [toolsLoading, setToolsLoading] = useState(false);
  const [activePanel, setActivePanel] = useState<"info" | "tools" | "prompt" | null>(null);
  const [userInput, setUserInput] = useState("");
  const [isStreaming, setIsStreaming] = useState(false);
  const [attachments, setAttachments] = useState<
    { name: string; path: string; uploading: boolean }[]
  >([]);

  const abortRef = useRef<AbortController | null>(null);
  const transcriptRef = useRef<HTMLDivElement>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const sessionIDRef = useRef<string | null>(null);
  const initialScrollSessionRef = useRef<string | null>(null);

  const enc = session ? encodeURIComponent(session.id) : "";
  const messagesQuery = useInfiniteQuery({
    queryKey: ["session-messages", session?.id],
    enabled: !!session,
    initialPageParam: 0,
    queryFn: ({ pageParam }) =>
      api<Message[]>("GET", `/api/sessions/${enc}/messages?limit=20&skip=${pageParam}`),
    getNextPageParam: (lastPage, allPages) =>
      lastPage.length === 20 ? allPages.reduce((sum, page) => sum + page.length, 0) : undefined,
  });
  const messages = [...(messagesQuery.data?.pages ?? [])].reverse().flat().concat(liveMessages);

  useEffect(() => {
    if (!session) {
      setLiveMessages([]);
      setSystemPrompt("");
      setActivePanel(null);
      setAttachments([]);
      return;
    }
    sessionIDRef.current = session.id;
    initialScrollSessionRef.current = null;
    setLiveMessages([]);

    const load = async () => {
      const e = encodeURIComponent(session.id);
      const pr = await api<{ system_prompt: string }>(
        "GET",
        `/api/sessions/${e}/system-prompt`,
      ).catch(() => null);
      if (sessionIDRef.current !== session.id) return;
      if (pr?.system_prompt) setSystemPrompt(pr.system_prompt);
    };
    load().catch(console.error);
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

  const loadTools = useCallback(async () => {
    setToolsLoading(true);
    try {
      setTools((await api<Tool[]>("GET", "/api/tools")) ?? []);
    } finally {
      setToolsLoading(false);
    }
  }, []);

  const togglePanel = useCallback(
    (name: "info" | "tools" | "prompt") => {
      setActivePanel((prev) => {
        const next = prev === name ? null : name;
        if (next === "tools" && tools.length === 0) loadTools().catch(console.error);
        return next;
      });
    },
    [tools.length, loadTools],
  );

  const handleFileSelect = useCallback(
    async (files: FileList | null) => {
      if (!files || files.length === 0 || !session) return;
      for (const file of Array.from(files)) {
        const placeholder = { name: file.name, path: "", uploading: true };
        setAttachments((prev) => [...prev, placeholder]);
        try {
          const form = new FormData();
          form.append("file", file);
          const res = await api<{ path: string }>(
            "POST",
            `/api/sessions/${enc}/workspace/upload`,
            form,
          );
          setAttachments((prev) =>
            prev.map((a) => (a === placeholder ? { ...a, path: res.path, uploading: false } : a)),
          );
        } catch (e) {
          console.error("upload failed:", e);
          setAttachments((prev) => prev.filter((a) => a !== placeholder));
        }
      }
    },
    [session, enc],
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
    const content = parts.join("\n");

    setUserInput("");
    setAttachments([]);
    setLiveMessages((prev) => [
      ...prev,
      { role: "user", content, timestamp: new Date().toISOString() },
    ]);
    setIsStreaming(true);
    abortRef.current = new AbortController();
    setTimeout(() => {
      if (transcriptRef.current)
        transcriptRef.current.scrollTop = transcriptRef.current.scrollHeight;
    }, 0);

    try {
      const res = await fetch(`/api/sessions/${enc}/messages`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ content }),
        signal: abortRef.current.signal,
      });
      if (!res.ok) throw new Error((await res.text()) || res.statusText);

      const reader = res.body!.getReader();
      const decoder = new TextDecoder();
      let buffer = "";
      let currentEvent = "";
      let currentData = "";

      const scrollToBottom = () => {
        if (transcriptRef.current) {
          const el = transcriptRef.current;
          if (el.scrollHeight - el.scrollTop - el.clientHeight < 200)
            el.scrollTop = el.scrollHeight;
        }
      };

      const dispatch = (event: string, dataStr: string) => {
        if (!dataStr) return;
        let data: Record<string, unknown>;
        try {
          data = JSON.parse(dataStr) as Record<string, unknown>;
        } catch {
          return;
        }

        if (event === "text") {
          const text = (data.text as string) || "";
          setLiveMessages((prev) => {
            const last = prev[prev.length - 1];
            if (last?.role === "assistant" && last._streaming) {
              const blocks = [...(last.blocks ?? [])];
              const lastBlock = blocks[blocks.length - 1];
              if (lastBlock?.type === "text") {
                blocks[blocks.length - 1] = { ...lastBlock, text: lastBlock.text + text };
              } else {
                blocks.push({ type: "text", text });
              }
              return [...prev.slice(0, -1), { ...last, blocks }];
            }
            return [
              ...prev,
              {
                role: "assistant",
                blocks: [{ type: "text", text }],
                timestamp: new Date().toISOString(),
                _streaming: true,
              },
            ];
          });
          scrollToBottom();
        } else if (event === "tool_use") {
          if ((data.type as string) === "tool_call") {
            setLiveMessages((prev) => {
              const last = prev[prev.length - 1];
              const newBlock = {
                type: "tool_call" as const,
                id: data.id as string,
                name: data.name as string,
                arguments: data.arguments as Record<string, unknown>,
                status: "running" as const,
              };
              if (last?.role === "assistant" && last._streaming) {
                return [
                  ...prev.slice(0, -1),
                  { ...last, blocks: [...(last.blocks ?? []), newBlock] },
                ];
              }
              return [
                ...prev,
                {
                  role: "assistant",
                  blocks: [newBlock],
                  timestamp: new Date().toISOString(),
                  _streaming: true,
                },
              ];
            });
            scrollToBottom();
          } else if ((data.type as string) === "tool_result") {
            setLiveMessages((prev) =>
              prev.map((msg) => {
                if (msg.role !== "assistant") return msg;
                const blocks = (msg.blocks ?? []).map((block) => {
                  if (block.type === "tool_call" && block.id === (data.tool_call_id as string)) {
                    return {
                      ...block,
                      result: {
                        tool_call_id: data.tool_call_id as string,
                        content: data.content as string,
                        is_error: data.is_error as boolean,
                      },
                      status: "done" as const,
                    };
                  }
                  return block;
                });
                return { ...msg, blocks };
              }),
            );
          }
        }
      };

      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        buffer += decoder.decode(value, { stream: true });
        const lines = buffer.split("\n");
        buffer = lines.pop() ?? "";
        for (const line of lines) {
          if (line.startsWith("event: ")) currentEvent = line.slice(7).trim();
          else if (line.startsWith("data: ")) currentData = line.slice(6).trim();
          else if (line === "") {
            if (currentEvent) dispatch(currentEvent, currentData);
            currentEvent = "";
            currentData = "";
          }
        }
      }
      setLiveMessages((prev) => {
        const last = prev[prev.length - 1];
        if (last?._streaming) return [...prev.slice(0, -1), { ...last, _streaming: undefined }];
        return prev;
      });
    } catch (e) {
      if ((e as Error).name !== "AbortError") console.error(e);
      setLiveMessages((prev) => {
        const last = prev[prev.length - 1];
        if (last?._streaming) return [...prev.slice(0, -1), { ...last, _streaming: undefined }];
        return prev;
      });
    } finally {
      setIsStreaming(false);
      abortRef.current = null;
    }
  }, [userInput, isStreaming, session, enc, attachments]);

  const copyID = useCallback(async () => {
    if (!session?.id) return;
    await navigator.clipboard.writeText(session.id);
  }, [session]);

  if (!session) {
    return (
      <div className="flex-1 min-w-0 flex flex-col overflow-hidden">
        <div className="flex-shrink-0 h-11 px-5 border-b border-border bg-background flex items-center justify-between">
          <button
            onClick={onToggleLeft}
            className="text-muted-foreground hover:text-foreground transition-colors cursor-pointer"
            title="Toggle sessions"
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
                d="M3.75 3.75v16.5h16.5V3.75H3.75Zm6 0v16.5"
              />
            </svg>
          </button>
          <button
            onClick={onToggleRight}
            className="text-muted-foreground hover:text-foreground transition-colors cursor-pointer"
            title="Toggle workspace"
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
                d="M3.75 3.75v16.5h16.5V3.75H3.75Zm10.5 0v16.5"
              />
            </svg>
          </button>
        </div>
        <div className="flex-1 flex flex-col items-center justify-center gap-2">
          <p className="text-sm text-muted-foreground">Select a session from the sidebar</p>
          <p className="text-xs text-muted-foreground/50 font-mono">
            or create a new one with + New
          </p>
        </div>
      </div>
    );
  }

  return (
    <div className="flex-1 min-w-0 flex flex-col overflow-hidden">
      {/* Header */}
      <div className="flex-shrink-0 h-11 px-5 border-b border-border bg-background flex items-center">
        <div className="flex items-center gap-2 w-full min-w-0">
          <button
            onClick={onToggleLeft}
            className="text-muted-foreground hover:text-foreground transition-colors cursor-pointer shrink-0"
            title="Toggle sessions"
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
                d="M3.75 3.75v16.5h16.5V3.75H3.75Zm6 0v16.5"
              />
            </svg>
          </button>
          <button
            onClick={onBack}
            className="lg:hidden text-xs text-muted-foreground hover:text-foreground cursor-pointer shrink-0"
          >
            ←
          </button>
          <h1 className="flex-1 text-lg font-normal tracking-tight truncate min-w-0">
            {session.title || "Untitled session"}
          </h1>
          <div className="flex items-center gap-1 shrink-0">
            <Button
              variant="ghost"
              size="xs"
              onClick={() => togglePanel("info")}
              className={cn(activePanel === "info" ? "text-primary" : "text-muted-foreground")}
              title="Session details"
            >
              <svg
                className="w-[15px] h-[15px]"
                fill="none"
                viewBox="0 0 24 24"
                strokeWidth="1.8"
                stroke="currentColor"
              >
                <path
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  d="m11.25 11.25.041-.02a.75.75 0 0 1 1.063.852l-.708 2.836a.75.75 0 0 0 1.063.853l.041-.021M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9-3.75h.008v.008H12V8.25Z"
                />
              </svg>
            </Button>
            <Button
              variant="ghost"
              size="xs"
              onClick={() => togglePanel("tools")}
              className={cn(
                "relative",
                activePanel === "tools" ? "text-primary" : "text-muted-foreground",
              )}
              title="Tools"
            >
              <svg
                className="w-[15px] h-[15px]"
                fill="none"
                viewBox="0 0 24 24"
                strokeWidth="1.8"
                stroke="currentColor"
              >
                <path
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  d="M11.42 15.17 17.25 21A2.652 2.652 0 0 0 21 17.25l-5.877-5.877M11.42 15.17l2.496-3.03c.317-.384.74-.626 1.208-.766M11.42 15.17l-4.655 5.653a2.548 2.548 0 1 1-3.586-3.586l6.837-5.63m5.108-.233c.55-.164 1.163-.188 1.743-.14a4.5 4.5 0 0 0 4.486-6.336l-3.276 3.277a3.004 3.004 0 0 1-2.25-2.25l3.276-3.276a4.5 4.5 0 0 0-6.336 4.486c.049.58.025 1.193-.14 1.743"
                />
              </svg>
              {tools.length > 0 && (
                <span className="absolute -top-0.5 -right-0.5 text-[9px] font-mono font-semibold leading-none bg-primary text-primary-foreground rounded-full px-1 py-0.5">
                  {tools.length}
                </span>
              )}
            </Button>
            <Button
              variant="ghost"
              size="xs"
              onClick={() => togglePanel("prompt")}
              className={cn(activePanel === "prompt" ? "text-primary" : "text-muted-foreground")}
              title="System Prompt"
            >
              <svg
                className="w-[15px] h-[15px]"
                fill="none"
                viewBox="0 0 24 24"
                strokeWidth="1.8"
                stroke="currentColor"
              >
                <path
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  d="M19.5 14.25v-2.625a3.375 3.375 0 0 0-3.375-3.375h-1.5A1.125 1.125 0 0 1 13.5 7.125v-1.5a3.375 3.375 0 0 0-3.375-3.375H8.25m0 12.75h7.5m-7.5 3H12M10.5 2.25H5.625c-.621 0-1.125.504-1.125 1.125v17.25c0 .621.504 1.125 1.125 1.125h12.75c.621 0 1.125-.504 1.125-1.125V11.25a9 9 0 0 0-9-9Z"
                />
              </svg>
            </Button>
          </div>
          <button
            onClick={onToggleRight}
            className="text-muted-foreground hover:text-foreground transition-colors cursor-pointer shrink-0"
            title="Toggle workspace"
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
                d="M3.75 3.75v16.5h16.5V3.75H3.75Zm10.5 0v16.5"
              />
            </svg>
          </button>
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
            <div className="flex-shrink-0 px-4 pb-4 pt-2 bg-background border-t border-border">
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
                  "relative rounded-2xl border bg-background transition-colors",
                  isStreaming
                    ? "border-primary/40"
                    : "border-border focus-within:border-primary/60",
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
                    if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
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
                    "w-full px-4 pb-11 text-sm bg-transparent border-0 resize-none focus:outline-none leading-relaxed overflow-y-auto",
                    attachments.length > 0 ? "pt-2" : "pt-3",
                  )}
                  style={{ minHeight: 52, maxHeight: 160 }}
                  rows={1}
                  disabled={isStreaming}
                />
                <div className="absolute bottom-2.5 left-4 right-3 flex items-center justify-between pointer-events-none">
                  {!isStreaming && (
                    <span className="text-[10px] font-mono text-muted-foreground/30 select-none">
                      ⌘↵ to send
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
                        onClick={() => abortRef.current?.abort()}
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
                        className="rounded-xl gap-1.5"
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
        {activePanel && (
          <InspectPanel
            panel={activePanel}
            session={session}
            messages={messages}
            systemPrompt={systemPrompt}
            tools={tools}
            toolsLoading={toolsLoading}
            onClose={() => setActivePanel(null)}
            onCopyID={copyID}
          />
        )}
      </div>
    </div>
  );
}

function InspectPanel({
  panel,
  session,
  messages,
  systemPrompt,
  tools,
  toolsLoading,
  onClose,
  onCopyID,
}: {
  panel: "info" | "tools" | "prompt";
  session: Session;
  messages: Message[];
  systemPrompt: string;
  tools: Tool[];
  toolsLoading: boolean;
  onClose: () => void;
  onCopyID: () => void;
}) {
  const sessionTotalTokens = messages.reduce((sum, m) => sum + (m.token_count ?? 0), 0);
  const title = panel === "info" ? "Session" : panel === "tools" ? "Tools" : "System Prompt";

  return (
    <aside className="flex w-80 shrink-0 flex-col border-l border-border bg-background">
      <div className="h-9 shrink-0 border-b border-border px-3 flex items-center justify-between">
        <span className="text-[9px] font-mono uppercase tracking-wider text-muted-foreground/40">
          {title}
        </span>
        <button
          onClick={onClose}
          className="text-xs text-muted-foreground/50 hover:text-foreground cursor-pointer"
        >
          ×
        </button>
      </div>

      {panel === "info" && (
        <div className="p-3 space-y-3 overflow-auto">
          <dl className="grid grid-cols-[76px_1fr] gap-x-3 gap-y-2 text-xs">
            <dt className="font-mono text-muted-foreground/40">Channel</dt>
            <dd className="truncate">{channelLabel(session.channel) || "unknown"}</dd>
            <dt className="font-mono text-muted-foreground/40">Agent</dt>
            <dd className="truncate">{session.agent_name || session.agent_id || "unknown"}</dd>
            <dt className="font-mono text-muted-foreground/40">Active</dt>
            <dd>{formatTime(session.last_active)}</dd>
            <dt className="font-mono text-muted-foreground/40">Messages</dt>
            <dd>{messages.length.toLocaleString()}</dd>
            <dt className="font-mono text-muted-foreground/40">Tokens</dt>
            <dd>{sessionTotalTokens > 0 ? sessionTotalTokens.toLocaleString() : "—"}</dd>
          </dl>
          <div className="pt-3 border-t border-border">
            <div className="text-[9px] font-mono uppercase tracking-wider text-muted-foreground/40 mb-2">
              Session ID
            </div>
            <button
              onClick={onCopyID}
              className="w-full text-left text-[11px] font-mono text-muted-foreground hover:text-foreground border border-border rounded-lg px-2 py-1.5 truncate cursor-pointer flex items-center gap-2"
              title="Copy session ID"
            >
              <span className="truncate flex-1">{session.id}</span>
              <span className="text-[10px] text-muted-foreground/40 shrink-0">copy</span>
            </button>
          </div>
        </div>
      )}

      {panel === "tools" && (
        <div className="overflow-auto">
          {toolsLoading ? (
            <div className="px-3 py-4 text-xs text-muted-foreground font-mono">Loading tools…</div>
          ) : tools.length === 0 ? (
            <div className="px-3 py-4 text-xs text-muted-foreground font-mono">
              No tools loaded.
            </div>
          ) : (
            <div className="p-2 space-y-2">
              {tools.map((tool) => (
                <ToolRow key={tool.name} tool={tool} compact />
              ))}
            </div>
          )}
        </div>
      )}

      {panel === "prompt" && (
        <div className="p-3 overflow-auto">
          <div className="flex items-center justify-between mb-2">
            <span className="text-[10px] font-mono text-muted-foreground/40">
              ~{Math.round(systemPrompt.length / 4)} tokens
            </span>
            <button
              onClick={() => navigator.clipboard.writeText(systemPrompt).catch(console.error)}
              className="text-[10px] font-mono text-muted-foreground hover:text-foreground cursor-pointer"
            >
              Copy
            </button>
          </div>
          <pre className="text-[10px] font-mono text-muted-foreground/70 whitespace-pre-wrap leading-relaxed bg-muted/50 rounded-lg p-3">
            {systemPrompt || "No system prompt available."}
          </pre>
        </div>
      )}
    </aside>
  );
}

function channelLabel(ch: string | null | undefined): string {
  if (!ch) return "";
  const m = ch.match(/:channel:([^:]+)/);
  return m ? m[1] : ch;
}

function ToolRow({ tool, compact = false }: { tool: Tool; compact?: boolean }) {
  const [expanded, setExpanded] = useState(false);
  return (
    <div>
      <button
        onClick={() => setExpanded((v) => !v)}
        className={cn(
          "w-full text-left flex items-center gap-3 hover:bg-muted transition-colors cursor-pointer",
          compact ? "px-2 py-2 border border-border rounded-lg" : "px-5 py-2.5",
        )}
      >
        <span className="text-[10px] text-muted-foreground">{expanded ? "▾" : "▸"}</span>
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2">
            <span className="text-sm font-mono font-medium">{tool.name}</span>
            <span className="text-[9px] border border-border rounded-full px-1.5 py-0.5">
              {tool.category}
            </span>
          </div>
          <p className="text-[11px] text-muted-foreground mt-0.5 truncate">{tool.description}</p>
        </div>
      </button>
      {expanded && (
        <div className="px-5 pb-3">
          <pre className="text-[10px] font-mono text-muted-foreground/70 bg-muted px-3 py-2 rounded overflow-x-auto">
            {JSON.stringify(tool.input_schema, null, 2)}
          </pre>
        </div>
      )}
    </div>
  );
}

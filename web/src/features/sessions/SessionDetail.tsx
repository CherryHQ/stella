import { useCallback, useEffect, useRef, useState } from "react";
import { useInfiniteQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";
import type { Message, Session, Tool } from "@/lib/types";
import { formatTime } from "@/lib/time";
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
  const [liveMessages, setLiveMessages] = useState<Message[]>([]);
  const [systemPrompt, setSystemPrompt] = useState("");
  const [tools, setTools] = useState<Tool[]>([]);
  const [toolsLoading, setToolsLoading] = useState(false);
  const [activePanel, setActivePanel] = useState<"tools" | "prompt" | null>(null);
  const [userInput, setUserInput] = useState("");
  const [isStreaming, setIsStreaming] = useState(false);

  const abortRef = useRef<AbortController | null>(null);
  const transcriptRef = useRef<HTMLDivElement>(null);
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
    (name: "tools" | "prompt") => {
      setActivePanel((prev) => {
        const next = prev === name ? null : name;
        if (next === "tools" && tools.length === 0) loadTools().catch(console.error);
        return next;
      });
    },
    [tools.length, loadTools],
  );

  const sendMessage = useCallback(async () => {
    if (!userInput.trim() || isStreaming || !session) return;
    const content = userInput.trim();
    setUserInput("");
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
  }, [userInput, isStreaming, session, enc]);

  const copyID = useCallback(async () => {
    if (!session?.id) return;
    await navigator.clipboard.writeText(session.id);
  }, [session]);

  if (!session) {
    return (
      <div className="flex-1 min-w-0 flex flex-col overflow-hidden">
        <div className="flex-1 flex items-center justify-center">
          <p className="text-sm text-muted-foreground">
            Select a session to inspect its transcript.
          </p>
        </div>
      </div>
    );
  }

  const sessionTotalTokens = messages.reduce((sum, m) => sum + (m.token_count ?? 0), 0);

  return (
    <div className="flex-1 min-w-0 flex flex-col overflow-hidden">
      {/* Header */}
      <div className="flex-shrink-0 px-5 py-3.5 border-b border-border bg-background">
        {/* Top row */}
        <div className="flex items-center gap-2 mb-1.5">
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
            <Button
              variant="ghost"
              size="xs"
              onClick={copyID}
              className="text-muted-foreground"
              title="Copy session ID"
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
                  d="M15.75 17.25v3.375c0 .621-.504 1.125-1.125 1.125h-9.75a1.125 1.125 0 0 1-1.125-1.125V7.875c0-.621.504-1.125 1.125-1.125H6.75a9.06 9.06 0 0 1 1.5.124m7.5 10.376h3.375c.621 0 1.125-.504 1.125-1.125V11.25c0-4.46-3.243-8.161-7.5-8.876a9.06 9.06 0 0 0-1.5-.124H9.375c-.621 0-1.125.504-1.125 1.125v3.5m7.5 10.375H9.375a1.125 1.125 0 0 1-1.125-1.125v-9.25m12 6.625v-1.875a3.375 3.375 0 0 0-3.375-3.375h-1.5a1.125 1.125 0 0 1-1.125-1.125v-1.5a3.375 3.375 0 0 0-3.375-3.375H9.75"
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

        {/* Meta row */}
        <div className="flex items-center gap-2 flex-wrap text-xs font-mono text-muted-foreground">
          <span className="text-[10px] border border-border rounded-full px-2 py-0.5">
            {channelLabel(session.channel) || "unknown"}
          </span>
          {(session.agent_name || session.agent_id) && (
            <span className="text-[10px] border border-border rounded-full px-2 py-0.5">
              {session.agent_name || session.agent_id}
            </span>
          )}
          <div className="w-px h-3 bg-border mx-0.5" />
          <span>{formatTime(session.last_active)}</span>
          <span className="text-muted-foreground/30">&middot;</span>
          <span>{messages.length} msgs</span>
          {sessionTotalTokens > 0 && (
            <>
              <span className="text-muted-foreground/30">&middot;</span>
              <span className="text-[10px] text-primary/60">
                {sessionTotalTokens.toLocaleString()} tok
              </span>
            </>
          )}
        </div>

        {/* Tools panel */}
        {activePanel === "tools" && (
          <div className="mt-4 -mx-5 border-t border-border">
            {toolsLoading ? (
              <div className="px-5 py-4 text-xs text-muted-foreground font-mono">
                Loading tools…
              </div>
            ) : (
              <div className="divide-y divide-border">
                {tools.map((tool) => (
                  <ToolRow key={tool.name} tool={tool} />
                ))}
              </div>
            )}
          </div>
        )}

        {/* System prompt panel */}
        {activePanel === "prompt" && systemPrompt && (
          <div className="mt-4 -mx-5 px-5 pt-4 border-t border-border">
            <div className="flex items-center justify-between mb-2">
              <span className="text-[9px] font-mono text-muted-foreground/40 uppercase tracking-wider">
                System Prompt
              </span>
              <span className="text-[10px] font-mono text-muted-foreground/40">
                ~{Math.round(systemPrompt.length / 4)} tokens
              </span>
            </div>
            <div className="max-h-40 overflow-y-auto border-l-2 border-primary/25 pl-3">
              <pre className="text-[10px] font-mono text-muted-foreground/60 whitespace-pre-wrap leading-relaxed">
                {systemPrompt}
              </pre>
            </div>
          </div>
        )}
      </div>

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
          <div
            className={cn(
              "relative rounded-2xl border bg-background transition-colors shadow-sm",
              isStreaming ? "border-primary/40" : "border-border focus-within:border-primary/60",
            )}
          >
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
              placeholder="Message…"
              className="w-full px-4 pt-3 pb-11 text-sm bg-transparent border-0 resize-none focus:outline-none leading-relaxed overflow-y-auto"
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
                    disabled={!userInput.trim()}
                    onClick={() => sendMessage().catch(console.error)}
                    className="rounded-xl gap-1.5"
                  >
                    <svg className="w-4 h-4" viewBox="0 0 24 24" fill="currentColor">
                      <path d="M3.478 2.405a.75.75 0 0 0-.926.94l2.432 7.905H13.5a.75.75 0 0 1 0 1.5H4.984l-2.432 7.905a.75.75 0 0 0 .926.94 60.519 60.519 0 0 0 18.445-8.986.75.75 0 0 0 0-1.218A60.517 60.517 0 0 0 3.478 2.405Z" />
                    </svg>
                    Send
                  </Button>
                )}
              </div>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

function channelLabel(ch: string | null | undefined): string {
  if (!ch) return "";
  const m = ch.match(/:channel:([^:]+)/);
  return m ? m[1] : ch;
}

function ToolRow({ tool }: { tool: Tool }) {
  const [expanded, setExpanded] = useState(false);
  return (
    <div>
      <button
        onClick={() => setExpanded((v) => !v)}
        className="w-full text-left flex items-center gap-3 px-5 py-2.5 hover:bg-muted transition-colors cursor-pointer"
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

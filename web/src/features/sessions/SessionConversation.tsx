import { useCallback, useEffect, useRef, useState } from "react";
import { useInfiniteQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";
import type { Message } from "@/lib/types";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogHeader,
  DialogPanel,
  DialogPopup,
  DialogTitle,
} from "@/components/ui/dialog";
import { Transcript } from "./Transcript";

interface Props {
  sessionId: string;
  placeholder?: string;
  className?: string;
  bodyClassName?: string;
}

export function SessionConversation({
  sessionId,
  placeholder = "Ask Stella about this…",
  className = "",
  bodyClassName = "h-[28rem]",
}: Props) {
  const [input, setInput] = useState("");
  const [liveMessages, setLiveMessages] = useState<Message[]>([]);
  const [isStreaming, setIsStreaming] = useState(false);
  const [mobileOpen, setMobileOpen] = useState(false);
  const transcriptRef = useRef<HTMLDivElement>(null);
  const abortRef = useRef<AbortController | null>(null);
  const initialScrollSessionRef = useRef<string | null>(null);
  const enc = encodeURIComponent(sessionId);

  const messagesQuery = useInfiniteQuery({
    queryKey: ["session-messages", sessionId],
    initialPageParam: 0,
    queryFn: ({ pageParam }) =>
      api<Message[]>("GET", `/api/sessions/${enc}/messages?limit=20&skip=${pageParam}`),
    getNextPageParam: (lastPage, allPages) =>
      lastPage.length === 20 ? allPages.reduce((sum, page) => sum + page.length, 0) : undefined,
  });

  const messages = [...(messagesQuery.data?.pages ?? [])].reverse().flat().concat(liveMessages);

  useEffect(() => {
    initialScrollSessionRef.current = null;
    setLiveMessages([]);
  }, [sessionId]);

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

  const scrollToBottom = useCallback(() => {
    if (!transcriptRef.current) return;
    const el = transcriptRef.current;
    if (el.scrollHeight - el.scrollTop - el.clientHeight < 200) {
      el.scrollTop = el.scrollHeight;
    }
  }, []);

  const sendMessage = useCallback(async () => {
    const content = input.trim();
    if (!content || isStreaming) return;

    setInput("");
    setLiveMessages((prev) => [
      ...prev,
      { role: "user", content, timestamp: new Date().toISOString() },
    ]);
    setIsStreaming(true);
    abortRef.current = new AbortController();

    setTimeout(() => {
      if (transcriptRef.current) {
        transcriptRef.current.scrollTop = transcriptRef.current.scrollHeight;
      }
    }, 0);

    try {
      const res = await fetch(`/api/sessions/${enc}/messages`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ content }),
        signal: abortRef.current.signal,
      });
      if (!res.ok) throw new Error((await res.text()) || res.statusText);
      if (!res.body) return;

      const reader = res.body.getReader();
      const decoder = new TextDecoder();
      let buffer = "";
      let currentEvent = "";
      let currentData = "";

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
    } catch (e) {
      if ((e as Error).name !== "AbortError") console.error(e);
    } finally {
      setLiveMessages((prev) => {
        const last = prev[prev.length - 1];
        if (last?._streaming) return [...prev.slice(0, -1), { ...last, _streaming: undefined }];
        return prev;
      });
      setIsStreaming(false);
      abortRef.current = null;
    }
  }, [enc, input, isStreaming, scrollToBottom]);

  const renderBody = () => (
    <div className="flex h-full min-h-0 flex-col">
      <Transcript
        ref={transcriptRef}
        messages={messages}
        messagesLoading={messagesQuery.isLoading || messagesQuery.isFetchingNextPage}
        onScroll={() => void loadOlderMessages()}
      />
      <div className="flex flex-col gap-2 border-t border-border p-2 sm:flex-row sm:p-3">
        <input
          value={input}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !e.shiftKey) {
              e.preventDefault();
              void sendMessage();
            }
          }}
          placeholder={placeholder}
          className="min-w-0 flex-1 rounded-md border border-input bg-background px-3 py-2 text-sm outline-hidden focus:ring-2 focus:ring-ring"
        />
        <Button
          size="sm"
          className="w-full sm:w-auto"
          loading={isStreaming}
          disabled={!input.trim()}
          onClick={sendMessage}
        >
          Continue
        </Button>
      </div>
    </div>
  );

  return (
    <>
      <div className="overflow-hidden rounded-2xl border border-border bg-background sm:hidden">
        <div className="flex items-center justify-between gap-3 px-3 py-3">
          <div className="min-w-0">
            <div className="text-sm font-semibold">Conversation</div>
            <div className="truncate font-mono text-[10px] text-muted-foreground">{sessionId}</div>
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
            <div className="text-sm font-semibold">Conversation</div>
            <div className="truncate font-mono text-[10px] text-muted-foreground">{sessionId}</div>
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
            <DialogTitle>Conversation</DialogTitle>
            <div className="truncate font-mono text-[10px] text-muted-foreground">{sessionId}</div>
          </DialogHeader>
          <DialogPanel className="flex min-h-0 flex-1 flex-col p-0" scrollFade={false}>
            {renderBody()}
          </DialogPanel>
        </DialogPopup>
      </Dialog>
    </>
  );
}

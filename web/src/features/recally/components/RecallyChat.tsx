import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQuery, useMutation } from "@tanstack/react-query";
import { useChat } from "@ai-sdk/react";
import { MessageCircle, RotateCcw, Sparkles, Loader2, ArrowUp } from "lucide-react";
import { useI18n } from "@/lib/i18n";
import { getSessionMessages, createSession } from "@/lib/api-client/sdk.gen";
import { getArticleOptions } from "@/lib/api-client/@tanstack/react-query.gen";
import { agentsQueryOptions } from "@/lib/queries/agents";
import { cn } from "@/lib/utils";
import { MarkdownPreview } from "@/components/MarkdownPreview";
import { ChatErrorNotice } from "@/components/chat/ChatErrorNotice";
import { Button } from "@/components/ui/button";
import {
  createSessionTransport,
  mergeToolResults,
  messageToUIMessage,
  sessionMessagesToMessages,
  uiMessageToMessage,
} from "@/lib/chat-transport";

interface Props {
  articleId: string;
  onClose?: () => void;
}

export function RecallyChat({ articleId, onClose }: Props) {
  const { t } = useI18n();
  const [userInput, setUserInput] = useState("");
  const [sessionLoading, setSessionLoading] = useState(false);
  const [activeSessionId, setActiveSessionId] = useState<string | null>(null);
  const [activeAgentId, setActiveAgentId] = useState<string | null>(null);

  const messagesEndRef = useRef<HTMLDivElement>(null);
  const chatContainerRef = useRef<HTMLDivElement>(null);

  // Fetch article for context
  const articleQuery = useQuery({
    ...getArticleOptions({
      path: { id: articleId },
      query: { include: "content" },
    }),
    enabled: !!articleId,
  });
  const article = articleQuery.data;

  // 1. Fetch available agents to run the session
  const { data: agents = [] } = useQuery(agentsQueryOptions);
  const selectedAgent = useMemo(() => agents[0], [agents]);

  // Load session from localStorage on mount or articleId change
  useEffect(() => {
    const cached = localStorage.getItem(`recally-session-${articleId}`);
    if (cached) {
      try {
        const { sessionId, agentId } = JSON.parse(cached) as { sessionId: string; agentId: string };
        setActiveSessionId(sessionId);
        setActiveAgentId(agentId);
      } catch {
        localStorage.removeItem(`recally-session-${articleId}`);
      }
    } else {
      setActiveSessionId(null);
      setActiveAgentId(null);
    }
  }, [articleId]);

  // Create session mutation
  const createSessionMut = useMutation({
    mutationFn: async () => {
      if (!selectedAgent?.id) throw new Error("No agent selected");
      setSessionLoading(true);
      const { data } = await createSession({
        path: { agentId: selectedAgent.id },
        body: { kind: "chat" },
        throwOnError: true,
      });
      return data;
    },
    onSuccess: (data) => {
      if (data.id && data.agent_id) {
        setActiveSessionId(data.id);
        setActiveAgentId(data.agent_id);
        localStorage.setItem(
          `recally-session-${articleId}`,
          JSON.stringify({ sessionId: data.id, agentId: data.agent_id }),
        );
      }
    },
    onSettled: () => {
      setSessionLoading(false);
    },
  });

  // 3. Auto-create session if it doesn't exist in cache
  useEffect(() => {
    if (
      !activeSessionId &&
      selectedAgent?.id &&
      article &&
      !createSessionMut.isPending &&
      !sessionLoading
    ) {
      createSessionMut.mutate();
    }
  }, [activeSessionId, selectedAgent?.id, article, createSessionMut.isPending, sessionLoading]);

  // Reset Session
  const handleResetSession = useCallback(() => {
    if (!selectedAgent?.id) return;
    localStorage.removeItem(`recally-session-${articleId}`);
    setActiveSessionId(null);
    setActiveAgentId(null);
    createSessionMut.mutate();
  }, [selectedAgent?.id, articleId]);

  // 4. Hook up Vercel AI SDK useChat
  const transport = useMemo(() => {
    if (!activeAgentId || !activeSessionId) return undefined;
    return createSessionTransport(activeAgentId, activeSessionId);
  }, [activeAgentId, activeSessionId]);

  const {
    messages: chatMessages,
    sendMessage: chatSendMessage,
    setMessages: setChatMessages,
    status: chatStatus,
    error: chatError,
  } = useChat({
    id: activeSessionId ?? "recally-chat-empty",
    transport,
    // Batch SSE deltas: without this every token re-renders the transcript.
    experimental_throttle: 50,
    onError: (err) => console.error("[recally chat]", err),
  });

  const isStreaming = chatStatus === "streaming" || chatStatus === "submitted";

  // Fetch messages from backend for history
  const { data: historicalMessages, isLoading: historyLoading } = useQuery({
    queryKey: ["session-messages-history", activeAgentId, activeSessionId],
    queryFn: async () => {
      if (!activeAgentId || !activeSessionId) return [];
      const { data } = await getSessionMessages({
        path: { agentId: activeAgentId, sessionId: activeSessionId },
        query: { limit: 50, skip: 0 },
        throwOnError: true,
      });
      return sessionMessagesToMessages(data?.messages);
    },
    enabled: !!activeAgentId && !!activeSessionId,
  });

  // Sync history to chat
  useEffect(() => {
    if (!historicalMessages) return;
    const merged = mergeToolResults(historicalMessages).reverse();
    const uiMessages = merged.map(messageToUIMessage);
    setChatMessages(uiMessages);
  }, [historicalMessages, setChatMessages]);

  const messages = useMemo(() => chatMessages.map(uiMessageToMessage), [chatMessages]);

  // Auto-scroll to bottom
  const scrollToBottom = () => {
    messagesEndRef.current?.scrollIntoView({ behavior: "smooth" });
  };

  useEffect(() => {
    scrollToBottom();
  }, [messages]);

  // Prepend article context to the first prompt
  const sendMessage = useCallback(async () => {
    if (!userInput.trim() || isStreaming || !activeSessionId || !article) return;

    let finalPrompt = userInput.trim();
    // If it's the first message, embed article context
    if (messages.length === 0) {
      finalPrompt = `[Context Article: "${article.title}" by ${article.author || "Unknown"}]\n\nSummary:\n${article.summary || "No summary available"}\n\nUser Question:\n${userInput.trim()}`;
    }

    setUserInput("");
    void chatSendMessage({ text: finalPrompt });
  }, [userInput, isStreaming, activeSessionId, messages.length, article]);

  if (articleQuery.isLoading || sessionLoading || !article || !activeSessionId || historyLoading) {
    return (
      <div className="flex h-full w-[340px] flex-col border-l border-border bg-card items-center justify-center gap-2 text-xs text-muted-foreground font-mono">
        <Loader2 className="size-4 animate-spin text-primary" />
        <span>{t("recally.chat.resolvingSession")}</span>
      </div>
    );
  }

  return (
    <div className="flex h-full w-[340px] flex-col border-l border-border bg-card">
      {/* Header */}
      <div className="flex h-11 shrink-0 items-center justify-between border-b border-border px-3.5">
        <div className="flex items-center gap-1.5">
          <Sparkles className="size-3.5 text-primary" />
          <span className="text-xs font-semibold text-foreground">{t("recally.chat.title")}</span>
        </div>
        <div className="flex items-center gap-1.5">
          <Button
            variant="ghost"
            size="icon-xs"
            onClick={handleResetSession}
            disabled={isStreaming}
            className="text-muted-foreground hover:text-foreground"
            title={t("recally.chat.reset")}
          >
            <RotateCcw className="size-3.5" />
          </Button>
          {onClose && (
            <Button
              variant="ghost"
              size="icon-xs"
              onClick={onClose}
              className="text-muted-foreground hover:text-foreground"
            >
              <span className="text-xs font-semibold font-mono">×</span>
            </Button>
          )}
        </div>
      </div>

      {/* Chat Messages */}
      <div ref={chatContainerRef} className="flex-1 overflow-y-auto px-4 py-4 space-y-4">
        {messages.length === 0 ? (
          <div className="flex flex-col items-center justify-center h-64 text-center px-4 space-y-3">
            <div className="size-8 rounded-full bg-primary/10 flex items-center justify-center text-primary">
              <MessageCircle className="size-4.5" />
            </div>
            <div>
              <p className="text-xs font-semibold text-foreground">
                {t("recally.chat.discussArticle")}
              </p>
              <p className="text-xs text-muted-foreground mt-1 max-w-44 leading-normal">
                {t("recally.chat.discussDesc")}
              </p>
            </div>
          </div>
        ) : (
          messages.map((msg, idx) => {
            const isUser = msg.role === "user";
            // Strip out context headers from display if it was prepended
            let text = msg.content ?? "";
            if (idx === 0 && isUser && text.startsWith("[Context Article:")) {
              const questionIdx = text.indexOf("User Question:\n");
              if (questionIdx !== -1) {
                text = text.substring(questionIdx + "User Question:\n".length);
              }
            }

            return (
              <div
                key={idx}
                className={cn(
                  "flex flex-col max-w-full gap-2",
                  isUser
                    ? "pt-4 border-t border-border/40 mt-4 first:pt-0 first:border-0 first:mt-0"
                    : "pb-2",
                )}
              >
                {isUser ? (
                  <h3 className="text-xs font-semibold tracking-tight text-foreground leading-tight">
                    {text}
                  </h3>
                ) : (
                  <div className="text-xs leading-relaxed text-foreground font-sans">
                    <MarkdownPreview
                      content={text}
                      className="[&_code]:text-xs [&_pre]:bg-muted/40 [&_pre]:p-1.5 text-xs"
                    />
                  </div>
                )}
              </div>
            );
          })
        )}

        {isStreaming && (
          <div className="flex items-center gap-1.5 text-xs font-mono text-primary/80 animate-pulse pl-1">
            <Loader2 className="size-3 animate-spin text-primary" />
            <span>{t("recally.chat.typing")}</span>
          </div>
        )}
        <div ref={messagesEndRef} />
      </div>

      <ChatErrorNotice error={chatError} className="max-w-full px-4 pb-2 sm:px-4" />

      {/* Input Form */}
      <div className="shrink-0 border-t border-border bg-card p-3">
        <div className="relative flex items-center bg-card border border-border rounded-xl px-2.5 py-1 focus-within:border-primary focus-within:ring-1 focus-within:ring-primary transition-all">
          <textarea
            value={userInput}
            onChange={(e) => setUserInput(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !e.shiftKey) {
                e.preventDefault();
                void sendMessage();
              }
            }}
            placeholder={t("recally.chat.placeholder")}
            className="flex-1 resize-none bg-transparent py-1.5 text-xs text-foreground focus:outline-none placeholder:text-muted-foreground/45 max-h-20 min-h-7 leading-relaxed font-sans"
            rows={1}
            disabled={isStreaming}
          />
          <Button
            type="button"
            size="icon-xs"
            onClick={sendMessage}
            disabled={!userInput.trim() || isStreaming}
            className="shrink-0"
          >
            <ArrowUp className="size-3.5 stroke-[2.5]" />
          </Button>
        </div>
      </div>
    </div>
  );
}

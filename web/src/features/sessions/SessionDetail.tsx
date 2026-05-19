import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useInfiniteQuery } from "@tanstack/react-query";
import { useChat } from "@ai-sdk/react";
import { useI18n } from "@/lib/i18n";
import { api } from "@/lib/api";
import { formatTime } from "@/lib/time";
import type { Message, Session, Skill, Tool } from "@/lib/types";
import { cn } from "@/lib/utils";
import {
  createSessionTransport,
  mergeToolResults,
  messageToUIMessage,
  uiMessageToMessage,
} from "@/lib/chat-transport";
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
  const [systemPrompt, setSystemPrompt] = useState("");
  const [tools, setTools] = useState<Tool[]>([]);
  const [toolsLoading, setToolsLoading] = useState(false);
  const [skills, setSkills] = useState<Skill[]>([]);
  const [skillsLoading, setSkillsLoading] = useState(false);
  const [inspectOpen, setInspectOpen] = useState(false);
  const [inspectTab, setInspectTab] = useState<"session" | "tools" | "prompt" | "skills">(
    "session",
  );
  const [userInput, setUserInput] = useState("");
  const [attachments, setAttachments] = useState<
    { name: string; path: string; uploading: boolean; error?: boolean }[]
  >([]);

  const transcriptRef = useRef<HTMLDivElement>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const sessionIDRef = useRef<string | null>(null);
  const initialScrollSessionRef = useRef<string | null>(null);

  const enc = session ? encodeURIComponent(session.id) : "";

  const transport = useMemo(
    () => (session ? createSessionTransport(session.id) : undefined),
    [session?.id],
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
    queryFn: ({ pageParam }) =>
      api<Message[]>("GET", `/api/sessions/${enc}/messages?limit=20&skip=${pageParam}`),
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
      setSystemPrompt("");
      setInspectOpen(false);
      setAttachments([]);
      return;
    }
    sessionIDRef.current = session.id;
    initialScrollSessionRef.current = null;
    setChatMessages([]);
    setSkills([]);

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

  const loadSkills = useCallback(async () => {
    if (!session) return;
    setSkillsLoading(true);
    try {
      const all = (await api<Skill[]>("GET", "/api/skills")) ?? [];
      setSkills(
        all.filter(
          (skill) =>
            skill.status !== "deprecated" &&
            !skill.disable_model_invocation &&
            (skill.scope === "system" ||
              (skill.scope === "agent" && skill.agent_id === session.agent_id) ||
              (skill.scope === "user" && skill.user_id === session.user_id)),
        ),
      );
    } finally {
      setSkillsLoading(false);
    }
  }, [session]);

  const loadInspectTab = useCallback(
    (tab: "session" | "tools" | "prompt" | "skills") => {
      if (tab === "tools" && tools.length === 0) loadTools().catch(console.error);
      if (tab === "skills" && skills.length === 0) loadSkills().catch(console.error);
    },
    [tools.length, skills.length, loadTools, loadSkills],
  );

  const toggleInspect = useCallback(() => {
    setInspectOpen((prev) => {
      const next = !prev;
      if (next) loadInspectTab(inspectTab);
      return next;
    });
  }, [inspectTab, loadInspectTab]);

  const selectInspectTab = useCallback(
    (tab: "session" | "tools" | "prompt" | "skills") => {
      setInspectTab(tab);
      loadInspectTab(tab);
    },
    [loadInspectTab],
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
          setAttachments((prev) =>
            prev.map((a) => (a === placeholder ? { ...a, uploading: false, error: true } : a)),
          );
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
    const text = parts.join("\n");

    setUserInput("");
    setAttachments([]);
    setTimeout(() => {
      if (transcriptRef.current)
        transcriptRef.current.scrollTop = transcriptRef.current.scrollHeight;
    }, 0);

    void chatSendMessage({ text });
  }, [userInput, isStreaming, session, attachments, chatSendMessage]);

  const copyID = useCallback(async () => {
    if (!session?.id) return;
    await navigator.clipboard.writeText(session.id);
  }, [session]);

  if (!session) {
    return (
      <div className="flex-1 min-w-0 flex flex-col overflow-hidden bg-background">
        <div className="flex-shrink-0 h-[52px] px-6 border-b border-accent flex items-center justify-between">
          <button
            onClick={onToggleLeft}
            className="text-muted-foreground hover:text-foreground transition-colors cursor-pointer"
            title="Toggle sessions"
            aria-label="Toggle sidebar"
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
            aria-label="Toggle workspace"
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
        <div className="flex-1 flex flex-col items-center justify-center gap-2 bg-secondary">
          <p className="text-lg font-semibold tracking-tight text-foreground">
            Pick a conversation
          </p>
          <p className="text-sm text-muted-foreground">or start something new</p>
        </div>
      </div>
    );
  }

  return (
    <div className="flex-1 min-w-0 flex flex-col overflow-hidden bg-background">
      {/* Header */}
      <div className="flex-shrink-0 h-[52px] px-6 border-b border-accent flex items-center">
        <div className="flex items-center gap-3 w-full min-w-0">
          <button
            onClick={onToggleLeft}
            className="text-muted-foreground hover:text-foreground transition-colors cursor-pointer shrink-0"
            title="Toggle sessions"
            aria-label="Toggle sidebar"
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
            className="lg:hidden text-sm text-muted-foreground hover:text-foreground cursor-pointer shrink-0"
            aria-label="Go back"
          >
            ←
          </button>
          <div className="flex items-center gap-3 flex-1 min-w-0">
            <h1 className="font-semibold tracking-tight truncate min-w-0">
              {session.title || "Untitled session"}
            </h1>
            {session.agent_name && (
              <span className="text-xs text-muted-foreground shrink-0">{session.agent_name}</span>
            )}
          </div>
          <div className="flex items-center gap-1 shrink-0">
            <button
              onClick={toggleInspect}
              className={cn(
                "w-8 h-8 rounded-lg flex items-center justify-center transition-colors cursor-pointer",
                inspectOpen
                  ? "text-primary"
                  : "text-muted-foreground hover:text-foreground hover:bg-secondary",
              )}
              title="Details"
              aria-label="Toggle details"
            >
              <svg
                className="w-4 h-4"
                fill="none"
                viewBox="0 0 24 24"
                strokeWidth="1.8"
                stroke="currentColor"
              >
                <circle cx="12" cy="12" r="10" />
                <path strokeLinecap="round" d="M12 16v-4M12 8h.01" />
              </svg>
            </button>
            <button
              onClick={onToggleRight}
              className="w-8 h-8 rounded-lg flex items-center justify-center text-muted-foreground hover:text-foreground hover:bg-secondary transition-colors cursor-pointer"
              title="Toggle workspace"
              aria-label="Toggle workspace"
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

          {/* Composer */}
          {session.user_id === currentUserID && (
            <div className="flex-shrink-0 px-6 pb-5 pt-3 bg-background">
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
              <div className="mx-auto max-w-[720px] relative">
                <div
                  className={cn(
                    "flex items-end bg-secondary rounded-[18px] border transition-colors duration-150",
                    isStreaming ? "border-primary/40" : "border-border focus-within:border-primary",
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
                  <div className="flex-1 min-w-0">
                    {attachments.length > 0 && (
                      <div className="flex flex-wrap gap-1.5 px-5 pt-3">
                        {attachments.map((a, i) => (
                          <span
                            key={i}
                            className={cn(
                              "inline-flex items-center gap-1.5 text-xs font-mono rounded-lg px-2.5 py-1 max-w-48 border",
                              a.error
                                ? "bg-destructive/5 text-destructive border-destructive/20"
                                : a.uploading
                                  ? "bg-muted/50 text-muted-foreground/50 border-border"
                                  : "bg-primary/5 text-primary border-primary/20",
                            )}
                          >
                            {a.uploading ? (
                              <div className="w-3 h-3 border border-muted-foreground/30 border-t-muted-foreground rounded-full animate-spin shrink-0" />
                            ) : a.error ? (
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
                                  d="M12 9v3.75m9-.75a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9 3.75h.008v.008H12v-.008Z"
                                />
                              </svg>
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
                            <span className="truncate">{a.error ? "Upload failed" : a.name}</span>
                            {!a.uploading && (
                              <button
                                onClick={() => removeAttachment(i)}
                                className="text-muted-foreground/50 hover:text-foreground cursor-pointer shrink-0"
                                aria-label="Remove attachment"
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
                        "w-full pl-5 pr-2 bg-transparent border-0 resize-none focus:outline-none leading-relaxed overflow-y-auto",
                        attachments.length > 0 ? "pt-2 pb-3" : "py-3",
                      )}
                      style={{ minHeight: 25, maxHeight: 160 }}
                      rows={1}
                      disabled={isStreaming}
                    />
                  </div>
                  <div className="flex items-center gap-1 p-2 shrink-0">
                    {!isStreaming && (
                      <button
                        onClick={() => fileInputRef.current?.click()}
                        className="w-[34px] h-[34px] rounded-full flex items-center justify-center text-muted-foreground hover:text-foreground cursor-pointer transition-colors active:scale-95"
                        title="Attach file"
                        aria-label="Attach file"
                      >
                        <svg
                          className="w-[18px] h-[18px]"
                          fill="none"
                          viewBox="0 0 24 24"
                          strokeWidth="1.8"
                          stroke="currentColor"
                        >
                          <path
                            strokeLinecap="round"
                            d="m21.44 11.05-9.19 9.19a6 6 0 0 1-8.49-8.49l9.19-9.19a4 4 0 0 1 5.66 5.66l-9.2 9.19a2 2 0 0 1-2.83-2.83l8.49-8.48"
                          />
                        </svg>
                      </button>
                    )}
                    {isStreaming ? (
                      <button
                        onClick={() => chatStop()}
                        className="h-[34px] px-3 rounded-full flex items-center justify-center text-destructive text-sm cursor-pointer hover:bg-destructive/10 transition-colors active:scale-95"
                      >
                        Stop
                      </button>
                    ) : (
                      <button
                        disabled={
                          (!userInput.trim() && attachments.length === 0) ||
                          attachments.some((a) => a.uploading)
                        }
                        onClick={() => sendMessage().catch(console.error)}
                        className="w-[34px] h-[34px] rounded-full flex items-center justify-center bg-primary text-primary-foreground cursor-pointer hover:bg-[#0071e3] disabled:bg-border disabled:text-muted-foreground disabled:cursor-default transition-colors active:scale-95"
                        title="Send"
                        aria-label="Send message"
                      >
                        <svg className="w-4 h-4" viewBox="0 0 24 24" fill="currentColor">
                          <path d="M3.478 2.405a.75.75 0 0 0-.926.94l2.432 7.905H13.5a.75.75 0 0 1 0 1.5H4.984l-2.432 7.905a.75.75 0 0 0 .926.94 60.519 60.519 0 0 0 18.445-8.986.75.75 0 0 0 0-1.218A60.517 60.517 0 0 0 3.478 2.405Z" />
                        </svg>
                      </button>
                    )}
                  </div>
                </div>
                {!isStreaming && (
                  <span className="absolute -bottom-5 left-5 text-[11px] text-muted-foreground/40 select-none">
                    ↵ send · ⇧↵ new line
                  </span>
                )}
                {isStreaming && (
                  <span className="absolute -bottom-5 left-5 text-[11px] text-primary/50 select-none">
                    generating…
                  </span>
                )}
              </div>
            </div>
          )}
        </div>
        {inspectOpen && (
          <InspectPanel
            tab={inspectTab}
            session={session}
            messages={messages}
            systemPrompt={systemPrompt}
            tools={tools}
            toolsLoading={toolsLoading}
            skills={skills}
            skillsLoading={skillsLoading}
            onTabChange={selectInspectTab}
            onClose={() => setInspectOpen(false)}
            onCopyID={copyID}
          />
        )}
      </div>
    </div>
  );
}

function InspectPanel({
  tab,
  session,
  messages,
  systemPrompt,
  tools,
  toolsLoading,
  skills,
  skillsLoading,
  onTabChange,
  onClose,
  onCopyID,
}: {
  tab: "session" | "tools" | "prompt" | "skills";
  onTabChange: (tab: "session" | "tools" | "prompt" | "skills") => void;
  session: Session;
  messages: Message[];
  systemPrompt: string;
  tools: Tool[];
  toolsLoading: boolean;
  skills: Skill[];
  skillsLoading: boolean;
  onClose: () => void;
  onCopyID: () => void;
}) {
  const sessionTotalTokens = messages.reduce((sum, m) => sum + (m.token_count ?? 0), 0);
  return (
    <aside className="flex w-[300px] shrink-0 flex-col border-l border-accent bg-secondary">
      <div className="h-[52px] shrink-0 px-5 flex items-center justify-between">
        <span className="text-sm font-semibold tracking-tight text-foreground">Details</span>
        <button
          onClick={onClose}
          className="w-7 h-7 rounded-lg flex items-center justify-center text-muted-foreground hover:text-foreground hover:bg-accent cursor-pointer text-lg leading-none"
          aria-label="Close details"
        >
          ×
        </button>
      </div>

      <div className="shrink-0 px-3 pb-3 flex gap-1">
        {(["session", "tools", "prompt", "skills"] as const).map((item) => (
          <button
            key={item}
            onClick={() => onTabChange(item)}
            className={cn(
              "flex-1 py-1.5 rounded-lg text-[13px] capitalize cursor-pointer transition-colors",
              tab === item
                ? "bg-background text-foreground font-medium"
                : "text-muted-foreground hover:text-foreground hover:bg-accent",
            )}
          >
            {item}
          </button>
        ))}
      </div>

      <div className="flex-1 overflow-auto">
        {tab === "session" && (
          <div className="px-5 py-4">
            <div className="grid grid-cols-[80px_1fr] gap-x-3 gap-y-3.5 text-sm">
              <span className="text-muted-foreground">Agent</span>
              <span className="truncate">
                {session.agent_name || session.agent_id || "unknown"}
              </span>

              <span className="text-muted-foreground">Channel</span>
              <span className="truncate">{channelLabel(session.channel) || "unknown"}</span>

              <span className="text-muted-foreground">Active</span>
              <span>{formatTime(session.last_active)}</span>

              <span className="text-muted-foreground">Messages</span>
              <span>{messages.length.toLocaleString()}</span>

              <span className="text-muted-foreground">Tokens</span>
              <span>{sessionTotalTokens > 0 ? sessionTotalTokens.toLocaleString() : "—"}</span>
            </div>

            <div className="mt-5 pt-4 border-t border-accent">
              <div className="text-xs text-muted-foreground mb-2">Session ID</div>
              <button
                onClick={onCopyID}
                className="w-full text-left font-mono text-xs text-muted-foreground hover:border-border border border-accent bg-background rounded-[11px] px-3 py-2.5 truncate cursor-pointer flex items-center gap-2 transition-colors"
                title="Copy session ID"
              >
                <span className="truncate flex-1">{session.id}</span>
                <span className="text-xs text-primary shrink-0">Copy</span>
              </button>
            </div>
          </div>
        )}

        {tab === "tools" && (
          <div>
            {toolsLoading ? (
              <div className="px-5 py-4 text-sm text-muted-foreground">Loading tools…</div>
            ) : tools.length === 0 ? (
              <div className="px-5 py-4 text-sm text-muted-foreground">No tools available.</div>
            ) : (
              <div className="p-2 space-y-1.5">
                {tools.map((tool) => (
                  <ToolRow key={tool.name} tool={tool} />
                ))}
              </div>
            )}
          </div>
        )}

        {tab === "skills" && (
          <div>
            {skillsLoading ? (
              <div className="px-5 py-4 text-sm text-muted-foreground">Loading skills…</div>
            ) : skills.length === 0 ? (
              <div className="px-5 py-4 text-sm text-muted-foreground">
                No skills available for this session.
              </div>
            ) : (
              <div className="p-2 space-y-1.5">
                {skills.map((skill) => (
                  <SkillRow key={skill.id} skill={skill} />
                ))}
              </div>
            )}
          </div>
        )}

        {tab === "prompt" && (
          <div className="px-5 py-4">
            <div className="flex items-center justify-between mb-3">
              <span className="text-xs text-muted-foreground">
                ~{Math.round(systemPrompt.length / 4)} tokens
              </span>
              <button
                onClick={() => navigator.clipboard.writeText(systemPrompt).catch(console.error)}
                className="text-xs text-primary hover:text-foreground cursor-pointer"
              >
                Copy
              </button>
            </div>
            <pre className="text-[12px] font-mono text-muted-foreground/70 whitespace-pre-wrap leading-relaxed bg-background rounded-[11px] p-3.5">
              {systemPrompt || "No system prompt available."}
            </pre>
          </div>
        )}
      </div>
    </aside>
  );
}

function SkillRow({ skill }: { skill: Skill }) {
  return (
    <div className="bg-background rounded-[11px] px-3 py-2.5">
      <div className="flex items-center gap-2 min-w-0">
        <span className="text-sm font-medium truncate">{skill.name || skill.id}</span>
        <span className="text-[10px] text-muted-foreground border border-border rounded-full px-1.5 py-0.5 shrink-0">
          {skill.scope}
        </span>
        {skill.status === "active" && (
          <span className="text-[10px] text-primary bg-primary/10 rounded-full px-1.5 py-0.5 shrink-0">
            active
          </span>
        )}
      </div>
      {skill.description && (
        <p className="text-xs text-muted-foreground mt-1 leading-relaxed">{skill.description}</p>
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
    <div className="bg-background rounded-[11px] overflow-hidden">
      <button
        onClick={() => setExpanded((v) => !v)}
        className="w-full text-left flex items-center gap-2.5 px-3 py-2.5 hover:bg-accent transition-colors cursor-pointer"
      >
        <span className="text-[10px] text-muted-foreground w-3 text-center shrink-0">
          {expanded ? "▾" : "▸"}
        </span>
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2">
            <span className="text-sm font-medium">{tool.name}</span>
            <span className="text-[10px] text-muted-foreground border border-border rounded-full px-1.5 py-0.5">
              {tool.category}
            </span>
          </div>
          <p className="text-xs text-muted-foreground mt-0.5 truncate">{tool.description}</p>
        </div>
      </button>
      {expanded && (
        <div className="px-3 pb-3">
          <pre className="text-[11px] font-mono text-muted-foreground/70 bg-secondary px-3 py-2 rounded-lg overflow-x-auto">
            {JSON.stringify(tool.input_schema, null, 2)}
          </pre>
        </div>
      )}
    </div>
  );
}

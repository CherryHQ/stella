import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useInfiniteQuery } from "@tanstack/react-query";
import { useChat } from "@ai-sdk/react";
import { useI18n } from "@/lib/i18n";
import { api } from "@/lib/api";
import { formatTime } from "@/lib/time";
import type { Message, Session, Skill, Tool } from "@/lib/types";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import {
  createSessionTransport,
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
    { name: string; path: string; uploading: boolean }[]
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
    const historical = [...messagesQuery.data.pages].reverse().flat();
    if (historical.length === 0) return;
    const uiMessages = historical.map(messageToUIMessage);
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
      <div className="flex-1 min-w-0 flex flex-col overflow-hidden">
        <div className="flex-shrink-0 h-12 px-4 border-b border-border/60 bg-background flex items-center justify-between">
          <button
            onClick={onToggleLeft}
            className="text-muted-foreground/60 hover:text-foreground transition-colors duration-150 cursor-pointer"
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
            className="text-muted-foreground/60 hover:text-foreground transition-colors duration-150 cursor-pointer"
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
    <div className="flex-1 min-w-0 flex flex-col overflow-hidden">
      {/* Header */}
      <div className="flex-shrink-0 h-12 px-4 border-b border-border/60 bg-background flex items-center">
        <div className="flex items-center gap-2.5 w-full min-w-0">
          <button
            onClick={onToggleLeft}
            className="text-muted-foreground/60 hover:text-foreground transition-colors duration-150 cursor-pointer shrink-0"
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
          <h1 className="flex-1 text-[15px] font-medium tracking-tight truncate min-w-0">
            {session.title || "Untitled session"}
          </h1>
          <div className="flex items-center gap-1 shrink-0">
            <Button
              variant="ghost"
              size="xs"
              onClick={toggleInspect}
              className={cn(inspectOpen ? "text-primary" : "text-muted-foreground")}
              title="Inspect session"
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
          </div>
          <button
            onClick={onToggleRight}
            className="text-muted-foreground/60 hover:text-foreground transition-colors duration-150 cursor-pointer shrink-0"
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
            <div className="flex-shrink-0 px-4 pb-4 pt-3 bg-background">
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
                  "relative rounded-2xl border bg-background transition-all duration-150",
                  isStreaming
                    ? "border-primary/40 shadow-sm"
                    : "border-border focus-within:border-primary/50 focus-within:shadow-[0_0_0_2px_oklch(0.642_0.1691_38.5815/0.1)]",
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
                        className="rounded-xl gap-1.5 active:scale-[0.98] transition-transform"
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
    <aside className="flex w-80 shrink-0 flex-col border-l border-border bg-background">
      <div className="h-9 shrink-0 border-b border-border px-3 flex items-center justify-between">
        <span className="text-[9px] font-mono uppercase tracking-wider text-muted-foreground/40">
          Inspect
        </span>
        <button
          onClick={onClose}
          className="text-xs text-muted-foreground/50 hover:text-foreground cursor-pointer"
        >
          ×
        </button>
      </div>

      <div className="h-10 shrink-0 border-b border-border px-2 flex items-center gap-1">
        {(["session", "tools", "prompt", "skills"] as const).map((item) => (
          <button
            key={item}
            onClick={() => onTabChange(item)}
            className={cn(
              "flex-1 rounded-md px-2 py-1.5 text-[10px] font-mono capitalize cursor-pointer",
              tab === item
                ? "bg-muted text-foreground"
                : "text-muted-foreground/60 hover:text-foreground hover:bg-muted/50",
            )}
          >
            {item}
          </button>
        ))}
      </div>

      {tab === "session" && (
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

      {tab === "tools" && (
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

      {tab === "skills" && (
        <div className="overflow-auto">
          {skillsLoading ? (
            <div className="px-3 py-4 text-xs text-muted-foreground font-mono">Loading skills…</div>
          ) : skills.length === 0 ? (
            <div className="px-3 py-4 text-xs text-muted-foreground font-mono">
              No enabled skills available for this session.
            </div>
          ) : (
            <div className="p-2 space-y-2">
              {skills.map((skill) => (
                <SkillRow key={skill.id} skill={skill} />
              ))}
            </div>
          )}
        </div>
      )}

      {tab === "prompt" && (
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

function SkillRow({ skill }: { skill: Skill }) {
  return (
    <div className="border border-border rounded-lg px-2 py-2">
      <div className="flex items-center gap-2 min-w-0">
        <span className="text-sm font-mono font-medium truncate">{skill.name || skill.id}</span>
        <span className="text-[9px] border border-border rounded-full px-1.5 py-0.5 shrink-0">
          {skill.scope}
        </span>
        <span
          className={cn(
            "text-[9px] rounded-full px-1.5 py-0.5 shrink-0",
            skill.status === "active"
              ? "bg-primary/10 text-primary"
              : "bg-muted text-muted-foreground",
          )}
        >
          {skill.status}
        </span>
      </div>
      {skill.description && (
        <p className="text-[11px] text-muted-foreground mt-1 leading-relaxed">
          {skill.description}
        </p>
      )}
      {skill.files && skill.files.length > 0 && (
        <p className="text-[10px] font-mono text-muted-foreground/50 mt-1">
          {skill.files.length} files
        </p>
      )}
    </div>
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

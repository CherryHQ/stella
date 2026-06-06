import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { listGroupMessages, createSession, uploadWorkspaceFile } from "@/lib/api-client/sdk.gen";
import type { GroupMessage } from "@/lib/api-client/types.gen";
import { groupMembersQueryOptions } from "@/lib/queries/groups";
import { agentSkillsOptions } from "@/lib/queries/agents";
import { ChatComposer } from "@/features/sessions/ChatComposer";
import { useFileAttachments } from "@/features/sessions/useFileAttachments";
import { GroupTranscript, type DisplayMessage } from "./GroupTranscript";
import { sendGroupMessage, groupMessageToDisplay } from "./group-transport";

interface Props {
  groupId: string;
}

export function GroupChat({ groupId }: Props) {
  const queryClient = useQueryClient();
  const { data: members = [] } = useQuery(groupMembersQueryOptions(groupId));

  const [historicalMessages, setHistoricalMessages] = useState<GroupMessage[]>([]);
  const [streamingMessages, setStreamingMessages] = useState<Map<string, DisplayMessage>>(
    new Map(),
  );
  const [userInput, setUserInput] = useState("");
  const [pendingUserMessage, setPendingUserMessage] = useState<DisplayMessage | null>(null);
  const [isStreaming, setIsStreaming] = useState(false);
  const [loading, setLoading] = useState(true);
  const transcriptRef = useRef<HTMLDivElement>(null);
  const abortRef = useRef<AbortController | null>(null);
  const currentAgentRef = useRef<string>("");

  const [mentionQuery, setMentionQuery] = useState<string | null>(null);
  const inputRef = useRef<HTMLTextAreaElement>(null);

  const firstAgentId = members[0]?.agent_id ?? "";
  const { data: skills = [] } = useQuery(agentSkillsOptions(firstAgentId));
  const composerSkills = useMemo(
    () => skills.map((s) => ({ name: s.name, description: s.description })),
    [skills],
  );

  const [uploadContext, setUploadContext] = useState<{ agentId: string; sessionId: string } | null>(
    null,
  );

  const uploadFn = useCallback(
    async (file: File): Promise<string> => {
      let ctx = uploadContext;
      if (!ctx) {
        const agentId = members[0]?.agent_id;
        if (!agentId) throw new Error("no members");
        const { data } = await createSession({
          path: { agentId },
          body: { kind: "chat" },
          throwOnError: true,
        });
        ctx = { agentId, sessionId: data.id };
        setUploadContext(ctx);
        const key = `stella:group-upload-session:${groupId}`;
        localStorage.setItem(key, JSON.stringify(ctx));
      }
      const { data: res } = await uploadWorkspaceFile({
        path: { agentId: ctx.agentId, sessionId: ctx.sessionId },
        body: { file },
        throwOnError: true,
      });
      return res.path;
    },
    [members, uploadContext, groupId],
  );

  const { attachments, selectFiles, removeAttachment, clearAttachments, buildMessageText } =
    useFileAttachments(uploadFn);

  const loadMessages = useCallback(async () => {
    setLoading(true);
    try {
      const { data } = await listGroupMessages({
        path: { groupId },
        query: { page_size: 50 },
        throwOnError: true,
      });
      setHistoricalMessages((data?.messages as GroupMessage[]) ?? []);
    } finally {
      setLoading(false);
    }
  }, [groupId]);

  useEffect(() => {
    void loadMessages();
    setStreamingMessages(new Map());
    setPendingUserMessage(null);
    setIsStreaming(false);

    if (members.length === 0) {
      setUploadContext(null);
      return;
    }
    const key = `stella:group-upload-session:${groupId}`;
    const saved = localStorage.getItem(key);
    if (saved) {
      try {
        const parsed = JSON.parse(saved);
        if (parsed.agentId && parsed.sessionId) {
          setUploadContext(parsed);
          return;
        }
      } catch {
        // ignore
      }
    }
    setUploadContext(null);
  }, [groupId, loadMessages, members]);

  const scrollToBottom = useCallback(() => {
    setTimeout(() => {
      if (transcriptRef.current) {
        transcriptRef.current.scrollTop = transcriptRef.current.scrollHeight;
      }
    }, 0);
  }, []);

  const agentNameMap = useMemo(() => {
    const map = new Map<string, string>();
    for (const m of members) {
      if (m.agent_name) map.set(m.agent_id, m.agent_name);
    }
    return map;
  }, [members]);

  const resolveAgentName = useCallback(
    (id?: string, fallback?: string): string | undefined => {
      if (!id) return fallback;
      return agentNameMap.get(id) ?? fallback;
    },
    [agentNameMap],
  );

  const displayMessages = useMemo((): DisplayMessage[] => {
    const resolve = (msg: DisplayMessage): DisplayMessage => {
      if (msg.role === "assistant" && msg.agentId) {
        return { ...msg, agentName: resolveAgentName(msg.agentId, msg.agentName) };
      }
      return msg;
    };
    const msgs: DisplayMessage[] = [...historicalMessages]
      .reverse()
      .map(groupMessageToDisplay)
      .map(resolve);
    if (pendingUserMessage) {
      msgs.push(pendingUserMessage);
    }
    for (const sm of streamingMessages.values()) {
      msgs.push(resolve(sm));
    }
    return msgs;
  }, [historicalMessages, streamingMessages, pendingUserMessage, resolveAgentName]);

  const handleSend = useCallback(
    async (overrideText?: string) => {
      const input = overrideText ?? userInput;
      const hasContent = input.trim() || attachments.length > 0;
      if (!hasContent || isStreaming) return;
      if (attachments.some((a) => a.uploading)) return;

      const content = buildMessageText(input);
      setUserInput("");
      setMentionQuery(null);
      clearAttachments();
      setIsStreaming(true);

      const userMsg: DisplayMessage = {
        id: `pending-user-${Date.now()}`,
        role: "user",
        content,
        timestamp: new Date().toISOString(),
      };
      setPendingUserMessage(userMsg);
      scrollToBottom();

      const abort = new AbortController();
      abortRef.current = abort;

      await sendGroupMessage(
        groupId,
        content,
        {
          onAgentStart: (agentId, agentName, messageId) => {
            currentAgentRef.current = agentId;
            setStreamingMessages((prev) => {
              const next = new Map(prev);
              next.set(agentId, {
                id: messageId,
                role: "assistant",
                agentId,
                agentName,
                content: "",
                reasoning: "",
                streaming: true,
              });
              return next;
            });
            scrollToBottom();
          },
          onTextDelta: (agentId, delta) => {
            const id = agentId || currentAgentRef.current;
            setStreamingMessages((prev) => {
              const next = new Map(prev);
              const existing = next.get(id);
              if (existing) {
                next.set(id, { ...existing, content: existing.content + delta });
              }
              return next;
            });
            scrollToBottom();
          },
          onReasoningDelta: (agentId, delta) => {
            const id = agentId || currentAgentRef.current;
            setStreamingMessages((prev) => {
              const next = new Map(prev);
              const existing = next.get(id);
              if (existing) {
                next.set(id, {
                  ...existing,
                  reasoning: (existing.reasoning || "") + delta,
                });
              }
              return next;
            });
          },
          onToolStart: (agentId, toolCallId, toolName, input) => {
            const id = agentId || currentAgentRef.current;
            setStreamingMessages((prev) => {
              const next = new Map(prev);
              const existing = next.get(id);
              if (existing) {
                const toolCalls = [
                  ...(existing.toolCalls || []),
                  { id: toolCallId, name: toolName, input },
                ];
                next.set(id, { ...existing, toolCalls });
              }
              return next;
            });
            scrollToBottom();
          },
          onToolEnd: (agentId, toolCallId, output, isError) => {
            const id = agentId || currentAgentRef.current;
            setStreamingMessages((prev) => {
              const next = new Map(prev);
              const existing = next.get(id);
              if (existing) {
                const toolCalls = (existing.toolCalls || []).map((tc) =>
                  tc.id === toolCallId ? { ...tc, output, isError } : tc,
                );
                next.set(id, { ...existing, toolCalls });
              }
              return next;
            });
          },
          onAgentEnd: (agentId) => {
            setStreamingMessages((prev) => {
              const next = new Map(prev);
              const existing = next.get(agentId);
              if (existing) {
                next.set(agentId, { ...existing, streaming: false });
              }
              return next;
            });
          },
          onFinish: () => {
            setIsStreaming(false);
            setPendingUserMessage(null);
            setStreamingMessages(new Map());
            void loadMessages();
            void queryClient.invalidateQueries({ queryKey: ["groups"] });
          },
          onError: (error) => {
            console.error("Group chat error:", error);
            setIsStreaming(false);
          },
        },
        abort.signal,
      );
    },
    [
      userInput,
      attachments,
      isStreaming,
      groupId,
      scrollToBottom,
      loadMessages,
      queryClient,
      buildMessageText,
      clearAttachments,
    ],
  );

  const handleStop = useCallback(() => {
    abortRef.current?.abort();
    setIsStreaming(false);
  }, []);

  const mentionCandidates = useMemo(() => {
    if (mentionQuery === null) return [];
    const q = mentionQuery.toLowerCase();
    return members.filter(
      (m) =>
        m.agent_id.toLowerCase().includes(q) ||
        (m.agent_name && m.agent_name.toLowerCase().includes(q)),
    );
  }, [mentionQuery, members]);

  const handleInputChange = useCallback((val: string) => {
    setUserInput(val);
    const textarea = inputRef.current;
    const pos = textarea?.selectionStart ?? val.length;
    const before = val.slice(0, pos);
    const atMatch = before.match(/@(\S*)$/);
    setMentionQuery(atMatch ? atMatch[1] : null);
  }, []);

  const insertMention = useCallback(
    (agentId: string) => {
      const textarea = inputRef.current;
      if (!textarea) return;

      const pos = textarea.selectionStart ?? userInput.length;
      const before = userInput.slice(0, pos);
      const after = userInput.slice(pos);
      const atIdx = before.lastIndexOf("@");
      if (atIdx < 0) return;

      const newVal = before.slice(0, atIdx) + `@${agentId} ` + after;
      setUserInput(newVal);
      setMentionQuery(null);
      textarea.focus();
    },
    [userInput],
  );

  const mentionOverlay =
    mentionQuery !== null && mentionCandidates.length > 0 ? (
      <div className="absolute bottom-full left-4 right-4 mb-1 max-h-40 overflow-y-auto rounded-lg border border-border bg-popover p-1 shadow-lg sm:left-8 sm:right-8">
        <div className="mx-auto max-w-3xl">
          {mentionCandidates.map((m) => (
            <button
              key={m.agent_id}
              type="button"
              onClick={() => insertMention(m.agent_id)}
              className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm text-foreground transition-colors hover:bg-muted"
            >
              <span className="font-medium">{m.agent_name || m.agent_id}</span>
              <span className="text-xs text-muted-foreground">@{m.agent_id}</span>
            </button>
          ))}
        </div>
      </div>
    ) : null;

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <GroupTranscript
        ref={transcriptRef}
        messages={displayMessages}
        loading={loading}
        agentNames={agentNameMap}
        uploadAgentId={uploadContext?.agentId}
        uploadSessionId={uploadContext?.sessionId}
      />
      <ChatComposer
        value={userInput}
        onChange={handleInputChange}
        onSend={(text) => void handleSend(text)}
        onStop={handleStop}
        isStreaming={isStreaming}
        placeholder="Message the group… (@ to mention)"
        overlay={mentionOverlay}
        textareaRef={inputRef}
        attachments={attachments}
        onFileSelect={(files) => void selectFiles(files)}
        onRemoveAttachment={removeAttachment}
        skills={composerSkills}
      />
    </div>
  );
}

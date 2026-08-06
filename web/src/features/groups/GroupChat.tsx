import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useChat } from "@ai-sdk/react";
import type { UIMessage } from "ai";
import { listGroupMessages, createSession, uploadWorkspaceFile } from "@/lib/api-client/sdk.gen";
import { useI18n } from "@/lib/i18n";
import type { GroupMessage } from "@/lib/api-client/types.gen";
import { groupMembersQueryOptions } from "@/lib/queries/groups";
import { agentSkillsOptions } from "@/lib/queries/agents";
import { createGroupTransport, groupMessagesToUIMessages } from "@/lib/chat-transport";
import { ChatPane } from "@/components/chat/ChatPane";
import { ChatErrorNotice } from "@/components/chat/ChatErrorNotice";
import { BUILTIN_COMMANDS, ChatComposer } from "@/features/sessions/ChatComposer";
import { useFileAttachments } from "@/features/sessions/useFileAttachments";
import { GroupInspector } from "./GroupInspector";
import { GroupTranscript } from "./GroupTranscript";

interface Props {
  groupId: string;
}

export function GroupChat({ groupId }: Props) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { data: members = [] } = useQuery(groupMembersQueryOptions(groupId));

  const [historicalMessages, setHistoricalMessages] = useState<GroupMessage[]>([]);
  const [userInput, setUserInput] = useState("");
  const [loading, setLoading] = useState(true);
  const transcriptRef = useRef<HTMLDivElement>(null);

  const [mentionQuery, setMentionQuery] = useState<string | null>(null);
  const inputRef = useRef<HTMLTextAreaElement>(null);

  const firstAgentId = members[0]?.agent_id ?? "";
  const { data: skills = [] } = useQuery(agentSkillsOptions(firstAgentId));
  const composerSkills = useMemo(
    () => [
      ...BUILTIN_COMMANDS,
      ...skills.map((s) => ({ name: s.name, description: s.description })),
    ],
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

  const agentNameMap = useMemo(() => {
    const map = new Map<string, string>();
    for (const m of members) {
      if (m.agent_name) map.set(m.agent_id, m.agent_name);
    }
    return map;
  }, [members]);

  const transport = useMemo(() => createGroupTransport(groupId), [groupId]);

  const {
    messages: chatMessages,
    sendMessage: chatSendMessage,
    setMessages: setChatMessages,
    status: chatStatus,
    stop: chatStop,
    error: chatError,
  } = useChat({
    id: `group-${groupId}`,
    transport,
    // Batch SSE deltas: without this every token re-renders the transcript.
    experimental_throttle: 50,
    onFinish: () => {
      void loadMessages();
      void queryClient.invalidateQueries({ queryKey: ["groups"] });
    },
    onError: (err) => console.error("[group chat]", err),
  });

  const isStreaming = chatStatus === "streaming" || chatStatus === "submitted";

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

  useEffect(() => {
    if (historicalMessages.length === 0) return;
    const chrono = [...historicalMessages].reverse();
    const uiMessages = groupMessagesToUIMessages(chrono, agentNameMap);
    const newIDs = new Set(uiMessages.map((m) => m.id));
    setChatMessages((prev) => {
      const liveSlice = prev.filter((m) => !newIDs.has(m.id));
      return [...uiMessages, ...liveSlice];
    });
  }, [historicalMessages, agentNameMap, setChatMessages]);

  const scrollToBottom = useCallback(() => {
    setTimeout(() => {
      if (transcriptRef.current) {
        transcriptRef.current.scrollTop = transcriptRef.current.scrollHeight;
      }
    }, 0);
  }, []);

  useEffect(() => {
    if (isStreaming) scrollToBottom();
  }, [chatMessages, isStreaming, scrollToBottom]);

  const handleSend = useCallback(
    (overrideText?: string) => {
      const input = overrideText ?? userInput;
      const hasContent = input.trim() || attachments.length > 0;
      if (!hasContent || isStreaming) return;
      if (attachments.some((a) => a.uploading)) return;

      const content = buildMessageText(input);
      setUserInput("");
      setMentionQuery(null);
      clearAttachments();
      scrollToBottom();

      void chatSendMessage({ text: content });
    },
    [
      userInput,
      attachments,
      isStreaming,
      buildMessageText,
      clearAttachments,
      scrollToBottom,
      chatSendMessage,
    ],
  );

  const mentionCandidates = useMemo(() => {
    if (mentionQuery === null) return [];
    const q = mentionQuery.toLowerCase();
    return members.filter(
      (m) =>
        m.agent_id.toLowerCase().startsWith(q) ||
        (m.agent_name && m.agent_name.toLowerCase().startsWith(q)),
    );
  }, [mentionQuery, members]);

  // "Active" means responding right now: only agent-info parts from live
  // streaming messages count, never the merged history (grp-* ids).
  const activeAgentIds = useMemo(() => {
    if (!isStreaming) return new Set<string>();
    return collectActiveAgentIds(chatMessages.filter((m) => !m.id.startsWith("grp-")));
  }, [isStreaming, chatMessages]);

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
    <div className="relative flex min-h-0 flex-1 overflow-hidden">
      <ChatPane
        transcript={
          <GroupTranscript
            ref={transcriptRef}
            messages={chatMessages}
            loading={loading}
            agentNames={agentNameMap}
            uploadAgentId={uploadContext?.agentId}
            uploadSessionId={uploadContext?.sessionId}
          />
        }
        notice={<ChatErrorNotice error={chatError} />}
        composer={
          <ChatComposer
            value={userInput}
            onChange={handleInputChange}
            onSend={(text) => handleSend(text)}
            onStop={chatStop}
            isStreaming={isStreaming}
            placeholder={t("groups.messagePlaceholder")}
            overlay={mentionOverlay}
            textareaRef={inputRef}
            attachments={attachments}
            onFileSelect={(files) => void selectFiles(files)}
            onRemoveAttachment={removeAttachment}
            skills={composerSkills}
          />
        }
      />
      <GroupInspector
        members={members}
        messages={historicalMessages}
        activeAgentIds={activeAgentIds}
        uploadContext={uploadContext}
      />
    </div>
  );
}

function collectActiveAgentIds(messages: UIMessage[]) {
  const ids = new Set<string>();
  for (const message of messages) {
    for (const part of message.parts) {
      if (part.type !== "data-agent-info") continue;
      const data = (part as unknown as { data?: { agentId?: string } }).data;
      if (data?.agentId) ids.add(data.agentId);
    }
  }
  return ids;
}

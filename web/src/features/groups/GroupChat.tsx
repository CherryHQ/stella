import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useChat } from "@ai-sdk/react";
import type { UIMessage } from "ai";
import {
  abortGroupTurn,
  listGroupMessages,
  createSession,
  uploadWorkspaceFile,
} from "@/lib/api-client/sdk.gen";
import { useI18n } from "@/lib/i18n";
import type { GroupMessage } from "@/lib/api-client/types.gen";
import { groupMembersQueryOptions } from "@/lib/queries/groups";
import { agentSkillsOptions } from "@/lib/queries/agents";
import { createGroupTransport, groupMessagesToUIMessages } from "@/lib/chat-transport";
import { ChatPane } from "@/components/chat/ChatPane";
import { ChatErrorNotice } from "@/components/chat/ChatErrorNotice";
import { BUILTIN_COMMANDS, ChatComposer } from "@/features/sessions/ChatComposer";
import { skillTrigger, type ComposerTrigger } from "@/features/sessions/composer-triggers";
import { useFileAttachments } from "@/features/sessions/useFileAttachments";
import {
  GROUP_TURN_LINGER_MS,
  activeTurnAgentIds,
  applyTurn,
  clearRunningTurn,
  expireTurn,
  isTerminalTurn,
} from "./group-turns";
import { GroupInspector } from "./GroupInspector";
import { GroupTranscript } from "./GroupTranscript";
import { useGroupEvents, type GroupTurnEvent } from "./use-group-events";

interface Props {
  groupId: string;
}

export function GroupChat({ groupId }: Props) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { data: members = [] } = useQuery(groupMembersQueryOptions(groupId));

  const [canonicalBySeq, setCanonicalBySeq] = useState<Map<number, GroupMessage>>(new Map());
  const [turns, setTurns] = useState<Map<string, GroupTurnEvent>>(new Map());
  // Timers that retire a lingering terminal turn; cleared on unmount.
  const linger = useRef(new Set<ReturnType<typeof setTimeout>>());
  const [loading, setLoading] = useState(true);
  const transcriptRef = useRef<HTMLDivElement>(null);

  const firstAgentId = members[0]?.agent_id ?? "";
  const { data: skills = [] } = useQuery(agentSkillsOptions(firstAgentId));
  // "/" skills and "@" mentions are the same composer mechanism; the group
  // only supplies the member list as a second trigger.
  const composerTriggers = useMemo<ComposerTrigger[]>(
    () => [
      skillTrigger([
        ...BUILTIN_COMMANDS,
        ...skills.map((s) => ({ name: s.name, description: s.description })),
      ]),
      {
        char: "@",
        items: members.map((m) => ({
          key: m.agent_id,
          label: `@${m.agent_id}`,
          description: m.agent_name || undefined,
        })),
        replace: (item) => `${item.label} `,
      },
    ],
    [skills, members],
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

  const draftKey = `group:${groupId}`;
  const {
    attachments,
    selectFiles,
    retryAttachment,
    removeAttachment,
    clearAttachments,
    buildMessageText,
  } = useFileAttachments(uploadFn, draftKey);

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
      // The event stream has the accepted canonical row. Drop the request-local
      // frame so it cannot survive alongside that row in a second tab.
      setChatMessages([]);
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
      // SAFETY: listGroupMessages returns GroupMessage items under data.messages.
      const rows = (data?.messages as GroupMessage[]) ?? [];
      setCanonicalBySeq((current) => {
        const next = new Map(current);
        for (const message of rows) {
          // The EventSource may have already delivered a newer projection for
          // this seq, such as pending → delivered. Never overwrite it with the
          // list's older snapshot.
          if (!next.has(message.seq)) next.set(message.seq, message);
        }
        return next;
      });
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

  const canonicalMessages = useMemo(
    () => [...canonicalBySeq.values()].sort((a, b) => a.seq - b.seq),
    [canonicalBySeq],
  );
  const highestSeq = canonicalMessages.at(-1)?.seq ?? 0;
  const onCanonicalMessage = useCallback((message: GroupMessage) => {
    setCanonicalBySeq((current) => {
      const next = new Map(current);
      next.set(message.seq, message);
      return next;
    });
    // An agent's own message is proof its turn ended, whether or not the "done"
    // frame survived the hub.
    if (message.actor_type === "agent") {
      setTurns((current) => clearRunningTurn(current, message.actor_id));
    }
  }, []);
  const onTurn = useCallback((turn: GroupTurnEvent) => {
    setTurns((current) => applyTurn(current, turn));
    if (!isTerminalTurn(turn.state)) return;
    const timer = setTimeout(() => {
      linger.current.delete(timer);
      setTurns((current) => expireTurn(current, turn));
    }, GROUP_TURN_LINGER_MS);
    linger.current.add(timer);
  }, []);
  useEffect(() => {
    const timers = linger.current;
    return () => {
      for (const timer of timers) clearTimeout(timer);
      timers.clear();
    };
  }, []);
  useGroupEvents(groupId, { sinceSeq: highestSeq, onMessage: onCanonicalMessage, onTurn });

  const canonicalUIMessages = useMemo(
    () => groupMessagesToUIMessages(canonicalMessages, agentNameMap),
    [canonicalMessages, agentNameMap],
  );

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
    (input: string) => {
      const hasContent = input.trim() || attachments.length > 0;
      if (!hasContent || isStreaming) return;
      if (attachments.some((a) => a.uploading)) return;

      const content = buildMessageText(input);
      clearAttachments();
      scrollToBottom();

      void chatSendMessage({ text: content });
    },
    [attachments, isStreaming, buildMessageText, clearAttachments, scrollToBottom, chatSendMessage],
  );

  // "Active" means responding right now: only agent-info parts from live
  // streaming messages count, never the merged history (grp-* ids).
  // Agents the server says are generating right now. This is the stop button's
  // target list and the inspector's presence source; a fresh page load gets it
  // from the event stream's running snapshot.
  const runningAgentIds = useMemo(() => activeTurnAgentIds(turns), [turns]);
  const activeAgentIds = useMemo(() => {
    const ids = new Set(runningAgentIds);
    if (isStreaming) {
      for (const id of collectActiveAgentIds(chatMessages)) ids.add(id);
    }
    return ids;
  }, [runningAgentIds, isStreaming, chatMessages]);
  const displayMessages = useMemo(
    () => [
      ...canonicalUIMessages,
      ...(isStreaming ? chatMessages.filter((message) => message.role === "assistant") : []),
    ],
    [canonicalUIMessages, chatMessages, isStreaming],
  );
  // Stop only what is actually running: the server's `running` turn frames name
  // the responding agents, so the old broadcast-abort to every member is gone.
  const handleStop = useCallback(() => {
    void chatStop();
    for (const agentId of runningAgentIds) {
      void abortGroupTurn({ path: { groupId, agentId }, throwOnError: true });
    }
  }, [runningAgentIds, chatStop, groupId]);
  // Nothing running and no local request in flight means nothing to stop, so the
  // composer keeps its send affordance instead of offering a dead button.
  const canStop = isStreaming || runningAgentIds.length > 0;

  return (
    <div className="relative flex min-h-0 flex-1 overflow-hidden">
      <ChatPane
        transcript={
          <GroupTranscript
            ref={transcriptRef}
            messages={displayMessages}
            loading={loading}
            agentNames={agentNameMap}
            uploadAgentId={uploadContext?.agentId}
            uploadSessionId={uploadContext?.sessionId}
          />
        }
        notice={<ChatErrorNotice error={chatError} />}
        composer={
          <ChatComposer
            onSend={handleSend}
            onStop={canStop ? handleStop : undefined}
            isStreaming={isStreaming}
            placeholder={t("groups.messagePlaceholder")}
            draftKey={draftKey}
            attachments={attachments}
            onFileSelect={(files) => void selectFiles(files)}
            onRemoveAttachment={removeAttachment}
            onRetryAttachment={retryAttachment}
            triggers={composerTriggers}
          />
        }
      />
      <GroupInspector
        members={members}
        messages={canonicalMessages}
        activeAgentIds={activeAgentIds}
        turns={turns}
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
      const data =
        // SAFETY: a data-agent-info part is a tagged union member carrying its payload as .data.
        (part as unknown as { data?: { agentId?: string } }).data;
      if (data?.agentId) ids.add(data.agentId);
    }
  }
  return ids;
}

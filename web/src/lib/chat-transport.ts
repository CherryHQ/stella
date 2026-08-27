import { DefaultChatTransport } from "ai";
import type { UIMessage } from "ai";
import type { GroupMessage } from "@/lib/api-client/types.gen";
import type {
  ContentBlock,
  JsonObject,
  Message,
  RenderableReference,
  SessionMessage,
  TextBlock,
  ToolResult,
} from "./types";

export function createSessionTransport(agentId: string, sessionId: string) {
  const base = `/api/agents/${encodeURIComponent(agentId)}/sessions/${encodeURIComponent(sessionId)}`;
  return new DefaultChatTransport({
    api: `${base}/messages`,
    prepareSendMessagesRequest: ({ messages }) => {
      const last = messages[messages.length - 1];
      const parts = last.parts
        .map((p) => {
          if (p.type === "text") return { type: "text" as const, text: p.text };
          // File parts carry a workspace path the server resolves; the browser's
          // media type rides along as a hint only.
          if (p.type === "file")
            return { type: "file" as const, url: p.url, mimeType: p.mediaType };
          return null;
        })
        .filter(Boolean);
      return { body: { parts } };
    },
    // Read-only resume: watch a turn started elsewhere (server-driven
    // scheduler/task/delegate turns, or another tab) via the events SSE
    // endpoint. It answers 204 when no turn is in flight, which the SDK treats
    // as "nothing to resume".
    prepareReconnectToStreamRequest: () => ({ api: `${base}/events` }),
  });
}

export function createGroupTransport(groupId: string) {
  return new DefaultChatTransport({
    api: `/api/groups/${encodeURIComponent(groupId)}/messages`,
    prepareSendMessagesRequest: ({ messages }) => {
      const last = messages[messages.length - 1];
      const text = last.parts
        .filter(
          (p): p is Extract<(typeof last.parts)[number], { type: "text" }> => p.type === "text",
        )
        .map((p) => p.text)
        .join("");
      return { body: { content: text, client_message_id: last.id } };
    },
  });
}

export interface AgentInfoData {
  agentId: string;
  agentName: string;
  agentSessionId?: string;
}

export function groupMessagesToUIMessages(
  messages: GroupMessage[],
  agentNameMap: Map<string, string>,
): UIMessage[] {
  const result: UIMessage[] = [];
  let i = 0;

  while (i < messages.length) {
    const msg = messages[i];

    if (msg.actor_type === "human") {
      result.push({
        id: `grp-${msg.id}`,
        role: "user",
        parts: [{ type: "text" as const, text: msg.content }],
        metadata: { timestamp: msg.created_at },
      });
      i++;

      const agentParts: UIMessage["parts"] = [];
      let firstAgentId = "";

      while (i < messages.length && messages[i].actor_type === "agent") {
        const agentMsg = messages[i];
        const agentName =
          agentNameMap.get(agentMsg.actor_id) ?? agentMsg.actor_name ?? agentMsg.actor_id;

        if (!firstAgentId) firstAgentId = agentMsg.id;

        agentParts.push({ type: "step-start" as const });
        // SAFETY: this literal matches the data-agent-info member of the parts
        // union, whose agentName/agentSessionId are copied from the agent event.
        agentParts.push({
          type: "data-agent-info",
          id: `ai-${agentMsg.id}`,
          data: {
            agentId: agentMsg.actor_id,
            agentName,
            agentSessionId: agentMsg.agent_session_id,
          },
        } as UIMessage["parts"][number]);

        if (agentMsg.reasoning) {
          agentParts.push({ type: "reasoning", text: agentMsg.reasoning, providerMetadata: {} });
        }
        if (agentMsg.content) {
          agentParts.push({ type: "text" as const, text: agentMsg.content });
        }

        i++;
      }

      if (agentParts.length > 0) {
        result.push({
          id: `grp-ast-${firstAgentId}`,
          role: "assistant",
          parts: agentParts,
          metadata: { timestamp: messages[i - 1]?.created_at },
        });
      }
    } else {
      const agentMsg = messages[i];
      const agentName =
        agentNameMap.get(agentMsg.actor_id) ?? agentMsg.actor_name ?? agentMsg.actor_id;
      // SAFETY: the data-agent-info literal below matches the corresponding
      // member of the parts union; agentName is resolved before this array is built.
      const parts: UIMessage["parts"] = [
        { type: "step-start" as const },
        {
          type: "data-agent-info",
          id: `ai-${agentMsg.id}`,
          data: {
            agentId: agentMsg.actor_id,
            agentName,
            agentSessionId: agentMsg.agent_session_id,
          },
        } as UIMessage["parts"][number],
      ];
      if (agentMsg.reasoning) {
        parts.push({ type: "reasoning", text: agentMsg.reasoning, providerMetadata: {} });
      }
      if (agentMsg.content) {
        parts.push({ type: "text" as const, text: agentMsg.content });
      }
      result.push({
        id: `grp-ast-${agentMsg.id}`,
        role: "assistant",
        parts,
        metadata: { timestamp: agentMsg.created_at },
      });
      i++;
    }
  }

  return result;
}

/**
 * Merge standalone role:"tool" messages into the preceding assistant message's
 * tool_call blocks so that the conversion to UIMessage never produces orphan
 * tool-result rows. Returns a new array (no mutation).
 */
export function mergeToolResults(messages: Message[]): Message[] {
  const out: Message[] = [];
  for (const m of messages) {
    if (m.role === "tool" && m.tool_call_id) {
      const prev = out.length > 0 ? out[out.length - 1] : undefined;
      if (prev?.role === "assistant" && prev.blocks) {
        const blocks = prev.blocks.map((block) => {
          if (block.type === "tool_call" && block.id === m.tool_call_id && !block.result) {
            return {
              ...block,
              status: "done" as const,
              result: {
                tool_call_id: m.tool_call_id!,
                content: m.content ?? "",
                is_error: m.is_error ?? false,
                ...(m.blocks && m.blocks.length > 0
                  ? { blocks: m.blocks.filter(isTextOrImageBlock) }
                  : undefined),
                ...(m.references && m.references.length > 0
                  ? { references: m.references }
                  : undefined),
              },
            };
          }
          return block;
        });
        out[out.length - 1] = { ...prev, blocks };
        continue;
      }
    }
    out.push({ ...m });
  }
  return out;
}

// Tiny non-cryptographic content hash. Used to derive a stable historical ID
// that does not depend on the message's position in the list. Without this,
// fetching an older page shifts every index and the dedup in setChatMessages
// breaks — every already-loaded message would be added a second time.
const CANONICAL_RECONCILIATION_WINDOW_MS = 2 * 60 * 1000;

// reconcileHistoryUIMessages normally lets canonical history replace only the
// optimistic workspace image that produced it. Matching is one-to-one and
// timestamp-bound: an old, identical caption is not evidence that a new upload
// was persisted. After a stream closes, authoritative mode replaces all live
// IDs because the server has persisted the complete turn under canonical IDs.
export function reconcileHistoryUIMessages(
  history: UIMessage[],
  current: UIMessage[],
  options: { authoritative?: boolean } = {},
): UIMessage[] {
  if (options.authoritative) return history;
  const historyIDs = new Set(history.map((message) => message.id));
  const consumedOptimisticIDs = new Set<string>();

  for (const canonical of history) {
    if (!isCanonicalUserAttachment(canonical)) continue;
    let closest: UIMessage | undefined;
    let closestDistance = Infinity;
    for (const candidate of current) {
      if (consumedOptimisticIDs.has(candidate.id)) continue;
      const distance = canonicalAttachmentMatchDistance(canonical, candidate);
      if (distance !== undefined && distance < closestDistance) {
        closest = candidate;
        closestDistance = distance;
      }
    }
    if (closest) consumedOptimisticIDs.add(closest.id);
  }

  return [
    ...history,
    ...current.filter(
      (message) => !historyIDs.has(message.id) && !consumedOptimisticIDs.has(message.id),
    ),
  ];
}

function canonicalAttachmentMatchDistance(
  canonical: UIMessage,
  optimistic: UIMessage,
): number | undefined {
  if (!isWorkspaceUserAttachment(optimistic)) return undefined;
  if (uiMessageCaption(canonical) !== uiMessageCaption(optimistic)) return undefined;
  if (canonicalAttachmentCount(canonical) !== workspaceAttachmentCount(optimistic)) {
    return undefined;
  }
  const canonicalTime = uiMessageTimestamp(canonical);
  const optimisticTime = uiMessageTimestamp(optimistic);
  if (canonicalTime === undefined || optimisticTime === undefined) return undefined;
  const distance = Math.abs(canonicalTime - optimisticTime);
  return distance <= CANONICAL_RECONCILIATION_WINDOW_MS ? distance : undefined;
}

function uiMessageCaption(message: UIMessage): string {
  return message.parts
    .flatMap((part) =>
      part.type === "text" && !isStandaloneFileMarker(part.text) ? [part.text] : [],
    )
    .join("\n");
}

function uiMessageTimestamp(message: UIMessage): number | undefined {
  // SAFETY: the timestamp sits in UIMessage.metadata when messageToUIMessage
  // wrote it; a non-string value falls through to the typeof guard.
  const timestamp = (message.metadata as UiMetadata | undefined)?.timestamp;
  if (timestamp === undefined) return undefined;
  const parsed = Date.parse(timestamp);
  return Number.isFinite(parsed) ? parsed : undefined;
}

function isCanonicalUserAttachment(message: UIMessage): boolean {
  return message.role === "user" && canonicalAttachmentCount(message) > 0;
}

function isWorkspaceUserAttachment(message: UIMessage): boolean {
  return message.role === "user" && workspaceAttachmentCount(message) > 0;
}

function canonicalAttachmentCount(message: UIMessage): number {
  return message.parts.filter(
    (part) =>
      (part.type === "file" && isSessionMediaURL(part.url)) ||
      (part.type === "text" && isStandaloneFileMarker(part.text)),
  ).length;
}

function workspaceAttachmentCount(message: UIMessage): number {
  return message.parts.filter((part) => part.type === "file" && !isSessionMediaURL(part.url))
    .length;
}

function stableContentKey(m: Message): string {
  const sig = m.blocks ? JSON.stringify(m.blocks) : (m.content ?? "");
  let h = 5381;
  for (let i = 0; i < sig.length; i++) {
    h = (h * 33) ^ sig.charCodeAt(i);
  }
  return (h >>> 0).toString(36);
}

// sessionMessagesToMessages is the single boundary from generated API history
// types into the transcript model. It discards malformed optional blocks rather
// than reviving an untyped JSON path.
export function sessionMessagesToMessages(messages: SessionMessage[] | undefined): Message[] {
  return (messages ?? []).map((message) => {
    const blocks = message.blocks?.flatMap(sessionMessageBlockToContentBlock);
    // SAFETY: ref.intent carries the tool-intent discriminant of the
    // RenderableReference authored by the message writer, so it is that type.
    const references = message.references?.map((ref) => ({
      v: 1 as const,
      type: ref.type,
      id: ref.id,
      ...(ref.agent_id ? { agent_id: ref.agent_id } : undefined),
      ...(ref.intent ? { intent: ref.intent as RenderableReference["intent"] } : undefined),
      ...(ref.preview ? { preview: ref.preview } : undefined),
    }));
    return {
      id: message.id,
      role: message.role,
      content: message.content,
      ...(blocks && blocks.length > 0 ? { blocks } : undefined),
      tool_call_id: message.tool_call_id,
      references,
      timestamp: message.timestamp,
      token_count: message.token_count,
      actor_type: message.actor_type,
      actor_id: message.actor_id,
      source_session_id: message.source_session_id,
      ...(message.tool_name ? { tool_name: message.tool_name } : undefined),
      ...(message.is_error !== undefined ? { is_error: message.is_error } : undefined),
    };
  });
}

function sessionMessageBlockToContentBlock(
  block: NonNullable<SessionMessage["blocks"]>[number],
): ContentBlock[] {
  switch (block.type) {
    case "text":
      return block.text ? [{ type: "text", text: block.text }] : [];
    case "thinking":
      return block.thinking ? [{ type: "thinking", thinking: block.thinking }] : [];
    case "tool_call":
      return [
        {
          type: "tool_call",
          id: block.id ?? "",
          name: block.name,
          arguments: jsonObject(block.arguments),
        },
      ];
    case "image":
      return block.media_id && block.mime_type && block.url
        ? [{ type: "image", media_id: block.media_id, mime_type: block.mime_type, url: block.url }]
        : [];
    default:
      return [];
  }
}

function isTextOrImageBlock(
  block: ContentBlock,
): block is TextBlock | Extract<ContentBlock, { type: "image" }> {
  return block.type === "text" || block.type === "image";
}

// Durable API history retains every text part. The Web alone recognizes its
// own upload marker immediately before a durable image so it can replace the
// workspace preview without rendering the marker as prose.
function presentationBlocks(blocks: ContentBlock[]): ContentBlock[] {
  return blocks.filter(
    (block, index) =>
      !(
        block.type === "text" &&
        isStandaloneFileMarker(block.text) &&
        blocks[index + 1]?.type === "image"
      ),
  );
}

function isStandaloneFileMarker(text: string): boolean {
  return text.startsWith("[file: /") && text.endsWith("]");
}

export function messageToUIMessage(m: Message): UIMessage {
  const parts: UIMessage["parts"] = [];

  if (m.blocks && m.blocks.length > 0) {
    for (const block of presentationBlocks(m.blocks)) {
      switch (block.type) {
        case "text":
          parts.push({ type: "text", text: block.text });
          break;
        case "thinking":
          if (block.thinking) {
            parts.push({ type: "reasoning", text: block.thinking, providerMetadata: {} });
          }
          break;
        case "image":
          parts.push({ type: "file", url: block.url, mediaType: block.mime_type });
          break;
        case "tool_call": {
          const output = block.result?.blocks
            ? { content: block.result.content, blocks: presentationBlocks(block.result.blocks) }
            : block.result?.content;
          // SAFETY: the dynamic-tool literal carries the tool output shape that
          // this tool_call member of the parts union expects.
          parts.push({
            type: "dynamic-tool",
            toolName: block.name,
            toolCallId: block.id,
            state: block.result
              ? block.result.is_error
                ? "output-error"
                : "output-available"
              : "input-available",
            input: block.arguments ?? {},
            ...(block.result ? { output } : undefined),
            ...(block.result?.is_error ? { errorText: block.result.content } : undefined),
          } as UIMessage["parts"][number]);
          // Re-emit references as a data part so history rehydration feeds the
          // exact same channel as the live SSE stream — uiMessageToMessage reads
          // `data-tool-references` for both, so there is one rendering path.
          const refs = block.result?.references;
          if (refs && refs.length > 0) {
            // SAFETY: the data-tool-references literal matches the union member,
            // re-emitting the same references uiMessageToMessage reads back.
            parts.push({
              type: "data-tool-references",
              id: block.id,
              data: { toolCallId: block.id, references: refs },
            } as UIMessage["parts"][number]);
          }
          break;
        }
      }
    }
  } else if (m.content) {
    parts.push({ type: "text", text: m.content });
  }

  return {
    id: m.id ?? `hist-${m.timestamp}-${m.role}-${stableContentKey(m)}`,
    role: m.role === "tool" ? "assistant" : m.role,
    parts,
    metadata: {
      timestamp: m.timestamp,
      token_count: m.token_count,
      model: m.model,
      actor_type: m.actor_type,
      actor_id: m.actor_id,
      source_session_id: m.source_session_id,
    },
  };
}

type AnyToolPart = {
  type: string;
  toolCallId: string;
  toolName?: string;
  state: string;
  input?: JsonObject;
  output?: unknown;
  errorText?: string;
};

function isToolPart(
  part: UIMessage["parts"][number],
): part is AnyToolPart & UIMessage["parts"][number] {
  return part.type === "dynamic-tool" || part.type.startsWith("tool-");
}

function isTextOutput(value: AnyToolPart["output"]): value is string {
  return typeof value === "string";
}

function jsonObject(
  value: NonNullable<NonNullable<SessionMessage["blocks"]>[number]["arguments"]> | undefined,
): JsonObject {
  if (value === null || value === undefined || Array.isArray(value)) return {};
  // SAFETY: API tool-call arguments are JSON objects; the object/array guard above excludes other shapes.
  return value as JsonObject;
}

function extractToolName(part: AnyToolPart): string {
  if (part.toolName) return part.toolName;
  if (part.type.startsWith("tool-") && part.type.length > 5) return part.type.slice(5);
  return "";
}

interface UiMetadata {
  timestamp?: string;
  token_count?: number;
  model?: string;
  actor_type?: Message["actor_type"];
  actor_id?: string;
  source_session_id?: string;
}

// SAFETY: messageToUIMessage writes the six Message metadata fields into each
// UIMessage's metadata record; this parser reads exactly those back at the
// boundary instead of casting each field at the call site.
function parseUiMetadata(meta: UIMessage["metadata"]): UiMetadata {
  if (meta == null) return {};
  // SAFETY: UIMessage.metadata is an AI-sdk record; read each known field with
  // a typeof guard rather than trusting its value shape.
  const base = meta as UiMetadata;
  const timestamp = base.timestamp ?? "";
  const token_count = base.token_count;
  const model = base.model;
  // SAFETY: guarded by the typeof check; only the three known Message roles are
  // written by messageToUIMessage into actor_type.
  const rawActorType = base.actor_type;
  // SAFETY: rawActorType was either a string (one of the three Message roles) or undefined above.
  const actor_type = rawActorType as Message["actor_type"] | undefined;
  const actor_id = base.actor_id;
  const source_session_id = base.source_session_id;
  return { timestamp, token_count, model, actor_type, actor_id, source_session_id };
}

export function uiMessageToMessage(m: UIMessage): Message {
  const blocks: Message["blocks"] = [];
  let content = "";

  // Renderable references arrive as out-of-band `data-tool-references` parts
  // (the live counterpart of the stored `references[]`); collect them up front
  // and attach to the matching tool block so cards show mid-stream.
  const refsByTool = new Map<string, RenderableReference[]>();
  for (const part of m.parts) {
    if (part.type === "data-tool-references") {
      // SAFETY: guarded by the narrow part.type check above; the member carries
      // a data object keyed by toolCallId/references from the writer at line~
      const data = (part as { data?: { toolCallId?: string; references?: unknown } }).data;
      if (data?.toolCallId && Array.isArray(data.references)) {
        // SAFETY: Array.isArray(data.references) was just asserted above.
        refsByTool.set(data.toolCallId, data.references as RenderableReference[]);
      }
    }
  }

  for (const part of m.parts) {
    switch (part.type) {
      case "text":
        blocks.push({ type: "text", text: part.text });
        content += part.text;
        break;
      case "file": {
        if (isSessionMediaURL(part.url) && part.mediaType) {
          const mediaID = part.url.split("/").at(-1);
          if (mediaID) {
            blocks.push({
              type: "image",
              media_id: decodeURIComponent(mediaID),
              mime_type: part.mediaType,
              url: part.url,
            });
          }
          break;
        }
        // Optimistic workspace uploads and old stored markers retain their
        // marker path until canonical session history replaces them.
        const marker = `[file: ${part.url}]`;
        blocks.push({ type: "text", text: marker });
        content += content ? `\n${marker}` : marker;
        break;
      }
      case "reasoning":
        blocks.push({ type: "thinking", thinking: part.text });
        break;
      default:
        if (isToolPart(part)) {
          const hasOutput = part.state === "output-available" || part.state === "output-error";
          // SAFETY: part is tool-shaped (isToolPart above); its optionable output
          // is read defensively and only used when present.
          const output = part.output as { content?: unknown; blocks?: unknown } | undefined;
          const outputContent = hasOutput
            ? part.state === "output-error"
              ? (part.errorText ?? "error")
              : isTextOutput(part.output)
                ? part.output
                : isTextOutput(output?.content)
                  ? output.content
                  : JSON.stringify(part.output)
            : undefined;
          const outputBlocks = Array.isArray(output?.blocks)
            ? output.blocks.filter(isTextOrImageBlock)
            : undefined;
          const references = refsByTool.get(part.toolCallId);
          // SAFETY: part is tool-shaped (isToolPart above); its input is an
          // untyped JSON arguments blob that the tool_call block accepts as-is.
          blocks.push({
            type: "tool_call",
            id: part.toolCallId,
            name: extractToolName(part),
            arguments: part.input ?? {},
            status: hasOutput ? "done" : "running",
            ...(hasOutput
              ? {
                  result: {
                    tool_call_id: part.toolCallId,
                    content: outputContent!,
                    is_error: part.state === "output-error",
                    ...(outputBlocks && outputBlocks.length > 0
                      ? { blocks: outputBlocks }
                      : undefined),
                    ...(references && references.length > 0 ? { references } : undefined),
                  } satisfies ToolResult,
                }
              : undefined),
          });
        }
        break;
    }
  }

  const meta = parseUiMetadata(m.metadata);
  // SAFETY: parts carry an optional state string; checking it for 'streaming'
  // only ever reads the discriminant, so the record view is narrow and safe.
  const streaming = m.parts.some((p) => "state" in p && p.state === "streaming");

  return {
    id: m.id,
    role: m.role === "system" ? "assistant" : m.role,
    content: content || undefined,
    blocks: blocks.length > 0 ? blocks : undefined,
    timestamp: meta.timestamp ?? "",
    token_count: meta.token_count,
    model: meta.model,
    actor_type: meta.actor_type,
    actor_id: meta.actor_id,
    source_session_id: meta.source_session_id,
    streaming,
  };
}

function isSessionMediaURL(url: string): boolean {
  return /^\/api\/agents\/[^/]+\/sessions\/[^/]+\/media\/[^/]+$/.test(url);
}

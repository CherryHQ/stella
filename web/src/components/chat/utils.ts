import type { ContentBlock } from "@/lib/types";

export const IMAGE_EXT = /\.(png|jpe?g|gif|webp|svg|bmp|avif)$/i;

/** The runner appends this to bash output; the exit code is the real verdict. */
export const EXIT_TRAILER = /\n?\[exit:(\d+) \| (\d+ms)\]\s*$/;

/**
 * Whether a finished tool call failed.
 *
 * Two signals, and they disagree: the transport sets `is_error` for a call that
 * threw, but a shell command that ran to completion and exited 1 is a success
 * at that level and a failure at the one the reader cares about. A call with no
 * result yet is still in flight, not failed.
 *
 * Shared so the collapsed group summary and the expanded row's status footer
 * cannot drift into reporting different verdicts for the same call.
 */
export function toolCallFailed(block: ContentBlock & { type: "tool_call" }): boolean {
  if (!block.result) return false;
  if (block.result.is_error) return true;
  const exit = (block.result.content ?? "").match(EXIT_TRAILER);
  return exit ? exit[1] !== "0" : false;
}

/** Pretty-print a complete JSON object/array without rewriting ordinary tool output. */
export function formatToolOutput(content: string): string {
  const trimmed = content.trim();
  if (!trimmed || (trimmed[0] !== "{" && trimmed[0] !== "[")) return content;
  try {
    return JSON.stringify(JSON.parse(trimmed), null, 2);
  } catch {
    return content;
  }
}

export function isImagePath(path: string): boolean {
  return IMAGE_EXT.test(path);
}

export function basename(path: string): string {
  return path.split("/").pop() || path;
}

// Uploads are stored as "<YYYYMMDD>-<uuid>-<original name>" so they cannot
// collide. That prefix is 45 characters of noise in a chat bubble, and it
// pushes the real name past the truncation point, so strip it for display.
const UPLOAD_PREFIX = /^\d{8}-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}-(?=.)/i;

export function attachmentDisplayName(path: string): string {
  return basename(path).replace(UPLOAD_PREFIX, "");
}

// workspaceFileURL builds a raw file-content read URL for a message-embedded
// file path. The path is passed verbatim: an absolute sandbox-view (/user/...,
// /workspace/...) or host path is self-describing, and the server resolves which
// authorized workspace root contains it (host containment first, then sandbox
// mount mapping). The client cannot disambiguate mount views from real host
// paths, so it must not guess a scope — that decision belongs to the server.
export function workspaceFileURL(agentId: string, sessionId: string, path: string): string {
  return `/api/agents/${encodeURIComponent(agentId)}/sessions/${encodeURIComponent(
    sessionId,
  )}/workspace/file-content?path=${encodeURIComponent(path)}&raw=true`;
}

// parseFileRefs splits a user message into the `[file: path]` attachments the
// composer injected and the remaining prose, so attachments render as previews
// instead of raw text.
export function parseFileRefs(input: string): { files: string[]; text: string } {
  const files: string[] = [];
  const text = input
    .replace(/\[file:\s*([^\]]+)\]/g, (_, p: string) => {
      files.push(p.trim());
      return "";
    })
    .replace(/\n{3,}/g, "\n\n")
    .trim();
  return { files, text };
}

// userMessageRenderInput keeps canonical durable images ordered while still
// exposing unrelated workspace markers for the legacy file-preview path.
export function userMessageRenderInput(
  msg: { content?: string; blocks?: ContentBlock[] },
  agentNames?: Map<string, string>,
) {
  const canonicalBlocks = msg.blocks?.filter(
    (block): block is Extract<ContentBlock, { type: "text" | "image" }> =>
      block.type === "text" || block.type === "image",
  );
  const hasCanonicalImage = canonicalBlocks?.some((block) => block.type === "image") ?? false;
  const displayContent = replaceUUIDMentions(extractUserText(msg), agentNames);
  const { files, text } = parseFileRefs(displayContent);
  return {
    canonicalBlocks,
    hasCanonicalImage,
    text,
    images: files.filter(isImagePath),
    otherFiles: files.filter((file) => !isImagePath(file)),
  };
}

export function replaceUUIDMentions(text: string, agentNames?: Map<string, string>): string {
  if (!agentNames || agentNames.size === 0) return text;
  return text.replace(
    /@([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})/gi,
    (match, uuid: string) => {
      const name = agentNames.get(uuid);
      return name ? `@${name}` : match;
    },
  );
}

export function extractUserText(msg: { content?: string }): string {
  const raw = msg.content ?? "";
  // Fast path: plain text (the common case). Without this, every render of
  // every user bubble throws and catches a JSON.parse error.
  if (!raw.startsWith("[")) return raw;
  try {
    const parsed = JSON.parse(raw) as unknown;
    if (Array.isArray(parsed)) {
      return (parsed as Array<{ kind?: string; type?: string; text?: string }>)
        .filter((b) => b.kind === "text" || b.type === "text")
        .map((b) => b.text ?? "")
        .join("\n");
    }
  } catch {
    /* plain string */
  }
  return raw;
}

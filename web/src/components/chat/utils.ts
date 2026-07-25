export const IMAGE_EXT = /\.(png|jpe?g|gif|webp|svg|bmp|avif)$/i;

export function isImagePath(path: string): boolean {
  return IMAGE_EXT.test(path);
}

export function basename(path: string): string {
  return path.split("/").pop() || path;
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

export const IMAGE_EXT = /\.(png|jpe?g|gif|webp|svg|bmp|avif)$/i;

export function isImagePath(path: string): boolean {
  return IMAGE_EXT.test(path);
}

export function basename(path: string): string {
  return path.split("/").pop() || path;
}

// Sandbox mount prefixes on isolating backends. A file reference embedded in a
// message (e.g. an uploaded `[file: /user/assets/...]`) carries the sandbox-view
// path the agent reads; the file-content endpoint instead wants a workspace root
// plus a relative path. Map the mount prefix back to its scope and strip it so
// the read URL is scoped and relative (see UserDataViewFor/WorkspaceViewFor).
const SANDBOX_MOUNT_SCOPES: ReadonlyArray<readonly [string, "user" | "agent"]> = [
  ["/user/", "user"],
  ["/workspace/", "agent"],
];

// workspaceRef splits a message-embedded file path into the workspace-relative
// path and scope the file-content endpoint expects. Paths without a known sandbox
// mount prefix (e.g. non-isolating backends) pass through unchanged with no scope.
export function workspaceRef(path: string): { path: string; scope?: "user" | "agent" } {
  for (const [prefix, scope] of SANDBOX_MOUNT_SCOPES) {
    if (path.startsWith(prefix)) {
      return { path: path.slice(prefix.length), scope };
    }
  }
  return { path };
}

export function workspaceFileURL(agentId: string, sessionId: string, path: string): string {
  const ref = workspaceRef(path);
  const scopeParam = ref.scope ? `&scope=${ref.scope}` : "";
  return `/api/agents/${encodeURIComponent(agentId)}/sessions/${encodeURIComponent(
    sessionId,
  )}/workspace/file-content?path=${encodeURIComponent(ref.path)}&raw=true${scopeParam}`;
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

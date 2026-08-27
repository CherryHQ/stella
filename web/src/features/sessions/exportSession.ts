import type { ContentBlock, Message, Session } from "@/lib/types";

export interface ExportMeta {
  session: Session;
  agentName: string;
  exportedAt: string;
}

// Raw fields the backend serializes on role:"tool" messages that the shared
// Message type does not declare: tool_name and is_error are critical for export
// (they distinguish failed vs successful tool runs and identify which tool ran).
type RawToolMessage = Message & {
  tool_name?: string;
  is_error?: boolean;
};

interface ExportLine {
  type: string;
  id: string | undefined;
  role: Message["role"];
  timestamp: string;
  model: string | undefined;
  token_count: number | undefined;
  content: string | undefined;
  blocks: ContentBlock[] | undefined;
  tool_call_id?: string;
  tool_name?: string;
  is_error?: boolean;
}

function asRawTool(m: Message): RawToolMessage {
  // SAFETY: the export only emits tool messages after the caller filters by type.
  return m as RawToolMessage;
}

/**
 * Fold standalone role:"tool" messages back into the preceding assistant's
 * matching tool_call block, preserving the is_error flag from the raw payload.
 * Any tool message that cannot be matched is kept in place so downstream
 * formatters can still surface it (with tool_name + is_error).
 */
export function normalizeForExport(messages: Message[]): Message[] {
  const out: Message[] = [];
  for (const m of messages) {
    if (m.role === "tool" && m.tool_call_id) {
      const raw = asRawTool(m);
      let attached = false;
      for (let i = out.length - 1; i >= 0; i--) {
        const prev = out[i];
        if (prev.role !== "assistant" || !prev.blocks) continue;
        let modified = false;
        const blocks = prev.blocks.map((b) => {
          if (b.type === "tool_call" && b.id === m.tool_call_id && !b.result) {
            modified = true;
            return {
              ...b,
              status: "done" as const,
              result: {
                tool_call_id: m.tool_call_id!,
                content: m.content ?? "",
                is_error: Boolean(raw.is_error),
              },
            };
          }
          return b;
        });
        if (modified) {
          out[i] = { ...prev, blocks };
          attached = true;
          break;
        }
      }
      if (attached) continue;
    }
    out.push(m);
  }
  return out;
}

export function messagesToJSONL(messages: Message[], meta: ExportMeta): string {
  const normalized = normalizeForExport(messages);
  const header = {
    type: "session_meta",
    session_id: meta.session.id,
    agent_id: meta.session.agent_id,
    agent_name: meta.agentName,
    title: meta.session.title,
    kind: meta.session.kind,
    channel: meta.session.channel,
    exported_at: meta.exportedAt,
    message_count: normalized.length,
  };
  const lines = [JSON.stringify(header)];
  for (const m of normalized) {
    const base: ExportLine = {
      type: "message",
      id: m.id,
      role: m.role,
      timestamp: m.timestamp,
      model: m.model,
      token_count: m.token_count,
      content: m.content,
      blocks: m.blocks,
    };
    if (m.role === "tool") {
      const raw = asRawTool(m);
      base.tool_call_id = m.tool_call_id;
      base.tool_name = raw.tool_name;
      base.is_error = Boolean(raw.is_error);
    } else if (m.tool_call_id) {
      base.tool_call_id = m.tool_call_id;
    }
    lines.push(JSON.stringify(base));
  }
  return lines.join("\n") + "\n";
}

export function messagesToMarkdown(messages: Message[], meta: ExportMeta): string {
  const normalized = normalizeForExport(messages);
  const out: string[] = [];
  out.push(`# Session: ${meta.session.title || meta.session.id}`);
  out.push("");
  out.push(`- **Session ID**: \`${meta.session.id}\``);
  out.push(`- **Agent**: ${meta.agentName} (\`${meta.session.agent_id}\`)`);
  if (meta.session.kind) out.push(`- **Kind**: ${meta.session.kind}`);
  if (meta.session.channel) out.push(`- **Channel**: ${meta.session.channel}`);
  out.push(`- **Exported At**: ${meta.exportedAt}`);
  out.push(`- **Messages**: ${normalized.length}`);
  out.push("");
  out.push("---");
  out.push("");

  for (const m of normalized) {
    const tsLabel = m.timestamp ? ` · ${m.timestamp}` : "";
    const tags: string[] = [];
    if (m.model) tags.push(`model: \`${m.model}\``);
    if (m.token_count !== undefined) tags.push(`tokens: ${m.token_count}`);
    const metaSuffix = tags.length ? ` _(${tags.join(", ")})_` : "";

    if (m.role === "tool") {
      const raw = asRawTool(m);
      const name = raw.tool_name?.trim() || "unknown";
      const errorMark = raw.is_error ? " (ERROR)" : "";
      out.push(`## Tool Result: \`${name}\`${errorMark}${tsLabel}${metaSuffix}`);
      out.push("");
      if (m.tool_call_id) out.push(`_tool_call_id: \`${m.tool_call_id}\`_`);
      out.push("");
      if (m.content) {
        out.push(fencedBlock(m.content));
        out.push("");
      }
      continue;
    }

    out.push(`## ${roleHeading(m.role)}${tsLabel}${metaSuffix}`);
    out.push("");
    const blocks = m.blocks ?? (m.content ? [{ type: "text" as const, text: m.content }] : []);
    for (const block of blocks) {
      renderBlock(block, out);
    }
    out.push("");
  }

  return out.join("\n");
}

function roleHeading(role: Message["role"]): string {
  switch (role) {
    case "user":
      return "User";
    case "assistant":
      return "Assistant";
    case "tool":
      return "Tool Result";
    default:
      return role;
  }
}

function renderBlock(block: ContentBlock, out: string[]): void {
  if (block.type === "text") {
    if (block.text) out.push(block.text);
    out.push("");
    return;
  }
  if (block.type === "thinking") {
    if (block.redacted) {
      out.push("> _[thinking redacted]_");
    } else if (block.thinking) {
      out.push("<details><summary>thinking</summary>");
      out.push("");
      out.push(block.thinking);
      out.push("");
      out.push("</details>");
    }
    out.push("");
    return;
  }
  if (block.type === "tool_call") {
    const name = block.name ?? "(unknown)";
    const status = block.status ? ` _[${block.status}]_` : "";
    out.push(`### Tool: \`${name}\`${status}`);
    out.push("");
    out.push("**Arguments**:");
    out.push(fencedBlock(JSON.stringify(block.arguments ?? {}, null, 2), "json"));
    out.push("");
    if (block.result) {
      const errorMark = block.result.is_error ? " (ERROR)" : "";
      out.push(`**Result**${errorMark}:`);
      out.push(fencedBlock(block.result.content ?? ""));
      out.push("");
    }
  }
}

function fencedBlock(content: string, lang: string = ""): string {
  const fence = pickFence(content);
  return `${fence}${lang}\n${content}\n${fence}`;
}

function pickFence(content: string): string {
  let longest = 0;
  const re = /`{3,}/g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(content)) !== null) {
    if (m[0].length > longest) longest = m[0].length;
  }
  return "`".repeat(Math.max(3, longest + 1));
}

export function exportFileName(session: Session, ext: "jsonl" | "md"): string {
  const shortId = session.id.slice(0, 8);
  const date = new Date(session.last_active || session.created_at || Date.now())
    .toISOString()
    .slice(0, 10)
    .replace(/-/g, "");
  return `stella-session-${shortId}-${date}.${ext}`;
}

export function downloadTextFile(filename: string, content: string, mimeType: string): void {
  const blob = new Blob([content], { type: `${mimeType};charset=utf-8` });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 0);
}

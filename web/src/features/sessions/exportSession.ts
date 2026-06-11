import type { ContentBlock, Message, Session } from "@/lib/types";

export interface ExportMeta {
  session: Session;
  agentName: string;
  exportedAt: string;
}

export function messagesToJSONL(messages: Message[], meta: ExportMeta): string {
  const header = {
    type: "session_meta",
    session_id: meta.session.id,
    agent_id: meta.session.agent_id,
    agent_name: meta.agentName,
    title: meta.session.title,
    kind: meta.session.kind,
    channel: meta.session.channel,
    exported_at: meta.exportedAt,
    message_count: messages.length,
  };
  const lines = [JSON.stringify(header)];
  for (const m of messages) {
    lines.push(
      JSON.stringify({
        type: "message",
        id: m.id,
        role: m.role,
        timestamp: m.timestamp,
        model: m.model,
        token_count: m.token_count,
        tool_call_id: m.tool_call_id,
        content: m.content,
        blocks: m.blocks,
      }),
    );
  }
  return lines.join("\n") + "\n";
}

export function messagesToMarkdown(messages: Message[], meta: ExportMeta): string {
  const out: string[] = [];
  out.push(`# Session: ${meta.session.title || meta.session.id}`);
  out.push("");
  out.push(`- **Session ID**: \`${meta.session.id}\``);
  out.push(`- **Agent**: ${meta.agentName} (\`${meta.session.agent_id}\`)`);
  if (meta.session.kind) out.push(`- **Kind**: ${meta.session.kind}`);
  if (meta.session.channel) out.push(`- **Channel**: ${meta.session.channel}`);
  out.push(`- **Exported At**: ${meta.exportedAt}`);
  out.push(`- **Messages**: ${messages.length}`);
  out.push("");
  out.push("---");
  out.push("");

  for (const m of messages) {
    const roleLabel = roleHeading(m.role);
    const tsLabel = m.timestamp ? ` · ${m.timestamp}` : "";
    const tags: string[] = [];
    if (m.model) tags.push(`model: \`${m.model}\``);
    if (typeof m.token_count === "number") tags.push(`tokens: ${m.token_count}`);
    const metaSuffix = tags.length ? ` _(${tags.join(", ")})_` : "";

    out.push(`## ${roleLabel}${tsLabel}${metaSuffix}`);
    out.push("");

    const blocks = m.blocks ?? (m.content ? [{ type: "text" as const, text: m.content }] : []);
    if (blocks.length === 0 && m.role === "tool" && m.content) {
      out.push(fencedBlock(m.content));
      out.push("");
      continue;
    }

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

import { forwardRef, useState, useEffect } from "react";
import { MarkdownPreview } from "@/components/MarkdownPreview";
import type { Message, ContentBlock } from "@/lib/types";
import { formatTime } from "@/lib/time";
import { normalizeGeneratedToolName } from "@/lib/tool-names";
import { cn } from "@/lib/utils";
import { ChevronDown, Terminal, Brain, FileText } from "lucide-react";

interface Props {
  messages: Message[];
  messagesLoading: boolean;
  onScroll: () => void;
  agentId: string;
  sessionId: string;
}

export const Transcript = forwardRef<HTMLDivElement, Props>(function Transcript(
  { messages, messagesLoading, onScroll, agentId, sessionId },
  ref,
) {
  const processed = processMessages(messages);

  return (
    <div
      ref={ref}
      className="stella-transcript-scroll flex-1 overflow-y-auto px-4 py-5 sm:px-8 sm:py-7"
      onScroll={onScroll}
    >
      {messagesLoading && messages.length > 0 && (
        <div className="flex items-center justify-center gap-2 mb-4">
          <div className="w-3 h-3 border border-muted-foreground/30 border-t-muted-foreground rounded-full animate-spin" />
          <span className="text-[10px] font-mono text-muted-foreground">
            Loading earlier messages…
          </span>
        </div>
      )}
      {messages.length === 0 && !messagesLoading && (
        <div className="text-center py-16">
          <p className="text-xs text-muted-foreground font-mono">Empty session.</p>
        </div>
      )}
      <div className="mx-auto max-w-4xl space-y-5">
        {processed.map((msg, idx) => (
          <div key={idx} className={cn(msg.sameRoleAsPrev ? "-mt-1" : "")}>
            {msg.role === "user" && (
              <UserMessage msg={msg} agentId={agentId} sessionId={sessionId} />
            )}
            {msg.role === "assistant" && <AssistantMessage msg={msg} />}
          </div>
        ))}
      </div>
    </div>
  );
});

function UserMessage({
  msg,
  agentId,
  sessionId,
}: {
  msg: ProcessedMessage;
  agentId: string;
  sessionId: string;
}) {
  const { files, text } = parseFileRefs(extractUserText(msg));
  const images = files.filter(isImagePath);
  const otherFiles = files.filter((f) => !isImagePath(f));

  return (
    <div className="flex justify-end">
      <div className="max-w-[80%] min-w-0 flex flex-col items-end gap-1.5">
        {images.length > 0 && (
          <div className="flex flex-wrap justify-end gap-1.5">
            {images.map((path, i) => {
              const url = workspaceFileURL(agentId, sessionId, path);
              return (
                <a
                  key={i}
                  href={url}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="block overflow-hidden rounded-2xl border border-primary/15 dark:border-primary/25 shadow-2xs"
                >
                  <img
                    src={url}
                    alt={basename(path)}
                    className="max-h-60 max-w-full object-cover"
                    loading="lazy"
                  />
                </a>
              );
            })}
          </div>
        )}
        {otherFiles.length > 0 && (
          <div className="flex flex-wrap justify-end gap-1.5">
            {otherFiles.map((path, i) => (
              <a
                key={i}
                href={workspaceFileURL(agentId, sessionId, path)}
                target="_blank"
                rel="noopener noreferrer"
                className="inline-flex items-center gap-1.5 rounded-full border border-primary/20 bg-primary/5 px-3 py-1 text-[11px] font-mono text-primary max-w-64"
              >
                <FileText className="size-3 shrink-0" />
                <span className="truncate">{basename(path)}</span>
              </a>
            ))}
          </div>
        )}
        {text && (
          <div className="rounded-[20px] rounded-tr-[4px] bg-primary/[0.04] dark:bg-primary/[0.08] border border-primary/10 dark:border-primary/20 px-4 py-2.5 text-foreground break-all shadow-2xs">
            <MarkdownPreview content={text} className="[&_*]:text-foreground" />
          </div>
        )}
        {msg.showTimestamp && (
          <div className="flex items-center justify-end gap-2 text-[10px] font-mono text-muted-foreground/40 mt-1 pr-1">
            <span>{formatTime(msg.timestamp)}</span>
            {(msg.token_count ?? 0) > 0 && (
              <span className="text-primary/40">{msg.token_count!.toLocaleString()} tok</span>
            )}
          </div>
        )}
      </div>
    </div>
  );
}

type GroupedBlock =
  | { type: "text"; block: ContentBlock }
  | { type: "steps"; blocks: ContentBlock[] };

function groupBlocks(blocks: ContentBlock[]): GroupedBlock[] {
  const result: GroupedBlock[] = [];
  let currentSteps: ContentBlock[] = [];

  for (const block of blocks) {
    if (block.type === "thinking" || block.type === "tool_call") {
      currentSteps.push(block);
    } else {
      if (currentSteps.length > 0) {
        result.push({ type: "steps", blocks: currentSteps });
        currentSteps = [];
      }
      result.push({ type: "text", block });
    }
  }

  if (currentSteps.length > 0) {
    result.push({ type: "steps", blocks: currentSteps });
  }

  return result;
}

function AssistantMessage({ msg }: { msg: ProcessedMessage }) {
  const grouped = groupBlocks(msg.blocks ?? []);

  return (
    <div className="min-w-0 flex-1 space-y-3">
      {grouped.map((item, gi) => {
        if (item.type === "text") {
          return <BlockRenderer key={gi} block={item.block} />;
        } else {
          return <StepsGroup key={gi} blocks={item.blocks} />;
        }
      })}
      {msg.showTimestamp && (
        <div className="flex items-center gap-2 text-[10px] font-mono text-muted-foreground/30 mt-1 pl-1">
          {msg.model && <span>{msg.model}</span>}
          <span>{formatTime(msg.timestamp)}</span>
          {(msg.token_count ?? 0) > 0 && (
            <span className="text-primary/40">{msg.token_count!.toLocaleString()} tok</span>
          )}
        </div>
      )}
    </div>
  );
}

function BlockRenderer({ block }: { block: ContentBlock }) {
  if (block.type === "text")
    return <MarkdownPreview content={block.text} className="px-1 leading-relaxed" />;
  return null;
}

function StepsGroup({ blocks }: { blocks: ContentBlock[] }) {
  const hasRunning = blocks.some((b) => b.type === "tool_call" && !b.result);
  const [expanded, setExpanded] = useState(hasRunning);

  useEffect(() => {
    if (hasRunning) {
      setExpanded(true);
    }
  }, [hasRunning]);

  const labelText = hasRunning
    ? "Thinking..."
    : `Worked for ${Math.max(1, Math.round(blocks.length * 0.8))}s`;

  if (!expanded) {
    return (
      <button
        onClick={() => setExpanded(true)}
        className="flex items-center gap-1.5 px-3 py-1.5 rounded-full border border-border/40 bg-muted/20 hover:bg-muted/30 transition-all font-mono text-xs text-muted-foreground hover:text-foreground cursor-pointer w-fit shadow-2xs"
      >
        <Brain className="size-3.5 text-primary/60 shrink-0" />
        <span>{labelText}</span>
        <ChevronDown className="size-3.5 text-muted-foreground/45 shrink-0 ml-0.5" />
      </button>
    );
  }

  return (
    <div className="rounded-[18px] border border-border/50 bg-muted/15 px-4 py-3.5 transition-all duration-200 space-y-3.5 max-w-3xl w-full shadow-2xs">
      <div className="flex items-center">
        <button
          onClick={() => setExpanded(false)}
          className="flex items-center gap-1.5 font-mono text-xs text-muted-foreground hover:text-foreground cursor-pointer font-semibold"
        >
          <Brain className="size-3.5 text-primary/60 shrink-0" />
          <span>{labelText}</span>
          <ChevronDown className="size-3.5 transition-transform duration-200 text-muted-foreground/50 rotate-180" />
        </button>
      </div>

      <div className="space-y-4 pt-2 border-t border-border/20">
        {blocks.map((block, idx) => {
          if (block.type === "thinking" && block.thinking) {
            return (
              <div key={idx} className="flex gap-2.5 items-start py-0.5">
                <span className="flex items-center justify-center text-primary/50 mt-1 shrink-0">
                  <Brain className="size-3.5" />
                </span>
                <div className="text-xs text-muted-foreground/75 leading-relaxed whitespace-pre-wrap border-l border-border/40 pl-3">
                  {block.thinking}
                </div>
              </div>
            );
          }
          if (block.type === "tool_call") {
            return <ToolStepRow key={idx} block={block} />;
          }
          return null;
        })}
      </div>
    </div>
  );
}

function ToolStepRow({ block }: { block: ContentBlock & { type: "tool_call" } }) {
  const [open, setOpen] = useState(false);
  const n = normalizeGeneratedToolName(block.name ?? "tool");
  const args = block.arguments ?? {};

  let cmdPreview = "";
  if (n === "bash") {
    cmdPreview = toolArgText(args.command ?? args.input ?? args);
  } else if (n === "read" || n === "write" || n === "edit") {
    cmdPreview = toolArgText(args.path ?? args.file_path ?? args.input);
    const pts = cmdPreview.split("/");
    cmdPreview = pts.length > 2 ? "…/" + pts.slice(-2).join("/") : cmdPreview;
  } else {
    cmdPreview = JSON.stringify(args);
  }

  const isFileTool = n === "read" || n === "write" || n === "edit";

  return (
    <div className="space-y-2 py-1">
      <button
        onClick={() => setOpen(!open)}
        className={cn(
          "flex items-center gap-2 px-3 py-1.5 rounded-lg border border-border/40 bg-card hover:bg-muted/40 hover:border-border/60 transition-all cursor-pointer font-mono text-[11px] text-muted-foreground hover:text-foreground shadow-2xs w-fit max-w-full min-w-0",
          open && "border-primary/20 bg-muted/20",
        )}
      >
        <span className="flex items-center justify-center text-muted-foreground/60 shrink-0">
          {isFileTool ? <FileText className="size-3.5" /> : <Terminal className="size-3.5" />}
        </span>
        <span
          className={cn(
            "px-1.5 py-0.5 rounded text-[9px] font-semibold uppercase tracking-wider font-sans shrink-0",
            n === "bash"
              ? "bg-amber-500/10 text-amber-600 dark:text-amber-400"
              : "bg-primary/10 text-primary",
          )}
        >
          {n}
        </span>
        <span className="truncate text-foreground/90 font-medium">{cmdPreview}</span>
        <span className="text-[9px] text-muted-foreground/40 shrink-0 ml-0.5">
          {open ? "▾" : "▸"}
        </span>
      </button>

      {open && (
        <div className="space-y-1.5 border border-border/40 rounded-lg p-2.5 bg-muted/20 font-mono text-[10px] max-w-full overflow-hidden">
          <div className="bg-muted/50 rounded overflow-hidden p-2 border border-border/20">
            <div className="text-[9px] text-muted-foreground/50 border-b border-border/20 pb-1 mb-1.5 flex items-center gap-1.5">
              <Terminal className="size-2.5" />
              <span>{n} input</span>
            </div>
            <pre className="whitespace-pre-wrap max-h-56 overflow-y-auto leading-relaxed text-muted-foreground/90 break-all">
              {toolArgText(args.command ?? args.input ?? args)}
            </pre>
          </div>

          {block.result && (
            <div
              className={cn(
                "rounded overflow-hidden p-2 border",
                block.result.is_error
                  ? "border-destructive/20 bg-destructive/5"
                  : "border-success/20 bg-success/5",
              )}
            >
              <div
                className={cn(
                  "text-[9px] border-b border-border/20 pb-1 mb-1.5 flex items-center gap-1.5",
                  block.result.is_error ? "text-destructive" : "text-success",
                )}
              >
                <span>{block.result.is_error ? "✕" : "✓"}</span>
                <span>{block.result.is_error ? "Error output" : "Result output"}</span>
              </div>
              <pre
                className={cn(
                  "whitespace-pre-wrap max-h-56 overflow-y-auto leading-relaxed break-all",
                  block.result.is_error ? "text-destructive/80" : "text-muted-foreground/75",
                )}
              >
                {block.result.content || "(empty)"}
              </pre>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function toolArgText(value: unknown): string {
  if (value === undefined || value === null) return "";
  if (typeof value === "string") return value;
  return JSON.stringify(value, null, 2);
}

const IMAGE_EXT = /\.(png|jpe?g|gif|webp|svg|bmp|avif)$/i;

function isImagePath(path: string): boolean {
  return IMAGE_EXT.test(path);
}

function basename(path: string): string {
  return path.split("/").pop() || path;
}

function workspaceFileURL(agentId: string, sessionId: string, path: string): string {
  return `/api/agents/${encodeURIComponent(agentId)}/sessions/${encodeURIComponent(
    sessionId,
  )}/workspace/file-content?path=${encodeURIComponent(path)}&raw=true`;
}

// parseFileRefs splits a user message into the `[file: path]` attachments the
// composer injected and the remaining prose, so attachments render as previews
// instead of raw text.
function parseFileRefs(input: string): { files: string[]; text: string } {
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

function extractUserText(msg: { content?: string }): string {
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

interface ProcessedMessage extends Message {
  showTimestamp: boolean;
  sameRoleAsPrev: boolean;
}

function mergeConsecutiveMessages(messages: Message[]): Message[] {
  const result: Message[] = [];

  for (const msg of messages) {
    if (
      result.length > 0 &&
      result[result.length - 1].role === "assistant" &&
      msg.role === "assistant"
    ) {
      const prev = result[result.length - 1];
      const prevBlocks = prev.blocks ?? [];
      const currBlocks = msg.blocks ?? [];

      let mergedBlocks = [...prevBlocks];
      if (currBlocks.length > 0) {
        mergedBlocks.push(...currBlocks);
      } else if (msg.content) {
        mergedBlocks.push({ type: "text", text: msg.content });
      }

      result[result.length - 1] = {
        ...prev,
        blocks: mergedBlocks,
        token_count: (prev.token_count ?? 0) + (msg.token_count ?? 0),
        timestamp: msg.timestamp,
        model: msg.model || prev.model,
      };
    } else {
      const blocks = msg.blocks ?? [];
      if (blocks.length === 0 && msg.content && msg.role !== "tool") {
        result.push({
          ...msg,
          blocks: [{ type: "text", text: msg.content }],
        });
      } else {
        result.push({ ...msg });
      }
    }
  }

  return result;
}

function processMessages(messages: Message[]): ProcessedMessage[] {
  const filtered = messages.filter((m) => m.role !== "tool");
  const msgs = mergeConsecutiveMessages(filtered);
  return msgs.map((msg, i) => ({
    ...msg,
    showTimestamp: i === msgs.length - 1 || msgs[i + 1].role !== msg.role,
    sameRoleAsPrev: i > 0 && msgs[i - 1].role === msg.role,
  }));
}

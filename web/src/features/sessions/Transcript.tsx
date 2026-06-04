import { forwardRef, useState, useEffect } from "react";
import { MarkdownPreview } from "@/components/MarkdownPreview";
import type { Message, ContentBlock } from "@/lib/types";
import { formatTime } from "@/lib/time";
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
      className="stella-transcript-scroll flex-1 overflow-y-auto px-4 py-6 sm:px-8 sm:py-8 bg-background"
      onScroll={onScroll}
    >
      {messagesLoading && messages.length > 0 && (
        <div className="flex items-center justify-center gap-2 mb-6">
          <div className="w-3 h-3 border border-muted-foreground/30 border-t-muted-foreground rounded-full animate-spin" />
          <span className="text-[10px] font-mono text-muted-foreground">
            Loading earlier messages…
          </span>
        </div>
      )}
      {messages.length === 0 && !messagesLoading && (
        <div className="text-center py-20">
          <p className="text-xs text-muted-foreground/50 font-mono">Empty session.</p>
        </div>
      )}
      <div className="mx-auto max-w-3xl space-y-8">
        {processed.map((msg, idx) => (
          <div
            key={idx}
            className={cn(
              msg.sameRoleAsPrev ? "-mt-2" : "",
              idx > 0 && msg.role === "user" ? "border-t border-border/40 pt-8 mt-8" : "",
            )}
          >
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
    <div className="flex justify-start">
      <div className="w-full min-w-0 flex flex-col items-start gap-3">
        {text && (
          <h1 className="text-xl sm:text-2xl font-semibold tracking-tight text-foreground/90 leading-tight">
            {text}
          </h1>
        )}

        {images.length > 0 && (
          <div className="flex flex-wrap gap-2 pt-1">
            {images.map((path, i) => {
              const url = workspaceFileURL(agentId, sessionId, path);
              return (
                <a
                  key={i}
                  href={url}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="block overflow-hidden rounded-lg border border-border hover:border-primary/40 transition-colors"
                >
                  <img
                    src={url}
                    alt={basename(path)}
                    className="max-h-56 max-w-full object-cover"
                    loading="lazy"
                  />
                </a>
              );
            })}
          </div>
        )}

        {otherFiles.length > 0 && (
          <div className="flex flex-wrap gap-2 pt-1">
            {otherFiles.map((path, i) => (
              <a
                key={i}
                href={workspaceFileURL(agentId, sessionId, path)}
                target="_blank"
                rel="noopener noreferrer"
                className="inline-flex items-center gap-2 rounded-md border border-border bg-card hover:bg-muted transition-colors px-3 py-1.5 text-xs text-secondary-foreground"
              >
                <FileText className="size-3.5 text-muted-foreground shrink-0" />
                <span className="truncate max-w-48 font-medium">{basename(path)}</span>
              </a>
            ))}
          </div>
        )}

        {msg.showTimestamp && (
          <div className="flex items-center gap-2 text-[10px] font-mono text-muted-foreground/60 mt-1">
            <span>{formatTime(msg.timestamp)}</span>
            {(msg.token_count ?? 0) > 0 && (
              <span className="text-primary/70">{msg.token_count!.toLocaleString()} tok</span>
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
    <div className="min-w-0 flex-1 space-y-4">
      {grouped.map((item, gi) => {
        if (item.type === "text") {
          return <BlockRenderer key={gi} block={item.block} />;
        } else {
          return <StepsGroup key={gi} blocks={item.blocks} />;
        }
      })}
      {msg.showTimestamp && (
        <div className="flex items-center gap-2 text-[10px] font-mono text-muted-foreground/60 mt-2 pl-0.5">
          {msg.model && (
            <span className="bg-muted border border-border/10 px-1.5 py-0.5 rounded text-foreground/75 font-medium">
              {msg.model}
            </span>
          )}
          <span>{formatTime(msg.timestamp)}</span>
          {(msg.token_count ?? 0) > 0 && (
            <span className="text-primary/70">{msg.token_count!.toLocaleString()} tok</span>
          )}
        </div>
      )}
    </div>
  );
}

function BlockRenderer({ block }: { block: ContentBlock }) {
  if (block.type === "text")
    return (
      <MarkdownPreview
        content={block.text}
        className="px-0.5 leading-relaxed text-[15px] text-foreground/90 font-sans"
      />
    );
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
        className="flex items-center gap-2 px-3 py-1.5 rounded-lg border border-border bg-muted/40 hover:bg-muted/70 hover:border-foreground/10 transition-all duration-120 font-mono text-xs text-muted-foreground hover:text-foreground cursor-pointer w-fit shadow-none"
      >
        <Brain className="size-3.5 text-primary shrink-0" />
        <span>{labelText}</span>
        <ChevronDown className="size-3.5 text-muted-foreground/60 shrink-0 ml-0.5" />
      </button>
    );
  }

  return (
    <div className="rounded-xl border border-border bg-card px-4 py-3.5 transition-all duration-120 space-y-3.5 max-w-3xl w-full shadow-none">
      <div className="flex items-center">
        <button
          onClick={() => setExpanded(false)}
          className="flex items-center gap-1.5 font-mono text-xs text-muted-foreground hover:text-foreground cursor-pointer font-semibold"
        >
          <Brain className="size-3.5 text-primary shrink-0" />
          <span>{labelText}</span>
          <ChevronDown className="size-3.5 transition-transform duration-120 text-muted-foreground/50 rotate-180" />
        </button>
      </div>

      <div className="space-y-3 pt-3 border-t border-border/60">
        {blocks.map((block, idx) => {
          if (block.type === "thinking" && block.thinking) {
            return (
              <div key={idx} className="flex gap-2.5 items-start py-0.5">
                <span className="flex items-center justify-center text-primary/60 mt-1 shrink-0">
                  <Brain className="size-3.5" />
                </span>
                <div className="text-xs text-muted-foreground/80 leading-relaxed whitespace-pre-wrap border-l border-border/60 pl-3 font-mono">
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
  const n = block.name ?? "tool";
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
          "flex items-center gap-2 px-3 py-1.5 rounded-lg border border-border bg-card hover:bg-muted hover:border-border/80 transition-colors cursor-pointer font-mono text-[11px] text-muted-foreground hover:text-foreground shadow-none w-fit max-w-full min-w-0",
          open && "border-primary/20 bg-muted/40",
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
        <span className="text-[9px] text-muted-foreground/45 shrink-0 ml-0.5">
          {open ? "▾" : "▸"}
        </span>
      </button>

      {open && (
        <div className="space-y-1.5 border border-border rounded-lg p-2.5 bg-card/20 font-mono text-[10px] max-w-full overflow-hidden">
          <div className="bg-card/40 rounded overflow-hidden p-2 border border-border">
            <div className="text-[9px] text-muted-foreground/50 border-b border-border pb-1 mb-1.5 flex items-center gap-1.5">
              <Terminal className="size-2.5" />
              <span>{n} input</span>
            </div>
            <pre className="whitespace-pre-wrap max-h-56 overflow-y-auto leading-relaxed text-muted-foreground/90 break-all font-mono">
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
                  "text-[9px] border-b border-border pb-1 mb-1.5 flex items-center gap-1.5",
                  block.result.is_error ? "text-destructive" : "text-success",
                )}
              >
                <span>{block.result.is_error ? "✕" : "✓"}</span>
                <span>{block.result.is_error ? "Error output" : "Result output"}</span>
              </div>
              <pre
                className={cn(
                  "whitespace-pre-wrap max-h-56 overflow-y-auto leading-relaxed break-all font-mono",
                  block.result.is_error ? "text-destructive/80" : "text-muted-foreground/80",
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

import { forwardRef, useState, useEffect } from "react";
import { Streamdown } from "streamdown";
import type { Message, ContentBlock } from "@/lib/types";
import { formatTime } from "@/lib/time";
import { cn } from "@/lib/utils";
import { ChevronDown, Terminal } from "lucide-react";

interface Props {
  messages: Message[];
  messagesLoading: boolean;
  onScroll: () => void;
}

export const Transcript = forwardRef<HTMLDivElement, Props>(function Transcript(
  { messages, messagesLoading, onScroll },
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
            {msg.role === "user" && <UserMessage msg={msg} />}
            {msg.role === "assistant" && <AssistantMessage msg={msg} />}
          </div>
        ))}
      </div>
    </div>
  );
});

function UserMessage({ msg }: { msg: ProcessedMessage }) {
  return (
    <div className="flex justify-end">
      <div className="max-w-[80%] min-w-0">
        <div className="rounded-[20px] rounded-tr-[4px] bg-muted/65 border border-border/50 px-4 py-2.5 text-foreground">
          <div className="prose prose-sm max-w-none text-foreground [&_*]:text-foreground">
            <Streamdown>{extractUserText(msg)}</Streamdown>
          </div>
        </div>
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
    <div className="flex items-start gap-3">
      {!msg.sameRoleAsPrev && (
        <div className="mt-0.5 flex size-8 shrink-0 items-center justify-center rounded-[11px] bg-card border border-border/80 text-foreground shadow-xs">
          <svg className="size-4 text-primary" viewBox="0 0 24 24" fill="currentColor">
            <path d="M11.645 20.91l-.007-.003-.022-.012a15.247 15.247 0 0 1-.383-.218 25.18 25.18 0 0 1-4.244-3.17C4.688 15.36 2.25 12.174 2.25 8.25 2.25 5.322 4.714 3 7.688 3A5.5 5.5 0 0 1 12 5.052 5.5 5.5 0 0 1 16.313 3c2.973 0 5.437 2.322 5.437 5.25 0 3.925-2.438 7.111-4.739 9.256a25.175 25.175 0 0 1-4.244 3.17 15.247 15.247 0 0 1-.383.219l-.022.012-.007.004-.003.001a.752.752 0 0 1-.704 0l-.003-.001Z" />
          </svg>
        </div>
      )}
      <div className={cn("min-w-0 flex-1 space-y-3", msg.sameRoleAsPrev && "pl-11")}>
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
    </div>
  );
}

function BlockRenderer({ block }: { block: ContentBlock }) {
  if (block.type === "text")
    return (
      <div className="prose prose-sm max-w-none rounded-[20px] rounded-tl-[4px] border border-border/60 bg-card/45 px-5 py-3.5 text-foreground shadow-xs">
        <Streamdown>{block.text}</Streamdown>
      </div>
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

  return (
    <div className="rounded-[14px] border border-border/50 bg-muted/15 px-4 py-3 transition-all duration-150 space-y-2.5 max-w-3xl">
      <div className="flex items-center">
        <button
          onClick={() => setExpanded((v) => !v)}
          className="flex items-center gap-1.5 font-mono text-xs text-muted-foreground/80 hover:text-foreground cursor-pointer"
        >
          <span>
            {hasRunning
              ? "Thinking..."
              : `Worked for ${Math.max(1, Math.round(blocks.length * 0.8))}s`}
          </span>
          <ChevronDown
            className={cn(
              "size-3.5 transition-transform duration-200 text-muted-foreground/50",
              expanded && "rotate-180",
            )}
          />
        </button>
      </div>

      {expanded && (
        <div className="space-y-3.5 pt-1">
          {blocks.map((block, idx) => {
            if (block.type === "thinking" && block.thinking) {
              return (
                <div
                  key={idx}
                  className="text-xs text-muted-foreground/90 leading-relaxed whitespace-pre-wrap"
                >
                  {block.thinking}
                </div>
              );
            }
            if (block.type === "tool_call") {
              return <ToolStepRow key={idx} block={block} />;
            }
            return null;
          })}
        </div>
      )}
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

  return (
    <div className="space-y-1.5">
      <button
        onClick={() => setOpen(!open)}
        className="flex items-center gap-1.5 text-xs text-muted-foreground/80 hover:text-foreground cursor-pointer font-mono"
      >
        <span>
          Ran <span className="font-semibold text-foreground/90">{n}</span> {cmdPreview}
        </span>
        <span className="text-[10px] text-muted-foreground/40">{open ? "▾" : "▸"}</span>
      </button>

      {open && (
        <div className="space-y-1.5 border border-border/40 rounded-lg p-2 bg-muted/20 font-mono text-[10px]">
          <div className="bg-muted/50 rounded overflow-hidden p-1.5 border border-border/20">
            <div className="text-[9px] text-muted-foreground/50 border-b border-border/20 pb-0.5 mb-1 flex items-center gap-1.5">
              <Terminal className="size-2.5" />
              <span>{n} input</span>
            </div>
            <pre className="whitespace-pre-wrap max-h-40 overflow-y-auto leading-relaxed text-muted-foreground/90">
              {toolArgText(args.command ?? args.input ?? args)}
            </pre>
          </div>

          {block.result && (
            <div
              className={cn(
                "rounded overflow-hidden p-1.5 border",
                block.result.is_error
                  ? "border-destructive/20 bg-destructive/5"
                  : "border-success/20 bg-success/5",
              )}
            >
              <div
                className={cn(
                  "text-[9px] border-b border-border/20 pb-0.5 mb-1 flex items-center gap-1.5",
                  block.result.is_error ? "text-destructive" : "text-success",
                )}
              >
                <span>{block.result.is_error ? "✕" : "✓"}</span>
                <span>{block.result.is_error ? "Error output" : "Result output"}</span>
              </div>
              <pre
                className={cn(
                  "whitespace-pre-wrap max-h-40 overflow-y-auto leading-relaxed",
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

function processMessages(messages: Message[]): ProcessedMessage[] {
  // Tool results are merged upstream by mergeToolResults; filter any stragglers.
  const msgs = messages.filter((m) => m.role !== "tool");
  return msgs.map((msg, i) => ({
    ...msg,
    showTimestamp: i === msgs.length - 1 || msgs[i + 1].role !== msg.role,
    sameRoleAsPrev: i > 0 && msgs[i - 1].role === msg.role,
  }));
}

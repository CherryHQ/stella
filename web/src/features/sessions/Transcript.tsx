import { forwardRef, useState } from "react";
import { Streamdown } from "streamdown";
import type { Message, ContentBlock } from "@/lib/types";
import { formatTime } from "@/lib/time";
import { cn } from "@/lib/utils";

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
    <div ref={ref} className="flex-1 overflow-y-auto px-6 py-6" onScroll={onScroll}>
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
      <div className="space-y-4">
        {processed.map((msg, idx) => (
          <div key={idx} className={cn(msg.sameRoleAsPrev ? "-mt-2" : "")}>
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
    <div className="flex gap-3 items-start">
      <div
        className={cn(
          "w-6 h-6 rounded-full shrink-0 mt-0.5 flex items-center justify-center bg-muted",
          msg.sameRoleAsPrev ? "invisible" : "",
        )}
      >
        <svg
          className="w-3.5 h-3.5 text-muted-foreground/60"
          fill="none"
          viewBox="0 0 24 24"
          strokeWidth="2"
          stroke="currentColor"
        >
          <path
            strokeLinecap="round"
            strokeLinejoin="round"
            d="M15.75 6a3.75 3.75 0 1 1-7.5 0 3.75 3.75 0 0 1 7.5 0ZM4.501 20.118a7.5 7.5 0 0 1 14.998 0A17.933 17.933 0 0 1 12 21.75c-2.676 0-5.216-.584-7.499-1.632Z"
          />
        </svg>
      </div>
      <div className="flex-1 min-w-0">
        <div className="prose prose-sm max-w-none text-foreground">
          <Streamdown>{extractUserText(msg)}</Streamdown>
        </div>
        {msg.showTimestamp && (
          <div className="flex items-center gap-2 text-[10px] font-mono text-muted-foreground/40 mt-1">
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

function AssistantMessage({ msg }: { msg: ProcessedMessage }) {
  return (
    <div className="flex gap-3">
      <div
        className={cn(
          "w-6 h-6 rounded-full shrink-0 mt-0.5 flex items-center justify-center bg-primary/10",
          msg.sameRoleAsPrev ? "invisible" : "",
        )}
      >
        <svg className="w-3.5 h-3.5 text-primary" viewBox="0 0 24 24" fill="currentColor">
          <path d="M11.645 20.91l-.007-.003-.022-.012a15.247 15.247 0 0 1-.383-.218 25.18 25.18 0 0 1-4.244-3.17C4.688 15.36 2.25 12.174 2.25 8.25 2.25 5.322 4.714 3 7.688 3A5.5 5.5 0 0 1 12 5.052 5.5 5.5 0 0 1 16.313 3c2.973 0 5.437 2.322 5.437 5.25 0 3.925-2.438 7.111-4.739 9.256a25.175 25.175 0 0 1-4.244 3.17 15.247 15.247 0 0 1-.383.219l-.022.012-.007.004-.003.001a.752.752 0 0 1-.704 0l-.003-.001Z" />
        </svg>
      </div>
      <div className="flex-1 min-w-0 space-y-2">
        {(msg.blocks ?? []).map((block, bi) => (
          <BlockRenderer key={bi} block={block} />
        ))}
        {msg.showTimestamp && (
          <div className="flex items-center gap-2 text-[10px] font-mono text-muted-foreground/30">
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
  if (block.type === "thinking") return <ThinkingBlock block={block} />;
  if (block.type === "text")
    return (
      <div className="prose prose-sm max-w-none text-foreground">
        <Streamdown>{block.text}</Streamdown>
      </div>
    );
  if (block.type === "tool_call") return <ToolCallBlock block={block} />;
  return null;
}

function ThinkingBlock({ block }: { block: ContentBlock & { type: "thinking" } }) {
  const [expanded, setExpanded] = useState(false);
  return (
    <div className="pl-3 border-l border-border">
      <button
        onClick={() => setExpanded((v) => !v)}
        className="text-[10px] font-mono text-muted-foreground hover:text-foreground cursor-pointer flex items-center gap-1 py-0.5"
      >
        <span>{expanded ? "▾" : "▸"}</span>
        <span className="italic">{block.redacted ? "thinking (redacted)" : "thinking"}</span>
        {block.thinking && !block.redacted && block.thinking.length > 100 && (
          <span className="text-muted-foreground/40">
            ~{Math.round(block.thinking.length / 4)} tok
          </span>
        )}
      </button>
      {expanded && (
        <div className="mt-1">
          <pre className="text-[10px] font-mono text-muted-foreground italic whitespace-pre-wrap max-h-48 overflow-y-auto leading-relaxed bg-muted/50 px-3 py-2 rounded">
            {block.thinking || "(redacted)"}
          </pre>
        </div>
      )}
    </div>
  );
}

function ToolCallBlock({ block }: { block: ContentBlock & { type: "tool_call" } }) {
  const [expanded, setExpanded] = useState(false);
  const colorClass = toolColor(block.name);

  return (
    <div className="pl-3 border-l-2 border-primary/20">
      <button
        onClick={() => setExpanded((v) => !v)}
        className="text-xs font-mono cursor-pointer flex items-center gap-1.5 py-0.5 w-full min-w-0"
      >
        <span className="text-muted-foreground text-[10px] shrink-0">{expanded ? "▾" : "▸"}</span>
        <span className={cn("font-semibold shrink-0", colorClass)}>{block.name}</span>
        {block.id && (
          <span className="text-muted-foreground/30 text-[10px] shrink-0">
            #{block.id.slice(-4)}
          </span>
        )}
        {!expanded && (
          <span className="text-muted-foreground/50 text-[10px] truncate min-w-0 flex-1 text-left">
            {toolPreview(block)}
          </span>
        )}
        {!expanded && block.result && (
          <span
            className={cn(
              "ml-auto shrink-0 text-[10px] font-mono",
              block.result.is_error ? "text-destructive" : "text-success",
            )}
          >
            {block.result.is_error
              ? "✕ error"
              : block.result.content
                ? `~${Math.round(block.result.content.length / 4)} tok`
                : "✓"}
          </span>
        )}
        {!expanded && !block.result && (
          <span className="ml-auto shrink-0 flex gap-0.5">
            <span
              className="w-1 h-1 rounded-full bg-primary/50 animate-bounce"
              style={{ animationDelay: "0ms" }}
            />
            <span
              className="w-1 h-1 rounded-full bg-primary/50 animate-bounce"
              style={{ animationDelay: "150ms" }}
            />
            <span
              className="w-1 h-1 rounded-full bg-primary/50 animate-bounce"
              style={{ animationDelay: "300ms" }}
            />
          </span>
        )}
      </button>

      {expanded && (
        <div className="mt-1 space-y-1.5">
          <ToolInputRenderer block={block} />
          {block.result && (
            <div
              className={cn(
                "rounded overflow-hidden border",
                block.result.is_error ? "border-destructive/30" : "border-success/20",
              )}
            >
              <div
                className={cn(
                  "px-2.5 py-1 text-[9px] font-mono border-b flex items-center gap-1.5",
                  block.result.is_error
                    ? "text-destructive border-destructive/30 bg-destructive/5"
                    : "text-success border-success/20 bg-success/5",
                )}
              >
                <span>{block.result.is_error ? "✕" : "✓"}</span>
                <span>{block.result.is_error ? "error" : "result"}</span>
                {block.result.content && (
                  <span className="ml-auto text-muted-foreground/40">
                    ~{Math.round(block.result.content.length / 4)} tok
                  </span>
                )}
              </div>
              <pre
                className={cn(
                  "text-[10px] font-mono whitespace-pre-wrap max-h-48 overflow-y-auto leading-relaxed px-3 py-2",
                  block.result.is_error ? "text-destructive/70" : "text-muted-foreground/60",
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

function ToolInputRenderer({ block }: { block: ContentBlock & { type: "tool_call" } }) {
  const n = block.name.toLowerCase();
  const args = block.arguments ?? {};

  if (n === "bash")
    return (
      <div className="bg-muted/60 rounded overflow-hidden">
        <div className="px-2.5 py-1 text-[9px] font-mono text-muted-foreground/50 border-b border-border flex gap-1.5 items-center">
          <span className="text-warning font-bold">$</span>
          <span>bash</span>
        </div>
        <pre className="text-[10px] font-mono text-warning/80 px-3 py-2 whitespace-pre-wrap overflow-x-auto leading-relaxed">
          {String(args.command ?? args.input ?? JSON.stringify(args, null, 2))}
        </pre>
      </div>
    );

  if (n === "read" || n === "write" || n === "edit")
    return (
      <div className="bg-muted rounded overflow-hidden">
        <div className="px-2.5 py-1 text-[9px] font-mono text-muted-foreground/50 border-b border-border">
          {n}
        </div>
        <div className="px-3 py-2 text-[10px] font-mono text-muted-foreground/70">
          {String(args.path ?? args.file_path ?? args.input ?? "")}
        </div>
      </div>
    );

  return (
    <div className="bg-muted rounded overflow-hidden">
      <div className="px-2.5 py-1 text-[9px] font-mono text-muted-foreground/50 border-b border-border">
        input
      </div>
      <pre className="text-[10px] font-mono text-muted-foreground/70 px-3 py-2 overflow-x-auto max-h-64 overflow-y-auto">
        {JSON.stringify(args, null, 2)}
      </pre>
    </div>
  );
}

function toolColor(name: string): string {
  const n = (name || "").toLowerCase();
  if (n === "bash") return "text-warning";
  if (n === "skill" || n === "skills") return "text-info";
  if (n === "memory") return "text-accent-foreground";
  if (n === "agent") return "text-primary";
  if (n === "read" || n === "write" || n === "edit") return "text-muted-foreground";
  return "text-primary";
}

function toolPreview(block: ContentBlock & { type: "tool_call" }): string {
  const args = block.arguments ?? {};
  const n = block.name.toLowerCase();
  const trunc = (s: string, len = 55) => (s.length > len ? s.slice(0, len) + "…" : s);
  const shortPath = (p: string) => {
    const pts = (p || "").split("/");
    return pts.length > 2 ? "…/" + pts.slice(-2).join("/") : p;
  };
  if (n === "bash") return trunc("$ " + String(args.command ?? args.input ?? ""));
  if (n === "read") return shortPath(String(args.path ?? args.file_path ?? args.input ?? ""));
  if (n === "write") return shortPath(String(args.path ?? args.file_path ?? args.input ?? ""));
  if (n === "edit") return shortPath(String(args.path ?? args.file_path ?? args.input ?? ""));
  return "";
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
  const resultsByID: Record<string, Message> = {};
  for (const msg of messages) {
    if (msg.role === "tool" && msg.tool_call_id) {
      resultsByID[msg.tool_call_id] = msg;
    }
  }
  const msgs = messages
    .filter((m) => m.role !== "tool")
    .map((msg) => {
      if (msg.role !== "assistant") return msg;
      return {
        ...msg,
        blocks: (msg.blocks ?? []).map((block) => {
          if (block.type === "tool_call" && block.id && resultsByID[block.id]) {
            const r = resultsByID[block.id];
            return {
              ...block,
              result: {
                tool_call_id: block.id,
                content: r.content ?? "",
                is_error: false,
              },
            };
          }
          return block;
        }),
      };
    });
  return msgs.map((msg, i) => ({
    ...msg,
    showTimestamp: i === msgs.length - 1 || msgs[i + 1].role !== msg.role,
    sameRoleAsPrev: i > 0 && msgs[i - 1].role === msg.role,
  }));
}

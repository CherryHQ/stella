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
    <div ref={ref} className="flex-1 overflow-y-auto bg-secondary" onScroll={onScroll}>
      <div className="mx-auto max-w-[720px] px-6 py-8">
        {messagesLoading && messages.length > 0 && (
          <div className="flex items-center justify-center gap-2 mb-6">
            <div className="w-3 h-3 border border-muted-foreground/30 border-t-muted-foreground rounded-full animate-spin" />
            <span className="text-sm text-muted-foreground">Loading earlier messages…</span>
          </div>
        )}
        {messages.length === 0 && !messagesLoading && (
          <div className="flex flex-col items-center justify-center py-32 gap-2">
            <p className="text-lg font-semibold tracking-tight text-foreground">
              What can I help with?
            </p>
            <p className="text-sm text-muted-foreground">
              Ask a question, start a task, or just say hello.
            </p>
          </div>
        )}
        <div className="flex flex-col gap-7">
          {processed.map((msg, idx) => (
            <div key={idx} className={cn(msg.sameRoleAsPrev ? "-mt-3" : "")}>
              {msg.role === "user" && <UserMessage msg={msg} />}
              {msg.role === "assistant" && <AssistantMessage msg={msg} />}
            </div>
          ))}
        </div>
      </div>
    </div>
  );
});

function UserMessage({ msg }: { msg: ProcessedMessage }) {
  return (
    <div className="flex justify-end">
      <div className="max-w-[75%] min-w-0">
        <div className="bg-background rounded-[18px] rounded-br-[4px] px-[18px] py-3">
          <div className="prose max-w-none text-foreground">
            <Streamdown>{extractUserText(msg)}</Streamdown>
          </div>
        </div>
        {msg.showTimestamp && (
          <div className="flex items-center justify-end gap-2 text-xs text-muted-foreground/40 mt-1.5 pr-1">
            <span>{formatTime(msg.timestamp)}</span>
          </div>
        )}
      </div>
    </div>
  );
}

function AssistantMessage({ msg }: { msg: ProcessedMessage }) {
  return (
    <div className="flex gap-3.5 items-start">
      {!msg.sameRoleAsPrev && (
        <div className="w-[26px] h-[26px] rounded-full shrink-0 mt-0.5 flex items-center justify-center bg-primary">
          <svg
            className="w-3.5 h-3.5 text-primary-foreground"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth="2.5"
            strokeLinecap="round"
            strokeLinejoin="round"
          >
            <path d="M12 3l1.912 5.813L20 10.5l-4.588 3.562L17.1 20.1 12 16.65 6.9 20.1l1.688-6.038L4 10.5l6.088-1.687z" />
          </svg>
        </div>
      )}
      <div className={cn("flex-1 min-w-0 pb-1", !msg.sameRoleAsPrev ? "" : "pl-[40px]")}>
        <div className="flex flex-col gap-2">
          {(msg.blocks ?? []).map((block, bi) => (
            <BlockRenderer key={bi} block={block} />
          ))}
        </div>
        {msg.showTimestamp && (
          <div className="flex items-center gap-2 text-xs text-muted-foreground/40 mt-2">
            {msg.model && <span className="font-mono">{msg.model}</span>}
            <span>{formatTime(msg.timestamp)}</span>
            {(msg.token_count ?? 0) > 0 && (
              <span className="font-mono">{msg.token_count!.toLocaleString()} tok</span>
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
      <div className="prose max-w-none text-foreground leading-[1.53]">
        <Streamdown>{block.text}</Streamdown>
      </div>
    );
  if (block.type === "tool_call") return <ToolCallBlock block={block} />;
  return null;
}

function ThinkingBlock({ block }: { block: ContentBlock & { type: "thinking" } }) {
  const [expanded, setExpanded] = useState(false);
  return (
    <div>
      <button
        onClick={() => setExpanded((v) => !v)}
        className="flex items-center gap-2 px-3 py-[7px] bg-background rounded-lg text-[13px] text-muted-foreground hover:bg-accent cursor-pointer transition-colors"
      >
        <span className="text-[10px] w-3 text-center shrink-0">{expanded ? "▾" : "▸"}</span>
        <span className="italic">{block.redacted ? "Thinking (redacted)" : "Thinking"}</span>
        {block.thinking && !block.redacted && block.thinking.length > 100 && (
          <span className="font-mono text-[11px] text-muted-foreground/40">
            ~{Math.round(block.thinking.length / 4)} tok
          </span>
        )}
      </button>
      {expanded && (
        <pre className="mt-1.5 text-[11px] font-mono text-muted-foreground/60 italic whitespace-pre-wrap max-h-48 overflow-y-auto leading-relaxed bg-background px-3 py-2.5 rounded-lg">
          {block.thinking || "(redacted)"}
        </pre>
      )}
    </div>
  );
}

function ToolCallBlock({ block }: { block: ContentBlock & { type: "tool_call" } }) {
  const [expanded, setExpanded] = useState(false);
  const colorClass = toolColor(block.name ?? "");

  return (
    <div>
      <button
        onClick={() => setExpanded((v) => !v)}
        className="flex items-center gap-2 px-3 py-[7px] bg-background rounded-lg text-[13px] cursor-pointer hover:bg-accent transition-colors w-full min-w-0"
      >
        <span className="text-[10px] text-muted-foreground w-3 text-center shrink-0">
          {expanded ? "▾" : "▸"}
        </span>
        <span className={cn("font-mono font-semibold shrink-0", colorClass)}>{block.name}</span>
        {!expanded && (
          <span className="font-mono text-xs text-muted-foreground/50 truncate min-w-0 flex-1 text-left">
            {toolPreview(block)}
          </span>
        )}
        {!expanded && block.result && (
          <span
            className={cn(
              "ml-auto shrink-0 text-[11px] font-mono",
              block.result.is_error ? "text-destructive" : "text-[#34a853]",
            )}
          >
            {block.result.is_error ? "✕ error" : "✓"}
          </span>
        )}
        {!expanded && !block.result && (
          <span className="ml-auto shrink-0 flex gap-[3px] items-center">
            <span className="w-1 h-1 rounded-full bg-primary animate-[pulse-dot_1.2s_ease-in-out_infinite]" />
            <span className="w-1 h-1 rounded-full bg-primary animate-[pulse-dot_1.2s_ease-in-out_0.15s_infinite]" />
            <span className="w-1 h-1 rounded-full bg-primary animate-[pulse-dot_1.2s_ease-in-out_0.3s_infinite]" />
          </span>
        )}
      </button>

      {expanded && (
        <div className="mt-1.5 space-y-1.5">
          <ToolInputRenderer block={block} />
          {block.result && (
            <div
              className={cn(
                "rounded-lg overflow-hidden border",
                block.result.is_error ? "border-destructive/20" : "border-[#34a853]/20",
              )}
            >
              <div
                className={cn(
                  "px-3 py-1.5 text-[11px] font-mono border-b flex items-center gap-1.5",
                  block.result.is_error
                    ? "text-destructive border-destructive/20 bg-destructive/5"
                    : "text-[#34a853] border-[#34a853]/20 bg-[#34a853]/5",
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
                  "text-[11px] font-mono whitespace-pre-wrap max-h-48 overflow-y-auto leading-relaxed px-3 py-2.5",
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

function toolArgText(value: unknown): string {
  if (value === undefined || value === null) return "";
  if (typeof value === "string") return value;
  return JSON.stringify(value, null, 2);
}

function ToolInputRenderer({ block }: { block: ContentBlock & { type: "tool_call" } }) {
  const n = (block.name ?? "").toLowerCase();
  const args = block.arguments ?? {};

  if (n === "bash")
    return (
      <div className="bg-card rounded-[11px] overflow-hidden">
        <div className="px-3.5 py-2 text-xs font-mono text-muted-foreground/50 border-b border-border flex gap-1.5 items-center">
          <span className="text-[#b07d2b] font-bold">$</span>
          <span>bash</span>
        </div>
        <pre className="text-[13px] font-mono text-foreground/80 px-3.5 py-2.5 whitespace-pre-wrap overflow-x-auto leading-relaxed">
          {toolArgText(args.command ?? args.input ?? args)}
        </pre>
      </div>
    );

  if (n === "read" || n === "write" || n === "edit")
    return (
      <div className="bg-card rounded-[11px] overflow-hidden">
        <div className="px-3.5 py-2 text-xs font-mono text-muted-foreground/50 border-b border-border">
          {n}
        </div>
        <div className="px-3.5 py-2.5 text-[13px] font-mono text-muted-foreground/70">
          {toolArgText(args.path ?? args.file_path ?? args.input)}
        </div>
      </div>
    );

  return (
    <div className="bg-card rounded-[11px] overflow-hidden">
      <div className="px-3.5 py-2 text-xs font-mono text-muted-foreground/50 border-b border-border">
        input
      </div>
      <pre className="text-[11px] font-mono text-muted-foreground/70 px-3.5 py-2.5 overflow-x-auto max-h-64 overflow-y-auto">
        {JSON.stringify(args, null, 2)}
      </pre>
    </div>
  );
}

function toolColor(name: string): string {
  const n = (name || "").toLowerCase();
  if (n === "bash") return "text-[#b07d2b]";
  if (n === "skill" || n === "skills") return "text-primary";
  if (n === "agent") return "text-primary";
  if (n === "read" || n === "write" || n === "edit") return "text-muted-foreground";
  return "text-primary";
}

function toolPreview(block: ContentBlock & { type: "tool_call" }): string {
  const args = block.arguments ?? {};
  const n = (block.name ?? "").toLowerCase();
  const trunc = (s: string, len = 55) => (s.length > len ? s.slice(0, len) + "…" : s);
  const shortPath = (p: string) => {
    const pts = (p || "").split("/");
    return pts.length > 2 ? "…/" + pts.slice(-2).join("/") : p;
  };
  if (n === "bash") return trunc("$ " + toolArgText(args.command ?? args.input));
  if (n === "read") return shortPath(toolArgText(args.path ?? args.file_path ?? args.input));
  if (n === "write") return shortPath(toolArgText(args.path ?? args.file_path ?? args.input));
  if (n === "edit") return shortPath(toolArgText(args.path ?? args.file_path ?? args.input));
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
  const msgs = messages.filter((m) => m.role !== "tool");
  return msgs.map((msg, i) => ({
    ...msg,
    showTimestamp: i === msgs.length - 1 || msgs[i + 1].role !== msg.role,
    sameRoleAsPrev: i > 0 && msgs[i - 1].role === msg.role,
  }));
}

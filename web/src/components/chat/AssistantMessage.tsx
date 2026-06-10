import { useState, useEffect, useRef } from "react";
import { MarkdownPreview } from "@/components/MarkdownPreview";
import type { ContentBlock } from "@/lib/types";
import { formatTime } from "@/lib/time";
import { cn } from "@/lib/utils";
import { Terminal, Brain, FileText } from "lucide-react";
import { getAgentColor } from "@/lib/agent-colors";
import { CollapsibleThinking } from "./CollapsibleThinking";
import { SessionTrace } from "./SessionTrace";

export interface AssistantMessageProps {
  agentName: string;
  agentId: string;
  blocks: ContentBlock[];
  timestamp?: string;
  model?: string;
  tokenCount?: number;
  streaming?: boolean;
  showTimestamp?: boolean;
  sameRoleAsPrev?: boolean;
  agentSessionId?: string;
}

export function AssistantMessage({
  agentName,
  agentId,
  blocks,
  timestamp,
  model,
  tokenCount,
  streaming,
  showTimestamp,
  sameRoleAsPrev,
  agentSessionId,
}: AssistantMessageProps) {
  const color = getAgentColor(agentId);
  const grouped = groupBlocks(blocks);

  return (
    <div className="w-full min-w-0 flex flex-col gap-1.5">
      {!sameRoleAsPrev && (
        <div className="mb-2 flex items-center gap-2">
          <span
            className="grid size-5 place-items-center rounded-full text-[10px] font-bold text-primary-foreground shrink-0"
            style={{ background: color }}
          >
            {agentName[0]?.toUpperCase()}
          </span>
          <span className="text-xs font-semibold text-foreground">{agentName}</span>
          {streaming && (
            <span className="inline-flex items-center gap-1">
              <span className="size-1.5 animate-pulse rounded-full bg-primary" />
            </span>
          )}
          {timestamp && !streaming && (
            <span className="font-mono text-[10px] text-muted-foreground/50">
              {formatTime(timestamp)}
            </span>
          )}
        </div>
      )}
      <div className="pl-7 space-y-4">
        {grouped.map((item, gi) => {
          if (item.type === "text") {
            return <BlockRenderer key={gi} block={item.block} />;
          } else {
            const hasFinalOutputAfter = grouped.slice(gi + 1).some((next) => next.type === "text");
            return (
              <StepsGroup
                key={gi}
                blocks={item.blocks}
                active={Boolean(streaming && !hasFinalOutputAfter)}
              />
            );
          }
        })}
        {blocks.length === 0 && streaming && (
          <span className="inline-block size-2 animate-pulse rounded-full bg-muted-foreground/30" />
        )}
        {agentSessionId && !streaming && !blocks.some((b) => b.type === "tool_call") && (
          <SessionTrace
            agentId={agentId}
            agentName={agentName}
            sessionId={agentSessionId}
            matchContent={blocks
              .filter((b) => b.type === "text")
              .map((b) => (b as { text: string }).text)
              .join("")}
          />
        )}
        {showTimestamp && (
          <div className="flex items-center gap-2 text-[10px] font-mono text-muted-foreground/60 mt-2">
            {model && (
              <span className="bg-muted border border-border/10 px-1.5 py-0.5 rounded text-foreground/75 font-medium">
                {model}
              </span>
            )}
            {timestamp && sameRoleAsPrev && <span>{formatTime(timestamp)}</span>}
            {(tokenCount ?? 0) > 0 && (
              <span className="text-primary/70">{tokenCount!.toLocaleString()} tok</span>
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

function StepsGroup({ blocks, active }: { blocks: ContentBlock[]; active: boolean }) {
  const hasRunning = active || blocks.some((b) => b.type === "tool_call" && !b.result);
  const [expanded, setExpanded] = useState(hasRunning);
  const wasRunningRef = useRef(hasRunning);

  useEffect(() => {
    if (hasRunning) {
      setExpanded(true);
    } else if (wasRunningRef.current) {
      setExpanded(false);
    }
    wasRunningRef.current = hasRunning;
  }, [hasRunning]);

  const labelText = hasRunning
    ? "Thinking..."
    : `Worked for ${Math.max(1, Math.round(blocks.length * 0.8))}s`;

  return (
    <CollapsibleThinking labelText={labelText} expanded={expanded} onToggle={setExpanded}>
      <div className="space-y-3">
        {blocks.map((block, idx) => {
          if (block.type === "thinking" && block.thinking) {
            return (
              <div key={idx} className="flex gap-2.5 items-start py-0.5">
                <span className="flex items-center justify-center text-primary/60 mt-1 shrink-0">
                  <Brain className="size-3.5" />
                </span>
                <div className="text-xs text-muted-foreground/80 leading-relaxed whitespace-pre-wrap break-words overflow-hidden border-l border-border/60 pl-3 font-mono min-w-0">
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
    </CollapsibleThinking>
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
        <span className="text-[9px] text-muted-foreground/65 shrink-0 ml-0.5">
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

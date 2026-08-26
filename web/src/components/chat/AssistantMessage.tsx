import { useState, useEffect, useId, useMemo, useRef } from "react";
import { MarkdownPreview } from "@/components/MarkdownPreview";
import type { ContentBlock } from "@/lib/types";
import { formatTime } from "@/lib/time";
import { useI18n } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import {
  ChevronDown,
  Copy,
  Check,
  Terminal,
  FileText,
  FilePlus2,
  FilePen,
  Users,
  Sparkles,
  Library,
  Send,
  ListTodo,
  MessagesSquare,
  Wrench,
  type LucideIcon,
} from "lucide-react";
import { getAgentAvatarStyle } from "@/lib/agent-colors";
import { CollapsibleThinking } from "./CollapsibleThinking";
import { CopyButton, REVEAL_ON_HOVER } from "./CopyButton";
import { RenderableReferenceList } from "./references";
import { EXIT_TRAILER, formatToolOutput, toolCallFailed } from "./utils";
import { SessionTrace } from "./SessionTrace";
import { Spinner } from "@/components/ui/spinner";

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
  const avatarStyle = getAgentAvatarStyle(agentId);
  const grouped = groupBlocks(blocks);
  // SAFETY: a block typed text always carries its .text string.
  const textBlocks = blocks
    .filter((b) => b.type === "text")
    .map((b) => (b as { text: string }).text);
  const copyText = textBlocks.join("\n\n");

  return (
    <div className="group w-full min-w-0 flex flex-col gap-1.5">
      {!sameRoleAsPrev && (
        <div className="mb-1.5 flex items-center gap-2">
          <span
            className="grid size-5 place-items-center rounded-full text-xs font-semibold text-primary-foreground shrink-0"
            style={avatarStyle}
          >
            {agentName[0]?.toUpperCase()}
          </span>
          <span className="text-xs font-semibold text-foreground">{agentName}</span>
          {streaming && (
            <span className="inline-flex items-center gap-1">
              <span className="size-1.5 animate-pulse rounded-full bg-info" />
            </span>
          )}
        </div>
      )}
      <div className="min-w-0 space-y-3 ml-2.5 border-l border-border pl-4">
        {grouped.map((item, gi) => {
          // Keys derive from the group's start offset in `blocks`: during a
          // stream blocks are append-only, so existing groups keep their key
          // and never remount (index keys shifted whenever a group appeared).
          if (item.type === "text") {
            return <BlockRenderer key={`g${item.start}`} block={item.block} />;
          } else {
            const hasFinalOutputAfter = grouped.slice(gi + 1).some((next) => next.type === "text");
            return (
              <StepsGroup
                key={`g${item.start}`}
                blocks={item.blocks}
                active={Boolean(streaming && !hasFinalOutputAfter)}
              />
            );
          }
        })}
        {blocks.length === 0 && streaming && (
          <span className="inline-flex items-center gap-1 py-1">
            <span className="size-1.5 animate-pulse rounded-full bg-muted-foreground/50 [animation-delay:-0.3s]" />
            <span className="size-1.5 animate-pulse rounded-full bg-muted-foreground/50 [animation-delay:-0.15s]" />
            <span className="size-1.5 animate-pulse rounded-full bg-muted-foreground/50" />
          </span>
        )}
        {agentSessionId && !streaming && !blocks.some((b) => b.type === "tool_call") && (
          <SessionTrace
            agentId={agentId}
            agentName={agentName}
            sessionId={agentSessionId}
            matchContent={textBlocks.join("")}
          />
        )}
        {!streaming &&
          (copyText || (showTimestamp && (model || timestamp || (tokenCount ?? 0) > 0))) && (
            <div
              className={cn(
                "mt-1 flex items-center gap-2 text-xs font-mono text-muted-foreground",
                REVEAL_ON_HOVER,
              )}
            >
              {copyText && <CopyButton text={copyText} className="-ml-1.5" />}
              {showTimestamp && model && (
                <span className="rounded border border-border/10 bg-muted px-1.5 py-0.5 font-medium text-foreground">
                  {model}
                </span>
              )}
              {showTimestamp && timestamp && <span>{formatTime(timestamp)}</span>}
              {showTimestamp && (tokenCount ?? 0) > 0 && (
                <span>{tokenCount!.toLocaleString()} tok</span>
              )}
            </div>
          )}
      </div>
    </div>
  );
}

// `start` is the group's first index in the source blocks array — a
// remount-stable React key, since blocks only append during a stream.
type GroupedBlock =
  | { type: "text"; block: ContentBlock; start: number }
  | { type: "steps"; blocks: ContentBlock[]; start: number };

function groupBlocks(blocks: ContentBlock[]): GroupedBlock[] {
  const result: GroupedBlock[] = [];
  let currentSteps: ContentBlock[] = [];
  let stepsStart = 0;

  for (let i = 0; i < blocks.length; i++) {
    const block = blocks[i];
    if (block.type === "thinking" || block.type === "tool_call") {
      if (currentSteps.length === 0) stepsStart = i;
      currentSteps.push(block);
    } else {
      if (currentSteps.length > 0) {
        result.push({ type: "steps", blocks: currentSteps, start: stepsStart });
        currentSteps = [];
      }
      result.push({ type: "text", block, start: i });
    }
  }

  if (currentSteps.length > 0) {
    result.push({ type: "steps", blocks: currentSteps, start: stepsStart });
  }

  return result;
}

function BlockRenderer({ block }: { block: ContentBlock }) {
  if (block.type === "text")
    return (
      <MarkdownPreview content={block.text} className="px-0.5 text-sm text-foreground font-sans" />
    );
  if (block.type === "image")
    return (
      <a
        href={block.url}
        target="_blank"
        rel="noopener noreferrer"
        className="block w-fit overflow-hidden rounded-lg border border-border hover:border-primary/40 transition-colors"
      >
        <img
          src={block.url}
          alt="Tool image output"
          className="max-h-56 max-w-full object-cover"
          loading="lazy"
        />
      </a>
    );
  return null;
}

function StepsGroup({ blocks, active }: { blocks: ContentBlock[]; active: boolean }) {
  const { t } = useI18n();
  // Drive running state purely from the parent-supplied `active` flag (which
  // already encodes `streaming && !hasFinalOutputAfter`). Do NOT fall back to
  // "any tool_call without a result" — paginated history can land in the middle
  // of a turn (assistant tool_call without its tool result row), and that
  // fallback would start a Thinking-for-Xs… timer that never stops.
  const hasRunning = active;
  const [expanded, setExpanded] = useState(hasRunning);
  const wasRunningRef = useRef(hasRunning);

  // Real wall-clock timer: starts ticking when the group first becomes active,
  // freezes at the elapsed value when streaming completes. Historical messages
  // (never active in this session) fall back to a label without seconds since
  // we don't persist the original elapsed time on the wire yet.
  const startedAtRef = useRef<number | null>(null);
  const [elapsedSec, setElapsedSec] = useState<number | null>(null);

  // Auto-expand when the group first becomes active. Do NOT auto-collapse on
  // running→done: with multi-step agentic flows, each subsequent text block
  // flips `active` to false for an earlier StepsGroup, and snapping that group
  // shut mid-stream reads as a flicker. Initial state stays driven by
  // useState(hasRunning), so historical messages still default to collapsed.
  useEffect(() => {
    if (hasRunning) {
      setExpanded(true);
      if (startedAtRef.current === null) {
        startedAtRef.current = Date.now();
      }
      const tick = () => {
        const start = startedAtRef.current;
        if (start !== null) {
          setElapsedSec(Math.max(1, Math.floor((Date.now() - start) / 1000)));
        }
      };
      tick();
      const id = window.setInterval(tick, 1000);
      wasRunningRef.current = true;
      return () => window.clearInterval(id);
    }
    if (wasRunningRef.current) {
      // Freeze the elapsed time at the moment streaming completed.
      const start = startedAtRef.current;
      if (start !== null) {
        setElapsedSec(Math.max(1, Math.floor((Date.now() - start) / 1000)));
      }
    }
    wasRunningRef.current = false;
  }, [hasRunning]);

  let labelText: string;
  if (hasRunning) {
    labelText =
      elapsedSec !== null
        ? t("sessions.transcript.thinkingFor", { seconds: elapsedSec })
        : t("sessions.transcript.thinking");
  } else if (elapsedSec !== null) {
    labelText = t("sessions.transcript.workedFor", { seconds: elapsedSec });
  } else {
    labelText = t("sessions.transcript.worked");
  }

  // Renderable references the agent emitted while creating entities in this
  // step group. Surfaced as cards OUTSIDE the collapsible — the raw tool output
  // defaults to collapsed, so a card buried inside would read as "not done".
  const references = blocks.flatMap((b) =>
    b.type === "tool_call" ? (b.result?.references ?? []) : [],
  );

  return (
    <div className="space-y-3">
      <CollapsibleThinking labelText={labelText} expanded={expanded} onToggle={setExpanded}>
        <div className="space-y-3">
          {blocks.map((block, idx) => {
            if (block.type === "thinking" && block.thinking) {
              return (
                <div
                  key={`t${idx}`}
                  className="py-0.5 text-xs text-muted-foreground leading-relaxed whitespace-pre-wrap break-words overflow-hidden border-l border-border/60 pl-3 font-sans min-w-0"
                >
                  {block.thinking}
                </div>
              );
            }
            if (block.type === "tool_call") {
              // Keyed by the stable tool-call id so an appended sibling never
              // remounts an existing row (which would drop its open state).
              return <ToolStepRow key={block.id || `i${idx}`} block={block} active={active} />;
            }
            return null;
          })}
        </div>
      </CollapsibleThinking>
      {references.length > 0 && <RenderableReferenceList references={references} />}
    </div>
  );
}

const TOOL_META: Record<string, { icon: LucideIcon; verb: string; surface: string }> = {
  bash: { icon: Terminal, verb: "Ran", surface: "Shell" },
  read: { icon: FileText, verb: "Read", surface: "File" },
  write: { icon: FilePlus2, verb: "Wrote", surface: "File" },
  edit: { icon: FilePen, verb: "Edited", surface: "File" },
  delegate: { icon: Users, verb: "Delegated to", surface: "Agent" },
  skills: { icon: Sparkles, verb: "Used skill", surface: "Skill" },
  memory: { icon: Library, verb: "Memory", surface: "Memory" },
  notify: { icon: Send, verb: "Notified", surface: "Message" },
  task_control: { icon: ListTodo, verb: "Task", surface: "Task" },
  session: { icon: MessagesSquare, verb: "Session", surface: "Session" },
};

// memory's verb depends on the `action` arg so the line reads as a sentence.
const MEMORY_VERBS: Record<string, string> = {
  search: "Searched memory",
  add: "Saved to memory",
  update: "Updated memory",
  delete: "Removed from memory",
};

function ToolStepRow({
  block,
  active,
}: {
  block: ContentBlock & { type: "tool_call" };
  active: boolean;
}) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const [copied, setCopied] = useState(false);
  const panelId = useId();
  const n = block.name ?? "tool";
  const args = block.arguments ?? {};
  const failed = toolCallFailed(block);
  const running = active && !block.result;

  const meta = TOOL_META[n] ?? { icon: Wrench, verb: n, surface: n };
  const Icon = meta.icon;
  let verb = meta.verb;
  const isFileTool = n === "read" || n === "write" || n === "edit";
  const isBash = n === "bash";
  const isSession = n === "session";

  let cmdPreview = "";
  if (isBash) {
    cmdPreview = toolArgText(args.command ?? args.input ?? args);
  } else if (isFileTool) {
    cmdPreview = toolArgText(args.path ?? args.file_path ?? args.input);
    const pts = cmdPreview.split("/");
    cmdPreview = pts.length > 2 ? "…/" + pts.slice(-2).join("/") : cmdPreview;
  } else if (n === "delegate") {
    cmdPreview = toolArgText(args.agent ?? args.target ?? args.to ?? args.name ?? args);
  } else if (n === "skills") {
    cmdPreview = toolArgText(args.skill ?? args.name ?? args.command ?? args);
  } else if (n === "memory") {
    const action = typeof args.action === "string" ? args.action : "";
    verb = MEMORY_VERBS[action] ?? meta.verb;
    cmdPreview = toolArgText(args.pattern ?? args.query ?? args.content ?? args.scope ?? "");
  } else if (n === "notify") {
    cmdPreview = toolArgText(args.message ?? args.text ?? args.content ?? "");
  } else if (isSession) {
    const action = typeof args.action === "string" ? args.action : "";
    if (action === "create") {
      verb = failed
        ? t("sessions.tool.createSessionFailed")
        : running
          ? t("sessions.tool.creatingSession")
          : block.result
            ? t("sessions.tool.createdSession")
            : meta.verb;
      if (!running && !block.result) cmdPreview = action;
    } else if (action === "send") {
      verb = failed
        ? t("sessions.tool.sendFailed")
        : running
          ? t("sessions.tool.waitingForReply")
          : block.result
            ? t("sessions.tool.receivedReply")
            : meta.verb;
      if (!running && !block.result) cmdPreview = action;
    } else {
      cmdPreview = firstScalarArg(args);
    }
  } else {
    // Unknown / plugin tool: show the first scalar arg, never the raw JSON blob.
    cmdPreview = firstScalarArg(args);
  }
  if (cmdPreview.length > 200) cmdPreview = cmdPreview.slice(0, 200) + "…";

  // Input/output derivation (JSON.stringify of args, regex over the full tool
  // output) is only needed once the row is expanded; collapsed rows on the
  // streaming path must stay free of per-render serialization work.
  const details = useMemo(() => {
    if (!open) return null;
    const inputText = toolArgText(
      args.command ?? args.input ?? args.path ?? args.file_path ?? args,
    );
    // The runner appends a trailing "[exit:N | Xms]" line; lift it into the
    // status footer instead of leaving it in the output body.
    let outputText = block.result?.content ?? "";
    const outputBlocks = block.result?.blocks ?? [];
    if (outputBlocks.length > 0) outputText = "";
    let duration = "";
    const exitMatch = outputText.match(EXIT_TRAILER);
    if (exitMatch) {
      outputText = outputText.slice(0, exitMatch.index).replace(/\s+$/, "");
      duration = exitMatch[2];
    }
    outputText = formatToolOutput(outputText);
    return { inputText, outputText, outputBlocks, duration, exitOk: !toolCallFailed(block) };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- args derives from block
  }, [open, block]);

  const onCopy = () => {
    if (!details) return;
    void navigator.clipboard?.writeText(details.inputText);
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1500);
  };

  return (
    <div className="py-1">
      <button
        type="button"
        aria-expanded={open}
        aria-controls={open ? panelId : undefined}
        onClick={() => setOpen(!open)}
        className={cn(
          "flex max-w-full min-w-0 cursor-pointer items-center gap-1.5 py-0.5 font-mono text-xs transition-colors",
          failed
            ? "text-destructive-foreground hover:text-destructive-foreground"
            : "text-muted-foreground hover:text-foreground",
        )}
      >
        {running ? (
          <Spinner className="size-3.5 shrink-0 text-info" />
        ) : (
          <Icon
            className={cn(
              "size-3.5 shrink-0",
              failed ? "text-destructive-foreground" : "text-muted-foreground",
            )}
          />
        )}
        <span className="truncate">
          {verb} {cmdPreview}
        </span>
        <ChevronDown
          className={cn(
            "size-3.5 shrink-0 transition-transform duration-150",
            failed ? "text-destructive-foreground" : "text-muted-foreground",
            open && "rotate-180",
          )}
        />
      </button>

      {open && (
        <div
          id={panelId}
          className="mt-1.5 rounded-xl bg-muted px-4 py-3 font-mono text-xs max-w-full overflow-hidden"
        >
          <div className="mb-1.5 flex items-center justify-between gap-2">
            <span className="text-muted-foreground">{meta.surface}</span>
            <button
              onClick={onCopy}
              title="Copy"
              className="shrink-0 text-muted-foreground hover:text-foreground transition-colors cursor-pointer"
            >
              {copied ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
            </button>
          </div>
          <pre className="whitespace-pre-wrap break-all leading-relaxed text-foreground">
            {isBash ? `$ ${details!.inputText}` : details!.inputText}
          </pre>
          {block.result && details!.outputText && (
            <pre
              className={cn(
                "mt-1 max-h-64 overflow-y-auto whitespace-pre-wrap break-all leading-relaxed",
                failed ? "text-destructive-foreground" : "text-muted-foreground",
              )}
            >
              {details!.outputText}
            </pre>
          )}
          {block.result &&
            details!.outputBlocks.map((output, index) =>
              output.type === "text" ? (
                <pre
                  key={`text-${index}`}
                  className={cn(
                    "mt-1 max-h-64 overflow-y-auto whitespace-pre-wrap break-all leading-relaxed",
                    failed ? "text-destructive-foreground" : "text-muted-foreground",
                  )}
                >
                  {formatToolOutput(output.text)}
                </pre>
              ) : (
                <a
                  key={`image-${output.media_id}-${index}`}
                  href={output.url}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="mt-2 block w-fit overflow-hidden rounded-lg border border-border hover:border-primary/40 transition-colors"
                >
                  <img
                    src={output.url}
                    alt="Tool image output"
                    className="max-h-56 max-w-full object-cover"
                    loading="lazy"
                  />
                </a>
              ),
            )}
          {block.result && (
            <div
              className={cn(
                "mt-2 text-right text-muted-foreground",
                failed && "text-destructive-foreground",
              )}
            >
              {details!.exitOk ? "✓ Success" : "✕ Failed"}
              {details!.duration && ` · ${details!.duration}`}
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

// First string/number value among a tool's args — a readable hint for plugin
// tools we don't have a bespoke preview for, instead of dumping the whole blob.
function firstScalarArg(args: Record<string, unknown>): string {
  for (const v of Object.values(args)) {
    if (typeof v === "string" && v.trim()) return v;
    if (typeof v === "number") return String(v);
  }
  return "";
}

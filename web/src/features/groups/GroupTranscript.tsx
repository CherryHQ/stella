import { forwardRef } from "react";
import { MarkdownPreview } from "@/components/MarkdownPreview";
import { formatTime } from "@/lib/time";
import { cn } from "@/lib/utils";

export interface DisplayMessage {
  id: string;
  role: "user" | "assistant";
  agentId?: string;
  agentName?: string;
  content: string;
  reasoning?: string;
  timestamp?: string;
  streaming?: boolean;
}

const AGENT_COLORS = [
  "bg-blue-500",
  "bg-emerald-500",
  "bg-purple-500",
  "bg-amber-500",
  "bg-rose-500",
  "bg-cyan-500",
];

function agentColor(agentId: string): string {
  let h = 0;
  for (let i = 0; i < agentId.length; i++) h = (h * 31 + agentId.charCodeAt(i)) & 0xffffff;
  return AGENT_COLORS[h % AGENT_COLORS.length];
}

interface Props {
  messages: DisplayMessage[];
  loading?: boolean;
  onScroll?: () => void;
}

export const GroupTranscript = forwardRef<HTMLDivElement, Props>(function GroupTranscript(
  { messages, loading, onScroll },
  ref,
) {
  return (
    <div
      ref={ref}
      className="flex-1 overflow-y-auto bg-background px-4 py-6 sm:px-8 sm:py-8"
      onScroll={onScroll}
    >
      {loading && messages.length > 0 && (
        <div className="mb-6 flex items-center justify-center gap-2">
          <div className="size-3 animate-spin rounded-full border border-muted-foreground/30 border-t-muted-foreground" />
          <span className="font-mono text-[10px] text-muted-foreground">Loading...</span>
        </div>
      )}
      {messages.length === 0 && !loading && (
        <div className="py-20 text-center">
          <p className="font-mono text-xs text-muted-foreground/60">
            No messages yet. Start a conversation!
          </p>
        </div>
      )}
      <div className="mx-auto max-w-3xl space-y-5">
        {messages.map((msg) => (
          <div key={msg.id}>
            {msg.role === "user" ? <UserBubble msg={msg} /> : <AgentBubble msg={msg} />}
          </div>
        ))}
      </div>
    </div>
  );
});

function UserBubble({ msg }: { msg: DisplayMessage }) {
  return (
    <div className="border-t border-border/40 pt-5 first:border-0 first:pt-0">
      <div className="flex items-center gap-2 mb-1.5">
        <span className="grid size-5 place-items-center rounded-full bg-foreground text-[10px] font-bold text-background">
          U
        </span>
        <span className="text-xs font-medium text-muted-foreground">You</span>
        {msg.timestamp && (
          <time className="text-[11px] text-muted-foreground/50">{formatTime(msg.timestamp)}</time>
        )}
      </div>
      <div className="pl-7 text-[15px] font-medium leading-relaxed text-foreground/90 whitespace-pre-wrap">
        {msg.content}
      </div>
    </div>
  );
}

function AgentBubble({ msg }: { msg: DisplayMessage }) {
  const name = msg.agentName || msg.agentId || "Agent";
  const colorClass = agentColor(msg.agentId || "default");

  return (
    <div className="group">
      <div className="flex items-center gap-2 mb-1.5">
        <span
          className={cn(
            "grid size-5 place-items-center rounded-full text-[10px] font-bold text-white",
            colorClass,
          )}
        >
          {name[0]?.toUpperCase()}
        </span>
        <span className="text-xs font-semibold text-foreground">{name}</span>
        {msg.timestamp && (
          <time className="text-[11px] text-muted-foreground/50">{formatTime(msg.timestamp)}</time>
        )}
        {msg.streaming && (
          <span className="inline-flex items-center gap-1">
            <span className="size-1.5 animate-pulse rounded-full bg-primary" />
          </span>
        )}
      </div>
      <div className="pl-7">
        {msg.reasoning && (
          <details className="mb-2">
            <summary className="cursor-pointer text-xs text-muted-foreground hover:text-foreground">
              Thinking...
            </summary>
            <div className="mt-1 rounded-lg bg-muted/30 p-3 text-xs text-muted-foreground">
              <MarkdownPreview content={msg.reasoning} />
            </div>
          </details>
        )}
        {msg.content ? (
          <div className="prose prose-sm max-w-none text-foreground dark:prose-invert">
            <MarkdownPreview content={msg.content} />
          </div>
        ) : msg.streaming ? (
          <span className="inline-block size-2 animate-pulse rounded-full bg-muted-foreground/30" />
        ) : null}
      </div>
    </div>
  );
}

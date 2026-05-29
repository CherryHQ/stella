import type { TFunction } from "i18next";
import { cn } from "@/lib/utils";
import type { MessageKey } from "@/lib/i18n/messages";
import type { ComponentsGoal, ComponentsTask } from "@/lib/api-client/types.gen";

export type Status =
  | "draft"
  | "ready"
  | "planning"
  | "running"
  | "blocked"
  | "reviewing"
  | "done"
  | "failed"
  | "cancelled";

interface StatusMeta {
  dot: string;
  pill: string;
  bar: string;
}

const STATUS_META: Record<string, StatusMeta> = {
  done: {
    dot: "bg-emerald-500",
    pill: "bg-emerald-500/10 text-emerald-600 border-emerald-500/25 dark:text-emerald-400",
    bar: "bg-emerald-500",
  },
  running: {
    dot: "bg-blue-500",
    pill: "bg-blue-500/10 text-blue-600 border-blue-500/25 dark:text-blue-400",
    bar: "bg-blue-500",
  },
  planning: {
    dot: "bg-blue-400",
    pill: "bg-blue-500/10 text-blue-600 border-blue-500/25 dark:text-blue-400",
    bar: "bg-blue-400",
  },
  blocked: {
    dot: "bg-amber-500",
    pill: "bg-amber-500/10 text-amber-600 border-amber-500/25 dark:text-amber-400",
    bar: "bg-amber-500",
  },
  reviewing: {
    dot: "bg-violet-500",
    pill: "bg-violet-500/10 text-violet-600 border-violet-500/25 dark:text-violet-400",
    bar: "bg-violet-500",
  },
  failed: {
    dot: "bg-destructive",
    pill: "bg-destructive/10 text-destructive border-destructive/25",
    bar: "bg-destructive",
  },
  draft: {
    dot: "bg-muted-foreground/40",
    pill: "bg-muted text-muted-foreground border-border",
    bar: "bg-muted-foreground/40",
  },
  ready: {
    dot: "bg-muted-foreground/50",
    pill: "bg-muted text-muted-foreground border-border",
    bar: "bg-muted-foreground/50",
  },
  cancelled: {
    dot: "bg-muted-foreground/30",
    pill: "bg-muted text-muted-foreground border-border line-through",
    bar: "bg-muted-foreground/30",
  },
};

export function statusMeta(status: string): StatusMeta {
  return STATUS_META[status] ?? STATUS_META.draft;
}

const STATUS_KEY: Record<string, MessageKey> = {
  running: "tasks.statusRunning",
  blocked: "tasks.statusBlocked",
  reviewing: "tasks.statusReview",
  done: "tasks.statusDone",
  failed: "tasks.statusFailed",
  cancelled: "tasks.statusCancelled",
};

export function statusLabel(t: TFunction, status: string): string {
  const key = STATUS_KEY[status];
  if (key) return t(key);
  return status.charAt(0).toUpperCase() + status.slice(1);
}

export function StatusDot({ status, className }: { status: string; className?: string }) {
  return (
    <span
      className={cn("inline-block size-2 shrink-0 rounded-full", statusMeta(status).dot, className)}
    />
  );
}

export function StatusPill({ status, label }: { status: string; label: string }) {
  return (
    <span
      className={cn(
        "inline-flex h-[21px] items-center gap-1.5 rounded-full border px-2.5 font-mono text-[11px] font-semibold capitalize",
        statusMeta(status).pill,
      )}
    >
      <span className={cn("size-1.5 rounded-full", statusMeta(status).dot)} />
      {label}
    </span>
  );
}

/** Goal needs human attention when blocked or in review. */
export function goalNeedsYou(g: ComponentsGoal): boolean {
  return g.status === "blocked" || g.status === "reviewing";
}

export interface Rollup {
  done: number;
  running: number;
  reviewing: number;
  blocked: number;
  failed: number;
  other: number;
  total: number;
}

export function rollup(tasks: ComponentsTask[]): Rollup {
  const r: Rollup = {
    done: 0,
    running: 0,
    reviewing: 0,
    blocked: 0,
    failed: 0,
    other: 0,
    total: tasks.length,
  };
  for (const t of tasks) {
    if (t.status === "done") r.done++;
    else if (t.status === "running") r.running++;
    else if (t.status === "reviewing") r.reviewing++;
    else if (t.status === "blocked") r.blocked++;
    else if (t.status === "failed") r.failed++;
    else r.other++;
  }
  return r;
}

const BAR_ORDER: { key: keyof Rollup; status: string }[] = [
  { key: "done", status: "done" },
  { key: "running", status: "running" },
  { key: "reviewing", status: "reviewing" },
  { key: "blocked", status: "blocked" },
  { key: "failed", status: "failed" },
];

export function ProgressBar({ r, className }: { r: Rollup; className?: string }) {
  if (!r.total) return null;
  return (
    <div className={cn("flex h-1.5 overflow-hidden rounded-full bg-muted", className)}>
      {BAR_ORDER.map(({ key, status }) =>
        r[key] ? (
          <span
            key={status}
            className={cn("h-full", statusMeta(status).bar)}
            style={{ width: `${(r[key] / r.total) * 100}%` }}
          />
        ) : null,
      )}
    </div>
  );
}

export function avatarInitials(name: string): string {
  return name
    .split(/\s+/)
    .map((w) => w[0])
    .join("")
    .slice(0, 2)
    .toUpperCase();
}

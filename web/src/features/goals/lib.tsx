import type { TFunction } from "i18next";
import { cn } from "@/lib/utils";
import type { MessageKey } from "@/lib/i18n/messages";
import type { ComponentsGoal } from "@/lib/api-client/types.gen";
import type { SchedulerJob } from "@/lib/types";
import { parseGoDuration } from "@/features/goals/types";

export { goalNeedsYou } from "@/features/goals/types";

// A goal's lifecycle is the source of truth, but blocked splits by block_reason
// and done splits by done_reason so the UI can show the actionable state.
export type DisplayStatus =
  | "draft"
  | "pending"
  | "active"
  | "review"
  | "blocked"
  | "accepted"
  | "failed"
  | "cancelled";

export function displayStatus(d: ComponentsGoal): DisplayStatus {
  switch (d.lifecycle) {
    case "blocked":
      return d.block_reason === "needs_verdict" ? "review" : "blocked";
    case "done":
      if (d.done_reason === "accepted") return "accepted";
      if (d.done_reason === "cancelled") return "cancelled";
      return "failed";
    case "active":
      return "active";
    case "pending":
      return "pending";
    default:
      return "draft";
  }
}

interface StatusMeta {
  dot: string;
  pill: string;
  bar: string;
}

// A goal's state is a verdict, so it takes the status tokens. It used to take
// `chart-*`, which is for plotted and categorical data: those sit ~0.2L brighter
// because a chart reads them as areas, and in light mode that put the pill text
// at 2.2-2.5:1 and the dot itself under the 3:1 non-text floor. Pills carry the
// `-foreground` half because their text sits on a tint of their own token.
const STATUS_META = {
  accepted: {
    dot: "bg-success",
    pill: "bg-success/10 text-success-foreground border-success/25",
    bar: "bg-success",
  },
  active: {
    dot: "bg-info",
    pill: "bg-info/10 text-info-foreground border-info/25",
    bar: "bg-info",
  },
  review: {
    dot: "bg-primary",
    pill: "bg-primary/10 text-primary border-primary/25",
    bar: "bg-primary",
  },
  blocked: {
    dot: "bg-warning",
    pill: "bg-warning/10 text-warning-foreground border-warning/25",
    bar: "bg-warning",
  },
  failed: {
    dot: "bg-destructive",
    pill: "bg-destructive/10 text-destructive-foreground border-destructive/25",
    bar: "bg-destructive",
  },
  cancelled: {
    dot: "bg-muted-foreground/30",
    pill: "bg-muted text-muted-foreground border-border line-through",
    bar: "bg-muted-foreground/30",
  },
  draft: {
    dot: "bg-muted-foreground/40",
    pill: "bg-muted text-muted-foreground border-border",
    bar: "bg-muted-foreground/40",
  },
  pending: {
    dot: "bg-muted-foreground/50",
    pill: "bg-muted text-muted-foreground border-border",
    bar: "bg-muted-foreground/50",
  },
} satisfies Record<DisplayStatus, StatusMeta>;

export function statusMeta(status: DisplayStatus): StatusMeta {
  return STATUS_META[status] ?? STATUS_META.draft;
}

const STATUS_KEY = {
  draft: "goals.statusDraft",
  pending: "goals.statusPending",
  active: "goals.statusActive",
  review: "goals.statusReview",
  blocked: "goals.statusBlocked",
  accepted: "goals.statusAccepted",
  failed: "goals.statusFailed",
  cancelled: "goals.statusCancelled",
} satisfies Record<DisplayStatus, MessageKey>;

export function statusLabel(t: TFunction, status: DisplayStatus): string {
  return t(STATUS_KEY[status] ?? STATUS_KEY.draft);
}

/** Convenience: resolve a goal straight to its localized status label. */
export function goalStatusLabel(t: TFunction, d: ComponentsGoal): string {
  return statusLabel(t, displayStatus(d));
}

export function StatusDot({ status, className }: { status: DisplayStatus; className?: string }) {
  return (
    <span
      className={cn("inline-block size-2 shrink-0 rounded-full", statusMeta(status).dot, className)}
    />
  );
}

export function StatusPill({ status, label }: { status: DisplayStatus; label: string }) {
  return (
    <span
      className={cn(
        "inline-flex h-[21px] shrink-0 items-center gap-1.5 whitespace-nowrap rounded-full border px-2.5 font-mono text-xs font-semibold capitalize",
        statusMeta(status).pill,
      )}
    >
      <span className={cn("size-1.5 rounded-full", statusMeta(status).dot)} />
      {label}
    </span>
  );
}

/** Localized one-line reason a goal is blocked, from its block_reason. */
export function blockReasonLabel(t: TFunction, d: ComponentsGoal): string | null {
  if (d.lifecycle !== "blocked") return null;
  switch (d.block_reason) {
    case "needs_verdict":
      return t("goals.blockNeedsVerdict");
    case "needs_plan_approval":
      return t("goals.blockNeedsPlanApproval");
    case "budget_exhausted":
      return t("goals.blockBudget");
    case "env_unavailable":
      return t("goals.blockEnvUnavailable");
    case "contract_conflict":
      return t("goals.blockContractConflict");
    default:
      return t("goals.statusBlocked");
  }
}

export interface Rollup {
  accepted: number;
  active: number;
  review: number;
  blocked: number;
  failed: number;
  other: number;
  total: number;
}

/** Aggregate a composite's children by display status for a progress rollup. */
export function rollup(children: ComponentsGoal[]): Rollup {
  const r: Rollup = {
    accepted: 0,
    active: 0,
    review: 0,
    blocked: 0,
    failed: 0,
    other: 0,
    total: children.length,
  };
  for (const c of children) {
    const s = displayStatus(c);
    if (s === "accepted") r.accepted++;
    else if (s === "active") r.active++;
    else if (s === "review") r.review++;
    else if (s === "blocked") r.blocked++;
    else if (s === "failed") r.failed++;
    else r.other++;
  }
  return r;
}

const BAR_ORDER: { key: keyof Rollup; status: DisplayStatus }[] = [
  { key: "accepted", status: "accepted" },
  { key: "active", status: "active" },
  { key: "review", status: "review" },
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

/** Localized schedule text: "Every 30 min", a raw cron expression, or a one-shot time. */
export function humanScheduleText(t: TFunction, j: SchedulerJob): string {
  if (j.cron) return j.cron;
  if (j.every) {
    const ms = parseGoDuration(j.every);
    if (ms != null && ms > 0) {
      if (ms % 3_600_000 === 0) return t("hub.everyHours", { n: ms / 3_600_000 });
      if (ms % 60_000 === 0) return t("hub.everyMinutes", { n: ms / 60_000 });
    }
    return t("hub.everyDuration", { d: j.every });
  }
  if (j.at) return t("hub.onceAt", { time: new Date(j.at).toLocaleString() });
  return "—";
}

/** Localized countdown for a future time; falls back to a date beyond 24h. */
export function formatUntil(t: TFunction, d: Date): string {
  const diff = d.getTime() - Date.now();
  if (diff < 60_000) return t("hub.nextNow");
  if (diff < 3_600_000) return t("hub.inMinutes", { n: Math.round(diff / 60_000) });
  if (diff < 86_400_000) return t("hub.inHours", { n: Math.round(diff / 3_600_000) });
  return d.toLocaleString();
}

export function priorityLabel(t: TFunction, priority: string): string {
  if (priority === "urgent") return t("goals.priorityUrgent");
  return t("goals.priorityRoutine");
}

const POLICY_KEY = {
  none: "hub.policyNone",
  human: "hub.policyHuman",
} satisfies Record<string, MessageKey>;

export function policyLabel(t: TFunction, policy: string): string {
  // SAFETY: unknown policies intentionally render their original value below.
  const key = POLICY_KEY[policy as keyof typeof POLICY_KEY];
  return key ? t(key) : policy;
}

export function avatarInitials(name: string): string {
  return name
    .split(/\s+/)
    .map((w) => w[0])
    .join("")
    .slice(0, 2)
    .toUpperCase();
}

import type { ComponentsGoal, ComponentsTask } from "@/lib/api-client/types.gen";
import type { SchedulerJob } from "@/lib/types";

export type ItemKind = "goal" | "schedule" | "task";

export type AutomationItem =
  | { kind: "goal"; id: string; data: ComponentsGoal }
  | { kind: "schedule"; id: string; data: SchedulerJob }
  | { kind: "task"; id: string; data: ComponentsTask };

export function itemKey(item: AutomationItem): string {
  return `${item.kind}:${item.id}`;
}

export function parseItemKey(key: string): { kind: ItemKind; id: string } | null {
  const m = key.match(/^(goal|schedule|task):(.+)$/);
  if (!m) return null;
  return { kind: m[1] as ItemKind, id: m[2] };
}

export function itemName(item: AutomationItem): string {
  return item.kind === "schedule" ? item.data.name : item.data.title;
}

export function itemUpdatedAt(item: AutomationItem): string {
  if (item.kind === "schedule") return item.data.last_run_at ?? item.data.created_at ?? "";
  return item.data.updated_at;
}

export function goalNeedsYou(g: ComponentsGoal): boolean {
  return g.status === "blocked" || g.status === "reviewing";
}

export function taskNeedsYou(t: ComponentsTask): boolean {
  return t.status === "blocked" || t.status === "reviewing" || t.status === "failed";
}

export type Section = "needs-you" | "active" | "schedules" | "closed";

export function classifyItem(item: AutomationItem): Section {
  if (item.kind === "schedule") return "schedules";
  if (item.kind === "goal") {
    if (goalNeedsYou(item.data)) return "needs-you";
    if (item.data.status === "running" || item.data.status === "planning") return "active";
    return "closed";
  }
  // task
  if (taskNeedsYou(item.data)) return "needs-you";
  if (
    item.data.status === "running" ||
    item.data.status === "ready" ||
    item.data.status === "draft"
  )
    return "active";
  return "closed";
}

export function classifyAll(items: AutomationItem[]): Record<Section, AutomationItem[]> {
  const result: Record<Section, AutomationItem[]> = {
    "needs-you": [],
    active: [],
    schedules: [],
    closed: [],
  };
  for (const item of items) {
    result[classifyItem(item)].push(item);
  }

  const byUpdated = (a: AutomationItem, b: AutomationItem) =>
    new Date(itemUpdatedAt(b)).getTime() - new Date(itemUpdatedAt(a)).getTime();

  result["needs-you"].sort(byUpdated);
  result.active.sort(byUpdated);
  result.closed.sort(byUpdated);
  result.schedules.sort((a, b) => {
    if (a.kind !== "schedule" || b.kind !== "schedule") return 0;
    if (a.data.enabled !== b.data.enabled) return a.data.enabled ? -1 : 1;
    return byUpdated(a, b);
  });

  return result;
}

export function jobScheduleText(j: SchedulerJob): string {
  if (j.cron) return j.cron;
  if (j.every) return "every " + j.every;
  if (j.at) return "at " + j.at;
  return "—";
}

/** Parse a Go duration string ("30m", "1h30m", "90s") into milliseconds. */
export function parseGoDuration(s: string): number | null {
  const re = /(\d+(?:\.\d+)?)(h|ms|m|s)/g;
  let ms = 0;
  let matched = false;
  for (const m of s.matchAll(re)) {
    matched = true;
    const n = Number(m[1]);
    if (m[2] === "h") ms += n * 3_600_000;
    else if (m[2] === "m") ms += n * 60_000;
    else if (m[2] === "s") ms += n * 1_000;
    else ms += n;
  }
  return matched ? ms : null;
}

/**
 * Next run time, when it is computable client-side: interval jobs derive it
 * from last_run_at + interval; one-shot jobs use their timestamp. Cron jobs
 * return null (no cron parser shipped) and fall back to the schedule text.
 */
export function jobNextRunAt(j: SchedulerJob): Date | null {
  if (!j.enabled) return null;
  if (j.every && j.last_run_at) {
    const ms = parseGoDuration(j.every);
    if (ms == null) return null;
    const next = new Date(j.last_run_at).getTime() + ms;
    return new Date(Math.max(next, Date.now()));
  }
  if (j.at) {
    const at = new Date(j.at);
    return at.getTime() > Date.now() ? at : null;
  }
  return null;
}

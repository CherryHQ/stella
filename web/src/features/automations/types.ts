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

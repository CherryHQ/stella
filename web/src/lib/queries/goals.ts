import { queryOptions } from "@tanstack/react-query";
import { getGoal, getTaskReadiness, listGoals } from "@/lib/api-client";
import type { ComponentsDep, ComponentsGoal, ComponentsTask } from "@/lib/api-client/types.gen";
import {
  fetchAllGoalTasks,
  fetchAllGoals,
  fetchAllTaskDeps,
  fetchAllTaskEvents,
  fetchAllTaskReviews,
  fetchAllTaskRuns,
  offsetPageToken,
} from "@/lib/paginated";

// goalsOptions fetches every goal (all pages) for callers that aggregate over
// the whole set (e.g. the overview dashboard). The Goals page itself uses the
// server-paginated goalsPageOptions instead.
export function goalsOptions(agentId: string, archived = false) {
  return queryOptions({
    queryKey: ["goals", agentId, archived ? "archived" : "active"],
    queryFn: async () => fetchAllGoals(agentId, archived),
    enabled: !!agentId,
  });
}

export const GOALS_PAGE_SIZE = 24;

export interface GoalsPageParams {
  agentId: string;
  archived?: boolean;
  /** undefined = any; false = active (non-terminal); true = terminal (history). */
  terminal?: boolean;
  /** exact status; undefined = all statuses in scope. */
  status?: string;
  /** case-insensitive substring on title/description; server-side. */
  q?: string;
  /** 1-based page. */
  page?: number;
}

export interface GoalsPage {
  goals: ComponentsGoal[];
  total: number;
}

// goalsPageOptions drives the Goals page from one server page: filtering,
// search, and pagination all run in the DB so the first paint no longer waits
// for every goal to download.
export function goalsPageOptions(p: GoalsPageParams) {
  const page = Math.max(1, p.page ?? 1);
  return queryOptions({
    queryKey: [
      "goals-page",
      p.agentId,
      p.archived ?? false,
      p.terminal ?? null,
      p.status ?? "",
      p.q ?? "",
      page,
    ],
    queryFn: async (): Promise<GoalsPage> => {
      const { data } = await listGoals({
        query: {
          agent_id: p.agentId,
          archived: p.archived || undefined,
          terminal: p.terminal,
          status: p.status || undefined,
          q: p.q || undefined,
          page_size: GOALS_PAGE_SIZE,
          page_token: offsetPageToken((page - 1) * GOALS_PAGE_SIZE),
        },
        throwOnError: true,
      });
      return { goals: data?.goals ?? [], total: data?.total ?? 0 };
    },
    enabled: !!p.agentId,
  });
}

async function goalsCount(agentId: string, q: { archived?: boolean; terminal?: boolean }) {
  const { data } = await listGoals({
    query: { agent_id: agentId, ...q, page_size: 1 },
    throwOnError: true,
  });
  return data?.total ?? 0;
}

// goalCountsOptions powers the active/history/archived header badges with cheap
// server-side counts, independent of the current page or filters.
export function goalCountsOptions(agentId: string) {
  return queryOptions({
    queryKey: ["goals-counts", agentId],
    queryFn: async () => {
      const [active, history, archived] = await Promise.all([
        goalsCount(agentId, { terminal: false }),
        goalsCount(agentId, { terminal: true }),
        goalsCount(agentId, { archived: true }),
      ]);
      return { active, history, archived };
    },
    enabled: !!agentId,
  });
}

export function goalOptions(goalId: string) {
  return queryOptions({
    queryKey: ["goal", goalId],
    queryFn: async () => {
      const { data } = await getGoal({
        path: { goalId: goalId },
        throwOnError: true,
      });
      return data as ComponentsGoal;
    },
    enabled: !!goalId,
  });
}

export interface GoalGraph {
  tasks: ComponentsTask[];
  deps: ComponentsDep[];
}

export function goalGraphOptions(goalId: string) {
  return queryOptions({
    queryKey: ["goal-graph", goalId],
    queryFn: async (): Promise<GoalGraph> => {
      const tasks = await fetchAllGoalTasks(goalId);
      const depLists = await Promise.all(
        tasks.map((t) => fetchAllTaskDeps(t.id).catch(() => [] as ComponentsDep[])),
      );
      return { tasks, deps: depLists.flat() };
    },
    enabled: !!goalId,
  });
}

export function taskReadinessOptions(taskId: string | undefined) {
  return queryOptions({
    queryKey: ["task-readiness", taskId],
    queryFn: async () => {
      const { data } = await getTaskReadiness({
        path: { taskId: taskId! },
        throwOnError: true,
      });
      return data ?? null;
    },
    enabled: !!taskId,
  });
}

export function taskRunsOptions(taskId: string | undefined) {
  return queryOptions({
    queryKey: ["task-runs", taskId],
    queryFn: async () => {
      return fetchAllTaskRuns(taskId!);
    },
    enabled: !!taskId,
  });
}

export function taskReviewsOptions(taskId: string | undefined) {
  return queryOptions({
    queryKey: ["task-reviews", taskId],
    queryFn: async () => {
      return fetchAllTaskReviews(taskId!);
    },
    enabled: !!taskId,
  });
}

export function taskEventsOptions(taskId: string | undefined) {
  return queryOptions({
    queryKey: ["task-events", taskId],
    queryFn: async () => {
      return fetchAllTaskEvents(taskId!);
    },
    enabled: !!taskId,
  });
}

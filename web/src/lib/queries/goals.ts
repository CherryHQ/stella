import { keepPreviousData, queryOptions } from "@tanstack/react-query";
import {
  getGoal,
  getGoalReadiness,
  listAcceptanceEvents,
  listAttempts,
  listGoalChildren,
  listGoals,
  listEdges,
} from "@/lib/api-client";
import type { ComponentsGoal } from "@/lib/api-client/types.gen";
import { fetchAllGoals, fetchAllSubtree, offsetPageToken } from "@/lib/paginated";

// goalsOptions fetches every root (all pages) for callers that aggregate
// over the whole set (e.g. the overview hub). The list page uses the
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
  /** exact lifecycle; undefined = all lifecycles in scope. */
  lifecycle?: string;
  /** exact workflow version row; undefined = all roots. */
  workflowId?: string;
  /** case-insensitive substring on title/intent; server-side. */
  q?: string;
  /** 1-based page. */
  page?: number;
}

export interface GoalsPage {
  goals: ComponentsGoal[];
  total: number;
}

// goalsPageOptions drives the list page from one server page: filtering,
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
      p.lifecycle ?? "",
      p.workflowId ?? "",
      p.q ?? "",
      page,
    ],
    queryFn: async (): Promise<GoalsPage> => {
      const { data } = await listGoals({
        query: {
          agent_id: p.agentId,
          archived: p.archived || undefined,
          terminal: p.terminal,
          lifecycle: p.lifecycle || undefined,
          workflow_id: p.workflowId || undefined,
          q: p.q || undefined,
          page_size: GOALS_PAGE_SIZE,
          page_token: offsetPageToken((page - 1) * GOALS_PAGE_SIZE),
        },
        throwOnError: true,
      });
      return { goals: data?.goals ?? [], total: data?.total ?? 0 };
    },
    enabled: !!p.agentId,
    // Keep the previous page's data (and its total) on screen while the next
    // page loads, so the pager's total never transiently drops to 0.
    placeholderData: keepPreviousData,
  });
}

async function goalsCount(agentId: string, q: { archived?: boolean; terminal?: boolean }) {
  const { data } = await listGoals({
    query: { agent_id: agentId, ...q, page_size: 1 },
    throwOnError: true,
  });
  return data?.total ?? 0;
}

// goalCountsOptions powers the active/history/archived header badges with
// cheap server-side counts, independent of the current page or filters.
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
        path: { id: goalId },
        throwOnError: true,
      });
      // SAFETY: getGoal returns the requested ComponentsGoal on success.
      return data as ComponentsGoal;
    },
    enabled: !!goalId,
  });
}

/** Direct children of a composite, in position order. */
export function goalChildrenOptions(goalId: string | undefined) {
  return queryOptions({
    queryKey: ["goal-children", goalId],
    queryFn: async () => {
      const { data } = await listGoalChildren({
        path: { id: goalId! },
        throwOnError: true,
      });
      return data?.goals ?? [];
    },
    enabled: !!goalId,
  });
}

/** Every goal in the tree (the root_id family) for graph/tree views. */
export function goalSubtreeOptions(rootId: string | undefined) {
  return queryOptions({
    queryKey: ["goal-subtree", rootId],
    queryFn: async () => fetchAllSubtree(rootId!),
    enabled: !!rootId,
  });
}

export function goalReadinessOptions(goalId: string | undefined) {
  return queryOptions({
    queryKey: ["goal-readiness", goalId],
    queryFn: async () => {
      const { data } = await getGoalReadiness({
        path: { id: goalId! },
        throwOnError: true,
      });
      return data ?? null;
    },
    enabled: !!goalId,
  });
}

export function goalAttemptsOptions(goalId: string | undefined) {
  return queryOptions({
    queryKey: ["goal-attempts", goalId],
    queryFn: async () => {
      const { data } = await listAttempts({
        path: { id: goalId! },
        throwOnError: true,
      });
      return data?.attempts ?? [];
    },
    enabled: !!goalId,
  });
}

export function goalEventsOptions(goalId: string | undefined) {
  return queryOptions({
    queryKey: ["goal-events", goalId],
    queryFn: async () => {
      const { data } = await listAcceptanceEvents({
        path: { id: goalId! },
        throwOnError: true,
      });
      return data?.acceptance_events ?? [];
    },
    enabled: !!goalId,
  });
}

export function goalEdgesOptions(goalId: string | undefined) {
  return queryOptions({
    queryKey: ["goal-edges", goalId],
    queryFn: async () => {
      const { data } = await listEdges({
        path: { id: goalId! },
        throwOnError: true,
      });
      return data?.edges ?? [];
    },
    enabled: !!goalId,
  });
}

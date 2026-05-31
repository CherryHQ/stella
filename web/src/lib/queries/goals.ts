import { queryOptions } from "@tanstack/react-query";
import { getGoal, getTaskReadiness } from "@/lib/api-client";
import type { ComponentsDep, ComponentsGoal, ComponentsTask } from "@/lib/api-client/types.gen";
import {
  fetchAllGoalTasks,
  fetchAllGoals,
  fetchAllTaskDeps,
  fetchAllTaskEvents,
  fetchAllTaskReviews,
  fetchAllTaskRuns,
} from "@/lib/paginated";

export function goalsOptions(agentId: string) {
  return queryOptions({
    queryKey: ["goals", agentId],
    queryFn: async () => {
      const items = await fetchAllGoals();
      return items.filter((g) => !agentId || g.agent_id === agentId);
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

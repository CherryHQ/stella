import { queryOptions } from "@tanstack/react-query";
import {
  getGoal,
  getTaskReadiness,
  listGoals,
  listGoalTasks,
  listTaskDeps,
  listTaskEvents,
  listTaskReviews,
  listTaskRuns,
} from "@/lib/api-client";
import type { ComponentsDep, ComponentsGoal, ComponentsTask } from "@/lib/api-client/types.gen";

export function goalsOptions(agentId: string) {
  return queryOptions({
    queryKey: ["goals", agentId],
    queryFn: async () => {
      const { data } = await listGoals({ throwOnError: true });
      const items = data?.goals ?? [];
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
        path: { goalID: goalId },
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
      const { data } = await listGoalTasks({
        path: { goalID: goalId },
        throwOnError: true,
      });
      const tasks = data?.tasks ?? [];
      const depLists = await Promise.all(
        tasks.map((t) =>
          listTaskDeps({ path: { taskID: t.id }, throwOnError: true })
            .then((r) => r.data?.deps ?? [])
            .catch(() => [] as ComponentsDep[]),
        ),
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
        path: { taskID: taskId! },
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
      const { data } = await listTaskRuns({
        path: { taskID: taskId! },
        throwOnError: true,
      });
      return data?.runs ?? [];
    },
    enabled: !!taskId,
  });
}

export function taskReviewsOptions(taskId: string | undefined) {
  return queryOptions({
    queryKey: ["task-reviews", taskId],
    queryFn: async () => {
      const { data } = await listTaskReviews({
        path: { taskID: taskId! },
        throwOnError: true,
      });
      return data?.reviews ?? [];
    },
    enabled: !!taskId,
  });
}

export function taskEventsOptions(taskId: string | undefined) {
  return queryOptions({
    queryKey: ["task-events", taskId],
    queryFn: async () => {
      const { data } = await listTaskEvents({
        path: { taskID: taskId! },
        throwOnError: true,
      });
      return data?.events ?? [];
    },
    enabled: !!taskId,
  });
}

import {
  getSessionMessages,
  listGoals,
  listGoalTasks,
  listSchedulerJobRuns,
  listTaskDeps,
  listTaskEvents,
  listTaskReviews,
  listTaskRuns,
  listTasks,
} from "@/lib/api-client";
import type {
  ComponentsDep,
  ComponentsEvent,
  ComponentsGoal,
  ComponentsReview,
  ComponentsRun,
  ComponentsTask,
  JobRun,
} from "@/lib/api-client/types.gen";
import type { Message } from "@/lib/types";

export async function fetchAllTasks(agentId?: string): Promise<ComponentsTask[]> {
  const all: ComponentsTask[] = [];
  let pageToken: string | undefined;
  do {
    const { data } = await listTasks({
      query: { agent_id: agentId, page_size: 500, page_token: pageToken },
      throwOnError: true,
    });
    all.push(...(data?.tasks ?? []));
    pageToken = data?.next_page_token ?? undefined;
  } while (pageToken);
  return all;
}

export async function fetchAllGoals(): Promise<ComponentsGoal[]> {
  const all: ComponentsGoal[] = [];
  let pageToken: string | undefined;
  do {
    const { data } = await listGoals({
      query: { page_size: 500, page_token: pageToken },
      throwOnError: true,
    });
    all.push(...(data?.goals ?? []));
    pageToken = data?.next_page_token ?? undefined;
  } while (pageToken);
  return all;
}

export async function fetchAllGoalTasks(goalId: string): Promise<ComponentsTask[]> {
  const all: ComponentsTask[] = [];
  let pageToken: string | undefined;
  do {
    const { data } = await listGoalTasks({
      path: { goalId },
      query: { page_size: 500, page_token: pageToken },
      throwOnError: true,
    });
    all.push(...(data?.tasks ?? []));
    pageToken = data?.next_page_token ?? undefined;
  } while (pageToken);
  return all;
}

export async function fetchAllTaskDeps(taskId: string): Promise<ComponentsDep[]> {
  const all: ComponentsDep[] = [];
  let pageToken: string | undefined;
  do {
    const { data } = await listTaskDeps({
      path: { taskId },
      query: { page_size: 500, page_token: pageToken },
      throwOnError: true,
    });
    all.push(...(data?.deps ?? []));
    pageToken = data?.next_page_token ?? undefined;
  } while (pageToken);
  return all;
}

export async function fetchAllTaskRuns(taskId: string): Promise<ComponentsRun[]> {
  const all: ComponentsRun[] = [];
  let pageToken: string | undefined;
  do {
    const { data } = await listTaskRuns({
      path: { taskId },
      query: { page_size: 500, page_token: pageToken },
      throwOnError: true,
    });
    all.push(...(data?.runs ?? []));
    pageToken = data?.next_page_token ?? undefined;
  } while (pageToken);
  return all;
}

export async function fetchAllTaskReviews(taskId: string): Promise<ComponentsReview[]> {
  const all: ComponentsReview[] = [];
  let pageToken: string | undefined;
  do {
    const { data } = await listTaskReviews({
      path: { taskId },
      query: { page_size: 500, page_token: pageToken },
      throwOnError: true,
    });
    all.push(...(data?.reviews ?? []));
    pageToken = data?.next_page_token ?? undefined;
  } while (pageToken);
  return all;
}

export async function fetchAllTaskEvents(taskId: string): Promise<ComponentsEvent[]> {
  const all: ComponentsEvent[] = [];
  let pageToken: string | undefined;
  do {
    const { data } = await listTaskEvents({
      path: { taskId },
      query: { page_size: 500, page_token: pageToken },
      throwOnError: true,
    });
    all.push(...(data?.events ?? []));
    pageToken = data?.next_page_token ?? undefined;
  } while (pageToken);
  return all;
}

export async function fetchAllSessionMessages(
  agentId: string,
  sessionId: string,
  onProgress?: (count: number) => void,
): Promise<Message[]> {
  const pages: Message[][] = [];
  const limit = 200;
  let skip = 0;
  let total = 0;
  while (true) {
    const { data } = await getSessionMessages({
      path: { agentId, sessionId },
      query: { limit, skip },
      throwOnError: true,
    });
    const batch = (data?.messages as unknown as Message[] | undefined) ?? [];
    if (batch.length === 0) break;
    pages.push(batch);
    total += batch.length;
    onProgress?.(total);
    if (batch.length < limit) break;
    skip += batch.length;
  }
  return pages.reverse().flat();
}

export async function fetchAllSchedulerJobRuns(agentId: string, jobId: string): Promise<JobRun[]> {
  const all: JobRun[] = [];
  let pageToken: string | undefined;
  do {
    const { data } = await listSchedulerJobRuns({
      path: { agentId, jobId },
      query: { page_size: 500, page_token: pageToken },
      throwOnError: true,
    });
    all.push(...(data?.runs ?? []));
    pageToken = data?.next_page_token ?? undefined;
  } while (pageToken);
  return all;
}

import type { QueryClient } from "@tanstack/react-query";
import { createGoalTimelineEvent } from "@/lib/api-client";

function bool<T>(v: T): boolean {
  return v === true || v === "true";
}

export async function postGoalTimelineMessage(qc: QueryClient, goalId: string, text: string) {
  const { data } = await createGoalTimelineEvent({
    path: { id: goalId },
    body: { text },
    throwOnError: true,
  });
  await Promise.all([
    qc.invalidateQueries({ queryKey: ["goal", goalId] }),
    qc.invalidateQueries({ queryKey: ["goal-attempts", goalId] }),
    qc.invalidateQueries({ queryKey: ["goal-events", goalId] }),
    qc.invalidateQueries({ queryKey: ["goal-timeline", goalId] }),
    qc.invalidateQueries({ queryKey: ["goals"] }),
    qc.invalidateQueries({ queryKey: ["goals-page"] }),
  ]);
  return bool(data?.payload?.reattempt_authorized);
}

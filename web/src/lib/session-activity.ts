import type { Session } from "@/lib/types";

export type SessionActivityStatus = Session["activity_status"] | undefined;

/** Running outranks terminal attention when a section summarizes many sessions. */
export function aggregateSessionActivity(sessions: Session[]): SessionActivityStatus {
  if (sessions.some((session) => session.activity_status === "running")) return "running";
  if (sessions.some((session) => session.activity_status === "unread")) return "unread";
  return "idle";
}

import { describe, expect, it } from "vitest";
import type { Session } from "@/lib/types";
import { aggregateSessionActivity } from "./session-activity";

function session(activity_status: Session["activity_status"]): Session {
  return { activity_status } as Session;
}

describe("aggregateSessionActivity", () => {
  it("prioritizes running over unread attention", () => {
    expect(aggregateSessionActivity([session("unread"), session("running")])).toBe("running");
  });

  it("reports unread when completed work needs review", () => {
    expect(aggregateSessionActivity([session("idle"), session("unread")])).toBe("unread");
  });

  it("stays idle without attention", () => {
    expect(aggregateSessionActivity([session("idle")])).toBe("idle");
  });
});

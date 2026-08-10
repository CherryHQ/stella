import { beforeEach, describe, expect, it, vi } from "vitest";
import { listSessions } from "@/lib/api-client/sdk.gen";
import { agentLevelThreads, sortedThreads, threadSessionsInfiniteQueryOptions } from "./sessions";
import type { Session } from "@/lib/types";

vi.mock("@/lib/api-client/sdk.gen", () => ({ listSessions: vi.fn() }));

beforeEach(() => vi.mocked(listSessions).mockReset());

function session(id: string, kind: Session["kind"], overrides: Partial<Session> = {}): Session {
  return {
    id,
    kind,
    archived: false,
    last_active: `2026-08-10T00:00:0${id}Z`,
    ...overrides,
  } as Session;
}

describe("visible session threads", () => {
  it("includes session-created delegate threads but excludes machine-owned work sessions", () => {
    expect(
      sortedThreads([
        session("1", "chat"),
        session("2", "delegate"),
        session("3", "task"),
        session("4", "scheduler"),
        session("5", "delegate", { archived: true }),
      ]).map((item) => item.id),
    ).toEqual(["2", "1"]);
  });

  it("keeps project chats out of the agent-level conversation list", () => {
    expect(
      agentLevelThreads([
        session("1", "chat", { project_id: "project" }),
        session("2", "delegate"),
      ]).map((item) => item.id),
    ).toEqual(["2"]);
  });

  it("walks past an invisible raw page to find delegate threads", async () => {
    vi.mocked(listSessions)
      .mockResolvedValueOnce({
        data: {
          sessions: [session("3", "task"), session("4", "scheduler")],
          next_page_token: "page-2",
        },
      } as never)
      .mockResolvedValueOnce({
        data: { sessions: [session("2", "delegate")] },
      } as never);

    const options = threadSessionsInfiniteQueryOptions("agent");
    const queryFn = options.queryFn as (context: {
      pageParam?: string;
    }) => Promise<{ sessions: Session[]; nextPageToken?: string }>;
    const page = await queryFn({ pageParam: undefined });

    expect(page.sessions.map((item) => item.id)).toEqual(["2"]);
    expect(listSessions).toHaveBeenCalledTimes(2);
    expect(vi.mocked(listSessions).mock.calls[1][0].query?.page_token).toBe("page-2");
  });
});

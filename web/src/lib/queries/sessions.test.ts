import { beforeEach, describe, expect, it, vi } from "vitest";
import { listSessions } from "@/lib/api-client/sdk.gen";
import { agentLevelThreads, allThreadSessionsQueryOptions, sortedThreads } from "./sessions";
import type { Session } from "@/lib/types";

vi.mock("@/lib/api-client/sdk.gen", () => ({ listSessions: vi.fn() }));

beforeEach(() => vi.mocked(listSessions).mockReset());

function session(id: string, kind: Session["kind"], overrides: Partial<Session> = {}): Session {
  // SAFETY: the fixture fills the required session fields and returns a full Session.
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

  it("requests chat and delegate explicitly and walks both cursors", async () => {
    // SAFETY: listSessions is mocked; the returned objects are the SDK-shaped payloads it would resolve.
    vi.mocked(listSessions)
      .mockResolvedValueOnce({
        data: { sessions: [session("1", "chat")], next_page_token: "chat-2" },
      } as never)
      .mockResolvedValueOnce({ data: { sessions: [session("2", "delegate")] } } as never)
      .mockResolvedValueOnce({ data: { sessions: [session("3", "chat")] } } as never);

    const options = allThreadSessionsQueryOptions("agent");
    // SAFETY: the query's queryFn resolves the session list the page consumes.
    const queryFn = options.queryFn as () => Promise<Session[]>;
    const threads = await queryFn();

    expect(threads.map((item) => item.id)).toEqual(["3", "2", "1"]);
    expect(vi.mocked(listSessions).mock.calls.map(([request]) => request.query?.kind)).toEqual([
      "chat",
      "delegate",
      "chat",
    ]);
  });
});

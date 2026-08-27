import { describe, expect, it, vi } from "vitest";
import type { Session } from "@/lib/types";
import {
  agentLevelThreads,
  allThreadSessionsQueryOptions,
  sortedThreads,
  type SessionListClient,
} from "./sessions";

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
    const clientMock = vi.fn();
    // SAFETY: the injected client returns the SDK-shaped payloads this query consumes.
    const client = clientMock as SessionListClient;
    // SAFETY: transport metadata is irrelevant to this query's pagination contract.
    const response = <T>(data: T) => Promise.resolve({ data }) as never;
    clientMock
      .mockResolvedValueOnce(
        response({ sessions: [session("1", "chat")], next_page_token: "chat-2" }),
      )
      .mockResolvedValueOnce(response({ sessions: [session("2", "delegate")] }))
      .mockResolvedValueOnce(response({ sessions: [session("3", "chat")] }));

    const options = allThreadSessionsQueryOptions("agent", true, undefined, client);
    // SAFETY: the query's queryFn resolves the session list the page consumes.
    const queryFn = options.queryFn as () => Promise<Session[]>;
    const threads = await queryFn();

    expect(threads.map((item) => item.id)).toEqual(["3", "2", "1"]);
    expect(clientMock.mock.calls.map(([request]) => request.query?.kind)).toEqual([
      "chat",
      "delegate",
      "chat",
    ]);
  });
});

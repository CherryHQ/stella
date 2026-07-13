import { beforeEach, describe, expect, it, vi } from "vitest";

const api = vi.hoisted(() => ({
  listAgentSkills: vi.fn(),
  listProfileChangelog: vi.fn(),
  listProfileKnowledge: vi.fn(),
}));

vi.mock("@/lib/api-client/sdk.gen", () => api);

import {
  agentSkillsInfiniteQueryOptions,
  agentSkillsOptions,
  flattenAgentSkillPages,
} from "@/lib/queries/agents";
import {
  flattenKnowledgePages,
  flattenMemoryChangelogPages,
  knowledgeInfiniteQueryOptions,
  memoryChangelogInfiniteQueryOptions,
} from "@/lib/queries/memories";
import { validateMemorySearch, validateSkillsSearch } from "@/lib/route-search";

type QueryOptions = {
  queryFn?: unknown;
};

async function runQuery(options: QueryOptions, pageParam?: string) {
  const queryFn = options.queryFn as
    | ((context: { pageParam?: string }) => Promise<unknown>)
    | undefined;
  if (!queryFn) {
    throw new Error("queryFn is not callable");
  }
  return queryFn({ pageParam });
}

function nextPage(options: { getNextPageParam?: unknown }, lastPage: unknown) {
  const getNextPageParam = options.getNextPageParam as
    | ((page: unknown, pages: unknown[], pageParam: unknown, pageParams: unknown[]) => unknown)
    | undefined;
  return getNextPageParam?.(lastPage, [], undefined, []);
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe("lifecycle infinite queries", () => {
  it("includes every Knowledge and changelog filter in query keys", () => {
    expect(knowledgeInfiniteQueryOptions("agent-1", "active").queryKey).not.toEqual(
      knowledgeInfiniteQueryOptions("agent-1", "removed").queryKey,
    );
    expect(knowledgeInfiniteQueryOptions("agent-1", "active").queryKey).not.toEqual(
      knowledgeInfiniteQueryOptions("agent-2", "active").queryKey,
    );
    expect(memoryChangelogInfiniteQueryOptions("agent-1").queryKey).not.toEqual(
      memoryChangelogInfiniteQueryOptions("agent-1", "knowledge").queryKey,
    );
  });

  it("includes every Skill management filter in the query key", () => {
    const base = {
      agentId: "agent-1",
      projectId: "project-1",
      sessionId: "session-1",
      state: "active" as const,
      scopeGroup: "agent" as const,
      createdBy: "reflect" as const,
      q: " deploy ",
    };
    const keys = [
      agentSkillsInfiniteQueryOptions(base).queryKey,
      agentSkillsInfiniteQueryOptions({ ...base, agentId: "agent-2" }).queryKey,
      agentSkillsInfiniteQueryOptions({ ...base, projectId: "project-2" }).queryKey,
      agentSkillsInfiniteQueryOptions({ ...base, sessionId: "session-2" }).queryKey,
      agentSkillsInfiniteQueryOptions({ ...base, state: "removed" }).queryKey,
      agentSkillsInfiniteQueryOptions({ ...base, scopeGroup: "user" }).queryKey,
      agentSkillsInfiniteQueryOptions({ ...base, createdBy: "manual" }).queryKey,
      agentSkillsInfiniteQueryOptions({ ...base, q: "release" }).queryKey,
    ];

    expect(new Set(keys.map((key) => JSON.stringify(key)))).toHaveLength(keys.length);
  });

  it("passes no initial token and forwards exact later tokens", async () => {
    api.listProfileKnowledge.mockResolvedValue({
      data: { knowledge: [], total_size: 0, next_page_token: null },
    });
    api.listProfileChangelog.mockResolvedValue({
      data: { entries: [], next_page_token: null },
    });
    api.listAgentSkills.mockResolvedValue({
      data: {
        skills: [],
        total_size: 0,
        scope_counts: { all: 0, system: 0, agent: 0, user: 0, project: 0 },
        next_page_token: null,
      },
    });

    const knowledge = knowledgeInfiniteQueryOptions("agent-1", "active");
    await runQuery(knowledge);
    await runQuery(knowledge, "knowledge-next");
    expect(api.listProfileKnowledge.mock.calls[0][0].query).not.toHaveProperty("page_token");
    expect(api.listProfileKnowledge.mock.calls[1][0].query.page_token).toBe("knowledge-next");

    const changelog = memoryChangelogInfiniteQueryOptions("agent-1", "knowledge");
    await runQuery(changelog);
    await runQuery(changelog, "changelog-next");
    expect(api.listProfileChangelog.mock.calls[0][0].query).not.toHaveProperty("page_token");
    expect(api.listProfileChangelog.mock.calls[1][0].query.page_token).toBe("changelog-next");

    const skills = agentSkillsInfiniteQueryOptions({
      agentId: "agent-1",
      projectId: "project-1",
      sessionId: "session-1",
      state: "removed",
      scopeGroup: "agent",
      createdBy: "reflect",
      q: " deploy ",
    });
    await runQuery(skills);
    await runQuery(skills, "skill-next");
    expect(api.listAgentSkills.mock.calls[0][0].query).toEqual({
      state: "removed",
      scope_group: "agent",
      created_by: "reflect",
      q: "deploy",
      page_size: 12,
      session_id: "session-1",
    });
    expect(api.listAgentSkills.mock.calls[1][0].query.page_token).toBe("skill-next");
  });

  it("stops pagination when the server omits or nulls the next token", () => {
    const knowledge = knowledgeInfiniteQueryOptions("agent-1", "active");
    const changelog = memoryChangelogInfiniteQueryOptions("agent-1");
    const skills = agentSkillsInfiniteQueryOptions({ agentId: "agent-1", state: "active" });

    expect(nextPage(knowledge, { next_page_token: null })).toBeUndefined();
    expect(nextPage(changelog, {})).toBeUndefined();
    expect(nextPage(skills, { next_page_token: "next" })).toBe("next");
  });

  it("flattens pages without changing server order", () => {
    expect(
      flattenKnowledgePages([
        { knowledge: [{ id: "k1" }], total_size: 2 },
        { knowledge: [{ id: "k2" }], total_size: 2 },
      ] as never),
    ).toEqual([{ id: "k1" }, { id: "k2" }]);
    expect(
      flattenMemoryChangelogPages([
        { entries: [{ id: "c1" }] },
        { entries: [{ id: "c2" }] },
      ] as never),
    ).toEqual([{ id: "c1" }, { id: "c2" }]);
    expect(
      flattenAgentSkillPages([
        { skills: [{ id: "s1" }], total_size: 2, scope_counts: {} },
        { skills: [{ id: "s2" }], total_size: 2, scope_counts: {} },
      ] as never),
    ).toEqual([{ id: "s1" }, { id: "s2" }]);
  });

  it("only sends created_by=reflect when the generated filter is enabled", async () => {
    api.listAgentSkills.mockResolvedValue({
      data: { skills: [], total_size: 0, scope_counts: {} },
    });

    await runQuery(agentSkillsInfiniteQueryOptions({ agentId: "agent-1", state: "active" }));
    await runQuery(
      agentSkillsInfiniteQueryOptions({
        agentId: "agent-1",
        state: "active",
        createdBy: "reflect",
      }),
    );

    expect(api.listAgentSkills.mock.calls[0][0].query).not.toHaveProperty("created_by");
    expect(api.listAgentSkills.mock.calls[1][0].query.created_by).toBe("reflect");
  });

  it("keeps the complete active Skill query unpaginated", async () => {
    api.listAgentSkills.mockResolvedValue({
      data: { skills: [], scope_counts: {}, total_size: 0 },
    });

    await runQuery(agentSkillsOptions("agent-1"));

    expect(api.listAgentSkills.mock.calls.at(-1)?.[0]).not.toHaveProperty("query");
  });
});

describe("lifecycle route search", () => {
  it("accepts confirmed Memory and Skill values", () => {
    expect(validateMemorySearch({ knowledge: "removed" })).toEqual({ knowledge: "removed" });
    expect(
      validateSkillsSearch({
        source: "removed",
        fscope: "agent",
        generated: "true",
        sel: "skill-1",
        new: "true",
      }),
    ).toEqual({
      source: "removed",
      fscope: "agent",
      generated: true,
      sel: "skill-1",
      new: true,
    });
  });

  it("discards invalid values instead of throwing", () => {
    expect(validateMemorySearch({ knowledge: "archive" })).toEqual({});
    expect(validateSkillsSearch({ source: "archive", fscope: "global", generated: "yes" })).toEqual(
      { new: false, generated: false },
    );
  });
});

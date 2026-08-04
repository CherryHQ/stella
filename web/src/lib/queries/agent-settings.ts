import { queryOptions } from "@tanstack/react-query";
import {
  getMe,
  listAgents,
  listAgentSkills,
  listBuiltinResources,
  listChannels,
  listModels,
  listProfileMemories,
} from "@/lib/api-client/sdk.gen";
import type {
  AgentDetail,
  AgentSandbox,
  BuiltinItem,
  Channel,
  Personalisation,
  Skill,
  User,
} from "@/lib/types";
import { fetchAllAuthUsers } from "@/lib/auth-users";

export type ModelOption = { value: string; label: string };

type ProfileMemory = { agent_id: string; soul?: string; content?: string };

function profileMemories(value: unknown) {
  return (value as ProfileMemory[]) ?? [];
}

/**
 * The API tolerates a missing, string, or array allowlist; the editor only ever
 * works with the normalized array form so every consumer is spared the union.
 */
export function normalizeSandbox(sandbox: unknown): AgentSandbox {
  const s = sandbox as AgentSandbox | undefined;
  const mode = s?.network?.mode ?? "allow_all";
  const rawAllowlist = s?.network?.allowlist;
  const allowlist = Array.isArray(rawAllowlist)
    ? rawAllowlist
    : typeof rawAllowlist === "string"
      ? (rawAllowlist as string)
          .split(/\r?\n|,/)
          .map((v) => v.trim())
          .filter(Boolean)
      : [];
  return { network: { mode, allowlist } };
}

export function normalizeChannel(ch: Channel): Channel {
  return {
    ...ch,
    type: ch.type || ch.id,
    agent_id: ch.agent_id || "",
    config: ch.config || "{}",
    _config: (() => {
      try {
        return JSON.parse(ch.config || "{}") as Record<string, unknown>;
      } catch {
        return {};
      }
    })(),
  };
}

/**
 * Everything the agent editor needs, in one payload. Both the settings routes
 * (via their loader) and the agent profile page (via {@link
 * agentSettingsQueryOptions}) bootstrap the editor from this shape.
 */
export interface AgentsSettingsLoaderData {
  agents: AgentDetail[];
  channels: Channel[];
  cachedModels: ModelOption[];
  isAdmin: boolean;
  currentUserId: string;
  allUsers: User[];
  builtinTemplates: BuiltinItem[];
  builtinSouls: BuiltinItem[];
  agentSkills: Skill[];
  selectedChannelIDs: string[];
  personalisation: Personalisation;
  selectedAgent?: AgentDetail;
}

export async function loadAgentsSettingsData(agentId = ""): Promise<AgentsSettingsLoaderData> {
  const [agentsRaw, modelsRaw, me, catalog] = await Promise.all([
    listAgents({ query: { include_all: true }, throwOnError: true })
      .then(({ data }) => data?.agents ?? [])
      .catch(() => []),
    listModels({ throwOnError: true })
      .then(({ data }) => data?.models ?? [])
      .catch(() => []),
    getMe({ throwOnError: true })
      .then(({ data }) => data)
      .catch(() => null),
    Promise.all([
      listBuiltinResources({ path: { kind: "template" }, throwOnError: true })
        .then(({ data }) => (data?.resources as BuiltinItem[]) ?? [])
        .catch(() => []),
      listBuiltinResources({ path: { kind: "soul" }, throwOnError: true })
        .then(({ data }) => (data?.resources as BuiltinItem[]) ?? [])
        .catch(() => []),
    ]),
  ]);
  const isAdmin = me?.is_admin ?? false;
  const agents = (agentsRaw ?? []).map((a) => ({
    ...a,
    sandbox: normalizeSandbox(a.sandbox),
    _highlight: a.id === agentId,
  })) as AgentDetail[];
  const selectedAgent = agentId ? agents.find((a) => a.id === agentId) : undefined;
  const channels = isAdmin
    ? (
        (await listChannels({ throwOnError: true })
          .then(({ data }) => data?.channels ?? [])
          .catch(() => [])) ?? []
      ).map(normalizeChannel)
    : [];
  const allUsers = isAdmin ? await fetchAllAuthUsers().catch(() => []) : [];
  const agentSkills = (
    agentId
      ? ((await listAgentSkills({ path: { id: agentId }, throwOnError: true })
          .then(({ data }) => data?.skills ?? [])
          .catch(() => [])) ?? [])
      : []
  ) as Skill[];
  let personalisation: Personalisation = {
    soul: "",
    soulDraft: "",
    profile: "",
    profileDraft: "",
    loaded: !!agentId,
  };
  if (agentId) {
    const { data: memsData } = await listProfileMemories({ throwOnError: true }).catch(() => ({
      data: undefined,
    }));
    const mems = profileMemories(memsData);
    const mem = mems.find((m) => m.agent_id === agentId);
    const soul = mem?.soul ?? "";
    const profile = mem?.content ?? "";
    personalisation = { soul, soulDraft: soul, profile, profileDraft: profile, loaded: true };
  }
  const [builtinTemplates, builtinSouls] = catalog as [BuiltinItem[], BuiltinItem[]];
  return {
    agents,
    channels,
    cachedModels: (modelsRaw ?? []).map((m) => ({
      value: `${m.provider}/${m.model}`,
      label: `${m.provider_name || m.provider}/${m.model}`,
    })),
    isAdmin,
    currentUserId: me?.id ?? "",
    allUsers,
    builtinTemplates: builtinTemplates ?? [],
    builtinSouls: builtinSouls ?? [],
    agentSkills,
    selectedAgent,
    selectedChannelIDs: agentId
      ? channels.filter((ch) => ch.id !== ch.type && ch.agent_id === agentId).map((c) => c.id)
      : [],
    personalisation,
  };
}

/**
 * Editor bootstrap for surfaces without a route loader. `enabled` is a caller
 * concern on purpose: the payload carries system prompts, so the profile page
 * only turns it on once the viewer passed the admin-or-creator check.
 */
export function agentSettingsQueryOptions(agentId: string, enabled = true) {
  return queryOptions({
    queryKey: ["agent-settings", agentId],
    queryFn: () => loadAgentsSettingsData(agentId),
    enabled: enabled && !!agentId,
    staleTime: 30 * 1000,
  });
}

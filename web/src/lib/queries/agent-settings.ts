import { queryOptions } from "@tanstack/react-query";
import {
  getMe,
  listAgents,
  listAgentSkills,
  listBuiltinResources,
  listModels,
  listProfileMemories,
} from "@/lib/api-client/sdk.gen";
import type {
  AgentDetail,
  AgentSandbox,
  BuiltinItem,
  Personalisation,
  Skill,
  User,
} from "@/lib/types";
import { fetchAllAuthUsers } from "@/lib/auth-users";

export type ModelOption = { value: string; label: string };

type ProfileMemory = { agent_id: string; soul?: string; content?: string };

// GET /api/users/me/memories wraps the list: { memories: [...] }.
function profileMemories(value: unknown) {
  // SAFETY: the API returns either the wrapper object or the bare array; both
  // shapes carry the memories list this function normalizes.
  const v = value as { memories?: ProfileMemory[] } | ProfileMemory[] | undefined;
  return (Array.isArray(v) ? v : v?.memories) ?? [];
}

/**
 * The API tolerates a missing, string, or array allowlist; the editor only ever
 * works with the normalized array form so every consumer is spared the union.
 */
export function normalizeSandbox(sandbox: unknown): AgentSandbox {
  // SAFETY: normalizeSandbox receives API-shaped sandbox data at the boundary;
  // missing fields fall back through the ?. chain below.
  const s = sandbox as AgentSandbox | undefined;
  const mode = s?.network?.mode ?? "allow_all";
  const rawAllowlist = s?.network?.allowlist;
  const allowlist = Array.isArray(rawAllowlist)
    ? rawAllowlist
    : typeof rawAllowlist === "string"
      ? // SAFETY: guarded above that rawAllowlist is a string before splitting.
        (rawAllowlist as string)
          .split(/\r?\n|,/)
          .map((v) => v.trim())
          .filter(Boolean)
      : [];
  return { network: { mode, allowlist } };
}

/**
 * Everything the agent editor needs, in one payload. Both the settings routes
 * (via their loader) and the agent profile page (via {@link
 * agentSettingsQueryOptions}) bootstrap the editor from this shape.
 */
export interface AgentsSettingsLoaderData {
  agents: AgentDetail[];
  cachedModels: ModelOption[];
  isAdmin: boolean;
  currentUserId: string;
  allUsers: User[];
  builtinTemplates: BuiltinItem[];
  builtinSouls: BuiltinItem[];
  agentSkills: Skill[];
  agentSkillCanManageActivation: boolean;
  agentSkillPolicyDiagnostics: {
    dangling_disabled_refs: string[];
  };
  personalisation: Personalisation;
  selectedAgent?: AgentDetail;
}

/**
 * Every request here fails loudly on purpose. Swallowing them into `[]` turned a
 * server outage into a convincing empty account — no agents, no models, no
 * skills, and a blank soul/profile draft that overwrites real memory on save.
 * Rejections propagate to the route's `errorComponent` (or the caller's
 * `isError`), which is the only place that can tell the user what happened.
 */
export async function loadAgentsSettingsData(agentId = ""): Promise<AgentsSettingsLoaderData> {
  const [agentsRaw, modelsRaw, me, catalog] = await Promise.all([
    listAgents({ query: { include_all: true }, throwOnError: true }).then(
      ({ data }) => data?.agents ?? [],
    ),
    listModels({ throwOnError: true }).then(({ data }) => data?.models ?? []),
    getMe({ throwOnError: true }).then(({ data }) => data),
    Promise.all([
      listBuiltinResources({ path: { kind: "template" }, throwOnError: true }).then(
        // SAFETY: listBuiltinResources returns resources keyed under data.resources.
        ({ data }) => (data?.resources as BuiltinItem[]) ?? [],
      ),
      listBuiltinResources({ path: { kind: "soul" }, throwOnError: true }).then(
        // SAFETY: listBuiltinResources returns resources keyed under data.resources.
        ({ data }) => (data?.resources as BuiltinItem[]) ?? [],
      ),
    ]),
  ]);
  const isAdmin = me?.is_admin ?? false;
  // SAFETY: the agent-detail query response items are AgentDetail-shaped after
  // sandbox normalization; the map only adds the derived _highlight field.
  const agents = (agentsRaw ?? []).map((a) => ({
    ...a,
    sandbox: normalizeSandbox(a.sandbox),
    _highlight: a.id === agentId,
  })) as AgentDetail[];
  const selectedAgent = agentId ? agents.find((a) => a.id === agentId) : undefined;
  const allUsers = isAdmin ? await fetchAllAuthUsers() : [];
  const agentSkillResponse = agentId
    ? await listAgentSkills({ path: { id: agentId }, throwOnError: true }).then(({ data }) => data)
    : undefined;
  // SAFETY: listAgentSkills returns the agent's skills under data.skills.
  const agentSkills = (agentSkillResponse?.skills ?? []) as Skill[];
  let personalisation: Personalisation = {
    soul: "",
    soulDraft: "",
    profile: "",
    profileDraft: "",
    loaded: !!agentId,
  };
  if (agentId) {
    const { data: memsData } = await listProfileMemories({ throwOnError: true });
    const mems = profileMemories(memsData);
    const mem = mems.find((m) => m.agent_id === agentId);
    const soul = mem?.soul ?? "";
    const profile = mem?.content ?? "";
    personalisation = { soul, soulDraft: soul, profile, profileDraft: profile, loaded: true };
  }
  // SAFETY: catalog was built by the two listBuiltinResources calls above,
  // in the exact [templates, souls] order this destructure expects.
  const [builtinTemplates, builtinSouls] = catalog as [BuiltinItem[], BuiltinItem[]];
  return {
    agents,
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
    agentSkillCanManageActivation: agentSkillResponse?.can_manage_activation ?? false,
    agentSkillPolicyDiagnostics: agentSkillResponse?.policy_diagnostics ?? {
      dangling_disabled_refs: [],
    },
    selectedAgent,
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

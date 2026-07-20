import { useCallback, useEffect, useState } from "react";
import { useLoaderData, useNavigate, useParams } from "@tanstack/react-router";
import {
  assignAgentUser,
  createAgent,
  deleteAgent,
  deleteAgentSkill,
  deleteAgentSkillFile,
  getAgentSkill,
  getAgentSkillFile,
  getBuiltinResource,
  getMe,
  installAgentSkill,
  listAgents,
  listAgentSkills,
  listAgentUsers,
  listBuiltinResources,
  listChannels,
  listModels,
  listProfileMemories,
  removeAgentUser,
  updateAgent,
  updateAgentSkill,
  updateChannel,
  uploadAgentSkill,
} from "@/lib/api-client/sdk.gen";
import type {
  CreateAgentData,
  InstallAgentSkillData,
  UpdateAgentData,
  UpdateAgentSkillData,
} from "@/lib/api-client/types.gen";
import type {
  AgentDetail,
  AgentSandbox,
  BuiltinItem,
  Channel,
  Personalisation,
  Skill,
  User,
} from "@/lib/types";
import { apiErrorMessage } from "@/lib/api-error";
import { fetchAllAuthUsers } from "@/lib/auth-users";
import { AgentForm } from "./AgentForm";
import { TemplateModal } from "./TemplateModal";
import { SkillInstallModal } from "./SkillInstallModal";
import { ConfirmDialog } from "./ConfirmDialog";
import { useToast, ToastContainer } from "@/hooks/use-toast";
import { SettingsCard, SettingsGridPage } from "@/features/settings/SettingsCardGrid";
import { Button } from "@/components/ui/button";
import { Bot, Plus } from "lucide-react";
import { useI18n } from "@/lib/i18n";

type ProfileMemory = { agent_id: string; soul?: string; content?: string };

function profileMemories(value: unknown) {
  return (value as ProfileMemory[]) ?? [];
}

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

function emptyForm(): Omit<AgentDetail, "id"> {
  return {
    name: "",
    model: "",
    model_thinking: "",
    model_strong: "",
    model_strong_thinking: "",
    model_fast: "",
    model_fast_thinking: "",
    system_prompt: "",
    soul: "",
    scope: "restricted",
    enabled: true,
    creator_id: "",
    sandbox: { network: { mode: "allow_all", allowlist: [] } },
    template_id: "",
  };
}

function agentRequestBody(form: Omit<AgentDetail, "id">): CreateAgentData["body"] {
  return {
    name: form.name,
    model: form.model,
    model_thinking: form.model_thinking,
    model_strong: form.model_strong,
    model_strong_thinking: form.model_strong_thinking,
    model_fast: form.model_fast,
    model_fast_thinking: form.model_fast_thinking,
    system_prompt: form.system_prompt,
    soul: form.soul,
    scope: form.scope,
    enabled: form.enabled,
    creator_id: form.creator_id,
    sandbox: form.sandbox,
    template_id: form.template_id,
  };
}

export type ModelOption = { value: string; label: string };

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

export interface AgentsPageState {
  agents: AgentDetail[];
  channels: Channel[];
  cachedModels: ModelOption[];
  isAdmin: boolean;
  currentUserId: string;
  allUsers: User[];
  showForm: boolean;
  editingId: string | null;
  activeTab: string;
  showTemplateModal: boolean;
  form: Omit<AgentDetail, "id">;
  selectedSoulID: string;
  builtinTemplates: BuiltinItem[];
  builtinSouls: BuiltinItem[];
  builtinSkills: BuiltinItem[];
  assignedUsers: User[];
  addUserId: string;
  selectedChannelIDs: string[];
  confirmMsg: string;
  confirmAction: () => void;
  agentSkills: Skill[];
  agentSkillsLoading: boolean;
  userSkills: Skill[];
  skillViewFilter: string;
  skillScopeFilter: string;
  skillListQuery: string;
  selectedSkillKey: string;
  selectedSkill: Skill | null;
  selectedSkillLoading: boolean;
  selectedSkillSaving: boolean;
  selectedSkillDirty: boolean;
  selectedSkillEditMode: boolean;
  selectedSkillShowAdvanced: boolean;
  selectedSkillActiveFile: string;
  selectedSkillFileContent: string;
  /** "base64" when the active file is binary; empty for text files. */
  selectedSkillFileEncoding: string;
  selectedSkillFileLoading: boolean;
  selectedSkillFileCache: Record<string, { content: string; encoding?: string }>;
  selectedSkillAddingFile: boolean;
  selectedSkillNewFileName: string;
  skillInstallModalOpen: boolean;
  skillInstallScope: "user_agent" | "system_agent";
  personalisation: Personalisation;
}

export function AgentsPage() {
  const navigate = useNavigate();
  const params = useParams({ strict: false }) as { agentId?: string; tab?: string };
  const routeAgentId = params.agentId ?? "";
  const routeTab = params.tab ?? "config";
  const loaderData = useLoaderData({ strict: false }) as AgentsSettingsLoaderData | undefined;
  const selectedAgent = loaderData?.selectedAgent;
  const [state, setState] = useState<AgentsPageState>({
    agents: loaderData?.agents ?? [],
    channels: loaderData?.channels ?? [],
    cachedModels: loaderData?.cachedModels ?? [],
    isAdmin: loaderData?.isAdmin ?? false,
    currentUserId: loaderData?.currentUserId ?? "",
    allUsers: loaderData?.allUsers ?? [],
    showForm: !!selectedAgent,
    editingId: selectedAgent?.id ?? null,
    activeTab: routeTab,
    showTemplateModal: false,
    form: selectedAgent
      ? { ...selectedAgent, scope: selectedAgent.scope || "system", template_id: "" }
      : emptyForm(),
    selectedSoulID: "",
    builtinTemplates: loaderData?.builtinTemplates ?? [],
    builtinSouls: loaderData?.builtinSouls ?? [],
    builtinSkills: [],
    assignedUsers: [],
    addUserId: "",
    selectedChannelIDs: loaderData?.selectedChannelIDs ?? [],
    confirmMsg: "",
    confirmAction: () => {},
    agentSkills: loaderData?.agentSkills ?? [],
    agentSkillsLoading: false,
    userSkills: [],
    skillViewFilter: "enabled",
    skillScopeFilter: "all",
    skillListQuery: "",
    selectedSkillKey: "",
    selectedSkill: null,
    selectedSkillLoading: false,
    selectedSkillSaving: false,
    selectedSkillDirty: false,
    selectedSkillEditMode: false,
    selectedSkillShowAdvanced: false,
    selectedSkillActiveFile: "SKILL.md",
    selectedSkillFileContent: "",
    selectedSkillFileEncoding: "",
    selectedSkillFileLoading: false,
    selectedSkillFileCache: {},
    selectedSkillAddingFile: false,
    selectedSkillNewFileName: "",
    skillInstallModalOpen: false,
    skillInstallScope: "user_agent",
    personalisation: loaderData?.personalisation ?? {
      soul: "",
      soulDraft: "",
      profile: "",
      profileDraft: "",
      loaded: false,
    },
  });
  const { toasts, showToast } = useToast();
  const { t } = useI18n();

  const set = useCallback((patch: Partial<AgentsPageState>) => {
    setState((prev) => ({ ...prev, ...patch }));
  }, []);

  const requestedAgentID = useCallback(() => routeAgentId, [routeAgentId]);

  const loadAgents = useCallback(
    async (currentState?: AgentsPageState) => {
      try {
        const { data } = await listAgents({ query: { include_all: true }, throwOnError: true });
        const agents = (data?.agents ?? []).map((a) => ({
          ...a,
          sandbox: normalizeSandbox(a.sandbox),
          _highlight: a.id === requestedAgentID(),
        })) as AgentDetail[];
        setState((prev) => {
          const s = currentState ?? prev;
          return { ...s, agents };
        });
        return agents;
      } catch (e) {
        console.error(e);
        return [];
      }
    },
    [requestedAgentID],
  );

  const loadChannels = useCallback(async () => {
    try {
      const { data } = await listChannels({ throwOnError: true });
      const channels = ((data?.channels ?? []) as Channel[]).map(normalizeChannel);
      setState((prev) => ({ ...prev, channels }));
      return channels;
    } catch {
      setState((prev) => ({ ...prev, channels: [] }));
      return [];
    }
  }, []);

  const loadAgentSkills = useCallback(async (agentId: string | null) => {
    if (!agentId) {
      setState((prev) => ({ ...prev, agentSkills: [] }));
      return [];
    }
    setState((prev) => ({ ...prev, agentSkillsLoading: true }));
    try {
      const { data } = await listAgentSkills({ path: { id: agentId }, throwOnError: true });
      const agentSkills = data?.skills ?? [];
      setState((prev) => ({
        ...prev,
        agentSkills: agentSkills as Skill[],
        agentSkillsLoading: false,
      }));
      return agentSkills;
    } catch {
      setState((prev) => ({ ...prev, agentSkills: [], agentSkillsLoading: false }));
      return [];
    }
  }, []);

  const loadPersonalisation = useCallback(async (agentId: string) => {
    if (!agentId) return;
    setState((prev) => ({
      ...prev,
      personalisation: { soul: "", soulDraft: "", profile: "", profileDraft: "", loaded: false },
    }));
    try {
      const { data } = await listProfileMemories({ throwOnError: true });
      const mems = profileMemories(data);
      const mem = mems.find((m) => m.agent_id === agentId);
      const soul = mem?.soul ?? "";
      const profile = mem?.content ?? "";
      setState((prev) => ({
        ...prev,
        personalisation: { soul, soulDraft: soul, profile, profileDraft: profile, loaded: true },
      }));
    } catch {
      setState((prev) => ({
        ...prev,
        personalisation: { soul: "", soulDraft: "", profile: "", profileDraft: "", loaded: true },
      }));
    }
  }, []);

  const loadAssignedUsers = useCallback(async (agentId: string) => {
    try {
      const { data } = await listAgentUsers({ path: { id: agentId }, throwOnError: true });
      const assignedUsers = (data?.users ?? []).map(
        (u) => ({ id: u.id ?? "", email: u.username ?? "", name: "" }) as User,
      );
      setState((prev) => ({ ...prev, assignedUsers }));
    } catch {
      setState((prev) => ({ ...prev, assignedUsers: [] }));
    }
  }, []);

  const resetForm = useCallback(() => {
    void navigate({ to: "/settings/agents" });
    setState((prev) => ({
      ...prev,
      form: emptyForm(),
      selectedSoulID: "",
      editingId: null,
      showForm: false,
      activeTab: "config",
      agentSkills: [],
      skillViewFilter: "enabled",
      skillScopeFilter: "all",
      skillListQuery: "",
      selectedSkillKey: "",
      selectedSkill: null,
      selectedSkillDirty: false,
      selectedSkillEditMode: false,
      selectedSkillShowAdvanced: false,
      selectedSkillActiveFile: "SKILL.md",
      selectedSkillFileContent: "",
      selectedSkillFileEncoding: "",
      selectedSkillFileCache: {},
      selectedSkillAddingFile: false,
      selectedSkillNewFileName: "",
      assignedUsers: [],
      addUserId: "",
      selectedChannelIDs: [],
      personalisation: { soul: "", soulDraft: "", profile: "", profileDraft: "", loaded: false },
      skillInstallModalOpen: false,
    }));
  }, [navigate]);

  const dedicatedChannelsForAgent = useCallback((agentId: string, channels: Channel[]) => {
    return channels.filter((ch) => ch.id !== ch.type && ch.agent_id === agentId);
  }, []);

  const editAgent = useCallback(
    async (a: AgentDetail) => {
      void navigate({
        to: "/settings/agents/$agentId/$tab",
        params: { agentId: a.id, tab: "config" },
      });
      setState((prev) => ({
        ...prev,
        form: {
          ...a,
          scope: a.scope || "system",
          template_id: "",
          sandbox: normalizeSandbox(a.sandbox),
        },
        selectedSoulID: "",
        editingId: a.id,
        activeTab: routeTab,
        personalisation: { soul: "", soulDraft: "", profile: "", profileDraft: "", loaded: false },
        agentSkills: [],
        selectedSkillKey: "",
        selectedSkill: null,
        selectedSkillDirty: false,
        selectedSkillEditMode: false,
        selectedSkillShowAdvanced: false,
        selectedSkillFileCache: {},
        assignedUsers: [],
        showForm: true,
      }));
      const [channels] = await Promise.all([
        loadChannels(),
        loadAgentSkills(a.id),
        loadPersonalisation(a.id),
      ]);
      const selectedChannelIDs = dedicatedChannelsForAgent(a.id, channels).map((c) => c.id);
      setState((prev) => ({ ...prev, selectedChannelIDs }));
    },
    [
      navigate,
      routeTab,
      loadChannels,
      loadAgentSkills,
      loadPersonalisation,
      dedicatedChannelsForAgent,
    ],
  );

  const startCreate = useCallback(() => {
    resetForm();
    setState((prev) => {
      if (prev.builtinTemplates.length > 0) {
        return { ...prev, showTemplateModal: true, showForm: false };
      }
      return { ...prev, showForm: true };
    });
  }, [resetForm]);

  useEffect(() => {
    if (routeAgentId && state.editingId === routeAgentId && state.activeTab !== routeTab) {
      setState((prev) => ({ ...prev, activeTab: routeTab }));
    }
  }, [routeAgentId, routeTab, state.editingId, state.activeTab]);

  const saveAgent = useCallback(
    async (currentState: AgentsPageState) => {
      try {
        const payload = {
          ...currentState.form,
          sandbox: normalizeSandbox(currentState.form.sandbox),
        };
        if (payload.sandbox.network.mode !== "whitelist") {
          payload.sandbox.network.allowlist = [];
        }

        if (currentState.editingId) {
          await updateAgent({
            path: { id: currentState.editingId },
            body: agentRequestBody(payload) as UpdateAgentData["body"],
            throwOnError: true,
          });
          await saveChannelBindings(currentState.editingId, currentState);
        } else {
          const { data: created } = await createAgent({
            body: agentRequestBody(payload),
            throwOnError: true,
          });
          const newId = created!.id!;
          setState((prev) => ({ ...prev, editingId: newId }));
          await Promise.all([
            saveChannelBindings(newId, { ...currentState, editingId: newId }),
            loadAgentSkills(newId),
            loadPersonalisation(newId),
          ]);
          const channels = await loadChannels();
          const selectedChannelIDs = dedicatedChannelsForAgent(newId, channels).map((c) => c.id);
          setState((prev) => ({ ...prev, selectedChannelIDs }));
        }
        await loadAgents();
        showToast("Saved");
      } catch (e) {
        showToast(apiErrorMessage(e, t("common.error")), "error");
      }
    },
    [
      loadAgents,
      loadChannels,
      loadAgentSkills,
      loadPersonalisation,
      dedicatedChannelsForAgent,
      showToast,
    ],
  );

  const saveChannelBindings = useCallback(
    async (agentID: string, currentState: AgentsPageState) => {
      if (!currentState.isAdmin) return;
      const selected = new Set(currentState.selectedChannelIDs);
      const available = currentState.channels.filter(
        (ch) => ch.id !== ch.type && ch.enabled && (!ch.agent_id || ch.agent_id === agentID),
      );
      for (const ch of available) {
        const wantsAgent = selected.has(ch.id);
        const nextAgentID = wantsAgent ? agentID : "";
        if ((ch.agent_id || "") === nextAgentID) continue;
        await updateChannel({
          path: { id: ch.id },
          body: {
            type: ch.type,
            agent_id: nextAgentID,
            config: JSON.stringify(ch._config || {}),
          },
          throwOnError: true,
        });
      }
      await loadChannels();
    },
    [loadChannels],
  );

  const doDeleteAgent = useCallback(
    async (id: string) => {
      try {
        await deleteAgent({ path: { id }, throwOnError: true });
        setState((prev) => {
          if (prev.editingId === id) {
            return {
              ...prev,
              form: emptyForm(),
              editingId: null,
              showForm: false,
            };
          }
          return prev;
        });
        void navigate({ to: "/settings/agents" });
        await loadAgents();
        showToast("Deleted");
      } catch (e) {
        showToast(apiErrorMessage(e, t("common.error")), "error");
      }
    },
    [loadAgents, showToast, navigate],
  );

  const addUser = useCallback(
    async (currentState: AgentsPageState) => {
      if (!currentState.addUserId || !currentState.editingId) return;
      try {
        await assignAgentUser({
          path: { id: currentState.editingId },
          body: { user_id: currentState.addUserId },
          throwOnError: true,
        });
        setState((prev) => ({ ...prev, addUserId: "" }));
        await loadAssignedUsers(currentState.editingId);
        showToast("User assigned");
      } catch (e) {
        showToast(apiErrorMessage(e, t("common.error")), "error");
      }
    },
    [loadAssignedUsers, showToast],
  );

  const removeUser = useCallback(
    async (userId: string, editingId: string) => {
      try {
        await removeAgentUser({
          path: { id: editingId, userId },
          throwOnError: true,
        });
        await loadAssignedUsers(editingId);
        showToast("User removed");
      } catch (e) {
        showToast(apiErrorMessage(e, t("common.error")), "error");
      }
    },
    [loadAssignedUsers, showToast],
  );

  const skillKey = (sk: { scope: string; id: string }) => `${sk.scope}:${sk.id}`;

  const selectSkillFile = useCallback(
    async (
      path: string,
      skill: Skill,
      fileCache: Record<string, { content: string; encoding?: string }>,
      editingId: string | null,
      skipDirtyCheck = false,
      isDirty = false,
    ) => {
      if (!skill || !path) return;
      if (!skipDirtyCheck && isDirty && !confirm("Discard unsaved changes?")) return;
      setState((prev) => ({ ...prev, selectedSkillActiveFile: path }));
      if (Object.prototype.hasOwnProperty.call(fileCache, path)) {
        setState((prev) => ({
          ...prev,
          selectedSkillFileContent: fileCache[path].content,
          selectedSkillFileEncoding: fileCache[path].encoding ?? "",
          selectedSkillDirty: false,
        }));
        return;
      }
      setState((prev) => ({ ...prev, selectedSkillFileLoading: true }));
      try {
        const { data: res } = await getAgentSkillFile({
          path: { id: editingId ?? "", skillId: skill.name },
          query: { path, scope: skill.scope as UpdateAgentSkillData["query"]["scope"] },
          throwOnError: true,
        });
        const file = res as { content?: string; encoding?: string } | undefined;
        const content = file?.content ?? "";
        const encoding = file?.encoding ?? "";
        setState((prev) => ({
          ...prev,
          selectedSkillFileContent: content,
          selectedSkillFileEncoding: encoding,
          selectedSkillFileCache: {
            ...prev.selectedSkillFileCache,
            [path]: { content, encoding: encoding || undefined },
          },
          selectedSkillDirty: false,
          selectedSkillFileLoading: false,
        }));
      } catch (e) {
        showToast(apiErrorMessage(e, t("common.error")), "error");
        setState((prev) => ({ ...prev, selectedSkillFileLoading: false }));
      }
    },
    [showToast],
  );

  const selectSkill = useCallback(
    async (sk: Skill, currentState: AgentsPageState) => {
      if (!sk) return;
      if (currentState.selectedSkillDirty && !confirm("Discard unsaved changes?")) return;
      const key = skillKey(sk);
      setState((prev) => ({
        ...prev,
        selectedSkillKey: key,
        selectedSkillLoading: true,
        selectedSkill: null,
        selectedSkillDirty: false,
        selectedSkillEditMode: false,
        selectedSkillShowAdvanced: false,
        selectedSkillFileCache: {},
        selectedSkillAddingFile: false,
        selectedSkillNewFileName: "",
      }));
      try {
        const { data: raw } = await getAgentSkill({
          path: { id: currentState.editingId ?? "", skillId: sk.name },
          query: { scope: sk.scope as UpdateAgentSkillData["query"]["scope"] },
          throwOnError: true,
        });
        const unwrapped = raw as Skill;
        const skill: Skill = {
          ...unwrapped,
          scope: sk.scope,
        };
        const files = unwrapped.files ?? ["SKILL.md"];
        const initialFile = files.includes("SKILL.md") ? "SKILL.md" : files[0];
        setState((prev) => ({
          ...prev,
          selectedSkill: skill,
          selectedSkillLoading: false,
        }));
        await selectSkillFile(initialFile, skill, {}, currentState.editingId, true, false);
      } catch (e) {
        showToast(apiErrorMessage(e, t("common.error")), "error");
        setState((prev) => ({ ...prev, selectedSkillLoading: false }));
      }
    },
    [selectSkillFile, showToast],
  );

  const saveSelectedSkill = useCallback(
    async (currentState: AgentsPageState) => {
      const { selectedSkill, selectedSkillFileContent, selectedSkillActiveFile } = currentState;
      if (!selectedSkill || selectedSkill.scope === "system") return;
      // Binary files are view-only: writing their base64 transport form back
      // through the JSON files map would corrupt them.
      const activeFileEditable = currentState.selectedSkillFileEncoding !== "base64";
      setState((prev) => ({ ...prev, selectedSkillSaving: true }));
      try {
        await updateAgentSkill({
          path: {
            id: currentState.editingId ?? "",
            skillId: selectedSkill.name,
          },
          query: { scope: selectedSkill.scope as UpdateAgentSkillData["query"]["scope"] },
          body: {
            description: selectedSkill.description,
            disable_model_invocation: !!selectedSkill.disable_model_invocation,
            ...(activeFileEditable
              ? { files: { [selectedSkillActiveFile]: selectedSkillFileContent } }
              : {}),
          } as UpdateAgentSkillData["body"],
          throwOnError: true,
        });
        setState((prev) => ({
          ...prev,
          selectedSkillDirty: false,
          selectedSkillFileCache: activeFileEditable
            ? {
                ...prev.selectedSkillFileCache,
                [selectedSkillActiveFile]: { content: selectedSkillFileContent },
              }
            : prev.selectedSkillFileCache,
        }));
        const { data: raw2 } = await getAgentSkill({
          path: {
            id: currentState.editingId ?? "",
            skillId: selectedSkill.name,
          },
          query: { scope: selectedSkill.scope as UpdateAgentSkillData["query"]["scope"] },
          throwOnError: true,
        });
        setState((prev) => ({
          ...prev,
          selectedSkill: { ...(raw2 as Skill), scope: selectedSkill.scope },
          selectedSkillSaving: false,
        }));
        await loadAgentSkills(currentState.editingId);
        showToast("Saved");
      } catch (e) {
        showToast(apiErrorMessage(e, t("common.error")), "error");
        setState((prev) => ({ ...prev, selectedSkillSaving: false }));
      }
    },
    [loadAgentSkills, showToast],
  );

  const deleteSkill = useCallback(
    async (sk: Skill, currentState: AgentsPageState) => {
      if (sk.scope === "system") return;
      if (!confirm(`Delete skill "${sk.name}"? This cannot be undone.`)) return;
      try {
        await deleteAgentSkill({
          path: { id: currentState.editingId ?? "", skillId: sk.name },
          query: { scope: sk.scope as UpdateAgentSkillData["query"]["scope"] },
          throwOnError: true,
        });
        setState((prev) => {
          const wasSelected = prev.selectedSkillKey === skillKey(sk);
          return {
            ...prev,
            selectedSkillKey: wasSelected ? "" : prev.selectedSkillKey,
            selectedSkill: wasSelected ? null : prev.selectedSkill,
            selectedSkillDirty: wasSelected ? false : prev.selectedSkillDirty,
          };
        });
        await loadAgentSkills(currentState.editingId);
        showToast("Skill removed");
      } catch (e) {
        showToast(apiErrorMessage(e, t("common.error")), "error");
      }
    },
    [loadAgentSkills, showToast],
  );

  const doSkillInstall = useCallback(
    async (source: string, scope: "user_agent" | "system_agent", currentState: AgentsPageState) => {
      if (!source) {
        showToast("Choose a skill first", "error");
        return;
      }
      if (!currentState.editingId) return;
      setState(
        (prev) =>
          ({ ...prev, skillInstalling: true }) as AgentsPageState & { skillInstalling: boolean },
      );
      try {
        const { data: res } = await installAgentSkill({
          path: { id: currentState.editingId },
          body: { source, scope } as InstallAgentSkillData["body"],
          throwOnError: true,
        });
        showToast("Installed: " + (res?.name ?? "skill"));
        setState((prev) => ({ ...prev, skillInstallModalOpen: false }));
        const updated = await loadAgentSkills(currentState.editingId);
        const created = updated.find((sk) => sk.name === (res?.name ?? ""));
        if (created) {
          await selectSkill({ ...created, scope } as Skill, {
            ...currentState,
            agentSkills: updated as Skill[],
          });
        }
      } catch (e) {
        showToast(apiErrorMessage(e, t("common.error")), "error");
      } finally {
        setState((prev) => {
          const p = prev as AgentsPageState & { skillInstalling?: boolean };
          void p;
          return { ...prev };
        });
      }
    },
    [loadAgentSkills, selectSkill, showToast],
  );

  const doSkillUpload = useCallback(
    async (file: File, scope: "user_agent" | "system_agent", currentState: AgentsPageState) => {
      if (!file) {
        showToast("Choose a .zip file first", "error");
        return;
      }
      if (!currentState.editingId) return;
      try {
        const { data: res } = await uploadAgentSkill({
          path: { id: currentState.editingId },
          body: { file, scope },
          throwOnError: true,
        });
        showToast("Uploaded: " + (res?.name ?? "skill"));
        setState((prev) => ({ ...prev, skillInstallModalOpen: false }));
        const updated = await loadAgentSkills(currentState.editingId);
        const created = updated.find((sk) => sk.id === res?.id);
        if (created) {
          await selectSkill({ ...created, scope } as Skill, {
            ...currentState,
            agentSkills: updated as Skill[],
          });
        }
      } catch (e) {
        showToast(apiErrorMessage(e, t("common.error")), "error");
      }
    },
    [loadAgentSkills, selectSkill, showToast],
  );

  const deleteSelectedSkillFile = useCallback(
    async (currentState: AgentsPageState) => {
      const { selectedSkill, selectedSkillActiveFile, editingId } = currentState;
      if (!selectedSkill || selectedSkill.scope === "system") return;
      if (!confirm(`Delete file "${selectedSkillActiveFile}"?`)) return;
      try {
        await deleteAgentSkillFile({
          path: {
            id: editingId ?? "",
            skillId: selectedSkill.name,
          },
          query: {
            path: selectedSkillActiveFile,
            scope: selectedSkill.scope as UpdateAgentSkillData["query"]["scope"],
          },
          throwOnError: true,
        });
        const newFiles = (selectedSkill.files ?? ["SKILL.md"]).filter(
          (f) => f !== selectedSkillActiveFile,
        );
        setState((prev) => ({
          ...prev,
          selectedSkill: { ...selectedSkill, files: newFiles },
          selectedSkillActiveFile: "SKILL.md",
          selectedSkillFileCache: Object.fromEntries(
            Object.entries(prev.selectedSkillFileCache).filter(
              ([k]) => k !== selectedSkillActiveFile,
            ),
          ),
        }));
        await selectSkillFile(
          "SKILL.md",
          { ...selectedSkill, files: newFiles },
          currentState.selectedSkillFileCache,
          editingId,
          true,
          false,
        );
      } catch (e) {
        showToast(apiErrorMessage(e, t("common.error")), "error");
      }
    },
    [selectSkillFile, showToast],
  );

  const applySoul = useCallback(
    async (soulID: string) => {
      if (!soulID) return;
      try {
        const { data: full } = await getBuiltinResource({
          path: { kind: "soul", id: soulID },
          throwOnError: true,
        });
        setState((prev) => ({
          ...prev,
          form: { ...prev.form, soul: full?.content ?? "" },
        }));
      } catch (e) {
        setState((prev) => ({ ...prev, selectedSoulID: "" }));
        showToast(apiErrorMessage(e, t("common.error")), "error");
      }
    },
    [showToast],
  );

  const pickTemplate = useCallback(
    async (tmpl: BuiltinItem) => {
      try {
        const { data: full } = await getBuiltinResource({
          path: { kind: "template", id: tmpl.id },
          throwOnError: true,
        });
        const meta = (full?.metadata ?? {}) as Record<string, string>;
        let soulContent = "";
        if (meta.soul_id) {
          try {
            const { data: soul } = await getBuiltinResource({
              path: { kind: "soul", id: meta.soul_id },
              throwOnError: true,
            });
            soulContent = soul?.content ?? "";
          } catch {}
        }
        setState((prev) => ({
          ...prev,
          form: {
            ...prev.form,
            name: prev.form.name || tmpl.name || "",
            model: meta.model || prev.form.model || "",
            system_prompt: full?.content ?? "",
            soul: soulContent,
            template_id: tmpl.id,
          },
          showTemplateModal: false,
          showForm: true,
          activeTab: "config",
        }));
      } catch (e) {
        showToast(apiErrorMessage(e, t("common.error")), "error");
      }
    },
    [showToast],
  );

  const detail = state.showForm ? (
    <AgentForm
      state={state}
      onSetState={set}
      onSave={() => saveAgent(state)}
      onCancel={resetForm}
      onLoadAssignedUsers={loadAssignedUsers}
      onAddUser={() => addUser(state)}
      onRemoveUser={(userId) => removeUser(userId, state.editingId ?? "")}
      onApplySoul={applySoul}
      onSelectSkill={(sk) => selectSkill(sk, state)}
      onSaveSelectedSkill={() => saveSelectedSkill(state)}
      onDeleteSkill={(sk) => deleteSkill(sk, state)}
      onSelectSkillFile={(path, skipDirtyCheck) =>
        selectSkillFile(
          path,
          state.selectedSkill!,
          state.selectedSkillFileCache,
          state.editingId,
          skipDirtyCheck,
          state.selectedSkillDirty,
        )
      }
      onDeleteSkillFile={() => deleteSelectedSkillFile(state)}
      onOpenSkillInstallModal={(scope) =>
        setState((prev) => ({
          ...prev,
          skillInstallModalOpen: true,
          skillInstallScope:
            scope ?? (prev.isAdmin && prev.editingId ? "system_agent" : "user_agent"),
        }))
      }
      onDelete={state.editingId ? () => doDeleteAgent(state.editingId!) : undefined}
    />
  ) : undefined;

  const canEditAgent = (a: AgentDetail) =>
    state.isAdmin || (!!a.creator_id && a.creator_id === state.currentUserId);
  const modelLabel = (value: string) =>
    state.cachedModels.find((m) => m.value === value)?.label ?? value;

  return (
    <>
      {state.showForm ? (
        detail
      ) : (
        <SettingsGridPage
          title={t("settings.nav.agents")}
          action={
            <Button onClick={startCreate} variant="outline" size="sm">
              <Plus className="size-4" />
              {t("agents.form.newAgent")}
            </Button>
          }
        >
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
            {state.agents.map((a) => {
              const canEdit = canEditAgent(a);
              return (
                <SettingsCard
                  key={a.id}
                  icon={<Bot className="size-4" />}
                  title={a.name || a.id}
                  active={routeAgentId === a.id}
                  to={canEdit ? "/settings/agents/$agentId/$tab" : undefined}
                  params={canEdit ? { agentId: a.id, tab: "config" } : undefined}
                  onClick={canEdit ? () => void editAgent(a) : undefined}
                  footer={
                    <>
                      <span
                        className={`size-1.5 shrink-0 rounded-full ${
                          a.enabled ? "bg-chart-3" : "bg-muted-foreground"
                        }`}
                      />
                      <span className="truncate font-mono text-xs text-muted-foreground">
                        {a.model ? modelLabel(a.model) : "—"}
                      </span>
                    </>
                  }
                />
              );
            })}
          </div>
        </SettingsGridPage>
      )}
      {state.showTemplateModal && (
        <TemplateModal
          templates={state.builtinTemplates}
          onPick={pickTemplate}
          onPickBlank={() =>
            setState((prev) => ({
              ...prev,
              showTemplateModal: false,
              showForm: true,
              activeTab: "config",
            }))
          }
          onClose={() => setState((prev) => ({ ...prev, showTemplateModal: false }))}
        />
      )}
      {state.skillInstallModalOpen && (
        <SkillInstallModal
          state={state}
          onClose={() => setState((prev) => ({ ...prev, skillInstallModalOpen: false }))}
          onSetScope={(scope) => setState((prev) => ({ ...prev, skillInstallScope: scope }))}
          onInstall={(source, scope) => doSkillInstall(source, scope, state)}
          onUpload={(file, scope) => doSkillUpload(file, scope, state)}
          showToast={showToast}
        />
      )}
      {state.confirmMsg && (
        <ConfirmDialog
          message={state.confirmMsg}
          onConfirm={() => {
            state.confirmAction();
            setState((prev) => ({ ...prev, confirmMsg: "" }));
          }}
          onCancel={() => setState((prev) => ({ ...prev, confirmMsg: "" }))}
        />
      )}
      <ToastContainer messages={toasts} />
    </>
  );
}

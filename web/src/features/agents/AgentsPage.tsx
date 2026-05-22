import { useCallback, useEffect, useState } from "react";
import { useLoaderData, useNavigate, useParams } from "@tanstack/react-router";
import {
  assignAgentUser,
  createAgent,
  deleteAgent,
  deleteAgentScopedSkill,
  deleteAgentScopedSkillFile,
  getAgentScopedSkill,
  getAgentScopedSkillFile,
  getBuiltinResource,
  getMe,
  installAgentScopedSkill,
  listAgents,
  listAgentSkills,
  listAgentUsers,
  listAuthUsers,
  listBuiltinResources,
  listChannels,
  listModels,
  listProfileMemories,
  removeAgentUser,
  setProfileMemory,
  setProfileSoul,
  updateAgent,
  updateAgentScopedSkill,
  updateChannel,
  uploadAgentScopedSkill,
} from "@/lib/api-client/sdk.gen";
import type {
  CreateAgentData,
  InstallAgentScopedSkillData,
  UpdateAgentData,
  UpdateAgentScopedSkillData,
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
import { Button } from "@/components/ui/button";
import { AgentList } from "./AgentList";
import { AgentForm } from "./AgentForm";
import { TemplateModal } from "./TemplateModal";
import { SkillInstallModal } from "./SkillInstallModal";
import { ConfirmDialog } from "./ConfirmDialog";
import { SettingsDetailLayout } from "@/features/settings/SettingsDetailLayout";
import { SettingsListHeader } from "@/features/settings/SettingsListPanel";

type Toast = { message: string; type: "success" | "error" } | null;

function ToastAlert({ toast }: { toast: Toast }) {
  if (!toast) return null;
  return (
    <div
      className={`fixed bottom-4 right-4 z-50 w-auto max-w-sm rounded-xl border px-4 py-3 shadow-lg text-sm font-medium ${
        toast.type === "error"
          ? "border-destructive/30 bg-destructive/10 text-destructive-foreground"
          : "border-success/30 bg-success/10 text-success-foreground"
      }`}
    >
      {toast.message}
    </div>
  );
}

export function normalizeSandbox(sandbox: unknown): AgentSandbox {
  const s = sandbox as AgentSandbox | undefined;
  const mode = s?.network?.mode ?? "disabled";
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
    model_strong: "",
    model_fast: "",
    system_prompt: "",
    soul: "",
    scope: "restricted",
    enabled: true,
    creator_id: 0,
    sandbox: { network: { mode: "disabled", allowlist: [] } },
    template_id: "",
  };
}

function agentRequestBody(form: Omit<AgentDetail, "id">): CreateAgentData["body"] {
  return {
    name: form.name,
    model: form.model,
    model_strong: form.model_strong,
    model_fast: form.model_fast,
    system_prompt: form.system_prompt,
    soul: form.soul,
    scope: form.scope,
    enabled: form.enabled,
    creator_id: String(form.creator_id),
    sandbox: form.sandbox,
    template_id: form.template_id,
  };
}

export interface AgentsSettingsLoaderData {
  agents: AgentDetail[];
  channels: Channel[];
  cachedModels: string[];
  isAdmin: boolean;
  currentUserId: number;
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
    listAgents({ throwOnError: true })
      .then(({ data }) => (data?.items ?? []) as unknown as AgentDetail[])
      .catch(() => []),
    listModels({ throwOnError: true })
      .then(({ data }) => (data ?? []) as unknown as Array<{ provider: string; model: string }>)
      .catch(() => []),
    getMe({ throwOnError: true })
      .then(({ data }) => data as unknown as { is_admin?: boolean; user_id?: number } | undefined)
      .catch(() => null),
    Promise.all([
      listBuiltinResources({ path: { kind: "template" }, throwOnError: true })
        .then(({ data }) => (data ?? []) as unknown as BuiltinItem[])
        .catch(() => []),
      listBuiltinResources({ path: { kind: "soul" }, throwOnError: true })
        .then(({ data }) => (data ?? []) as unknown as BuiltinItem[])
        .catch(() => []),
    ]),
  ]);
  const isAdmin = me?.is_admin ?? false;
  const agents = (agentsRaw ?? []).map((a) => ({
    ...a,
    sandbox: normalizeSandbox(a.sandbox),
    _highlight: a.id === agentId,
  }));
  const selectedAgent = agentId ? agents.find((a) => a.id === agentId) : undefined;
  const channels = isAdmin
    ? (
        (await listChannels({ throwOnError: true })
          .then(({ data }) => (data?.items ?? []) as unknown as Channel[])
          .catch(() => [])) ?? []
      ).map(normalizeChannel)
    : [];
  const allUsers = isAdmin
    ? ((await listAuthUsers({ throwOnError: true })
        .then(({ data }) => (data ?? []) as unknown as User[])
        .catch(() => [])) ?? [])
    : [];
  const agentSkills = agentId
    ? ((await listAgentSkills({ path: { id: agentId }, throwOnError: true })
        .then(({ data }) => (data?.items ?? []) as unknown as Skill[])
        .catch(() => [])) ?? [])
    : [];
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
    const mems = (memsData ?? []) as unknown as Array<{
      agent_id: string;
      soul?: string;
      content?: string;
    }>;
    const mem = mems.find((m) => m.agent_id === agentId);
    const soul = mem?.soul ?? "";
    const profile = mem?.content ?? "";
    personalisation = { soul, soulDraft: soul, profile, profileDraft: profile, loaded: true };
  }
  const [builtinTemplates, builtinSouls] = catalog as [BuiltinItem[], BuiltinItem[]];
  return {
    agents,
    channels,
    cachedModels: (modelsRaw ?? []).map((m) => `${m.provider}/${m.model}`),
    isAdmin,
    currentUserId: me?.user_id ?? 0,
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
  cachedModels: string[];
  isAdmin: boolean;
  currentUserId: number;
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
  selectedSkillFileLoading: boolean;
  selectedSkillFileCache: Record<string, string>;
  selectedSkillAddingFile: boolean;
  selectedSkillNewFileName: string;
  skillInstallModalOpen: boolean;
  skillInstallScope: "user" | "agent";
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
    currentUserId: loaderData?.currentUserId ?? 0,
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
    selectedSkillFileLoading: false,
    selectedSkillFileCache: {},
    selectedSkillAddingFile: false,
    selectedSkillNewFileName: "",
    skillInstallModalOpen: false,
    skillInstallScope: "user",
    personalisation: loaderData?.personalisation ?? {
      soul: "",
      soulDraft: "",
      profile: "",
      profileDraft: "",
      loaded: false,
    },
  });
  const [toast, setToast] = useState<Toast>(null);

  const showToast = useCallback((message: string, type: "success" | "error" = "success") => {
    setToast({ message, type });
    setTimeout(() => setToast(null), 3000);
  }, []);

  const set = useCallback((patch: Partial<AgentsPageState>) => {
    setState((prev) => ({ ...prev, ...patch }));
  }, []);

  const requestedAgentID = useCallback(() => routeAgentId, [routeAgentId]);

  const loadAgents = useCallback(
    async (currentState?: AgentsPageState) => {
      try {
        const { data } = await listAgents({ throwOnError: true });
        const agents = ((data?.items ?? []) as unknown as AgentDetail[]).map((a) => ({
          ...a,
          sandbox: normalizeSandbox(a.sandbox),
          _highlight: a.id === requestedAgentID(),
        }));
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
      const channels = ((data?.items ?? []) as unknown as Channel[]).map(normalizeChannel);
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
      const agentSkills = (data?.items ?? []) as unknown as Skill[];
      setState((prev) => ({ ...prev, agentSkills, agentSkillsLoading: false }));
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
      const mems = (data ?? []) as unknown as Array<{
        agent_id: string;
        soul?: string;
        content?: string;
      }>;
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
      const assignedUsers = (data?.items ?? []) as unknown as User[];
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
          const newId = (created as unknown as AgentDetail).id;
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
        showToast((e as Error).message, "error");
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
        await loadAgents();
        showToast("Deleted");
      } catch (e) {
        showToast((e as Error).message, "error");
      }
    },
    [loadAgents, showToast],
  );

  const confirmDelete = useCallback((msg: string, action: () => void) => {
    setState((prev) => ({ ...prev, confirmMsg: msg, confirmAction: action }));
  }, []);

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
        showToast((e as Error).message, "error");
      }
    },
    [loadAssignedUsers, showToast],
  );

  const removeUser = useCallback(
    async (userId: number, editingId: string) => {
      try {
        await removeAgentUser({
          path: { id: editingId, userId: String(userId) },
          throwOnError: true,
        });
        await loadAssignedUsers(editingId);
        showToast("User removed");
      } catch (e) {
        showToast((e as Error).message, "error");
      }
    },
    [loadAssignedUsers, showToast],
  );

  const skillKey = (sk: { scope: string; id: string }) => `${sk.scope}:${sk.id}`;

  const selectSkillFile = useCallback(
    async (
      path: string,
      skill: Skill,
      fileCache: Record<string, string>,
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
          selectedSkillFileContent: fileCache[path],
          selectedSkillDirty: false,
        }));
        return;
      }
      setState((prev) => ({ ...prev, selectedSkillFileLoading: true }));
      try {
        const { data: res } = await getAgentScopedSkillFile({
          path: { id: editingId ?? "", scope: skill.scope, skillId: skill.id },
          query: { path },
          throwOnError: true,
        });
        const content = res?.content ?? "";
        setState((prev) => ({
          ...prev,
          selectedSkillFileContent: content,
          selectedSkillFileCache: { ...prev.selectedSkillFileCache, [path]: content },
          selectedSkillDirty: false,
          selectedSkillFileLoading: false,
        }));
      } catch (e) {
        showToast((e as Error).message, "error");
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
        const { data: full } = await getAgentScopedSkill({
          path: { id: currentState.editingId ?? "", scope: sk.scope, skillId: sk.id },
          throwOnError: true,
        });
        const skill: Skill = {
          ...(full as unknown as Skill & { content?: string }),
          scope: sk.scope,
        };
        const files = (full as unknown as Skill & { content?: string }).files ?? ["SKILL.md"];
        const initialFile = files.includes("SKILL.md") ? "SKILL.md" : files[0];
        setState((prev) => ({
          ...prev,
          selectedSkill: skill,
          selectedSkillLoading: false,
        }));
        await selectSkillFile(initialFile, skill, {}, currentState.editingId, true, false);
      } catch (e) {
        showToast((e as Error).message, "error");
        setState((prev) => ({ ...prev, selectedSkillLoading: false }));
      }
    },
    [selectSkillFile, showToast],
  );

  const toggleSkillStatus = useCallback(
    async (sk: Skill, currentState: AgentsPageState) => {
      if (sk.scope === "system") return;
      const next = sk.status === "active" ? "draft" : "active";
      try {
        await updateAgentScopedSkill({
          path: { id: currentState.editingId ?? "", scope: sk.scope, skillId: sk.id },
          body: { status: next },
          throwOnError: true,
        });
        setState((prev) => ({
          ...prev,
          agentSkills: prev.agentSkills.map((s) =>
            s.id === sk.id && s.scope === sk.scope ? { ...s, status: next } : s,
          ),
          userSkills: prev.userSkills.map((s) =>
            s.id === sk.id && s.scope === sk.scope ? { ...s, status: next } : s,
          ),
          selectedSkill:
            prev.selectedSkill && skillKey(prev.selectedSkill) === skillKey(sk)
              ? { ...prev.selectedSkill, status: next }
              : prev.selectedSkill,
        }));
      } catch (e) {
        showToast((e as Error).message, "error");
      }
    },
    [showToast],
  );

  const saveSelectedSkill = useCallback(
    async (currentState: AgentsPageState) => {
      const { selectedSkill, selectedSkillFileContent, selectedSkillActiveFile } = currentState;
      if (!selectedSkill || selectedSkill.scope === "system") return;
      setState((prev) => ({ ...prev, selectedSkillSaving: true }));
      try {
        await updateAgentScopedSkill({
          path: {
            id: currentState.editingId ?? "",
            scope: selectedSkill.scope,
            skillId: selectedSkill.id,
          },
          body: {
            description: selectedSkill.description,
            status: selectedSkill.status,
            disable_model_invocation: !!selectedSkill.disable_model_invocation,
            files: { [selectedSkillActiveFile]: selectedSkillFileContent },
          } as UpdateAgentScopedSkillData["body"],
          throwOnError: true,
        });
        setState((prev) => ({
          ...prev,
          selectedSkillDirty: false,
          selectedSkillFileCache: {
            ...prev.selectedSkillFileCache,
            [selectedSkillActiveFile]: selectedSkillFileContent,
          },
        }));
        const { data: full } = await getAgentScopedSkill({
          path: {
            id: currentState.editingId ?? "",
            scope: selectedSkill.scope,
            skillId: selectedSkill.id,
          },
          throwOnError: true,
        });
        setState((prev) => ({
          ...prev,
          selectedSkill: { ...(full as unknown as Skill), scope: selectedSkill.scope },
          selectedSkillSaving: false,
        }));
        await loadAgentSkills(currentState.editingId);
        showToast("Saved");
      } catch (e) {
        showToast((e as Error).message, "error");
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
        await deleteAgentScopedSkill({
          path: { id: currentState.editingId ?? "", scope: sk.scope, skillId: sk.id },
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
        showToast((e as Error).message, "error");
      }
    },
    [loadAgentSkills, showToast],
  );

  const doSkillInstall = useCallback(
    async (source: string, scope: "user" | "agent", currentState: AgentsPageState) => {
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
        const { data: res } = await installAgentScopedSkill({
          path: { id: currentState.editingId, scope },
          body: { source } as InstallAgentScopedSkillData["body"],
          throwOnError: true,
        });
        showToast("Installed: " + ((res as unknown as { name?: string })?.name ?? "skill"));
        setState((prev) => ({ ...prev, skillInstallModalOpen: false }));
        const updated = await loadAgentSkills(currentState.editingId);
        const created = updated.find(
          (sk) => sk.name === ((res as unknown as { name?: string })?.name ?? ""),
        );
        if (created) {
          await selectSkill({ ...created, scope }, { ...currentState, agentSkills: updated });
        }
      } catch (e) {
        showToast((e as Error).message, "error");
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
    async (file: File, scope: "user" | "agent", currentState: AgentsPageState) => {
      if (!file) {
        showToast("Choose a .zip file first", "error");
        return;
      }
      if (!currentState.editingId) return;
      try {
        const { data: res } = await uploadAgentScopedSkill({
          path: { id: currentState.editingId, scope },
          body: { file },
          throwOnError: true,
        });
        showToast("Uploaded: " + ((res as unknown as { name?: string })?.name ?? "skill"));
        setState((prev) => ({ ...prev, skillInstallModalOpen: false }));
        const updated = await loadAgentSkills(currentState.editingId);
        const created = updated.find((sk) => sk.id === (res as unknown as { id?: string })?.id);
        if (created) {
          await selectSkill({ ...created, scope }, { ...currentState, agentSkills: updated });
        }
      } catch (e) {
        showToast((e as Error).message, "error");
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
        await deleteAgentScopedSkillFile({
          path: { id: editingId ?? "", scope: selectedSkill.scope, skillId: selectedSkill.id },
          query: { path: selectedSkillActiveFile },
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
        showToast((e as Error).message, "error");
      }
    },
    [selectSkillFile, showToast],
  );

  const savePersonalisationSoul = useCallback(
    async (currentState: AgentsPageState) => {
      try {
        await setProfileSoul({
          path: { agentID: currentState.editingId ?? "" },
          body: { soul: currentState.personalisation.soulDraft },
          throwOnError: true,
        });
        setState((prev) => ({
          ...prev,
          personalisation: { ...prev.personalisation, soul: prev.personalisation.soulDraft },
        }));
        showToast("Soul saved");
      } catch (e) {
        showToast((e as Error).message, "error");
      }
    },
    [showToast],
  );

  const savePersonalisationProfile = useCallback(
    async (currentState: AgentsPageState) => {
      try {
        await setProfileMemory({
          path: { agentID: currentState.editingId ?? "" },
          body: { content: currentState.personalisation.profileDraft },
          throwOnError: true,
        });
        setState((prev) => ({
          ...prev,
          personalisation: { ...prev.personalisation, profile: prev.personalisation.profileDraft },
        }));
        showToast("Profile saved");
      } catch (e) {
        showToast((e as Error).message, "error");
      }
    },
    [showToast],
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
          form: { ...prev.form, soul: (full as unknown as BuiltinItem).content ?? "" },
        }));
      } catch (e) {
        setState((prev) => ({ ...prev, selectedSoulID: "" }));
        showToast((e as Error).message, "error");
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
        const meta = ((full as unknown as BuiltinItem).metadata ?? {}) as Record<string, string>;
        let soulContent = "";
        if (meta.soul_id) {
          try {
            const { data: soul } = await getBuiltinResource({
              path: { kind: "soul", id: meta.soul_id },
              throwOnError: true,
            });
            soulContent = (soul as unknown as BuiltinItem).content ?? "";
          } catch {}
        }
        setState((prev) => ({
          ...prev,
          form: {
            ...prev.form,
            name: prev.form.name || tmpl.name || "",
            model: meta.model || prev.form.model || "",
            system_prompt: (full as unknown as BuiltinItem).content ?? "",
            soul: soulContent,
            template_id: tmpl.id,
          },
          showTemplateModal: false,
          showForm: true,
          activeTab: "config",
        }));
      } catch (e) {
        showToast((e as Error).message, "error");
      }
    },
    [showToast],
  );

  const listHeader = (
    <SettingsListHeader
      title="Agents"
      action={
        <Button
          onClick={startCreate}
          variant="ghost"
          size="xs"
          className="text-primary font-medium"
        >
          + New Agent
        </Button>
      }
    />
  );

  const list = (
    <AgentList
      state={state}
      onEdit={editAgent}
      onConfirmDelete={confirmDelete}
      onDeleteAgent={doDeleteAgent}
    />
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
      onToggleSkillStatus={(sk) => toggleSkillStatus(sk, state)}
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
      onSavePersonalisationSoul={() => savePersonalisationSoul(state)}
      onSavePersonalisationProfile={() => savePersonalisationProfile(state)}
      onOpenSkillInstallModal={(scope) =>
        setState((prev) => ({
          ...prev,
          skillInstallModalOpen: true,
          skillInstallScope: scope ?? (prev.isAdmin && prev.editingId ? "agent" : "user"),
        }))
      }
    />
  ) : undefined;

  const emptyState = (
    <p className="text-sm text-muted-foreground">Select an agent to edit or create a new one.</p>
  );

  return (
    // Escape the p-8 px-10 padding from SettingsLayout's outlet wrapper
    <div className="h-full">
      <SettingsDetailLayout
        listHeader={listHeader}
        list={list}
        detail={detail}
        emptyState={emptyState}
      />
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
      <ToastAlert toast={toast} />
    </div>
  );
}

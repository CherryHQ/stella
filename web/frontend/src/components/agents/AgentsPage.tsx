import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import type {
  AgentDetail,
  AgentSandbox,
  BuiltinItem,
  Channel,
  Personalisation,
  Skill,
  User,
} from "@/lib/types";
import { AgentList } from "./AgentList";
import { AgentForm } from "./AgentForm";
import { TemplateModal } from "./TemplateModal";
import { SkillInstallModal } from "./SkillInstallModal";
import { ConfirmDialog } from "./ConfirmDialog";

type Toast = { message: string; type: "success" | "error" } | null;

function ToastAlert({ toast }: { toast: Toast }) {
  if (!toast) return null;
  return (
    <div
      className={`alert ${toast.type === "error" ? "alert-error" : "alert-success"} fixed bottom-4 right-4 z-50 w-auto max-w-sm shadow-lg`}
    >
      <span>{toast.message}</span>
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
      ? (rawAllowlist as string).split(/\r?\n|,/).map((v) => v.trim()).filter(Boolean)
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
      try { return JSON.parse(ch.config || "{}") as Record<string, unknown>; } catch { return {}; }
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
    scope: "system",
    enabled: true,
    creator_id: 0,
    sandbox: { network: { mode: "disabled", allowlist: [] } },
    template_id: "",
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
  const [state, setState] = useState<AgentsPageState>({
    agents: [],
    channels: [],
    cachedModels: [],
    isAdmin: false,
    currentUserId: 0,
    allUsers: [],
    showForm: false,
    editingId: null,
    activeTab: "config",
    showTemplateModal: false,
    form: emptyForm(),
    selectedSoulID: "",
    builtinTemplates: [],
    builtinSouls: [],
    builtinSkills: [],
    assignedUsers: [],
    addUserId: "",
    selectedChannelIDs: [],
    confirmMsg: "",
    confirmAction: () => {},
    agentSkills: [],
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
    personalisation: { soul: "", soulDraft: "", profile: "", profileDraft: "", loaded: false },
  });
  const [toast, setToast] = useState<Toast>(null);

  const showToast = useCallback((message: string, type: "success" | "error" = "success") => {
    setToast({ message, type });
    setTimeout(() => setToast(null), 3000);
  }, []);

  const set = useCallback((patch: Partial<AgentsPageState>) => {
    setState((prev) => ({ ...prev, ...patch }));
  }, []);

  const requestedAgentID = useCallback(() => {
    return new URLSearchParams(window.location.search).get("agent") || "";
  }, []);

  const loadAgents = useCallback(async (currentState?: AgentsPageState) => {
    try {
      const agents = ((await api<AgentDetail[]>("GET", "/api/agents")) ?? []).map((a) => ({
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
  }, [requestedAgentID]);

  const loadChannels = useCallback(async () => {
    try {
      const channels = ((await api<Channel[]>("GET", "/api/channels")) ?? []).map(normalizeChannel);
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
      const agentSkills = ((await api<Skill[]>("GET", `/api/agents/${agentId}/skills`)) ?? []);
      setState((prev) => ({ ...prev, agentSkills, agentSkillsLoading: false }));
      return agentSkills;
    } catch {
      setState((prev) => ({ ...prev, agentSkills: [], agentSkillsLoading: false }));
      return [];
    }
  }, []);

  const loadUserSkills = useCallback(async () => {
    try {
      const userSkills = ((await api<Skill[]>("GET", "/api/auth/profile/skills")) ?? []);
      setState((prev) => ({ ...prev, userSkills }));
      return userSkills;
    } catch {
      setState((prev) => ({ ...prev, userSkills: [] }));
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
      const mems = ((await api<Array<{ agent_id: string; soul?: string; content?: string }>>("GET", "/api/auth/profile/memories")) ?? []);
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
      const assignedUsers = ((await api<User[]>("GET", `/api/agents/${agentId}/users`)) ?? []);
      setState((prev) => ({ ...prev, assignedUsers }));
    } catch {
      setState((prev) => ({ ...prev, assignedUsers: [] }));
    }
  }, []);

  const resetForm = useCallback(() => {
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
  }, []);

  const dedicatedChannelsForAgent = useCallback((agentId: string, channels: Channel[]) => {
    return channels.filter((ch) => ch.id !== ch.type && ch.agent_id === agentId);
  }, []);

  const editAgent = useCallback(async (a: AgentDetail) => {
    setState((prev) => ({
      ...prev,
      form: { ...a, scope: a.scope || "system", template_id: "", sandbox: normalizeSandbox(a.sandbox) },
      selectedSoulID: "",
      editingId: a.id,
      activeTab: "config",
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
  }, [loadChannels, loadAgentSkills, loadPersonalisation, dedicatedChannelsForAgent]);

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
    const init = async () => {
      const [,, me, catalog] = await Promise.all([
        api<AgentDetail[]>("GET", "/api/agents").then((agents) => {
          const normalized = ((agents) ?? []).map((a) => ({
            ...a,
            sandbox: normalizeSandbox(a.sandbox),
          }));
          const reqId = new URLSearchParams(window.location.search).get("agent") || "";
          setState((prev) => ({
            ...prev,
            agents: normalized.map((a) => ({ ...a, _highlight: a.id === reqId })),
          }));
          return normalized;
        }).catch(() => []),
        api<Array<{ provider: string; model: string }>>("GET", "/api/models").then((models) => {
          const cachedModels = (models ?? []).map((m) => `${m.provider}/${m.model}`);
          setState((prev) => ({ ...prev, cachedModels }));
        }).catch(() => {}),
        api<{ is_admin?: boolean; user_id?: number }>("GET", "/api/auth/me").catch(() => null),
        Promise.all([
          api<BuiltinItem[]>("GET", "/api/builtin/template").catch(() => []),
          api<BuiltinItem[]>("GET", "/api/builtin/soul").catch(() => []),
          api<BuiltinItem[]>("GET", "/api/builtin/skill").catch(() => []),
        ]),
        api<Skill[]>("GET", "/api/auth/profile/skills").then((s) => {
          setState((prev) => ({ ...prev, userSkills: s ?? [] }));
        }).catch(() => {}),
      ]);

      const isAdmin = me?.is_admin ?? false;
      const currentUserId = me?.user_id ?? 0;
      const [templates, souls, skills] = catalog as [BuiltinItem[], BuiltinItem[], BuiltinItem[]];
      const builtinTemplates = templates ?? [];
      const builtinSouls = souls ?? [];
      const builtinSkills = skills ?? [];

      setState((prev) => ({
        ...prev,
        isAdmin,
        currentUserId,
        builtinTemplates,
        builtinSouls,
        builtinSkills,
      }));

      if (isAdmin) {
        await loadChannels();
        try {
          const users = await api<User[]>("GET", "/api/auth/users");
          setState((prev) => ({ ...prev, allUsers: users ?? [] }));
        } catch {}
      }

      // focus agent from URL after load
      setState((prev) => {
        const reqId = new URLSearchParams(window.location.search).get("agent") || "";
        if (!reqId) return prev;
        const agent = prev.agents.find((a) => a.id === reqId);
        if (agent) {
          // side effect: fire editAgent
          void editAgent(agent);
        }
        return prev;
      });
    };

    void init();
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const saveAgent = useCallback(async (currentState: AgentsPageState) => {
    try {
      const payload = {
        ...currentState.form,
        sandbox: normalizeSandbox(currentState.form.sandbox),
      };
      if (payload.sandbox.network.mode !== "whitelist") {
        payload.sandbox.network.allowlist = [];
      }

      if (currentState.editingId) {
        await api("PUT", `/api/agents/${currentState.editingId}`, payload);
        await saveChannelBindings(currentState.editingId, currentState);
      } else {
        const created = await api<AgentDetail>("POST", "/api/agents", payload);
        const newId = created.id;
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
  }, [loadAgents, loadChannels, loadAgentSkills, loadPersonalisation, dedicatedChannelsForAgent, showToast]);

  const saveChannelBindings = useCallback(async (agentID: string, currentState: AgentsPageState) => {
    if (!currentState.isAdmin) return;
    const selected = new Set(currentState.selectedChannelIDs);
    const available = currentState.channels.filter(
      (ch) => ch.id !== ch.type && ch.enabled && (!ch.agent_id || ch.agent_id === agentID),
    );
    for (const ch of available) {
      const wantsAgent = selected.has(ch.id);
      const nextAgentID = wantsAgent ? agentID : "";
      if ((ch.agent_id || "") === nextAgentID) continue;
      await api("PUT", `/api/channels/${encodeURIComponent(ch.id)}`, {
        type: ch.type,
        agent_id: nextAgentID,
        config: JSON.stringify(ch._config || {}),
      });
    }
    await loadChannels();
  }, [loadChannels]);

  const doDeleteAgent = useCallback(async (id: string) => {
    try {
      await api("DELETE", `/api/agents/${id}`);
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
  }, [loadAgents, showToast]);

  const confirmDelete = useCallback((msg: string, action: () => void) => {
    setState((prev) => ({ ...prev, confirmMsg: msg, confirmAction: action }));
  }, []);

  const addUser = useCallback(async (currentState: AgentsPageState) => {
    if (!currentState.addUserId || !currentState.editingId) return;
    try {
      await api("POST", `/api/agents/${currentState.editingId}/users`, {
        user_id: Number(currentState.addUserId),
      });
      setState((prev) => ({ ...prev, addUserId: "" }));
      await loadAssignedUsers(currentState.editingId);
      showToast("User assigned");
    } catch (e) {
      showToast((e as Error).message, "error");
    }
  }, [loadAssignedUsers, showToast]);

  const removeUser = useCallback(async (userId: number, editingId: string) => {
    try {
      await api("DELETE", `/api/agents/${editingId}/users/${userId}`);
      await loadAssignedUsers(editingId);
      showToast("User removed");
    } catch (e) {
      showToast((e as Error).message, "error");
    }
  }, [loadAssignedUsers, showToast]);

  const skillKey = (sk: { scope: string; id: string }) => `${sk.scope}:${sk.id}`;

  const skillItemURL = useCallback((scope: string, id: string, editingId: string | null) => {
    if (scope === "user") return `/api/auth/profile/skills/${id}`;
    if (scope === "agent") return `/api/agents/${encodeURIComponent(editingId ?? "")}/skills/${id}`;
    return `/api/builtin/skill/${id}`;
  }, []);

  const skillFileURL = useCallback((scope: string, id: string, path: string, editingId: string | null) => {
    if (scope === "user") return `/api/auth/profile/skills/${id}/file?path=${encodeURIComponent(path)}`;
    if (scope === "agent") return `/api/agents/${encodeURIComponent(editingId ?? "")}/skills/${id}/file?path=${encodeURIComponent(path)}`;
    return "";
  }, []);

  const selectSkillFile = useCallback(async (
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
    if (skill.scope === "system") {
      setState((prev) => ({
        ...prev,
        selectedSkillFileContent: fileCache[path] || "",
        selectedSkillDirty: false,
      }));
      return;
    }
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
      const res = await api<{ content?: string }>("GET", skillFileURL(skill.scope, skill.id, path, editingId));
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
  }, [skillFileURL, showToast]);

  const selectSkill = useCallback(async (sk: Skill, currentState: AgentsPageState) => {
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
      if (sk.scope === "system") {
        const full = await api<BuiltinItem>("GET", skillItemURL("system", sk.id, null));
        const skill: Skill = {
          ...sk,
          name: full.name ?? sk.name,
          description: full.description ?? sk.description ?? "",
          files: ["SKILL.md"],
          disable_model_invocation: false,
        };
        setState((prev) => ({
          ...prev,
          selectedSkill: skill,
          selectedSkillActiveFile: "SKILL.md",
          selectedSkillFileContent: full.content ?? "",
          selectedSkillFileCache: { "SKILL.md": full.content ?? "" },
          selectedSkillLoading: false,
        }));
      } else {
        const full = await api<Skill & { content?: string }>("GET", skillItemURL(sk.scope, sk.id, currentState.editingId));
        const skill: Skill = { ...full, scope: sk.scope };
        const files = full.files ?? ["SKILL.md"];
        const initialFile = files.includes("SKILL.md") ? "SKILL.md" : files[0];
        setState((prev) => ({
          ...prev,
          selectedSkill: skill,
          selectedSkillLoading: false,
        }));
        await selectSkillFile(initialFile, skill, {}, currentState.editingId, true, false);
      }
    } catch (e) {
      showToast((e as Error).message, "error");
      setState((prev) => ({ ...prev, selectedSkillLoading: false }));
    }
  }, [skillItemURL, selectSkillFile, showToast]);

  const toggleSkillStatus = useCallback(async (sk: Skill, currentState: AgentsPageState) => {
    if (sk.scope === "system") return;
    const next = sk.status === "active" ? "draft" : "active";
    try {
      await api("PUT", skillItemURL(sk.scope, sk.id, currentState.editingId), { status: next });
      setState((prev) => ({
        ...prev,
        agentSkills: prev.agentSkills.map((s) => (s.id === sk.id && s.scope === sk.scope ? { ...s, status: next } : s)),
        userSkills: prev.userSkills.map((s) => (s.id === sk.id && s.scope === sk.scope ? { ...s, status: next } : s)),
        selectedSkill:
          prev.selectedSkill && skillKey(prev.selectedSkill) === skillKey(sk)
            ? { ...prev.selectedSkill, status: next }
            : prev.selectedSkill,
      }));
    } catch (e) {
      showToast((e as Error).message, "error");
    }
  }, [skillItemURL, showToast]);

  const saveSelectedSkill = useCallback(async (currentState: AgentsPageState) => {
    const { selectedSkill, selectedSkillFileContent, selectedSkillActiveFile } = currentState;
    if (!selectedSkill || selectedSkill.scope === "system") return;
    setState((prev) => ({ ...prev, selectedSkillSaving: true }));
    try {
      await api("PUT", skillItemURL(selectedSkill.scope, selectedSkill.id, currentState.editingId), {
        description: selectedSkill.description,
        status: selectedSkill.status,
        disable_model_invocation: !!selectedSkill.disable_model_invocation,
        files: { [selectedSkillActiveFile]: selectedSkillFileContent },
      });
      setState((prev) => ({
        ...prev,
        selectedSkillDirty: false,
        selectedSkillFileCache: { ...prev.selectedSkillFileCache, [selectedSkillActiveFile]: selectedSkillFileContent },
      }));
      const full = await api<Skill>("GET", skillItemURL(selectedSkill.scope, selectedSkill.id, currentState.editingId));
      setState((prev) => ({ ...prev, selectedSkill: { ...full, scope: selectedSkill.scope }, selectedSkillSaving: false }));
      if (selectedSkill.scope === "user") await loadUserSkills();
      if (selectedSkill.scope === "agent") await loadAgentSkills(currentState.editingId);
      showToast("Saved");
    } catch (e) {
      showToast((e as Error).message, "error");
      setState((prev) => ({ ...prev, selectedSkillSaving: false }));
    }
  }, [skillItemURL, loadUserSkills, loadAgentSkills, showToast]);

  const deleteSkill = useCallback(async (sk: Skill, currentState: AgentsPageState) => {
    if (sk.scope === "system") return;
    if (!confirm(`Delete skill "${sk.name}"? This cannot be undone.`)) return;
    try {
      await api("DELETE", skillItemURL(sk.scope, sk.id, currentState.editingId));
      setState((prev) => {
        const wasSelected = prev.selectedSkillKey === skillKey(sk);
        return {
          ...prev,
          selectedSkillKey: wasSelected ? "" : prev.selectedSkillKey,
          selectedSkill: wasSelected ? null : prev.selectedSkill,
          selectedSkillDirty: wasSelected ? false : prev.selectedSkillDirty,
        };
      });
      if (sk.scope === "user") await loadUserSkills();
      if (sk.scope === "agent") await loadAgentSkills(currentState.editingId);
      showToast("Skill removed");
    } catch (e) {
      showToast((e as Error).message, "error");
    }
  }, [skillItemURL, loadUserSkills, loadAgentSkills, showToast]);

  const duplicateBuiltinToAgent = useCallback(async (currentState: AgentsPageState) => {
    const { selectedSkill, editingId } = currentState;
    if (!selectedSkill || selectedSkill.scope !== "system" || !currentState.isAdmin || !editingId) return;
    try {
      const res = await api<{ name?: string; id?: string }>(
        "POST",
        `/api/agents/${editingId}/skills/from-builtin/${selectedSkill.id}`,
      );
      showToast("Installed: " + (res?.name ?? selectedSkill.name));
      const updated = await loadAgentSkills(editingId);
      const created = updated.find((sk) => sk.name === (res?.name ?? selectedSkill.name));
      if (created) await selectSkill({ ...created, scope: "agent" }, { ...currentState, agentSkills: updated });
    } catch (e) {
      showToast((e as Error).message, "error");
    }
  }, [loadAgentSkills, selectSkill, showToast]);

  const doSkillInstall = useCallback(async (source: string, scope: "user" | "agent", currentState: AgentsPageState) => {
    if (!source) { showToast("Choose a skill first", "error"); return; }
    if (scope === "agent" && !currentState.editingId) return;
    setState((prev) => ({ ...prev, skillInstalling: true } as AgentsPageState & { skillInstalling: boolean }));
    try {
      const url = scope === "agent"
        ? `/api/agents/${currentState.editingId}/skills/install`
        : "/api/auth/profile/skills/install";
      const res = await api<{ name?: string; id?: string }>("POST", url, { source });
      showToast("Installed: " + (res?.name ?? "skill"));
      setState((prev) => ({ ...prev, skillInstallModalOpen: false }));
      if (scope === "agent") {
        const updated = await loadAgentSkills(currentState.editingId);
        const created = updated.find((sk) => sk.name === (res?.name ?? ""));
        if (created) await selectSkill({ ...created, scope: "agent" }, { ...currentState, agentSkills: updated });
      } else {
        await loadUserSkills();
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
  }, [loadAgentSkills, loadUserSkills, selectSkill, showToast]);

  const doSkillUpload = useCallback(async (file: File, scope: "user" | "agent", currentState: AgentsPageState) => {
    if (!file) { showToast("Choose a .zip file first", "error"); return; }
    if (scope === "agent" && !currentState.editingId) return;
    try {
      const url = scope === "agent"
        ? `/api/agents/${currentState.editingId}/skills/upload`
        : "/api/auth/profile/skills/upload";
      const form = new FormData();
      form.append("file", file);
      const res = await api<{ name?: string; id?: string }>("POST", url, form);
      showToast("Uploaded: " + (res?.name ?? "skill"));
      setState((prev) => ({ ...prev, skillInstallModalOpen: false }));
      if (scope === "agent") {
        const updated = await loadAgentSkills(currentState.editingId);
        const created = updated.find((sk) => sk.id === res?.id);
        if (created) await selectSkill({ ...created, scope: "agent" }, { ...currentState, agentSkills: updated });
      } else {
        await loadUserSkills();
      }
    } catch (e) {
      showToast((e as Error).message, "error");
    }
  }, [loadAgentSkills, loadUserSkills, selectSkill, showToast]);

  const deleteSelectedSkillFile = useCallback(async (currentState: AgentsPageState) => {
    const { selectedSkill, selectedSkillActiveFile, editingId } = currentState;
    if (!selectedSkill || selectedSkill.scope === "system") return;
    if (!confirm(`Delete file "${selectedSkillActiveFile}"?`)) return;
    const url = skillFileURL(selectedSkill.scope, selectedSkill.id, selectedSkillActiveFile, editingId);
    if (!url) return;
    try {
      await api("DELETE", url);
      const newFiles = (selectedSkill.files ?? ["SKILL.md"]).filter((f) => f !== selectedSkillActiveFile);
      setState((prev) => ({
        ...prev,
        selectedSkill: { ...selectedSkill, files: newFiles },
        selectedSkillActiveFile: "SKILL.md",
        selectedSkillFileCache: Object.fromEntries(
          Object.entries(prev.selectedSkillFileCache).filter(([k]) => k !== selectedSkillActiveFile),
        ),
      }));
      await selectSkillFile("SKILL.md", { ...selectedSkill, files: newFiles }, currentState.selectedSkillFileCache, editingId, true, false);
    } catch (e) {
      showToast((e as Error).message, "error");
    }
  }, [skillFileURL, selectSkillFile, showToast]);

  const savePersonalisationSoul = useCallback(async (currentState: AgentsPageState) => {
    try {
      await api("PUT", `/api/auth/profile/soul/${currentState.editingId}`, {
        soul: currentState.personalisation.soulDraft,
      });
      setState((prev) => ({
        ...prev,
        personalisation: { ...prev.personalisation, soul: prev.personalisation.soulDraft },
      }));
      showToast("Soul saved");
    } catch (e) {
      showToast((e as Error).message, "error");
    }
  }, [showToast]);

  const savePersonalisationProfile = useCallback(async (currentState: AgentsPageState) => {
    try {
      await api("PUT", `/api/auth/profile/memories/${currentState.editingId}`, {
        content: currentState.personalisation.profileDraft,
      });
      setState((prev) => ({
        ...prev,
        personalisation: { ...prev.personalisation, profile: prev.personalisation.profileDraft },
      }));
      showToast("Profile saved");
    } catch (e) {
      showToast((e as Error).message, "error");
    }
  }, [showToast]);

  const applySoul = useCallback(async (soulID: string) => {
    if (!soulID) return;
    try {
      const full = await api<BuiltinItem>("GET", `/api/builtin/soul/${soulID}`);
      setState((prev) => ({ ...prev, form: { ...prev.form, soul: full.content ?? "" } }));
    } catch (e) {
      setState((prev) => ({ ...prev, selectedSoulID: "" }));
      showToast((e as Error).message, "error");
    }
  }, [showToast]);

  const pickTemplate = useCallback(async (tmpl: BuiltinItem) => {
    try {
      const full = await api<BuiltinItem>("GET", `/api/builtin/template/${tmpl.id}`);
      const meta = (full.metadata ?? {}) as Record<string, string>;
      let soulContent = "";
      if (meta.soul_id) {
        try {
          const soul = await api<BuiltinItem>("GET", `/api/builtin/soul/${meta.soul_id}`);
          soulContent = soul.content ?? "";
        } catch {}
      }
      setState((prev) => ({
        ...prev,
        form: {
          ...prev.form,
          name: prev.form.name || tmpl.name || "",
          model: meta.model || prev.form.model || "",
          system_prompt: full.content ?? "",
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
  }, [showToast]);

  return (
    <div className="grid grid-cols-1 lg:grid-cols-[280px_minmax(0,1fr)] -mx-6 -mt-6 border-t border-base-300">
      <AgentList
        state={state}
        onEdit={editAgent}
        onStartCreate={startCreate}
        onConfirmDelete={confirmDelete}
        onDeleteAgent={doDeleteAgent}
      />
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
        onDuplicateBuiltinToAgent={() => duplicateBuiltinToAgent(state)}
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
      {state.showTemplateModal && (
        <TemplateModal
          templates={state.builtinTemplates}
          onPick={pickTemplate}
          onPickBlank={() => setState((prev) => ({ ...prev, showTemplateModal: false, showForm: true, activeTab: "config" }))}
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

import type { CreateAgentData } from "@/lib/api-client/types.gen";
import type { AgentDetail, BuiltinItem, Personalisation, Skill, User } from "@/lib/types";
import {
  normalizeSandbox,
  type AgentsSettingsLoaderData,
  type ModelOption,
} from "@/lib/queries/agent-settings";

export type { ModelOption };

export function emptyForm(): Omit<AgentDetail, "id"> {
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

export function agentRequestBody(form: Omit<AgentDetail, "id">): CreateAgentData["body"] {
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

/**
 * The agent editor's whole working set. It stays one flat object because every
 * tab reads across it (skills need the editing id, tools need the model list),
 * and a single `set` patch keeps the async handlers free of cross-field races.
 */
export interface AgentsPageState {
  agents: AgentDetail[];
  cachedModels: ModelOption[];
  isAdmin: boolean;
  currentUserId: string;
  allUsers: User[];
  showForm: boolean;
  editingId: string | null;
  showTemplateModal: boolean;
  form: Omit<AgentDetail, "id">;
  selectedSoulID: string;
  builtinTemplates: BuiltinItem[];
  builtinSouls: BuiltinItem[];
  builtinSkills: BuiltinItem[];
  assignedUsers: User[];
  addUserId: string;
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
  /**
   * True only after the active file's content was successfully fetched.
   * Saving must never write a file whose content was not loaded — an unloaded
   * or failed fetch would otherwise overwrite it with empty or stale text.
   */
  selectedSkillFileLoaded: boolean;
  selectedSkillFileLoading: boolean;
  selectedSkillFileCache: Record<string, { content: string; encoding?: string }>;
  selectedSkillAddingFile: boolean;
  selectedSkillNewFileName: string;
  skillInstallModalOpen: boolean;
  skillInstallScope: "user_agent" | "system_agent";
  personalisation: Personalisation;
}

/**
 * Seed the editor from a bootstrap payload. `agentId` empty means "create":
 * the form starts blank and the template picker takes over.
 */
export function initialAgentDetailState(
  data: AgentsSettingsLoaderData,
  agentId: string,
): AgentsPageState {
  const selectedAgent = agentId ? data.agents.find((a) => a.id === agentId) : undefined;
  return {
    agents: data.agents,
    cachedModels: data.cachedModels,
    isAdmin: data.isAdmin,
    currentUserId: data.currentUserId,
    allUsers: data.allUsers,
    showForm: !!selectedAgent,
    editingId: selectedAgent?.id ?? null,
    showTemplateModal: false,
    form: selectedAgent
      ? {
          ...selectedAgent,
          scope: selectedAgent.scope || "system",
          template_id: "",
          sandbox: normalizeSandbox(selectedAgent.sandbox),
        }
      : emptyForm(),
    selectedSoulID: "",
    builtinTemplates: data.builtinTemplates,
    builtinSouls: data.builtinSouls,
    builtinSkills: [],
    assignedUsers: [],
    addUserId: "",
    confirmMsg: "",
    confirmAction: () => {},
    agentSkills: data.agentSkills,
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
    selectedSkillFileLoaded: false,
    selectedSkillFileLoading: false,
    selectedSkillFileCache: {},
    selectedSkillAddingFile: false,
    selectedSkillNewFileName: "",
    skillInstallModalOpen: false,
    skillInstallScope: "user_agent",
    personalisation: data.personalisation,
  };
}

/**
 * Whether the caller may configure this agent.
 *
 * The server decides and says so in can_manage. A client that rebuilt the rule
 * from creator_id would need every agent's owner id, and that is exactly the
 * user directory an ordinary user must not be handed.
 */
export function canEditAgent(agent: Pick<AgentDetail, "can_manage"> | undefined): boolean {
  return agent?.can_manage === true;
}

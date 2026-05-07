import type { Skill, User } from "@/lib/types";
import type { AgentsPageState } from "./AgentsPage";
import { ConfigTab } from "./tabs/ConfigTab";
import { PromptTab } from "./tabs/PromptTab";
import { SkillsTab } from "./tabs/SkillsTab";
import { AdvancedTab } from "./tabs/AdvancedTab";
import { UsersTab } from "./tabs/UsersTab";
import { PersonalTab } from "./tabs/PersonalTab";

interface Props {
  state: AgentsPageState;
  onSetState: (patch: Partial<AgentsPageState>) => void;
  onSave: () => void;
  onCancel: () => void;
  onLoadAssignedUsers: (agentId: string) => void;
  onAddUser: () => void;
  onRemoveUser: (userId: number) => void;
  onApplySoul: (soulID: string) => void;
  onSelectSkill: (sk: Skill) => void;
  onToggleSkillStatus: (sk: Skill) => void;
  onSaveSelectedSkill: () => void;
  onDeleteSkill: (sk: Skill) => void;
  onDuplicateBuiltinToAgent: () => void;
  onSelectSkillFile: (path: string, skipDirtyCheck?: boolean) => void;
  onDeleteSkillFile: () => void;
  onSavePersonalisationSoul: () => void;
  onSavePersonalisationProfile: () => void;
  onOpenSkillInstallModal: (scope?: "user" | "agent") => void;
}

const TABS = [
  { id: "config", label: "Config" },
  { id: "prompt", label: "Prompt" },
  { id: "skills", label: "Skills" },
  { id: "advanced", label: "Advanced" },
];

export function AgentForm({
  state,
  onSetState,
  onSave,
  onCancel,
  onLoadAssignedUsers,
  onAddUser,
  onRemoveUser,
  onApplySoul,
  onSelectSkill,
  onToggleSkillStatus,
  onSaveSelectedSkill,
  onDeleteSkill,
  onDuplicateBuiltinToAgent,
  onSelectSkillFile,
  onDeleteSkillFile,
  onSavePersonalisationSoul,
  onSavePersonalisationProfile,
  onOpenSkillInstallModal,
}: Props) {
  const { showForm, editingId, activeTab, isAdmin, form } = state;

  if (!showForm) {
    return (
      <main className="px-6 py-6 min-w-0 hidden lg:block">
        <div className="flex flex-col items-center justify-center min-h-64 text-center gap-4 min-w-0">
          <p className="text-secondary text-sm">Select an agent to edit, or create a new one.</p>
        </div>
      </main>
    );
  }

  const setTab = (tab: string) => onSetState({ activeTab: tab });

  const availableUsers = state.allUsers.filter(
    (u: User) => !state.assignedUsers.some((a: User) => a.id === u.id),
  );

  return (
    <main className="px-6 py-6 min-w-0 block">
      <div className="border border-base-300 rounded-box overflow-hidden">
        <div className="border-b border-base-300 px-4 py-3 bg-base-200/30">
          <span className="font-medium text-sm">
            {editingId ? `Edit: ${form.name}` : "New agent"}
          </span>
        </div>
        <div className="border-b border-base-300 px-2 flex overflow-x-auto bg-base-200/20">
          {TABS.map((tab) => (
            <button
              key={tab.id}
              onClick={() => setTab(tab.id)}
              className={`px-3 py-2.5 text-xs font-mono border-b-2 transition-colors whitespace-nowrap ${
                activeTab === tab.id
                  ? "border-primary text-primary font-semibold"
                  : "border-transparent text-secondary hover:text-base-content"
              }`}
            >
              {tab.label}
            </button>
          ))}
          {isAdmin && (
            <button
              onClick={() => {
                setTab("users");
                if (editingId) onLoadAssignedUsers(editingId);
              }}
              className={`px-3 py-2.5 text-xs font-mono border-b-2 transition-colors whitespace-nowrap ${
                activeTab === "users"
                  ? "border-primary text-primary font-semibold"
                  : "border-transparent text-secondary hover:text-base-content"
              }`}
            >
              Users
            </button>
          )}
          {editingId && (
            <button
              onClick={() => setTab("personal")}
              className={`px-3 py-2.5 text-xs font-mono border-b-2 transition-colors whitespace-nowrap ${
                activeTab === "personal"
                  ? "border-primary text-primary font-semibold"
                  : "border-transparent text-secondary hover:text-base-content"
              }`}
            >
              Personal
            </button>
          )}
        </div>
        <div className="p-4 space-y-4">
          {activeTab === "config" && (
            <ConfigTab state={state} onSetState={onSetState} />
          )}
          {activeTab === "prompt" && (
            <PromptTab state={state} onSetState={onSetState} onApplySoul={onApplySoul} />
          )}
          {activeTab === "skills" && (
            <SkillsTab
              state={state}
              onSetState={onSetState}
              onSelectSkill={onSelectSkill}
              onToggleSkillStatus={onToggleSkillStatus}
              onSaveSelectedSkill={onSaveSelectedSkill}
              onDeleteSkill={onDeleteSkill}
              onDuplicateBuiltinToAgent={onDuplicateBuiltinToAgent}
              onSelectSkillFile={onSelectSkillFile}
              onDeleteSkillFile={onDeleteSkillFile}
              onOpenSkillInstallModal={onOpenSkillInstallModal}
            />
          )}
          {activeTab === "advanced" && (
            <AdvancedTab state={state} onSetState={onSetState} />
          )}
          {activeTab === "users" && (
            <UsersTab
              state={state}
              availableUsers={availableUsers}
              onSetState={onSetState}
              onAddUser={onAddUser}
              onRemoveUser={onRemoveUser}
            />
          )}
          {activeTab === "personal" && (
            <PersonalTab
              state={state}
              onSetState={onSetState}
              onSaveSoul={onSavePersonalisationSoul}
              onSaveProfile={onSavePersonalisationProfile}
            />
          )}
        </div>
        <div className="border-t border-base-300 px-4 py-3 flex items-center justify-end gap-2 bg-base-200/20">
          <button onClick={onCancel} className="btn btn-ghost btn-sm">Cancel</button>
          <button onClick={onSave} className="btn btn-primary btn-sm">
            {editingId ? "Update" : "Create"}
          </button>
        </div>
      </div>
    </main>
  );
}

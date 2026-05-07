import type { Skill } from "@/lib/types";
import type { AgentsPageState } from "../AgentsPage";

interface Props {
  state: AgentsPageState;
  onSetState: (patch: Partial<AgentsPageState>) => void;
  onSelectSkill: (sk: Skill) => void;
  onToggleSkillStatus: (sk: Skill) => void;
  onSaveSelectedSkill: () => void;
  onDeleteSkill: (sk: Skill) => void;
  onDuplicateBuiltinToAgent: () => void;
  onSelectSkillFile: (path: string, skipDirtyCheck?: boolean) => void;
  onDeleteSkillFile: () => void;
  onOpenSkillInstallModal: (scope?: "user" | "agent") => void;
}

function skillKey(sk: { scope: string; id: string }) {
  return `${sk.scope}:${sk.id}`;
}

function skillScopeLabel(scope: string) {
  return { system: "Built-in", user: "User", agent: "This agent" }[scope] ?? scope;
}

function skillScopeClass(scope: string) {
  return (
    {
      system: "badge-ghost",
      user: "badge-success badge-soft",
      agent: "badge-primary badge-soft",
    }[scope] ?? "badge-ghost"
  );
}

function skillStatusClass(status: string) {
  return (
    {
      active: "badge-success badge-soft",
      draft: "badge-warning badge-soft",
      deprecated: "badge-error badge-soft",
    }[status] ?? "badge-ghost"
  );
}

export function SkillsTab({
  state,
  onSetState,
  onSelectSkill,
  onToggleSkillStatus,
  onSaveSelectedSkill,
  onDeleteSkill,
  onDuplicateBuiltinToAgent,
  onSelectSkillFile,
  onDeleteSkillFile,
  onOpenSkillInstallModal,
}: Props) {
  const {
    agentSkills,
    agentSkillsLoading,
    userSkills,
    builtinSkills,
    skillViewFilter,
    skillScopeFilter,
    skillListQuery,
    selectedSkillKey,
    selectedSkill,
    selectedSkillLoading,
    selectedSkillSaving,
    selectedSkillDirty,
    selectedSkillEditMode,
    selectedSkillShowAdvanced,
    selectedSkillActiveFile,
    selectedSkillFileContent,
    selectedSkillFileLoading,
    selectedSkillAddingFile,
    selectedSkillNewFileName,
    editingId,
    isAdmin,
  } = state;

  const canInstallAgentSkills = isAdmin && !!editingId;
  const canEdit = !!selectedSkill && selectedSkill.scope !== "system";
  const canDelete = !!selectedSkill && selectedSkill.scope !== "system";

  const allSkills = (): Skill[] => {
    const system = builtinSkills.map((sk) => ({
      id: sk.id,
      scope: "system" as const,
      name: sk.name,
      description: sk.description ?? "",
      status: "active" as const,
      disable_model_invocation: false,
    }));
    const user = userSkills.map((sk) => ({ ...sk, scope: "user" as const }));
    const agent = agentSkills.map((sk) => ({ ...sk, scope: "agent" as const }));
    const ordered: Record<string, number> = { system: 0, user: 1, agent: 2 };
    return [...system, ...user, ...agent].sort((a, b) => {
      const diff = (ordered[a.scope] ?? 99) - (ordered[b.scope] ?? 99);
      if (diff !== 0) return diff;
      return (a.name ?? "").localeCompare(b.name ?? "");
    });
  };

  const filteredSkills = (): Skill[] => {
    const q = skillListQuery.trim().toLowerCase();
    return allSkills().filter((sk) => {
      if (skillViewFilter === "enabled" && sk.status !== "active") return false;
      if (skillViewFilter === "modified" && sk.scope === "system") return false;
      if (skillScopeFilter !== "all" && sk.scope !== skillScopeFilter) return false;
      if (!q) return true;
      return [sk.name, sk.description, sk.scope, sk.status].some((v) =>
        (v ?? "").toLowerCase().includes(q),
      );
    });
  };

  const skillFiles = selectedSkill?.files ?? ["SKILL.md"];

  const commitAddSkillFile = () => {
    if (!selectedSkill) return;
    const name = (selectedSkillNewFileName ?? "").trim();
    if (!name) return;
    if (name === "SKILL.md" || skillFiles.includes(name)) return;
    const newFiles = [...skillFiles, name];
    onSetState({
      selectedSkill: { ...selectedSkill, files: newFiles },
      selectedSkillFileContent: "",
      selectedSkillActiveFile: name,
      selectedSkillDirty: true,
      selectedSkillAddingFile: false,
      selectedSkillNewFileName: "",
    });
  };

  const viewFilters = [
    { id: "all", label: "All" },
    { id: "enabled", label: "Enabled" },
    { id: "modified", label: "Modified" },
  ];
  const scopeFilters = [
    { id: "all", label: "Any scope" },
    { id: "system", label: "Built-in" },
    { id: "user", label: "User" },
    { id: "agent", label: "This agent" },
  ];

  return (
    <div className="grid grid-cols-1 xl:grid-cols-[minmax(0,0.9fr)_minmax(0,1.1fr)] gap-4 min-w-0">
      {/* Skill list */}
      <div className="border border-base-300 rounded-box bg-base-100/70 min-w-0 overflow-hidden">
        <div className="p-4 border-b border-base-300 space-y-3">
          <div className="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
            <input
              value={skillListQuery}
              onChange={(e) => onSetState({ skillListQuery: e.target.value })}
              type="text"
              placeholder="Search skills..."
              className="input input-bordered input-sm w-full lg:max-w-sm"
            />
            <div className="flex items-center gap-2 flex-wrap">
              <button
                onClick={() => onOpenSkillInstallModal()}
                type="button"
                className="btn btn-primary btn-sm"
              >
                + Install skill
              </button>
            </div>
          </div>
          <div className="flex items-center gap-2 flex-wrap">
            {viewFilters.map((f) => (
              <button
                key={f.id}
                onClick={() => onSetState({ skillViewFilter: f.id })}
                type="button"
                className={`badge badge-sm cursor-pointer ${skillViewFilter === f.id ? "badge-primary" : "badge-ghost"}`}
              >
                {f.label}
              </button>
            ))}
          </div>
          <div className="flex items-center gap-2 flex-wrap">
            {scopeFilters.map((f) => (
              <button
                key={f.id}
                onClick={() => onSetState({ skillScopeFilter: f.id })}
                type="button"
                className={`badge badge-sm cursor-pointer ${skillScopeFilter === f.id ? "badge-accent" : "badge-ghost"}`}
              >
                {f.label}
              </button>
            ))}
          </div>
          {!editingId && (
            <div className="text-xs text-base-content/50">
              Save the agent first if you want to add or customize agent-specific skills.
            </div>
          )}
        </div>
        <div className="p-3 space-y-2 max-h-[70vh] overflow-y-auto min-w-0">
          {agentSkillsLoading && (
            <div className="py-4 flex justify-center">
              <span className="loading loading-spinner loading-sm"></span>
            </div>
          )}
          {!agentSkillsLoading &&
            filteredSkills().map((sk) => (
              <button
                key={skillKey(sk)}
                onClick={() => onSelectSkill(sk)}
                type="button"
                className={`w-full text-left rounded-box border border-base-300 px-3 py-3 transition-colors hover:bg-base-200/50 overflow-hidden ${
                  selectedSkillKey === skillKey(sk) ? "border-primary bg-primary/5" : ""
                }`}
              >
                <div className="flex items-center gap-3 min-w-0">
                  {sk.scope !== "system" ? (
                    <input
                      type="checkbox"
                      className="toggle toggle-primary toggle-xs shrink-0"
                      checked={sk.status === "active"}
                      onChange={(e) => {
                        e.stopPropagation();
                        onToggleSkillStatus(sk);
                      }}
                      onClick={(e) => e.stopPropagation()}
                      title={sk.status === "active" ? "Disable skill" : "Enable skill"}
                    />
                  ) : (
                    <span className="badge badge-ghost badge-xs shrink-0">read only</span>
                  )}
                  <div className="min-w-0 flex-1 overflow-hidden">
                    <div className="flex items-center gap-2 flex-wrap min-w-0">
                      <p className="text-sm font-mono truncate min-w-0">{sk.name}</p>
                      <span className={`badge badge-xs shrink-0 ${skillScopeClass(sk.scope)}`}>
                        {skillScopeLabel(sk.scope)}
                      </span>
                      <span className={`badge badge-xs shrink-0 ${skillStatusClass(sk.status)}`}>
                        {sk.status === "active" ? "Enabled" : sk.status}
                      </span>
                    </div>
                    {sk.description && (
                      <p className="text-xs text-base-content/60 truncate mt-1">{sk.description}</p>
                    )}
                  </div>
                </div>
              </button>
            ))}
          {!agentSkillsLoading && filteredSkills().length === 0 && (
            <div className="text-xs text-base-content/50 p-2">No skills match this filter.</div>
          )}
        </div>
      </div>

      {/* Skill detail */}
      <div className="border border-base-300 rounded-box bg-base-100/70 min-w-0 overflow-hidden">
        {!selectedSkill && !selectedSkillLoading && (
          <div className="p-8 text-center text-sm text-base-content/50">
            Select a skill to inspect or edit.
          </div>
        )}
        {selectedSkillLoading && (
          <div className="p-8 flex justify-center">
            <span className="loading loading-spinner loading-md"></span>
          </div>
        )}
        {selectedSkill && !selectedSkillLoading && (
          <div className="min-w-0">
            <div className="p-4 border-b border-base-300 flex flex-col gap-3 lg:flex-row lg:items-start lg:justify-between">
              <div className="min-w-0">
                <div className="flex items-center gap-2 flex-wrap">
                  <h3 className="font-medium text-base min-w-0 truncate">{selectedSkill.name}</h3>
                  <span className={`badge badge-xs ${skillScopeClass(selectedSkill.scope)}`}>
                    {skillScopeLabel(selectedSkill.scope)}
                  </span>
                  <span className={`badge badge-xs ${skillStatusClass(selectedSkill.status)}`}>
                    {selectedSkill.status === "active" ? "Enabled" : selectedSkill.status}
                  </span>
                </div>
                <p className="text-sm text-base-content/70 mt-2 break-words">
                  {selectedSkill.description || "No description yet."}
                </p>
                <p className="text-xs text-base-content/60 mt-2">
                  {selectedSkill.scope === "system"
                    ? "Built-in skill. Read-only here; duplicate it to this agent if you want to customize behavior."
                    : selectedSkill.scope === "user"
                      ? "Installed on your profile and available across agents."
                      : "Installed only on this agent."}
                </p>
              </div>
              <div className="flex items-center gap-2 flex-wrap">
                {selectedSkill.scope === "system" && canInstallAgentSkills && (
                  <button
                    onClick={onDuplicateBuiltinToAgent}
                    type="button"
                    className="btn btn-primary btn-xs"
                  >
                    Duplicate to agent
                  </button>
                )}
                {canEdit && !selectedSkillEditMode && (
                  <button
                    onClick={() => onSetState({ selectedSkillEditMode: true })}
                    type="button"
                    className="btn btn-ghost btn-xs"
                  >
                    Edit
                  </button>
                )}
                {canDelete && (
                  <button
                    onClick={() => onDeleteSkill(selectedSkill)}
                    type="button"
                    className="btn btn-ghost btn-xs text-error"
                  >
                    Delete
                  </button>
                )}
                {canEdit && selectedSkillEditMode && (
                  <>
                    <button
                      onClick={onSaveSelectedSkill}
                      type="button"
                      disabled={selectedSkillSaving || !selectedSkillDirty}
                      className="btn btn-primary btn-xs"
                    >
                      {selectedSkillSaving && (
                        <span className="loading loading-spinner loading-xs"></span>
                      )}
                      Save
                    </button>
                    <button
                      onClick={() => {
                        if (selectedSkillDirty && !confirm("Discard unsaved changes?")) return;
                        onSetState({ selectedSkillEditMode: false, selectedSkillDirty: false });
                      }}
                      type="button"
                      className="btn btn-ghost btn-xs"
                    >
                      Cancel
                    </button>
                  </>
                )}
              </div>
            </div>

            <div className="p-4 border-b border-base-300 grid grid-cols-1 md:grid-cols-2 gap-4">
              <div className="space-y-2">
                <p className="text-xs font-mono text-secondary uppercase tracking-wider">Status</p>
                {!selectedSkillEditMode ? (
                  <div className="text-sm">
                    {selectedSkill.status === "active" ? "Enabled" : selectedSkill.status}
                  </div>
                ) : (
                  <select
                    value={selectedSkill.status}
                    onChange={(e) => {
                      onSetState({
                        selectedSkill: { ...selectedSkill, status: e.target.value as Skill["status"] },
                        selectedSkillDirty: true,
                      });
                    }}
                    className="select select-bordered select-sm w-full"
                    disabled={!canEdit}
                  >
                    <option value="active">active</option>
                    <option value="draft">draft</option>
                    <option value="deprecated">deprecated</option>
                  </select>
                )}
              </div>
              <div className="space-y-2">
                <p className="text-xs font-mono text-secondary uppercase tracking-wider">Scope</p>
                <div className="text-sm">{skillScopeLabel(selectedSkill.scope)}</div>
                {selectedSkillEditMode && (
                  <label className="flex items-center gap-2 cursor-pointer pt-1">
                    <input
                      checked={!!selectedSkill.disable_model_invocation}
                      onChange={(e) => {
                        onSetState({
                          selectedSkill: { ...selectedSkill, disable_model_invocation: e.target.checked },
                          selectedSkillDirty: true,
                        });
                      }}
                      disabled={!canEdit}
                      type="checkbox"
                      className="toggle toggle-primary toggle-sm"
                    />
                    <span className="text-xs">Disable model invocation</span>
                  </label>
                )}
              </div>
            </div>

            {selectedSkillEditMode && (
              <div className="p-4 border-b border-base-300 space-y-3">
                <div>
                  <label className="label">
                    <span className="label-text text-xs font-medium">Description</span>
                  </label>
                  <input
                    value={selectedSkill.description}
                    onChange={(e) => {
                      onSetState({
                        selectedSkill: { ...selectedSkill, description: e.target.value },
                        selectedSkillDirty: true,
                      });
                    }}
                    disabled={!canEdit}
                    type="text"
                    className="input input-bordered input-sm w-full"
                  />
                </div>
              </div>
            )}

            <div className="p-4 border-b border-base-300">
              <button
                onClick={() => onSetState({ selectedSkillShowAdvanced: !selectedSkillShowAdvanced })}
                type="button"
                className="btn btn-ghost btn-sm px-0"
              >
                {selectedSkillShowAdvanced ? "Hide advanced" : "Show advanced"}
              </button>
              <p className="text-xs text-base-content/50 mt-2">
                Files and source editing live here so the main view stays focused on behavior.
              </p>
            </div>

            {selectedSkillShowAdvanced && (
              <div className="min-w-0">
                <div className="p-4 border-b border-base-300 space-y-2">
                  <div className="flex items-center gap-2 flex-wrap">
                    <select
                      value={selectedSkillActiveFile}
                      onChange={(e) => onSelectSkillFile(e.target.value)}
                      className="select select-bordered select-sm w-auto max-w-sm font-mono text-xs"
                    >
                      {skillFiles.map((f) => (
                        <option key={f} value={f}>{f}</option>
                      ))}
                    </select>
                    {canEdit && selectedSkillEditMode && selectedSkillActiveFile !== "SKILL.md" && (
                      <button
                        onClick={onDeleteSkillFile}
                        type="button"
                        className="btn btn-ghost btn-xs text-error"
                      >
                        Delete file
                      </button>
                    )}
                    {canEdit && selectedSkillEditMode && !selectedSkillAddingFile && (
                      <button
                        onClick={() => onSetState({ selectedSkillAddingFile: true, selectedSkillNewFileName: "" })}
                        type="button"
                        className="btn btn-ghost btn-xs"
                      >
                        + Add file
                      </button>
                    )}
                  </div>
                  {selectedSkillAddingFile && (
                    <div className="flex items-center gap-2">
                      <input
                        value={selectedSkillNewFileName}
                        onChange={(e) => onSetState({ selectedSkillNewFileName: e.target.value })}
                        onKeyDown={(e) => {
                          if (e.key === "Enter") { e.preventDefault(); commitAddSkillFile(); }
                          if (e.key === "Escape") onSetState({ selectedSkillAddingFile: false });
                        }}
                        type="text"
                        placeholder="reference.md"
                        className="input input-bordered input-xs flex-1 font-mono"
                        autoFocus
                      />
                      <button onClick={commitAddSkillFile} type="button" className="btn btn-primary btn-xs">
                        Add
                      </button>
                      <button
                        onClick={() => onSetState({ selectedSkillAddingFile: false })}
                        type="button"
                        className="btn btn-ghost btn-xs"
                      >
                        Cancel
                      </button>
                    </div>
                  )}
                </div>
                <div className="p-4 min-w-0">
                  {selectedSkillFileLoading ? (
                    <div className="py-8 flex justify-center">
                      <span className="loading loading-spinner loading-sm"></span>
                    </div>
                  ) : (
                    <textarea
                      value={selectedSkillFileContent}
                      onChange={(e) => onSetState({ selectedSkillFileContent: e.target.value, selectedSkillDirty: true })}
                      disabled={!canEdit || !selectedSkillEditMode}
                      rows={18}
                      className="textarea textarea-bordered w-full text-xs font-mono resize-y"
                      spellCheck={false}
                    />
                  )}
                </div>
              </div>
            )}
          </div>
        )}
      </div>
    </div>
  );
}

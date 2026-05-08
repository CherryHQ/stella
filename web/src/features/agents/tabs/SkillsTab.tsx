import type { Skill } from "@/lib/types";
import type { AgentsPageState } from "../AgentsPage";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import { Spinner } from "@/components/ui/spinner";

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

function skillScopeBadgeVariant(scope: string): "outline" | "success" | "default" {
  return (
    (
      {
        system: "outline",
        user: "success",
        agent: "default",
      } as Record<string, "outline" | "success" | "default">
    )[scope] ?? "outline"
  );
}

function skillStatusBadgeVariant(status: string): "success" | "warning" | "error" | "outline" {
  return (
    (
      {
        active: "success",
        draft: "warning",
        deprecated: "error",
      } as Record<string, "success" | "warning" | "error" | "outline">
    )[status] ?? "outline"
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
      <div className="border border-border rounded-xl bg-background/70 min-w-0 overflow-hidden">
        <div className="p-4 border-b border-border space-y-3">
          <div className="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
            <Input
              nativeInput
              value={skillListQuery}
              onChange={(e) => onSetState({ skillListQuery: (e.target as HTMLInputElement).value })}
              type="text"
              placeholder="Search skills..."
              size="sm"
              className="w-full lg:max-w-sm"
            />
            <div className="flex items-center gap-2 flex-wrap">
              <Button onClick={() => onOpenSkillInstallModal()} size="sm">
                + Install skill
              </Button>
            </div>
          </div>
          <div className="flex items-center gap-2 flex-wrap">
            {viewFilters.map((f) => (
              <Badge
                key={f.id}
                render={<button type="button" />}
                variant={skillViewFilter === f.id ? "default" : "outline"}
                size="sm"
                onClick={() => onSetState({ skillViewFilter: f.id })}
              >
                {f.label}
              </Badge>
            ))}
          </div>
          <div className="flex items-center gap-2 flex-wrap">
            {scopeFilters.map((f) => (
              <Badge
                key={f.id}
                render={<button type="button" />}
                variant={skillScopeFilter === f.id ? "secondary" : "outline"}
                size="sm"
                onClick={() => onSetState({ skillScopeFilter: f.id })}
              >
                {f.label}
              </Badge>
            ))}
          </div>
          {!editingId && (
            <div className="text-xs text-muted-foreground">
              Save the agent first if you want to add or customize agent-specific skills.
            </div>
          )}
        </div>
        <div className="p-3 space-y-2 max-h-[70vh] overflow-y-auto min-w-0">
          {agentSkillsLoading && (
            <div className="py-4 flex justify-center">
              <Spinner className="size-5" />
            </div>
          )}
          {!agentSkillsLoading &&
            filteredSkills().map((sk) => (
              <button
                key={skillKey(sk)}
                onClick={() => onSelectSkill(sk)}
                type="button"
                className={`w-full text-left rounded-xl border border-border px-3 py-3 transition-colors hover:bg-muted/50 overflow-hidden ${
                  selectedSkillKey === skillKey(sk) ? "border-primary bg-primary/5" : ""
                }`}
              >
                <div className="flex items-center gap-3 min-w-0">
                  {sk.scope !== "system" ? (
                    <Switch
                      checked={sk.status === "active"}
                      onCheckedChange={(e) => {
                        (e as unknown as Event).stopPropagation?.();
                        onToggleSkillStatus(sk);
                      }}
                      onClick={(e) => e.stopPropagation()}
                      className="shrink-0"
                      title={sk.status === "active" ? "Disable skill" : "Enable skill"}
                    />
                  ) : (
                    <Badge variant="outline" size="sm" className="shrink-0">
                      read only
                    </Badge>
                  )}
                  <div className="min-w-0 flex-1 overflow-hidden">
                    <div className="flex items-center gap-2 flex-wrap min-w-0">
                      <p className="text-sm font-mono truncate min-w-0">{sk.name}</p>
                      <Badge variant={skillScopeBadgeVariant(sk.scope)} size="sm">
                        {skillScopeLabel(sk.scope)}
                      </Badge>
                      <Badge variant={skillStatusBadgeVariant(sk.status)} size="sm">
                        {sk.status === "active" ? "Enabled" : sk.status}
                      </Badge>
                    </div>
                    {sk.description && (
                      <p className="text-xs text-muted-foreground truncate mt-1">
                        {sk.description}
                      </p>
                    )}
                  </div>
                </div>
              </button>
            ))}
          {!agentSkillsLoading && filteredSkills().length === 0 && (
            <div className="text-xs text-muted-foreground p-2">No skills match this filter.</div>
          )}
        </div>
      </div>

      {/* Skill detail */}
      <div className="border border-border rounded-xl bg-background/70 min-w-0 overflow-hidden">
        {!selectedSkill && !selectedSkillLoading && (
          <div className="p-8 text-center text-sm text-muted-foreground">
            Select a skill to inspect or edit.
          </div>
        )}
        {selectedSkillLoading && (
          <div className="p-8 flex justify-center">
            <Spinner className="size-6" />
          </div>
        )}
        {selectedSkill && !selectedSkillLoading && (
          <div className="min-w-0">
            <div className="p-4 border-b border-border flex flex-col gap-3 lg:flex-row lg:items-start lg:justify-between">
              <div className="min-w-0">
                <div className="flex items-center gap-2 flex-wrap">
                  <h3 className="font-medium text-base min-w-0 truncate">{selectedSkill.name}</h3>
                  <Badge variant={skillScopeBadgeVariant(selectedSkill.scope)} size="sm">
                    {skillScopeLabel(selectedSkill.scope)}
                  </Badge>
                  <Badge variant={skillStatusBadgeVariant(selectedSkill.status)} size="sm">
                    {selectedSkill.status === "active" ? "Enabled" : selectedSkill.status}
                  </Badge>
                </div>
                <p className="text-sm text-muted-foreground mt-2 break-words">
                  {selectedSkill.description || "No description yet."}
                </p>
                <p className="text-xs text-muted-foreground mt-2">
                  {selectedSkill.scope === "system"
                    ? "Built-in skill. Read-only here; duplicate it to this agent if you want to customize behavior."
                    : selectedSkill.scope === "user"
                      ? "Installed on your profile and available across agents."
                      : "Installed only on this agent."}
                </p>
              </div>
              <div className="flex items-center gap-2 flex-wrap">
                {selectedSkill.scope === "system" && canInstallAgentSkills && (
                  <Button onClick={onDuplicateBuiltinToAgent} size="xs">
                    Duplicate to agent
                  </Button>
                )}
                {canEdit && !selectedSkillEditMode && (
                  <Button
                    onClick={() => onSetState({ selectedSkillEditMode: true })}
                    variant="ghost"
                    size="xs"
                  >
                    Edit
                  </Button>
                )}
                {canDelete && (
                  <Button
                    onClick={() => onDeleteSkill(selectedSkill)}
                    variant="ghost"
                    size="xs"
                    className="text-destructive"
                  >
                    Delete
                  </Button>
                )}
                {canEdit && selectedSkillEditMode && (
                  <>
                    <Button
                      onClick={onSaveSelectedSkill}
                      disabled={selectedSkillSaving || !selectedSkillDirty}
                      loading={selectedSkillSaving}
                      size="xs"
                    >
                      Save
                    </Button>
                    <Button
                      onClick={() => {
                        if (selectedSkillDirty && !confirm("Discard unsaved changes?")) return;
                        onSetState({ selectedSkillEditMode: false, selectedSkillDirty: false });
                      }}
                      variant="ghost"
                      size="xs"
                    >
                      Cancel
                    </Button>
                  </>
                )}
              </div>
            </div>

            <div className="p-4 border-b border-border grid grid-cols-1 md:grid-cols-2 gap-4">
              <div className="space-y-2">
                <p className="text-xs font-mono text-muted-foreground uppercase tracking-wider">
                  Status
                </p>
                {!selectedSkillEditMode ? (
                  <div className="text-sm">
                    {selectedSkill.status === "active" ? "Enabled" : selectedSkill.status}
                  </div>
                ) : (
                  <select
                    value={selectedSkill.status}
                    onChange={(e) => {
                      onSetState({
                        selectedSkill: {
                          ...selectedSkill,
                          status: e.target.value as Skill["status"],
                        },
                        selectedSkillDirty: true,
                      });
                    }}
                    disabled={!canEdit}
                    className="w-full rounded-lg border border-input bg-background px-3 py-1.5 text-sm outline-none focus:ring-2 focus:ring-ring"
                  >
                    <option value="active">active</option>
                    <option value="draft">draft</option>
                    <option value="deprecated">deprecated</option>
                  </select>
                )}
              </div>
              <div className="space-y-2">
                <p className="text-xs font-mono text-muted-foreground uppercase tracking-wider">
                  Scope
                </p>
                <div className="text-sm">{skillScopeLabel(selectedSkill.scope)}</div>
                {selectedSkillEditMode && (
                  <label className="flex items-center gap-2 cursor-pointer pt-1">
                    <Switch
                      checked={!!selectedSkill.disable_model_invocation}
                      onCheckedChange={(checked) => {
                        onSetState({
                          selectedSkill: { ...selectedSkill, disable_model_invocation: checked },
                          selectedSkillDirty: true,
                        });
                      }}
                      disabled={!canEdit}
                    />
                    <span className="text-xs">Disable model invocation</span>
                  </label>
                )}
              </div>
            </div>

            {selectedSkillEditMode && (
              <div className="p-4 border-b border-border space-y-3">
                <div>
                  <label className="block text-xs font-medium mb-1">Description</label>
                  <Input
                    nativeInput
                    value={selectedSkill.description}
                    onChange={(e) => {
                      onSetState({
                        selectedSkill: {
                          ...selectedSkill,
                          description: (e.target as HTMLInputElement).value,
                        },
                        selectedSkillDirty: true,
                      });
                    }}
                    disabled={!canEdit}
                    type="text"
                    size="sm"
                  />
                </div>
              </div>
            )}

            <div className="p-4 border-b border-border">
              <Button
                onClick={() =>
                  onSetState({ selectedSkillShowAdvanced: !selectedSkillShowAdvanced })
                }
                variant="ghost"
                size="sm"
                className="px-0"
              >
                {selectedSkillShowAdvanced ? "Hide advanced" : "Show advanced"}
              </Button>
              <p className="text-xs text-muted-foreground mt-2">
                Files and source editing live here so the main view stays focused on behavior.
              </p>
            </div>

            {selectedSkillShowAdvanced && (
              <div className="min-w-0">
                <div className="p-4 border-b border-border space-y-2">
                  <div className="flex items-center gap-2 flex-wrap">
                    <select
                      value={selectedSkillActiveFile}
                      onChange={(e) => onSelectSkillFile(e.target.value)}
                      className="rounded-lg border border-input bg-background px-3 py-1.5 text-xs font-mono outline-none focus:ring-2 focus:ring-ring w-auto max-w-sm"
                    >
                      {skillFiles.map((f) => (
                        <option key={f} value={f}>
                          {f}
                        </option>
                      ))}
                    </select>
                    {canEdit && selectedSkillEditMode && selectedSkillActiveFile !== "SKILL.md" && (
                      <Button
                        onClick={onDeleteSkillFile}
                        variant="ghost"
                        size="xs"
                        className="text-destructive"
                      >
                        Delete file
                      </Button>
                    )}
                    {canEdit && selectedSkillEditMode && !selectedSkillAddingFile && (
                      <Button
                        onClick={() =>
                          onSetState({
                            selectedSkillAddingFile: true,
                            selectedSkillNewFileName: "",
                          })
                        }
                        variant="ghost"
                        size="xs"
                      >
                        + Add file
                      </Button>
                    )}
                  </div>
                  {selectedSkillAddingFile && (
                    <div className="flex items-center gap-2">
                      <Input
                        nativeInput
                        value={selectedSkillNewFileName}
                        onChange={(e) =>
                          onSetState({
                            selectedSkillNewFileName: (e.target as HTMLInputElement).value,
                          })
                        }
                        onKeyDown={(e) => {
                          if (e.key === "Enter") {
                            e.preventDefault();
                            commitAddSkillFile();
                          }
                          if (e.key === "Escape") onSetState({ selectedSkillAddingFile: false });
                        }}
                        type="text"
                        placeholder="reference.md"
                        size="sm"
                        className="flex-1 font-mono"
                        autoFocus
                      />
                      <Button onClick={commitAddSkillFile} size="xs">
                        Add
                      </Button>
                      <Button
                        onClick={() => onSetState({ selectedSkillAddingFile: false })}
                        variant="ghost"
                        size="xs"
                      >
                        Cancel
                      </Button>
                    </div>
                  )}
                </div>
                <div className="p-4 min-w-0">
                  {selectedSkillFileLoading ? (
                    <div className="py-8 flex justify-center">
                      <Spinner className="size-5" />
                    </div>
                  ) : (
                    <Textarea
                      value={selectedSkillFileContent}
                      onChange={(e) =>
                        onSetState({
                          selectedSkillFileContent: (e.target as HTMLTextAreaElement).value,
                          selectedSkillDirty: true,
                        })
                      }
                      disabled={!canEdit || !selectedSkillEditMode}
                      rows={18}
                      className="text-xs font-mono"
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

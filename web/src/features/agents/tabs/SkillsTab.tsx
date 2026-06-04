import type { Skill } from "@/lib/types";
import type { AgentsPageState } from "../AgentsPage";
import { Button } from "@/components/ui/button";
import { useI18n } from "@/lib/i18n";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import { Spinner } from "@/components/ui/spinner";
import { SkillFilePreview } from "@/features/sessions/SkillFilePreview";

interface Props {
  state: AgentsPageState;
  onSetState: (patch: Partial<AgentsPageState>) => void;
  onSelectSkill: (sk: Skill) => void;
  onToggleSkillStatus: (sk: Skill) => void;
  onSaveSelectedSkill: () => void;
  onDeleteSkill: (sk: Skill) => void;
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
  onSelectSkillFile,
  onDeleteSkillFile,
  onOpenSkillInstallModal,
}: Props) {
  const {
    agentSkills,
    agentSkillsLoading,
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

  const { t } = useI18n();
  const canInstallAgentSkills = isAdmin && !!editingId;
  void canInstallAgentSkills;
  const canEdit = !!selectedSkill && selectedSkill.scope !== "system";
  const canDelete = !!selectedSkill && selectedSkill.scope !== "system";

  const allSkills = (): Skill[] => {
    const ordered: Record<string, number> = { system: 0, agent: 1, user: 2 };
    return [...agentSkills].sort((a, b) => {
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
    <div className="grid grid-cols-1 xl:grid-cols-[minmax(0,0.95fr)_minmax(0,1.05fr)] gap-6 min-w-0">
      {/* Skill list */}
      <div className="min-w-0 overflow-hidden rounded-xl border border-border bg-card shadow-none">
        <div className="p-4 border-b border-border space-y-3">
          <div className="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
            <Input
              nativeInput
              value={skillListQuery}
              onChange={(e) => onSetState({ skillListQuery: (e.target as HTMLInputElement).value })}
              type="text"
              placeholder="Search skills..."
              size="sm"
              className="w-full lg:max-w-sm transition-all duration-120 focus-visible:border-primary focus-visible:ring-2 focus-visible:ring-primary/20"
            />
            <div className="flex items-center gap-2 flex-wrap">
              <Button
                onClick={() => onOpenSkillInstallModal()}
                size="sm"
                className="cursor-pointer duration-120"
              >
                + Install skill
              </Button>
            </div>
          </div>
          <div className="flex items-center gap-1.5 flex-wrap">
            {viewFilters.map((f) => (
              <Badge
                key={f.id}
                render={<button type="button" />}
                variant={skillViewFilter === f.id ? "default" : "outline"}
                className="cursor-pointer text-[10px] font-sans px-2.5 py-0.5 rounded-full transition-all duration-120 hover:border-foreground/20"
                onClick={() => onSetState({ skillViewFilter: f.id })}
              >
                {f.label}
              </Badge>
            ))}
          </div>
          <div className="flex items-center gap-1.5 flex-wrap">
            {scopeFilters.map((f) => (
              <Badge
                key={f.id}
                render={<button type="button" />}
                variant={skillScopeFilter === f.id ? "secondary" : "outline"}
                className="cursor-pointer text-[10px] font-sans px-2.5 py-0.5 rounded-full transition-all duration-120 hover:border-foreground/20"
                onClick={() => onSetState({ skillScopeFilter: f.id })}
              >
                {f.label}
              </Badge>
            ))}
          </div>
          {!editingId && (
            <div className="text-[11px] text-muted-foreground/70">
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
                className={`w-full text-left rounded-lg border px-3 py-3 transition-all duration-120 hover:bg-muted/30 hover:border-foreground/15 overflow-hidden cursor-pointer ${
                  selectedSkillKey === skillKey(sk)
                    ? "border-primary bg-primary/5 text-foreground"
                    : "border-border text-foreground/80"
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
                    <Badge
                      variant="outline"
                      className="text-[9px] tracking-wide uppercase font-mono px-1 rounded-[4px] shrink-0"
                    >
                      read only
                    </Badge>
                  )}
                  <div className="min-w-0 flex-1 overflow-hidden">
                    <div className="flex items-center gap-1.5 flex-wrap min-w-0">
                      <p className="text-sm font-mono truncate min-w-0 font-medium">{sk.name}</p>
                      <Badge
                        variant={skillScopeBadgeVariant(sk.scope)}
                        className="text-[9px] tracking-wider uppercase font-mono px-1 rounded-[4px]"
                      >
                        {skillScopeLabel(sk.scope)}
                      </Badge>
                      <Badge
                        variant={skillStatusBadgeVariant(sk.status)}
                        className="text-[9px] tracking-wider uppercase font-mono px-1 rounded-[4px]"
                      >
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
      <div className="min-w-0 overflow-hidden rounded-xl border border-border bg-card shadow-none">
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
            <div className="flex min-w-0 flex-col gap-3 border-b border-border p-4 lg:flex-row lg:items-start lg:justify-between bg-muted/5">
              <div className="min-w-0">
                <div className="flex items-center gap-2 flex-wrap">
                  <h3 className="font-semibold text-base min-w-0 truncate text-foreground/90">
                    {selectedSkill.name}
                  </h3>
                  <Badge
                    variant={skillScopeBadgeVariant(selectedSkill.scope)}
                    className="text-[9px] tracking-wider uppercase font-mono px-1 rounded-[4px]"
                  >
                    {skillScopeLabel(selectedSkill.scope)}
                  </Badge>
                  <Badge
                    variant={skillStatusBadgeVariant(selectedSkill.status)}
                    className="text-[9px] tracking-wider uppercase font-mono px-1 rounded-[4px]"
                  >
                    {selectedSkill.status === "active" ? "Enabled" : selectedSkill.status}
                  </Badge>
                </div>
                <p className="text-xs text-muted-foreground mt-2 break-words leading-relaxed">
                  {selectedSkill.description || "No description yet."}
                </p>
                <p className="text-[10px] text-muted-foreground/60 mt-1.5">
                  {selectedSkill.scope === "system"
                    ? "System skill. Read-only here."
                    : selectedSkill.scope === "user"
                      ? "Installed for you in this agent."
                      : "Installed for this agent."}
                </p>
              </div>
              <div className="flex shrink-0 items-center gap-2 flex-wrap">
                {canEdit && !selectedSkillEditMode && (
                  <Button
                    onClick={() => onSetState({ selectedSkillEditMode: true })}
                    variant="ghost"
                    size="xs"
                    className="cursor-pointer"
                  >
                    {t("common.edit")}
                  </Button>
                )}
                {canDelete && (
                  <Button
                    onClick={() => onDeleteSkill(selectedSkill)}
                    variant="ghost"
                    size="xs"
                    className="text-destructive hover:bg-destructive/10 cursor-pointer"
                  >
                    {t("common.delete")}
                  </Button>
                )}
                {canEdit && selectedSkillEditMode && (
                  <>
                    <Button
                      onClick={onSaveSelectedSkill}
                      disabled={selectedSkillSaving || !selectedSkillDirty}
                      loading={selectedSkillSaving}
                      size="xs"
                      className="cursor-pointer"
                    >
                      {t("common.save")}
                    </Button>
                    <Button
                      onClick={() => {
                        if (selectedSkillDirty && !confirm("Discard unsaved changes?")) return;
                        onSetState({ selectedSkillEditMode: false, selectedSkillDirty: false });
                      }}
                      variant="ghost"
                      size="xs"
                      className="cursor-pointer"
                    >
                      {t("common.cancel")}
                    </Button>
                  </>
                )}
              </div>
            </div>

            <div className="p-4 border-b border-border grid grid-cols-1 md:grid-cols-2 gap-4 bg-muted/5">
              <div className="space-y-1.5">
                <p className="text-[10px] font-semibold text-muted-foreground uppercase tracking-wider">
                  Status
                </p>
                {!selectedSkillEditMode ? (
                  <div className="text-xs font-mono font-medium text-foreground/80">
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
                    className="w-full rounded-lg border border-border bg-background px-3 py-1.5 text-xs outline-none focus:border-primary focus:ring-2 focus:ring-primary/20 transition-all duration-120 cursor-pointer font-mono"
                  >
                    <option value="active">active</option>
                    <option value="draft">draft</option>
                    <option value="deprecated">deprecated</option>
                  </select>
                )}
              </div>
              <div className="space-y-1.5">
                <p className="text-[10px] font-semibold text-muted-foreground uppercase tracking-wider">
                  Scope
                </p>
                <div className="text-xs font-mono font-medium text-foreground/80">
                  {skillScopeLabel(selectedSkill.scope)}
                </div>
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
                    <span className="text-xs text-muted-foreground font-medium">
                      Disable model invocation
                    </span>
                  </label>
                )}
              </div>
            </div>

            {selectedSkillEditMode && (
              <div className="p-4 border-b border-border space-y-3 bg-card">
                <div>
                  <label className="block text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-1.5">
                    Description
                  </label>
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
                    className="text-xs transition-all duration-120 focus-visible:border-primary focus-visible:ring-2 focus-visible:ring-primary/20"
                  />
                </div>
              </div>
            )}

            <div className="p-4 border-b border-border bg-muted/5">
              <Button
                onClick={() =>
                  onSetState({ selectedSkillShowAdvanced: !selectedSkillShowAdvanced })
                }
                variant="ghost"
                size="sm"
                className="px-0 text-primary cursor-pointer hover:bg-transparent"
              >
                {selectedSkillShowAdvanced ? "Hide advanced" : "Show advanced"}
              </Button>
              <p className="text-[11px] text-muted-foreground mt-1">
                Files and source editing live here so the main view stays focused on behavior.
              </p>
            </div>

            {selectedSkillShowAdvanced && (
              <div className="min-w-0">
                <div className="p-4 border-b border-border space-y-2 bg-card">
                  <div className="flex items-center gap-2 flex-wrap">
                    <select
                      value={selectedSkillActiveFile}
                      onChange={(e) => onSelectSkillFile(e.target.value)}
                      className="min-w-0 max-w-full rounded-lg border border-border bg-background px-3 py-1.5 text-xs font-mono outline-none focus:border-primary focus:ring-2 focus:ring-primary/20 transition-all duration-120 cursor-pointer sm:max-w-sm"
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
                        className="text-destructive hover:bg-destructive/10 cursor-pointer"
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
                        className="cursor-pointer"
                      >
                        + Add file
                      </Button>
                    )}
                  </div>
                  {selectedSkillAddingFile && (
                    <div className="flex items-center gap-2 pt-1">
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
                        className="flex-1 font-mono transition-all duration-120 focus-visible:border-primary focus-visible:ring-2 focus-visible:ring-primary/20"
                        autoFocus
                      />
                      <Button onClick={commitAddSkillFile} size="xs" className="cursor-pointer">
                        {t("common.add")}
                      </Button>
                      <Button
                        onClick={() => onSetState({ selectedSkillAddingFile: false })}
                        variant="ghost"
                        size="xs"
                        className="cursor-pointer"
                      >
                        {t("common.cancel")}
                      </Button>
                    </div>
                  )}
                </div>
                <div className="min-w-0 p-4 bg-muted/5">
                  {selectedSkillFileLoading ? (
                    <div className="py-8 flex justify-center">
                      <Spinner className="size-5" />
                    </div>
                  ) : selectedSkillEditMode ? (
                    <Textarea
                      value={selectedSkillFileContent}
                      onChange={(e) =>
                        onSetState({
                          selectedSkillFileContent: (e.target as HTMLTextAreaElement).value,
                          selectedSkillDirty: true,
                        })
                      }
                      disabled={!canEdit}
                      rows={18}
                      className="text-xs font-mono transition-all duration-120 focus-visible:border-primary focus-visible:ring-2 focus-visible:ring-primary/20 bg-card leading-relaxed"
                      spellCheck={false}
                    />
                  ) : (
                    <SkillFilePreview
                      path={selectedSkillActiveFile}
                      content={selectedSkillFileContent}
                      emptyText="No content yet."
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

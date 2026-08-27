import type { Skill } from "@/lib/types";
import { targetValue } from "@/lib/utils";
import type { AgentsPageState } from "../agent-detail-state";
import { Button } from "@/components/ui/button";
import { useI18n } from "@/lib/i18n";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import { Spinner } from "@/components/ui/spinner";
import { Alert, AlertAction, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { SkillFilePreview } from "@/features/sessions/SkillFilePreview";
import { skillScopeLabelKey } from "@/lib/skill-scope";
import { activationControlState, danglingClearControlState } from "./skills-tab-state";

interface Props {
  state: AgentsPageState;
  onSetState: (patch: Partial<AgentsPageState>) => void;
  onSelectSkill: (sk: Skill) => void;
  onSaveSelectedSkill: () => void;
  onDeleteSkill: (sk: Skill) => void;
  onSelectSkillFile: (path: string, skipDirtyCheck?: boolean) => void;
  onDeleteSkillFile: () => void;
  onOpenSkillInstallModal: (scope?: "user_agent" | "system_agent") => void;
  onToggleActivation: (skill: Skill, enabled: boolean) => void;
  onClearDanglingActivation: (ref: string) => void;
}

function skillKey(sk: { scope: string; id: string }) {
  return `${sk.scope}:${sk.id}`;
}

type SkillScopeBadgeVariants = Record<string, "outline" | "success" | "default">;
type SkillScopeOrder = Record<string, number>;

function skillScopeBadgeVariant(scope: string): "outline" | "success" | "default" {
  // SAFETY: the variant map is keyed by the known skill scopes; unknown scopes
  // read undefined and fall back to "outline" via the ?? at the end.
  return (
    (
      {
        system: "outline",
        user: "success",
        user_agent: "success",
        system_agent: "default",
      } satisfies SkillScopeBadgeVariants
    )[scope] ?? "outline"
  );
}

export function SkillsTab({
  state,
  onSetState,
  onSelectSkill,
  onSaveSelectedSkill,
  onDeleteSkill,
  onSelectSkillFile,
  onDeleteSkillFile,
  onOpenSkillInstallModal,
  onToggleActivation,
  onClearDanglingActivation,
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
    selectedSkillFileEncoding,
    selectedSkillFileLoaded,
    selectedSkillFileLoading,
    selectedSkillAddingFile,
    selectedSkillNewFileName,
    editingId,
    isAdmin,
    agentSkillCanManageActivation,
    agentSkillActivationPending,
    agentSkillPolicyDiagnostics,
  } = state;

  const { t } = useI18n();
  const canInstallAgentSkills = isAdmin && !!editingId;
  void canInstallAgentSkills;
  // Mirror the backend write rules: system/project are read-only, and the
  // shared system_agent scope is admin-only (agent ownership is not enough).
  const canManageScope = (scope?: string) =>
    scope === "user" || scope === "user_agent" || (scope === "system_agent" && isAdmin);
  const canEdit = !!selectedSkill && canManageScope(selectedSkill.scope);
  const canDelete = canEdit;
  const scopeLabel = (skill: Skill) =>
    skill.builtin
      ? t("agents.skills.scopeBuiltin")
      : t(skillScopeLabelKey(skill.scope) ?? "skills.scope.project.label");

  const allSkills = (): Skill[] => {
    const ordered: SkillScopeOrder = {
      builtin: 0,
      system: 1,
      system_agent: 2,
      user_agent: 3,
      user: 4,
    };
    return [...agentSkills].sort((a, b) => {
      const diff = (ordered[a.scope] ?? 99) - (ordered[b.scope] ?? 99);
      if (diff !== 0) return diff;
      return (a.name ?? "").localeCompare(b.name ?? "");
    });
  };

  const filteredSkills = (): Skill[] => {
    const q = skillListQuery.trim().toLowerCase();
    return allSkills().filter((sk) => {
      if (skillViewFilter === "enabled" && sk.enabled === false) return false;
      if (skillViewFilter === "modified" && (sk.scope === "system" || sk.builtin)) return false;
      if (skillScopeFilter !== "all") {
        const matches =
          skillScopeFilter === "agent"
            ? sk.scope === "system_agent" || sk.scope === "user_agent"
            : sk.scope === skillScopeFilter;
        if (!matches) return false;
      }
      if (!q) return true;
      return [sk.name, sk.description, sk.scope].some((v) => (v ?? "").toLowerCase().includes(q));
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
    { id: "all", label: t("agents.skills.filterAll") },
    { id: "enabled", label: t("agents.skills.filterEnabled") },
    { id: "modified", label: t("agents.skills.filterModified") },
  ];
  const scopeFilters = [
    { id: "all", label: t("agents.skills.scopeAll") },
    { id: "builtin", label: t("agents.skills.scopeBuiltin") },
    { id: "system", label: t("agents.skills.scopeSystem") },
    { id: "user", label: t("agents.skills.scopeUser") },
    { id: "agent", label: t("agents.skills.scopeAgent") },
  ];

  return (
    <div className="grid grid-cols-1 xl:grid-cols-[minmax(0,0.95fr)_minmax(0,1.05fr)] gap-6 min-w-0">
      {/* Skill list */}
      <div className="min-w-0 overflow-hidden rounded-xl border border-border bg-card">
        <div className="p-4 border-b border-border space-y-3">
          <div className="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
            <Input
              nativeInput
              value={skillListQuery}
              onChange={(e) => onSetState({ skillListQuery: targetValue(e) })}
              type="text"
              placeholder={t("agents.skills.searchPlaceholder")}
              size="sm"
              className="w-full lg:max-w-sm"
            />
            <div className="flex items-center gap-2 flex-wrap">
              <Button
                onClick={() => onOpenSkillInstallModal()}
                size="sm"
                className="cursor-pointer"
              >
                {t("agents.skills.installSkill")}
              </Button>
            </div>
          </div>
          <div className="flex items-center gap-1.5 flex-wrap">
            {viewFilters.map((f) => (
              <Badge
                key={f.id}
                render={<button type="button" />}
                variant={skillViewFilter === f.id ? "default" : "outline"}
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
                onClick={() => onSetState({ skillScopeFilter: f.id })}
              >
                {f.label}
              </Badge>
            ))}
          </div>
          {!editingId && (
            <div className="text-xs text-muted-foreground">{t("agents.skills.saveFirst")}</div>
          )}
          {agentSkillPolicyDiagnostics.dangling_disabled_refs.map((ref) => (
            <Alert key={ref} variant="warning">
              <AlertTitle>{t("agents.skills.danglingPolicyTitle", { ref })}</AlertTitle>
              <AlertDescription>{t("agents.skills.danglingPolicyDescription")}</AlertDescription>
              {danglingClearControlState(agentSkillCanManageActivation, agentSkillActivationPending)
                .visible && (
                <AlertAction>
                  <Button
                    size="xs"
                    variant="outline"
                    disabled={
                      danglingClearControlState(
                        agentSkillCanManageActivation,
                        agentSkillActivationPending,
                      ).disabled
                    }
                    onClick={() => onClearDanglingActivation(ref)}
                  >
                    {t("agents.skills.clearDangling")}
                  </Button>
                </AlertAction>
              )}
            </Alert>
          ))}
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
                className={`w-full text-left rounded-lg border px-3 py-3 overflow-hidden cursor-pointer ${
                  selectedSkillKey === skillKey(sk)
                    ? "border-primary bg-primary/5 text-foreground"
                    : "border-border text-foreground"
                }`}
              >
                <div className="flex items-center gap-3 min-w-0">
                  {!canManageScope(sk.scope) && (
                    <Badge variant="outline">{t("agents.skills.readOnly")}</Badge>
                  )}
                  <div className="min-w-0 flex-1 overflow-hidden">
                    <div className="flex items-center gap-1.5 flex-wrap min-w-0">
                      <p className="text-sm font-mono truncate min-w-0 font-medium">{sk.name}</p>
                      <Badge variant={skillScopeBadgeVariant(sk.scope)}>{scopeLabel(sk)}</Badge>
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
            <div className="text-xs text-muted-foreground p-2">{t("agents.skills.noMatch")}</div>
          )}
        </div>
      </div>

      {/* Skill detail */}
      <div className="min-w-0 overflow-hidden rounded-xl border border-border bg-card">
        {!selectedSkill && !selectedSkillLoading && (
          <div className="p-8 text-center text-sm text-muted-foreground">
            {t("agents.skills.selectSkill")}
          </div>
        )}
        {selectedSkillLoading && (
          <div className="p-8 flex justify-center">
            <Spinner className="size-6" />
          </div>
        )}
        {selectedSkill && !selectedSkillLoading && (
          <div className="min-w-0">
            <div className="flex min-w-0 flex-col gap-3 border-b border-border p-4 lg:flex-row lg:items-start lg:justify-between">
              <div className="min-w-0">
                <div className="flex items-center gap-2 flex-wrap">
                  <h3 className="font-semibold text-base min-w-0 truncate text-foreground">
                    {selectedSkill.name}
                  </h3>
                  <Badge variant={skillScopeBadgeVariant(selectedSkill.scope)}>
                    {scopeLabel(selectedSkill)}
                  </Badge>
                </div>
                <p className="text-xs text-muted-foreground mt-2 break-words leading-relaxed">
                  {selectedSkill.description || t("agents.skills.noDescription")}
                </p>
                <p className="text-xs text-muted-foreground mt-1.5">
                  {selectedSkill.scope === "system"
                    ? t("agents.skills.systemScope")
                    : selectedSkill.scope === "user"
                      ? t("agents.skills.userScope")
                      : t("agents.skills.agentScope")}
                </p>
              </div>
              <div className="flex shrink-0 items-center gap-2 flex-wrap">
                {activationControlState(
                  selectedSkill.logical_ref,
                  agentSkillCanManageActivation,
                  agentSkillActivationPending,
                ).visible && (
                  <label className="flex items-center gap-2 text-xs text-muted-foreground">
                    <Switch
                      checked={selectedSkill.enabled !== false}
                      disabled={
                        activationControlState(
                          selectedSkill.logical_ref,
                          agentSkillCanManageActivation,
                          agentSkillActivationPending,
                        ).disabled
                      }
                      onCheckedChange={(enabled) => onToggleActivation(selectedSkill, enabled)}
                    />
                    {t("agents.skills.activation")}
                  </label>
                )}
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
                    className="text-destructive-foreground hover:bg-destructive/10 cursor-pointer"
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

            <div className="p-4 border-b border-border grid grid-cols-1 md:grid-cols-2 gap-4">
              <div className="space-y-1.5">
                <p className="text-xs font-semibold text-muted-foreground">
                  {t("agents.form.scope")}
                </p>
                <div className="text-xs font-mono font-medium text-foreground">
                  {scopeLabel(selectedSkill)}
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
                      {t("agents.skills.disableModelInvocation")}
                    </span>
                  </label>
                )}
              </div>
            </div>

            {selectedSkillEditMode && (
              <div className="p-4 border-b border-border space-y-3 bg-card">
                <div>
                  <label className="block text-xs font-semibold text-muted-foreground mb-1.5">
                    {t("common.description")}
                  </label>
                  <Input
                    nativeInput
                    value={selectedSkill.description}
                    onChange={(e) => {
                      onSetState({
                        selectedSkill: {
                          ...selectedSkill,
                          description: targetValue(e),
                        },
                        selectedSkillDirty: true,
                      });
                    }}
                    disabled={!canEdit}
                    type="text"
                    size="sm"
                    className="text-xs"
                  />
                </div>
              </div>
            )}

            <div className="p-4 border-b border-border">
              <Button
                onClick={() =>
                  onSetState({ selectedSkillShowAdvanced: !selectedSkillShowAdvanced })
                }
                variant="link"
                size="sm"
              >
                {selectedSkillShowAdvanced
                  ? t("agents.skills.hideAdvanced")
                  : t("agents.skills.showAdvanced")}
              </Button>
              <p className="text-xs text-muted-foreground mt-1">
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
                      className="min-w-0 max-w-full rounded-lg border border-border bg-background px-3 py-1.5 text-xs font-mono outline-none cursor-pointer sm:max-w-sm"
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
                        className="text-destructive-foreground hover:bg-destructive/10 cursor-pointer"
                      >
                        {t("agents.skills.deleteFile")}
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
                        {t("agents.skills.addFile")}
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
                            selectedSkillNewFileName: targetValue(e),
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
                <div className="min-w-0 p-4">
                  {selectedSkillFileLoading ? (
                    <div className="py-8 flex justify-center">
                      <Spinner className="size-5" />
                    </div>
                  ) : selectedSkillEditMode &&
                    selectedSkillFileLoaded &&
                    selectedSkillFileEncoding !== "base64" ? (
                    <Textarea
                      value={selectedSkillFileContent}
                      onChange={(e) =>
                        onSetState({
                          selectedSkillFileContent: targetValue(e),
                          selectedSkillDirty: true,
                        })
                      }
                      disabled={!canEdit}
                      rows={18}
                      className="text-xs font-mono leading-relaxed"
                      spellCheck={false}
                    />
                  ) : (
                    <SkillFilePreview
                      path={selectedSkillActiveFile}
                      content={selectedSkillFileContent}
                      encoding={selectedSkillFileEncoding || undefined}
                      emptyText={t("agents.skills.noContent")}
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

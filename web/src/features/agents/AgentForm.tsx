import type { Skill, User } from "@/lib/types";
import type { AgentsPageState } from "./agent-detail-state";
import { Button } from "@/components/ui/button";
import { ProfilePanelSection } from "./ProfilePanelSection";
import { useI18n } from "@/lib/i18n";
import { canEditAgent } from "./agent-detail-state";
import { ConfigTab } from "./tabs/ConfigTab";
import { PromptTab } from "./tabs/PromptTab";
import { SkillsTab } from "./tabs/SkillsTab";
import { ToolsTab } from "./tabs/ToolsTab";
import { AdvancedTab } from "./tabs/AdvancedTab";
import { UsersTab } from "./tabs/UsersTab";

/**
 * `page` fills a dedicated settings pane and scrolls internally; `embedded`
 * sits inside a host page that owns the scroll.
 */
export type AgentFormLayout = "page" | "embedded";

interface Props {
  state: AgentsPageState;
  layout?: AgentFormLayout;
  /** Sections a host surface already covers elsewhere (e.g. the profile's own skills tab). */
  hiddenTabs?: readonly string[];
  onSetState: (patch: Partial<AgentsPageState>) => void;
  onSave: () => void;
  onCancel?: () => void;
  onAddUser: () => void;
  onRemoveUser: (userId: string) => void;
  onApplySoul: (soulID: string) => void;
  onSelectSkill: (sk: Skill) => void;
  onSaveSelectedSkill: () => void;
  onDeleteSkill: (sk: Skill) => void;
  onSelectSkillFile: (path: string, skipDirtyCheck?: boolean) => void;
  onDeleteSkillFile: () => void;
  onOpenSkillInstallModal: (scope?: "user_agent" | "system_agent") => void;
  onToggleActivation: (skill: Skill, enabled: boolean) => void;
  onClearDanglingActivation: (ref: string) => void;
  onDelete?: () => void;
}

export function AgentForm({
  state,
  layout = "page",
  hiddenTabs,
  onSetState,
  onSave,
  onCancel,
  onAddUser,
  onRemoveUser,
  onApplySoul,
  onSelectSkill,
  onSaveSelectedSkill,
  onDeleteSkill,
  onSelectSkillFile,
  onDeleteSkillFile,
  onOpenSkillInstallModal,
  onToggleActivation,
  onClearDanglingActivation,
  onDelete,
}: Props) {
  const { t } = useI18n();
  const { editingId, form } = state;
  const embedded = layout === "embedded";

  // A form with nothing saved yet has no agent to manage: creation is the edit.
  const canEdit = !editingId || canEditAgent(form);

  const availableUsers = state.allUsers.filter(
    (u: User) => !state.assignedUsers.some((a: User) => a.id === u.id),
  );

  const hidden = new Set(hiddenTabs ?? []);
  const shows = (tab: string) => !hidden.has(tab);

  return (
    <div
      className={
        embedded ? "flex min-w-0 flex-col" : "h-full min-h-0 min-w-0 flex flex-col bg-card"
      }
    >
      {!embedded && (
        <div className="border-b border-border px-6 py-4">
          <span className="font-medium text-sm text-foreground">
            {editingId
              ? t("agents.form.editAgent", { name: form.name })
              : t("agents.form.newAgent")}
          </span>
        </div>
      )}
      {/* One vertical column of sections, not a tab strip: the editor is
          embedded in a page that already owns a tab strip, and a second one
          inside it hid half the agent's settings behind a click. Everything
          below the basics folds (same idiom as the memory tab) so the column
          stays scannable; the basics stay expanded and uncollapsible because
          their model and channel pickers open absolutely-positioned lists that
          a collapsible panel's overflow clip would cut off. */}
      <div
        className={embedded ? "flex flex-col" : "flex-1 min-h-0 overflow-y-auto p-6 flex flex-col"}
      >
        {shows("config") && (
          <div className="pb-4">
            <ProfilePanelSection title={t("agents.sections.basics")}>
              <ConfigTab state={state} onSetState={onSetState} />
            </ProfilePanelSection>
          </div>
        )}
        {shows("prompt") && (
          <ProfilePanelSection collapsible title={t("agents.tabs.prompt")}>
            <PromptTab state={state} onSetState={onSetState} onApplySoul={onApplySoul} />
          </ProfilePanelSection>
        )}
        {shows("skills") && (
          <ProfilePanelSection collapsible title={t("agents.tabs.skills")}>
            <SkillsTab
              state={state}
              onSetState={onSetState}
              onSelectSkill={onSelectSkill}
              onSaveSelectedSkill={onSaveSelectedSkill}
              onDeleteSkill={onDeleteSkill}
              onSelectSkillFile={onSelectSkillFile}
              onDeleteSkillFile={onDeleteSkillFile}
              onOpenSkillInstallModal={onOpenSkillInstallModal}
              onToggleActivation={onToggleActivation}
              onClearDanglingActivation={onClearDanglingActivation}
            />
          </ProfilePanelSection>
        )}
        {shows("tools") && (
          <ProfilePanelSection collapsible title={t("agents.tabs.tools")}>
            <ToolsTab state={state} canEdit={canEdit} />
          </ProfilePanelSection>
        )}
        {shows("advanced") && (
          <ProfilePanelSection collapsible title={t("agents.tabs.advanced")}>
            <AdvancedTab state={state} canEdit={canEdit} onSetState={onSetState} />
          </ProfilePanelSection>
        )}
        {/* Not admin-only: the owner sets the agent's reach here. The tab keeps
            the per-user assignment list behind the admin check itself. */}
        {canEdit && shows("users") && (
          <ProfilePanelSection collapsible title={t("agents.tabs.users")}>
            <UsersTab
              state={state}
              availableUsers={availableUsers}
              onSetState={onSetState}
              onAddUser={onAddUser}
              onRemoveUser={onRemoveUser}
            />
          </ProfilePanelSection>
        )}
      </div>
      {/* One save for the whole column — the sections edit a single draft, so
          the actions stay at its foot rather than inside the basics. */}
      <div
        className={`shrink-0 flex items-center justify-between gap-2 py-4 ${
          embedded ? "" : "border-t border-border px-6"
        }`}
      >
        <div>
          {canEdit && editingId && onDelete && (
            <Button
              onClick={onDelete}
              variant="ghost"
              size="sm"
              className="text-muted-foreground hover:text-destructive-foreground cursor-pointer duration-120"
            >
              {t("common.delete")}
            </Button>
          )}
        </div>
        <div className="flex items-center gap-2">
          {onCancel && (
            <Button onClick={onCancel} variant="ghost" size="sm" className="cursor-pointer">
              {t("common.cancel")}
            </Button>
          )}
          {canEdit && (
            <Button onClick={onSave} size="sm" className="cursor-pointer">
              {editingId ? t("common.update") : t("common.create")}
            </Button>
          )}
        </div>
      </div>
    </div>
  );
}

import type { Skill, User } from "@/lib/types";
import type { AgentsPageState } from "./agent-detail-state";
import { Button } from "@/components/ui/button";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import { useI18n } from "@/lib/i18n";
import { ConfigTab } from "./tabs/ConfigTab";
import { PromptTab } from "./tabs/PromptTab";
import { SkillsTab } from "./tabs/SkillsTab";
import { ToolsTab } from "./tabs/ToolsTab";
import { AdvancedTab } from "./tabs/AdvancedTab";
import { UsersTab } from "./tabs/UsersTab";

/**
 * `page` fills a dedicated settings pane and scrolls internally; `embedded`
 * sits as one card inside a host page that owns the scroll.
 */
export type AgentFormLayout = "page" | "embedded";

interface Props {
  state: AgentsPageState;
  layout?: AgentFormLayout;
  hiddenTabs?: readonly string[];
  onSetState: (patch: Partial<AgentsPageState>) => void;
  onTabChange: (tab: string) => void;
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
  onDelete?: () => void;
}

export function AgentForm({
  state,
  layout = "page",
  hiddenTabs,
  onSetState,
  onTabChange,
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
  onDelete,
}: Props) {
  const { t } = useI18n();
  const { editingId, activeTab, isAdmin, form, currentUserId } = state;
  const embedded = layout === "embedded";

  const canEdit = isAdmin || !editingId || (form.creator_id && form.creator_id === currentUserId);

  const availableUsers = state.allUsers.filter(
    (u: User) => !state.assignedUsers.some((a: User) => a.id === u.id),
  );

  const hidden = new Set(hiddenTabs ?? []);
  const shows = (tab: string) => !hidden.has(tab);
  // A hidden tab must never stay selected — the trigger is gone, so the panel
  // would render nothing at all.
  const currentTab = shows(activeTab) ? activeTab : "config";

  return (
    <div
      className={
        embedded
          ? "flex min-w-0 flex-col overflow-hidden rounded-lg border border-border bg-card"
          : "h-full min-h-0 min-w-0 flex flex-col bg-card"
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
      <Tabs
        value={currentTab}
        onValueChange={(tab) => onTabChange(tab as string)}
        className={embedded ? "flex flex-col" : "flex-1 flex flex-col min-h-0"}
      >
        <TabsList
          variant="underline"
          className={`w-full justify-start border-b border-border rounded-none bg-transparent gap-2 h-11 ${
            embedded ? "px-4" : "px-6"
          }`}
        >
          {shows("config") && <TabsTrigger value="config">{t("agents.tabs.config")}</TabsTrigger>}
          {shows("prompt") && <TabsTrigger value="prompt">{t("agents.tabs.prompt")}</TabsTrigger>}
          {shows("skills") && <TabsTrigger value="skills">{t("agents.tabs.skills")}</TabsTrigger>}
          {shows("tools") && <TabsTrigger value="tools">{t("agents.tabs.tools")}</TabsTrigger>}
          {shows("advanced") && (
            <TabsTrigger value="advanced">{t("agents.tabs.advanced")}</TabsTrigger>
          )}
          {isAdmin && shows("users") && (
            <TabsTrigger value="users">{t("agents.tabs.users")}</TabsTrigger>
          )}
        </TabsList>
        <div
          className={embedded ? "p-4 space-y-6" : "flex-1 min-h-0 overflow-y-auto p-6 space-y-6"}
        >
          {shows("config") && (
            <TabsContent value="config">
              <ConfigTab state={state} onSetState={onSetState} />
            </TabsContent>
          )}
          {shows("prompt") && (
            <TabsContent value="prompt">
              <PromptTab state={state} onSetState={onSetState} onApplySoul={onApplySoul} />
            </TabsContent>
          )}
          {shows("skills") && (
            <TabsContent value="skills">
              <SkillsTab
                state={state}
                onSetState={onSetState}
                onSelectSkill={onSelectSkill}
                onSaveSelectedSkill={onSaveSelectedSkill}
                onDeleteSkill={onDeleteSkill}
                onSelectSkillFile={onSelectSkillFile}
                onDeleteSkillFile={onDeleteSkillFile}
                onOpenSkillInstallModal={onOpenSkillInstallModal}
              />
            </TabsContent>
          )}
          {shows("tools") && (
            <TabsContent value="tools">
              <ToolsTab state={state} />
            </TabsContent>
          )}
          {shows("advanced") && (
            <TabsContent value="advanced">
              <AdvancedTab state={state} onSetState={onSetState} />
            </TabsContent>
          )}
          {shows("users") && (
            <TabsContent value="users">
              <UsersTab
                state={state}
                availableUsers={availableUsers}
                onSetState={onSetState}
                onAddUser={onAddUser}
                onRemoveUser={onRemoveUser}
              />
            </TabsContent>
          )}
        </div>
      </Tabs>
      <div
        className={`shrink-0 border-t border-border py-4 flex items-center justify-between gap-2 ${
          embedded ? "px-4" : "px-6"
        }`}
      >
        <div>
          {canEdit && editingId && onDelete && (
            <Button
              onClick={onDelete}
              variant="ghost"
              size="sm"
              className="text-muted-foreground hover:text-destructive cursor-pointer duration-120"
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
